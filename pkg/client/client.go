// Package client provides a DNS query client for QMux. It sends queries
// directly to a configured server using the miekg/dns wire format, retries
// on timeout, falls back to TCP on truncation, and validates that responses
// match the original question.
package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/transport"
	"github.com/miekg/dns"
)

// Default configuration values.
const (
	defaultUDPSize        = 1232
	defaultAttemptTimeout = 1 * time.Second
	defaultAttempts       = 3
)

// ClientConfig holds the configuration for a Client.
type ClientConfig struct {
	// Resolver is the "host:port" string of the DNS server to query.
	Resolver string

	// Network is "udp" (default) or "tcp".
	Network string

	// AttemptTimeout is the maximum time to wait for a single attempt response.
	AttemptTimeout time.Duration

	// Attempts is the maximum number of attempts (UDP only).
	Attempts int

	// UDPSize is the UDP size to advertise in the OPT RR for UDP queries.
	UDPSize uint16

	// DisableTCPFallback prevents TCP fallback when a UDP response has TC set.
	DisableTCPFallback bool

	// Recursion sets the RD flag when true (for queries through a resolver).
	Recursion bool

	// Logger is the logger to use. When nil, slog.Default() is used.
	Logger *slog.Logger
}

// Query describes a single DNS question to send.
type Query struct {
	// Name is the QNAME to query (may or may not have a trailing dot).
	Name string

	// Class is the QCLASS.
	Class uint16

	// Type is the QTYPE.
	Type uint16

	// Version is the version label value. When non-zero, the client adds
	// the _v<N> label as the leftmost label of the QNAME.
	Version uint16
}

// Result holds the outcome of a successful query.
type Result struct {
	Name          string
	Key           protocol.ProtocolKey
	RCode         int
	Payload       []byte
	TTL           uint32
	Transport     string // "udp" or "tcp"
	FellBack      bool
	RTT           time.Duration
	QueryBytes    int
	ResponseBytes int
	Verified      bool
}

// ErrorKind identifies the kind of DNS error.
type ErrorKind string

const (
	KindRCode      ErrorKind = "rcode"
	KindTimeout    ErrorKind = "timeout"
	KindNetwork    ErrorKind = "network"
	KindMismatch   ErrorKind = "mismatch"
	KindMalformed  ErrorKind = "malformed"
	KindTruncation ErrorKind = "truncation"
)

// DNSError is an error returned by Client.Query for DNS-level failures.
type DNSError struct {
	Kind  ErrorKind
	RCode int
	Name  string
	Key   protocol.ProtocolKey
	Err   error
}

func (e *DNSError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("dns: %s for %s %s", e.Kind, e.Name, e.Key)
	}
	switch e.Kind {
	case KindRCode:
		return fmt.Sprintf("dns: rcode %s for %s %s", dns.RcodeToString[e.RCode], e.Name, e.Key)
	case KindTimeout:
		return fmt.Sprintf("dns: timeout for %s %s", e.Name, e.Key)
	case KindNetwork:
		return fmt.Sprintf("dns: network error for %s %s", e.Name, e.Key)
	case KindMismatch:
		return fmt.Sprintf("dns: mismatch for %s %s", e.Name, e.Key)
	case KindMalformed:
		return fmt.Sprintf("dns: malformed response for %s %s", e.Name, e.Key)
	case KindTruncation:
		return fmt.Sprintf("dns: truncation for %s %s", e.Name, e.Key)
	default:
		return fmt.Sprintf("dns: %s for %s %s", e.Kind, e.Name, e.Key)
	}
}

func (e *DNSError) Unwrap() error { return e.Err }

// AppError is returned when a protocol handler reports an application-level error.
type AppError struct {
	Status     protocol.Status
	ProtocolID string
	Version    uint16
}

func (e *AppError) Error() string {
	return fmt.Sprintf("app: %d (protocol=%s, version=%d)", e.Status, e.ProtocolID, e.Version)
}

// VerifyError is returned when response verification fails.
type VerifyError struct {
	Reason string
	Err    error
}

func (e *VerifyError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("verify: %s: %v", e.Reason, e.Err)
	}
	return fmt.Sprintf("verify: %s", e.Reason)
}

// Unwrap returns the underlying error for VerifyError.
func (e *VerifyError) Unwrap() error { return e.Err }

// Client sends DNS queries to a configured server.
type Client struct {
	cfg ClientConfig
	log *slog.Logger
}

