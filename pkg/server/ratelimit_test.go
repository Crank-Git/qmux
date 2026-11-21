package server

import (
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/transport"
)

// start is the time of the first query in the rate limit tests. The limiter reads the
// time from each query, so the tests set it and never sleep.
var start = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// queryFrom returns a valid query from addr that the server received at t.
func queryFrom(addr string, t time.Time) *transport.Query {
	q := validQuery()
	q.Source = netip.AddrPortFrom(netip.MustParseAddr(addr), 53000)
	q.ReceivedAt = t
	return q
}

// sourceAddr returns a distinct IPv4 address for each i below 2^24.
func sourceAddr(i int) netip.Addr {
	return netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)})
}

func TestServeQueryAnswersTheBurstAndRefusesTheNextQueryFromOneSource(t *testing.T) {
	s := newTestServer(t, ServerConfig{RateLimit: RateLimit{QPS: 1, Burst: 2}}, payloadHandler([]byte("ok")))
	checkRCode(t, answer(s, queryFrom("192.0.2.1", start)), transport.RCodeNoError)
	checkRCode(t, answer(s, queryFrom("192.0.2.1", start)), transport.RCodeNoError)

	got := answer(s, queryFrom("192.0.2.1", start))
	checkRCode(t, got, transport.RCodeRefused)
	if got[0].Payload != nil {
		t.Fatalf("the REFUSED answer holds %d payload bytes, want no answer", len(got[0].Payload))
	}
}

func TestServeQueryKeepsOneBucketForEachSourceAddress(t *testing.T) {
	s := newTestServer(t, ServerConfig{RateLimit: RateLimit{QPS: 1, Burst: 1}}, payloadHandler(nil))
	checkRCode(t, answer(s, queryFrom("192.0.2.1", start)), transport.RCodeNoError)
	checkRCode(t, answer(s, queryFrom("192.0.2.2", start)), transport.RCodeNoError)
	checkRCode(t, answer(s, queryFrom("192.0.2.1", start)), transport.RCodeRefused)
}

func TestServeQueryKeysTheBucketOnTheAddressAndNotThePort(t *testing.T) {
	s := newTestServer(t, ServerConfig{RateLimit: RateLimit{QPS: 1, Burst: 1}}, payloadHandler(nil))
	q := queryFrom("192.0.2.1", start)
	checkRCode(t, answer(s, q), transport.RCodeNoError)
	q.Source = netip.AddrPortFrom(q.Source.Addr(), 53001)
	checkRCode(t, answer(s, q), transport.RCodeRefused)
}

func TestServeQueryAnswersAgainAfterTheBucketRefills(t *testing.T) {
	s := newTestServer(t, ServerConfig{RateLimit: RateLimit{QPS: 2, Burst: 1}}, payloadHandler(nil))
	checkRCode(t, answer(s, queryFrom("192.0.2.1", start)), transport.RCodeNoError)
	checkRCode(t, answer(s, queryFrom("192.0.2.1", start.Add(400*time.Millisecond))), transport.RCodeRefused)
	checkRCode(t, answer(s, queryFrom("192.0.2.1", start.Add(500*time.Millisecond))), transport.RCodeNoError)
}

func TestServeQueryWithQPSZeroAnswers500QueriesInOneSecondFromOneSource(t *testing.T) {
	for _, rl := range []RateLimit{{}, {QPS: 0, Burst: 100}} {
		s := newTestServer(t, ServerConfig{RateLimit: rl}, payloadHandler(nil))
		for i := range 500 {
			at := start.Add(time.Duration(i) * 2 * time.Millisecond)
			if got := answer(s, queryFrom("192.0.2.1", at)); len(got) != 1 || got[0].RCode != transport.RCodeNoError {
				t.Fatalf("RateLimit %+v: query %d got %+v, want one NOERROR answer", rl, i, got)
			}
		}
	}
}

func TestServeQueryAnswersAbout100Of200QueriesInOneSecondWithTheQmuxdDefaults(t *testing.T) {
	s := newTestServer(t, ServerConfig{RateLimit: RateLimit{QPS: 50, Burst: 100}}, payloadHandler(nil))
	answered := 0
	for i := range 200 {
		at := start.Add(time.Duration(i) * 5 * time.Millisecond)
		if answer(s, queryFrom("192.0.2.1", at))[0].RCode == transport.RCodeNoError {
			answered++
		}
	}
	// The bucket holds 100 tokens and refills 50 tokens in the second.
	if answered < 100 || answered > 150 {
		t.Fatalf("answered %d of 200 queries, want 100 to 150", answered)
	}
}

