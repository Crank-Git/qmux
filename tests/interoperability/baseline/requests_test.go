package baseline_test

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/tests/interoperability/baseline"
)

// blackhole is a UDP socket on loopback that counts the datagrams it receives and
// never answers. It stands in for a server behind a link with loss 100%.
type blackhole struct {
	conn *net.UDPConn
	got  atomic.Int64
	mu   sync.Mutex
	// at holds the arrival time of each datagram, in the order of arrival.
	at []time.Time
}

// newBlackhole starts a blackhole and closes it at the end of the test.
func newBlackhole(t *testing.T) *blackhole {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	b := &blackhole{conn: conn}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 2048)
		for {
			if _, _, err := conn.ReadFromUDP(buf); err != nil {
				return
			}
			b.mu.Lock()
			b.at = append(b.at, time.Now())
			b.mu.Unlock()
			b.got.Add(1)
		}
	}()
	t.Cleanup(func() {
		_ = conn.Close()
		<-done
	})
	return b
}

func (b *blackhole) addr() string {
	return b.conn.LocalAddr().String()
}

// arrivals returns the arrival time of each datagram as an offset from start.
func (b *blackhole) arrivals(start time.Time) []time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]time.Duration, len(b.at))
	for i, at := range b.at {
		out[i] = at.Sub(start)
	}
	return out
}

func TestEachBaselineRecordsOneRequestPerLookupAtLossZero(t *testing.T) {
	r := start(t, false)
	c := newClient(t, r)
	for _, name := range baseline.Names {
		for i := range 3 {
			_, requests, err := c.LookupRequests(t.Context(), name)
			if err != nil {
				t.Fatalf("%s lookup %d: %v", name, i, err)
			}
			if requests != 1 {
				t.Errorf("%s lookup %d: %d requests, want 1", name, i, requests)
			}
		}
	}
}

func TestADNSBaselineSendsFiveRequestsInTheDeadlineWhenNoAnswerArrives(t *testing.T) {
	t.Parallel()
	hole := newBlackhole(t)
	c, err := baseline.NewClient(baseline.Target{DNS: hole.addr(), CoAP: hole.addr(), HTTPS: "127.0.0.1:1"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(c.Close)
	ctx, cancel := context.WithTimeout(t.Context(), baseline.Deadline)
	defer cancel()
	start := time.Now()
	_, requests, err := c.LookupRequests(ctx, baseline.QMuxPrivate)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("lookup succeeded against a server that never answers")
	}
	if requests != 5 {
		t.Errorf("requests = %d, want 5", requests)
	}
	if got := hole.got.Load(); got != 5 {
		t.Errorf("the server received %d queries, want 5", got)
	}
	if elapsed < 4900*time.Millisecond || elapsed > 5500*time.Millisecond {
		t.Errorf("the lookup failed after %v, want about 5 s", elapsed)
	}
}

func TestTheCoAPBaselineRetransmitsAfterOneSecond(t *testing.T) {
	t.Parallel()
	hole := newBlackhole(t)
	c, err := baseline.NewClient(baseline.Target{DNS: hole.addr(), CoAP: hole.addr(), HTTPS: "127.0.0.1:1"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(c.Close)
	// The go-coap defaults (2 s, random factor 1.5, a check every 4 s) send no
	// retransmission in 1.8 s. An ACK timeout of 1 s with random factor 1.0 sends
	// one at 1 s (FR-bench-20).
	ctx, cancel := context.WithTimeout(t.Context(), 1800*time.Millisecond)
	defer cancel()
	_, requests, err := c.LookupRequests(ctx, baseline.CoAP)
	if err == nil {
		t.Fatal("lookup succeeded against a server that never answers")
	}
	if got := hole.got.Load(); got != 2 {
		t.Errorf("the server received %d transmissions, want 2", got)
	}
	if requests != int(hole.got.Load()) {
		t.Errorf("requests = %d, the server received %d", requests, hole.got.Load())
	}
}

func TestTheCoAPBaselineSendsThreeTransmissionsAtZeroOneAndThreeSecondsInTheDeadline(t *testing.T) {
	t.Parallel()
	hole := newBlackhole(t)
	c, err := baseline.NewClient(baseline.Target{DNS: hole.addr(), CoAP: hole.addr(), HTTPS: "127.0.0.1:1"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(c.Close)
	// go-coap has no clock to replace, so the test waits the full 5 s deadline. RFC
	// 7252 doubles the 1 s ACK timeout after each transmission, so the fourth
	// transmission is due at 7 s, after the deadline (FR-bench-20).
	ctx, cancel := context.WithTimeout(t.Context(), baseline.Deadline)
	defer cancel()
	start := time.Now()
	_, requests, err := c.LookupRequests(ctx, baseline.CoAP)
	if err == nil {
		t.Fatal("lookup succeeded against a server that never answers")
	}
	at := hole.arrivals(start)
	if len(at) != 3 {
		t.Fatalf("the server received %d transmissions at %v, want 3", len(at), at)
	}
	if requests != len(at) {
		t.Errorf("requests = %d, the server received %d", requests, len(at))
	}
	t.Logf("coap: transmissions at %v", at)
	// The window of each transmission allows for the 10 ms retransmission check and
	// for the scheduler of a loaded test host.
	for i, want := range []time.Duration{0, time.Second, 3 * time.Second} {
		if at[i] < want-50*time.Millisecond || at[i] > want+300*time.Millisecond {
			t.Errorf("transmission %d arrived at %v, want about %v (all: %v)", i+1, at[i], want, at)
		}
	}
}

func TestEveryBaselineHasARetryPolicy(t *testing.T) {
	for _, name := range baseline.Names {
		p := baseline.RetryPolicy(name)
		if p.Policy == "" {
			t.Errorf("%s: the retry policy has no text", name)
		}
	}
	dns := baseline.RetryPolicy(baseline.DNSIn)
	if dns.AttemptTimeoutMS != 1000 || dns.MaxAttempts != 5 {
		t.Errorf("dns-in retry = %+v, want attempt timeout 1000 ms and 5 attempts", dns)
	}
	coap := baseline.RetryPolicy(baseline.CoAP)
	if coap.AttemptTimeoutMS != 1000 || coap.RandomFactor != 1.0 || coap.MaxAttempts != 5 {
		t.Errorf("coap retry = %+v, want ACK timeout 1000 ms, factor 1.0, 5 transmissions", coap)
	}
}
