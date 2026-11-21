package baseline

import (
	"fmt"
	"time"
)

// Deadline is the deadline of one lookup (FR-bench-18).
const Deadline = 5 * time.Second

// The retry settings of the DNS baselines (FR-bench-19).
const (
	dnsAttemptTimeout = time.Second
	dnsAttempts       = 5
)

// The transmission settings of the coap baseline (FR-bench-20). The go-coap v3.8.2
// defaults are 2 s, 1.5 and 4 (udp/client/config.go).
const (
	coapAckTimeout    = time.Second
	coapRandomFactor  = 1.0
	coapMaxRetransmit = 4
	// coapTick is the period of the retransmission check. It adds at most 10 ms to
	// each ACK timeout.
	coapTick = 10 * time.Millisecond
)

// Retry states the retry settings of one baseline (FR-bench-20a). The JSON names are
// the names of the retry object in the results file.
type Retry struct {
	// Policy states the retry behaviour in words.
	Policy string `json:"policy"`
	// AttemptTimeoutMS is the time in milliseconds before the first repeat of the
	// request. It is 0 when the client sets no timeout of its own.
	AttemptTimeoutMS int64 `json:"attempt_timeout_ms,omitempty"`
	// MaxAttempts is the largest number of requests that the client sends for one
	// lookup. It is 0 when the client sets no limit of its own.
	MaxAttempts int `json:"max_attempts,omitempty"`
	// RandomFactor is the CoAP ACK_RANDOM_FACTOR. It is 0 for the other baselines.
	RandomFactor float64 `json:"random_factor,omitempty"`
}

// RetryPolicy returns the retry settings of the baseline name. It returns the zero
// Retry for a name that is not in Names.
func RetryPolicy(name string) Retry {
	switch name {
	case QMuxPrivate, DNSIn, DNSInTXT:
		return Retry{
			Policy: fmt.Sprintf("a new UDP query on a new socket after each %v attempt timeout, "+
				"at most %d queries; one TCP query after a truncated UDP answer", dnsAttemptTimeout, dnsAttempts),
			AttemptTimeoutMS: dnsAttemptTimeout.Milliseconds(),
			MaxAttempts:      dnsAttempts,
		}
	case CoAP:
		return Retry{
			Policy: fmt.Sprintf("confirmable retransmission with binary exponential back-off (RFC 7252 4.2): "+
				"ACK timeout %v, random factor %.1f, %d retransmissions", coapAckTimeout, coapRandomFactor, coapMaxRetransmit),
			AttemptTimeoutMS: coapAckTimeout.Milliseconds(),
			MaxAttempts:      1 + coapMaxRetransmit,
			RandomFactor:     coapRandomFactor,
		}
	case HTTPSCold, HTTPSReuse:
		return Retry{
			Policy: "no application retry; the Linux TCP defaults of the client container " +
				"repeat lost segments (see tcp_syn_retries in the environment block)",
		}
	}
	return Retry{}
}