// NewClient creates a new Client with the given configuration.
// Zero fields receive sensible defaults.
func NewClient(cfg ClientConfig) *Client {
	if cfg.AttemptTimeout == 0 {
		cfg.AttemptTimeout = defaultAttemptTimeout
	}
	if cfg.Attempts == 0 {
		cfg.Attempts = defaultAttempts
	}
	if cfg.UDPSize == 0 {
		cfg.UDPSize = defaultUDPSize
	}
	if cfg.Network == "" {
		cfg.Network = "udp"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	return &Client{cfg: cfg, log: cfg.Logger}
}

// Query sends a DNS question to the configured server and returns the result.
func (c *Client) Query(ctx context.Context, q Query) (*Result, error) {
	// Ensure QNAME is fully qualified and lowercase.
	name := q.Name
	if !strings.HasSuffix(name, ".") {
		name = name + "."
	}
	name = strings.ToLower(name)

	// The actual name sent to the server (includes version label).
	sentName := name
	if q.Version != 0 {
		sentName = fmt.Sprintf("_v%d.%s", q.Version, name)
	}

	// Build the question message with the actual name (with version label).
	msg := c.buildQuery(sentName, q)

	// Determine network.
	network := c.cfg.Network
	if network == "" {
		network = "udp"
	}

	if network == "tcp" {
		return c.sendTCP(ctx, msg, sentName, q)
	}

	return c.sendUDP(ctx, msg, sentName, q)
}

// buildQuery creates a DNS query message with the given name and query parameters.
func (c *Client) buildQuery(name string, q Query) *dns.Msg {
	m := new(dns.Msg)
	m.SetQuestion(name, q.Type)
	m.Question[0].Qclass = q.Class
	m.RecursionDesired = false

	// FR-client-5: Add OPT RR for UDP queries.
	m.SetEdns0(c.cfg.UDPSize, false)

	return m
}

// sendUDP sends a UDP query with retries.
func (c *Client) sendUDP(ctx context.Context, msg *dns.Msg, sentName string, q Query) (*Result, error) {
	udpSize := int(c.cfg.UDPSize)
	if udpSize < dns.MinMsgSize {
		udpSize = dns.MinMsgSize
	}

	key := protocol.ProtocolKey{Class: q.Class, Type: q.Type}

	for attempt := 0; attempt < c.cfg.Attempts; attempt++ {
		// FR-client-7: Use a new random query ID for each attempt.
		attemptMsg := c.withRandomID(msg)

		attemptCtx, attemptCancel := context.WithTimeout(ctx, c.cfg.AttemptTimeout)
		resp, rtt, queryBytes, responseBytes, err := transport.SendUDP(attemptCtx, c.cfg.Resolver, attemptMsg, udpSize)
		attemptCancel()

		if err != nil {
			// Check if the context deadline was exceeded (timeout) or cancelled.
			if errors.Is(err, context.Canceled) {
				return nil, &DNSError{
					Kind: KindTimeout,
					Name: sentName,
					Key:  key,
					Err:  err,
				}
			}
			if errors.Is(err, context.DeadlineExceeded) {
				// FR-client-12: Timeout — try again.
				continue
			}
			// FR-client-23: Network error.
			return nil, &DNSError{
				Kind: KindNetwork,
				Name: sentName,
				Key:  key,
				Err:  err,
			}
		}

		// FR-client-14: Check TC bit for TCP fallback.
		if resp.Truncated {
			if c.cfg.DisableTCPFallback {
				// FR-client-15: Truncation error.
				return nil, &DNSError{
					Kind: KindTruncation,
					Name: sentName,
					Key:  key,
				}
			}
			// FR-client-14: Repeat over TCP.
			result, err := c.doTCP(sentName, q, attemptMsg, rtt, queryBytes, responseBytes)
			if err != nil {
				return nil, err
			}
			return result, nil
		}

		// FR-client-11: A response with any RCODE stops retries.
		result, err := c.matchResponseWithSizes(resp, sentName, q, "udp", false, queryBytes, responseBytes, rtt)
		if err != nil {
			return nil, err
		}
		return result, nil
	}

	// All attempts exhausted — timeout.
	return nil, &DNSError{
		Kind: KindTimeout,
		Name: sentName,
		Key:  key,
	}
}

// sendTCP sends a single TCP query.
func (c *Client) sendTCP(ctx context.Context, msg *dns.Msg, sentName string, q Query) (*Result, error) {
	resp, rtt, queryBytes, responseBytes, err := transport.SendTCP(ctx, c.cfg.Resolver, msg)
	if err != nil {
		return nil, &DNSError{
			Kind: KindNetwork,
			Name: sentName,
			Key:  protocol.ProtocolKey{Class: q.Class, Type: q.Type},
			Err:  err,
		}
	}

	// TCP should never have TC set (full message always delivered).
	if resp.Truncated {
		return nil, &DNSError{
			Kind: KindMalformed,
			Name: sentName,
			Key:  protocol.ProtocolKey{Class: q.Class, Type: q.Type},
		}
	}

	return c.matchResponseWithSizes(resp, sentName, q, "tcp", false, queryBytes, responseBytes, rtt)
}

// doTCP sends the query over TCP after a UDP truncation.
func (c *Client) doTCP(sentName string, q Query, queryMsg *dns.Msg, rtt time.Duration, queryBytes, responseBytes int) (*Result, error) {
	// Rebuild the message for TCP (with random ID).
	tcpMsg := c.withRandomID(queryMsg)

	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.AttemptTimeout)
	defer cancel()

	resp, tcpRTT, tcpQueryBytes, tcpResponseBytes, err := transport.SendTCP(ctx, c.cfg.Resolver, tcpMsg)
	if err != nil {
		return nil, &DNSError{
			Kind: KindNetwork,
			Name: sentName,
			Key:  protocol.ProtocolKey{Class: q.Class, Type: q.Type},
			Err:  err,
		}
	}

	if resp.Truncated {
		return nil, &DNSError{
			Kind: KindMalformed,
			Name: sentName,
			Key:  protocol.ProtocolKey{Class: q.Class, Type: q.Type},
		}
	}

	return c.matchResponseWithSizes(resp, sentName, q, "tcp", true, tcpQueryBytes, tcpResponseBytes, tcpRTT)
}

