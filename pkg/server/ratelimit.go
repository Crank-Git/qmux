package server

import (
	"net/netip"
	"sync"
	"time"
)

const (
	// maxSources is the number of source addresses that the limiter tracks
	// (FR-qmuxd-27). The limit keeps the memory of the table bounded under a flood of
	// spoofed sources.
	maxSources = 65536
	// idleTimeout is the time that a bucket stays full before the limiter removes it.
	// A full bucket holds no state that a new bucket does not hold, so the removal
	// changes no answer.
	idleTimeout = 60 * time.Second
	// sweepInterval is the shortest time between two scans of the table. One scan
	// reads every bucket, so the limiter does not scan for each query.
	sweepInterval = time.Second
)

// RateLimit holds the settings of the per-source token bucket of a Server.
//
// Each source IP address gets a bucket of Burst tokens that refills at QPS tokens per
// second. A query takes one token. When the bucket of the source is empty, the server
// answers REFUSED with no answer RR.
//
// A QPS of 0 or less disables the rate limit, so the zero value disables it. When QPS
// is above 0 and Burst is 0 or less, the bucket holds no token and the server refuses
// every query.
type RateLimit struct {
	// QPS is the refill rate of each bucket, in tokens per second.
	QPS float64
	// Burst is the capacity of each bucket, in tokens.
	Burst int
}

// bucket is one token bucket.
type bucket struct {
	tokens float64
	// last is the time of the last refill.
	last time.Time
}

// rateLimiter keeps one token bucket per source address. It reads the time from each
// query, so a test controls the time with no sleep.
type rateLimiter struct {
	qps   float64
	burst float64

	mu      sync.Mutex
	buckets map[netip.Addr]*bucket
	// shared is the bucket of every new source while the table is full
	// (FR-qmuxd-28).
	shared    bucket
	lastSweep time.Time
}

// newRateLimiter returns a limiter for rl, or nil when rl disables the rate limit.
func newRateLimiter(rl RateLimit) *rateLimiter {
	if rl.QPS <= 0 {
		return nil
	}
	burst := float64(max(rl.Burst, 0))
	return &rateLimiter{
		qps:     rl.QPS,
		burst:   burst,
		buckets: make(map[netip.Addr]*bucket),
		shared:  bucket{tokens: burst},
	}
}

// allow takes one token from the bucket of addr at the time now, and returns true. It
// returns false when the bucket is empty.
func (l *rateLimiter) allow(addr netip.Addr, now time.Time) bool {
	// An IPv4-mapped IPv6 address is the same host as the IPv4 address.
	addr = addr.Unmap()

	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastSweep) >= sweepInterval {
		l.lastSweep = now
		for a, b := range l.buckets {
			// The bucket is full (burst - tokens) / qps seconds after its last refill.
			// The comparison stays in seconds, so a very low QPS cannot overflow a
			// time.Duration.
			if now.Sub(b.last).Seconds() >= (l.burst-b.tokens)/l.qps+idleTimeout.Seconds() {
				delete(l.buckets, a)
			}
		}
	}
	b, ok := l.buckets[addr]
	switch {
	case ok:
	case len(l.buckets) < maxSources:
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[addr] = b
	default:
		b = &l.shared
	}
	// Concurrent queries can reach the limiter out of order. A negative interval adds
	// no tokens and keeps the later time.
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens = min(l.burst, b.tokens+elapsed.Seconds()*l.qps)
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
