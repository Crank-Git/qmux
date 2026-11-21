package main

import (
	"bytes"
	"errors"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/tests/interoperability/baseline"
)

// holdDelay is the time that the recorder holds each answer. Two lookups that start
// together then overlap at the recorder for this time.
const holdDelay = 20 * time.Millisecond

// recorder is a UDP server in front of a baseline server. It records the start of a
// lookup when a request arrives and the end when it sends the answer back. It serves
// each request on a goroutine of its own, so it does not put parallel lookups in
// sequence.
type recorder struct {
	conn     *net.UDPConn
	upstream *net.UDPAddr
	inFlight atomic.Int64
	// peak is the largest number of lookups between their start and their end at one
	// time.
	peak   atomic.Int64
	served atomic.Int64
	wg     sync.WaitGroup
}

// newRecorder starts a recorder that forwards each request to upstream, and stops
// it at the end of the test.
func newRecorder(t *testing.T, upstream string) *recorder {
	t.Helper()
	raddr, err := net.ResolveUDPAddr("udp", upstream)
	if err != nil {
		t.Fatalf("ResolveUDPAddr: %v", err)
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	r := &recorder{conn: conn, upstream: raddr}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		for {
			buf := make([]byte, 65535)
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			r.wg.Add(1)
			go func() {
				defer r.wg.Done()
				r.serve(t, from, buf[:n])
			}()
		}
	}()
	t.Cleanup(func() {
		_ = conn.Close()
		r.wg.Wait()
	})
	return r
}

func (r *recorder) addr() string {
	return r.conn.LocalAddr().String()
}

// serve forwards one request to the upstream server and holds the answer for
// holdDelay before it sends the answer back to from.
func (r *recorder) serve(t *testing.T, from *net.UDPAddr, req []byte) {
	now := r.inFlight.Add(1)
	defer r.inFlight.Add(-1)
	for {
		peak := r.peak.Load()
		if now <= peak || r.peak.CompareAndSwap(peak, now) {
			break
		}
	}
	up, err := net.DialUDP("udp", nil, r.upstream)
	if err != nil {
		t.Errorf("DialUDP: %v", err)
		return
	}
	defer up.Close() //nolint:errcheck // The test reads nothing more from the socket.
	if _, err := up.Write(req); err != nil {
		t.Errorf("forward the request: %v", err)
		return
	}
	if err := up.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Errorf("SetReadDeadline: %v", err)
		return
	}
	buf := make([]byte, 65535)
	n, err := up.Read(buf)
	if err != nil {
		t.Errorf("read the answer: %v", err)
		return
	}
	time.Sleep(holdDelay)
	// The write of the answer is the end of the lookup. A write after the close of
	// the recorder occurs only at the end of the test.
	if _, err := r.conn.WriteToUDP(buf[:n], from); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Errorf("send the answer: %v", err)
	}
	r.served.Add(1)
}

func TestOneBenchmarkRunSendsNoTwoLookupsInParallel(t *testing.T) {
	r, err := baseline.Start(t.Context(), baseline.Config{DNSAddr: "127.0.0.1:0", CoAPAddr: "127.0.0.1:0", HTTPSAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	tg := r.Target()
	stats := t.TempDir()
	writeStats(t, stats, "0", "0", "0", "0")
	const n = 8
	// At loss 0, each lookup of these baselines sends one UDP request and gets one UDP
	// answer, so the recorder sees the start and the end of each lookup.
	for name, upstream := range map[string]string{
		baseline.QMuxPrivate: tg.DNS, baseline.DNSIn: tg.DNS, baseline.DNSInTXT: tg.DNS, baseline.CoAP: tg.CoAP,
	} {
		rec := newRecorder(t, upstream)
		var out, errOut bytes.Buffer
		args := []string{"--baseline", name, "--n", strconv.Itoa(n), "--stats", stats,
			"--dns", rec.addr(), "--coap", rec.addr(), "--https", tg.HTTPS}
		if code := runLookups(t.Context(), args, &out, &errOut); code != 0 {
			t.Fatalf("%s: exit %d: %s", name, code, errOut.String())
		}
		if got := rec.served.Load(); got != n {
			t.Errorf("%s: the recorder served %d lookups, want %d", name, got, n)
		}
		if got := rec.peak.Load(); got != 1 {
			t.Errorf("%s: %d lookups of one benchmark run were in progress at one time, want 1 (FR-bench-17)", name, got)
		}
	}
}