func TestServeQueryLogsARateLimitedQueryAtDebugLevelAndWritesItsObservationLine(t *testing.T) {
	var log, obs syncBuffer
	s := newTestServer(t, ServerConfig{
		RateLimit: RateLimit{QPS: 1, Burst: 1}, Logger: debugLogger(&log), ObservationLog: &obs,
	}, payloadHandler(nil))
	for range 3 {
		answer(s, queryFrom("192.0.2.1", start))
	}
	out := log.String()
	if n := countLevel(out, slog.LevelInfo); n != 1 {
		t.Fatalf("the log holds %d info records, want 1 for the answered query: %q", n, out)
	}
	if n := strings.Count(out, "level=DEBUG msg=query"); n != 2 || strings.Count(out, "rcode=REFUSED") != 2 {
		t.Fatalf("the log holds %d debug query records, want 2 with rcode=REFUSED: %q", n, out)
	}
	if n := strings.Count(obs.String(), "\n"); n != 3 {
		t.Fatalf("the observation log holds %d lines, want 1 for each of 3 queries", n)
	}
}

func TestServeQueryLogsAQueryOutsideTheZonesAtInfoLevel(t *testing.T) {
	var log syncBuffer
	s := newTestServer(t, ServerConfig{Zones: []string{"other.example."}, Logger: debugLogger(&log)}, payloadHandler(nil))
	checkRCode(t, answer(s, validQuery()), transport.RCodeRefused)
	if out := log.String(); !strings.Contains(out, "level=INFO msg=query") {
		t.Fatalf("the log holds %q, want one info query record", out)
	}
}

func TestRateLimiterRemovesABucketThatIsFullFor60Seconds(t *testing.T) {
	l := newRateLimiter(RateLimit{QPS: 1, Burst: 2})
	a := netip.MustParseAddr("192.0.2.1")
	b := netip.MustParseAddr("192.0.2.2")
	l.allow(a, start)
	// The bucket of a is full 1 s after start, because it gave one token at 1 QPS.
	l.allow(b, start.Add(60*time.Second))
	if _, ok := l.buckets[a]; !ok {
		t.Fatal("the limiter removed the bucket of a after 59 s full, want it kept")
	}

	l.allow(b, start.Add(61*time.Second))
	if _, ok := l.buckets[a]; ok {
		t.Fatal("the limiter kept the bucket of a after 60 s full, want it removed")
	}
	if _, ok := l.buckets[b]; !ok {
		t.Fatal("the limiter removed the bucket of b, which is not full")
	}
}

func TestRateLimiterKeepsABucketThatIsNotFull(t *testing.T) {
	l := newRateLimiter(RateLimit{QPS: 0.01, Burst: 2})
	a := netip.MustParseAddr("192.0.2.1")
	l.allow(a, start)
	// At 0.01 QPS, the bucket of a needs 100 s to refill one token.
	l.allow(netip.MustParseAddr("192.0.2.2"), start.Add(90*time.Second))
	if _, ok := l.buckets[a]; !ok {
		t.Fatal("the limiter removed the bucket of a before it was full")
	}
}

func TestRateLimiterAppliesTheSharedBucketToSource65537(t *testing.T) {
	l := newRateLimiter(RateLimit{QPS: 1, Burst: 2})
	for i := range maxSources {
		if !l.allow(sourceAddr(i), start) {
			t.Fatalf("source %d was refused, want its own full bucket", i)
		}
	}
	last := sourceAddr(maxSources)
	for i := range 2 {
		if !l.allow(last, start) {
			t.Fatalf("source 65537 was refused at token %d, before the shared bucket was empty", i+1)
		}
	}
	if l.allow(last, start) {
		t.Fatal("source 65537 got a third token from a shared bucket of 2")
	}
	if l.allow(sourceAddr(maxSources+1), start) {
		t.Fatal("source 65538 got a token, want the empty shared bucket")
	}
	if n := len(l.buckets); n != maxSources {
		t.Fatalf("the table holds %d sources, want %d", n, maxSources)
	}
	if !l.allow(sourceAddr(0), start) {
		t.Fatal("source 1 was refused, want its own bucket")
	}
}

func TestRateLimiterTreatsAnIPv4MappedAddressAsTheIPv4Address(t *testing.T) {
	l := newRateLimiter(RateLimit{QPS: 1, Burst: 1})
	l.allow(netip.MustParseAddr("192.0.2.1"), start)
	if l.allow(netip.MustParseAddr("::ffff:192.0.2.1"), start) {
		t.Fatal("the IPv4-mapped address got a token from a second bucket")
	}
}

func TestNewRateLimiterReturnsNilWhenQPSIsZero(t *testing.T) {
	if l := newRateLimiter(RateLimit{Burst: 100}); l != nil {
		t.Fatal("newRateLimiter with QPS 0 returned a limiter, want nil")
	}
}

func TestRateLimiterGivesExactlyTheBurstToConcurrentQueries(t *testing.T) {
	l := newRateLimiter(RateLimit{QPS: 1, Burst: 100})
	a := netip.MustParseAddr("192.0.2.1")
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				if l.allow(a, start) {
					allowed.Add(1)
				}
			}
		})
	}
	wg.Wait()
	if n := allowed.Load(); n != 100 {
		t.Fatalf("the limiter gave %d tokens to 400 concurrent queries, want 100", n)
	}
}