// matchResponseWithSizes validates the response and builds a Result.
func (c *Client) matchResponseWithSizes(resp *dns.Msg, sentName string, q Query, transport string, fellBack bool, queryBytes, responseBytes int, rtt time.Duration) (*Result, error) {
	key := protocol.ProtocolKey{Class: q.Class, Type: q.Type}

	// FR-client-17: Check that the response question matches the query.
	if len(resp.Question) != 1 {
		return nil, &DNSError{
			Kind: KindMismatch,
			Name: sentName,
			Key:  key,
		}
	}

	respQ := resp.Question[0]
	// FR-client-18: Compare names case-insensitively.
	if strings.ToLower(respQ.Name) != sentName {
		return nil, &DNSError{
			Kind: KindMismatch,
			Name: sentName,
			Key:  key,
		}
	}
	if respQ.Qclass != q.Class {
		return nil, &DNSError{
			Kind: KindMismatch,
			Name: sentName,
			Key:  key,
		}
	}
	if respQ.Qtype != q.Type {
		return nil, &DNSError{
			Kind: KindMismatch,
			Name: sentName,
			Key:  key,
		}
	}

	// FR-client-21: Check RCODE.
	if resp.Rcode != dns.RcodeSuccess {
		return nil, &DNSError{
			Kind:  KindRCode,
			RCode: resp.Rcode,
			Name:  sentName,
			Key:   key,
		}
	}

	// FR-client-19: Check exactly one answer RR.
	if len(resp.Answer) != 1 {
		return nil, &DNSError{
			Kind: KindMismatch,
			Name: sentName,
			Key:  key,
		}
	}

	// FR-client-20: Check answer RR matches the question.
	ans := resp.Answer[0]
	if strings.ToLower(ans.Header().Name) != sentName {
		return nil, &DNSError{
			Kind: KindMismatch,
			Name: sentName,
			Key:  key,
		}
	}
	if ans.Header().Class != q.Class {
		return nil, &DNSError{
			Kind: KindMismatch,
			Name: sentName,
			Key:  key,
		}
	}
	if ans.Header().Rrtype != q.Type {
		return nil, &DNSError{
			Kind: KindMismatch,
			Name: sentName,
			Key:  key,
		}
	}

	// Extract payload from the answer RR.
	payload := extractPayload(ans)

	return &Result{
		Name:          sentName,
		Key:           key,
		RCode:         resp.Rcode,
		Payload:       payload,
		TTL:           ans.Header().Ttl,
		Transport:     transport,
		FellBack:      fellBack,
		RTT:           rtt,
		QueryBytes:    queryBytes,
		ResponseBytes: responseBytes,
		Verified:      false,
	}, nil
}

// withRandomID returns a copy of the message with a new random query ID.
func (c *Client) withRandomID(m *dns.Msg) *dns.Msg {
	m2 := m.Copy()
	var buf [2]byte
	_, _ = rand.Read(buf[:])
	m2.Id = uint16(buf[0])<<8 | uint16(buf[1])
	return m2
}

// extractPayload extracts the opaque RDATA bytes from a DNS RR.
func extractPayload(rr dns.RR) []byte {
	hdr := rr.Header()
	if hdr.Rrtype == dns.TypeOPT {
		return nil
	}

	// RFC3597 (opaque encoding) — the Rdata field is the hex-encoded payload.
	if rfc3597, ok := rr.(*dns.RFC3597); ok {
		data, err := hex.DecodeString(rfc3597.Rdata)
		if err == nil && len(data) > 0 {
			return data
		}
	}

	// For known types, extract payload via type assertion.
	switch v := rr.(type) {
	case *dns.TXT:
		return []byte(strings.Join(v.Txt, ""))
	case *dns.NS:
		return []byte(v.Ns)
	case *dns.CNAME:
		return []byte(v.Target)
	case *dns.SOA:
		return nil
	case *dns.MX:
		return nil
	case *dns.A:
		return v.A.To4()
	case *dns.AAAA:
		return v.AAAA
	case *dns.CAA:
		return nil
	case *dns.SRV:
		return nil
	}

	// Fallback: pack the RR and extract RDATA using Rdlength.
	wire := make([]byte, 512)
	n, err := dns.PackRR(rr, wire, 0, nil, false)
	if err != nil {
		return nil
	}
	_, rdoff, err := dns.UnpackRR(wire[:n], 0)
	if err != nil {
		return nil
	}
	if rdoff >= len(wire) {
		return nil
	}
	return wire[rdoff:]
}
