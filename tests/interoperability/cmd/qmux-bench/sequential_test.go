package main

import (
	"bytes"
	"errors"
	"fmt"
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
// lookup when a request arrives and the end immediately before it sends the answer
// back. It serves each request on a goroutine of its own, so it does not put
// parallel lookups in sequence.
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
	for {
		peak := r.peak.Load()
		if now <= peak || r.peak.CompareAndSwap(peak, now) {
			break
		}
	}
	answer, err := r.forward(req)
	if err != nil {
		r.inFlight.Add(-1)
		t.Error(err)
		return
	}
	time.Sleep(holdDelay)
	// The lookup ends before the write of the answer. After the write, the client can
	// send its next request, and runLookups can return, before this goroutine runs
	// again. An end recorded after the write then counts two lookups in sequence as
	// parallel, and the test can read served before it counts the last lookup.
	r.inFlight.Add(-1)
	r.served.Add(1)
	// A write after the close of the recorder occurs only at the end of the test.
	if _, err := r.conn.WriteToUDP(answer, from); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Errorf("send the answer: %v", err)
	}
}

// forward sends req to the upstream server and returns its answer.
func (r *recorder) forward(req []byte) ([]byte, error) {
	up, err := net.DialUDP("udp", nil, r.upstream)
	if err != nil {
		return nil, fmt.Errorf("DialUDP: %w", err)
	}
	defer up.Close() //nolint:errcheck // The test reads nothing more from the socket.
	if _, err := up.Write(req); err != nil {
		return nil, fmt.Errorf("forward the request: %w", err)
	}
	if err := up.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		return nil, fmt.Errorf("SetReadDeadline: %w", err)
	}
	buf := make([]byte, 65535)
	n, err := up.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("read the answer: %w", err)
	}
	return buf[:n], nil
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
