package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/transport"
)

// The defaults come from the timeouts table in docs/specs/spec.md.
const (
	defaultAttemptTimeout = time.Second
	defaultAttempts       = 3
	defaultUDPSize        = 1232
)

// ClientConfig is the configuration of a Client. NewClient replaces each zero or
// negative field with its default.
type ClientConfig struct {
	// Resolver is the IP address and port of the server, for example "127.0.0.1:5353".
	// A host name is an error at the first query, because the client never resolves a
	// name through DNS.
	Resolver string
	// Network is "udp" or "tcp". The empty string means "udp". Any other value is an
	// error at the first query.
	Network string
	// AttemptTimeout is the time that one attempt waits for a response. The default is
	// 1s.
	AttemptTimeout time.Duration
	// Attempts is the number of UDP attempts. The default is 3. The TCP fallback is one
	// extra attempt beyond Attempts. With Network "tcp", the client makes one attempt.
	Attempts int
	// UDPSize is the UDP size that the OPT RR of each query advertises. The default is
	// 1232.
	UDPSize uint16
	// DisableTCPFallback stops the TCP query after a UDP response with the TC bit. The
	// client then returns a truncation DNSError.
	DisableTCPFallback bool
	// Recursion sets the RD bit, for a query through a recursive resolver.
	Recursion bool
}

// Query names the resource that Client.Query asks for.
type Query struct {
	// Name is the QNAME. Client.Query adds the final "." when it is absent.
	Name  string
	Class uint16
	Type  uint16
	// Version adds the label "_v<Version>" before Name when it is not 0.
	Version uint16
}

// Result is the answer to a query.
type Result struct {
	// Name is the QNAME that the client sent, with the version label.
	Name  string
	Key   protocol.ProtocolKey
	RCode int
	// Payload holds the RDATA bytes of the answer RR, or nil without an answer RR.
	Payload []byte
	// TTL is the TTL of the answer RR. It is informational and never a security expiry.
	TTL uint32
	// FellBack is true when the client repeated the query over TCP after a UDP response
	// with the TC bit.
	FellBack bool
}

// Client sends queries to one server. A Client is safe for concurrent use, because it
// holds only its configuration.
type Client struct {
	cfg ClientConfig
}

// NewClient returns a client that sends every query to cfg.Resolver. It applies the
// defaults to each zero or negative field of cfg.
func NewClient(cfg ClientConfig) *Client {
	if cfg.AttemptTimeout <= 0 {
		cfg.AttemptTimeout = defaultAttemptTimeout
	}
	if cfg.Attempts <= 0 {
		cfg.Attempts = defaultAttempts
	}
	if cfg.UDPSize == 0 {
		cfg.UDPSize = defaultUDPSize
	}
	return &Client{cfg: cfg}
}

// Query sends q to the resolver and returns the matching response.
//
// Query sends over TCP when Network is "tcp", and over UDP otherwise. Each attempt uses
// a new query with a new random ID. If a UDP attempt gets no response within
// AttemptTimeout, Query makes a new attempt, up to Attempts attempts. Query makes no
// new attempt after a TCP timeout.
//
// If a UDP response has the TC bit, Query repeats the query once over TCP and sets
// Result.FellBack. DisableTCPFallback stops this TCP query.
//
// Query returns a *DNSError for each DNS outcome without a result:
//   - KindNetwork when the resolver is not an IP address and port, when Network is not
//     "udp", "tcp" or empty, or when the dial, the write or the read fails.
//   - KindTimeout when every attempt times out, or when ctx expires.
//   - KindTruncation when the UDP response has the TC bit and DisableTCPFallback is
//     true.
//   - KindMismatch when the response does not answer q.
//   - KindMalformed when the response does not parse, or when a TCP response has the TC
//     bit.
//   - KindRCode when the RCODE is not NOERROR.
//
// When ctx is cancelled, the error wraps context.Canceled and is not a DNSError.
func (c *Client) Query(ctx context.Context, q Query) (*Result, error) {
	name := qname(q)
	key := protocol.ProtocolKey{Class: q.Class, Type: q.Type}
	server, err := netip.ParseAddrPort(c.cfg.Resolver)
	if err != nil {
		return nil, &DNSError{Kind: KindNetwork, Name: name, Key: key,
			Err: fmt.Errorf("resolver %q is not an IP address and port: %w", c.cfg.Resolver, err)}
	}

	network, attempts := transport.UDP, c.cfg.Attempts
	switch c.cfg.Network {
	case "", transport.UDP:
	case transport.TCP:
		// The client never retries over TCP on a timeout.
		network, attempts = transport.TCP, 1
	default:
		return nil, &DNSError{Kind: KindNetwork, Name: name, Key: key,
			Err: fmt.Errorf("network %q is not %q or %q", c.cfg.Network, transport.UDP, transport.TCP)}
	}

	var last *DNSError
	for range attempts {
		p, err := transport.NewQuery(name, key, c.cfg.UDPSize, c.cfg.Recursion)
		if err != nil {
			return nil, fmt.Errorf("client: %w", err)
		}
		res, err := c.attempt(ctx, network, server, p)
		// Only a UDP attempt can return a truncation error, because a TCP response with
		// the TC bit is malformed.
		if errors.As(err, &last) && last.Kind == KindTruncation && !c.cfg.DisableTCPFallback {
			return c.fallback(ctx, server, p)
		}
		// Only the end of the attempt timeout starts a new attempt. A result, the end of
		// ctx and every other error stop the query.
		if !errors.As(err, &last) || last.Kind != KindTimeout || ctx.Err() != nil {
			return res, err
		}
	}
	last.Err = fmt.Errorf("no response after %d attempts to %s: %w", attempts, server, last.Err)
	return nil, last
}

// fallback repeats p once over TCP after a UDP response with the TC bit. This TCP
// attempt is one extra attempt beyond Attempts.
func (c *Client) fallback(ctx context.Context, server netip.AddrPort, p transport.Packet) (*Result, error) {
	res, err := c.attempt(ctx, transport.TCP, server, p)
	if err != nil {
		return nil, err
	}
	res.FellBack = true
	return res, nil
}

// attempt sends p to server once over a new socket. network is transport.UDP or
// transport.TCP. A new socket for each attempt is necessary, because the exchange sets
// the deadline of the socket to the past when its context ends.
//
// attempt returns a *DNSError for every error, except an error that wraps
// context.Canceled.
func (c *Client) attempt(ctx context.Context, network string, server netip.AddrPort, p transport.Packet) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.AttemptTimeout)
	defer cancel()

	got, err := exchange(ctx, network, server, p)
	if err == nil {
		return result(p, got)
	}
	var kind ErrorKind
	switch {
	case errors.Is(err, context.Canceled):
		return nil, fmt.Errorf("client: %w", err)
	case errors.Is(err, context.DeadlineExceeded):
		kind = KindTimeout
	case errors.Is(err, transport.ErrMismatch):
		kind = KindMismatch
	case errors.Is(err, transport.ErrMalformed):
		kind = KindMalformed
	default:
		kind = KindNetwork
	}
	return nil, &DNSError{Kind: kind, Name: p.Name, Key: p.Key, Err: err}
}

// exchange dials server over network and sends p once.
func exchange(ctx context.Context, network string, server netip.AddrPort, p transport.Packet) (transport.Received, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, network, server.String())
	if err != nil {
		return transport.Received{}, fmt.Errorf("dial %s %s: %w", network, server, err)
	}
	defer conn.Close() //nolint:errcheck // The exchange is complete, so a close error changes no result.

	send := transport.ExchangeUDP
	if network == transport.TCP {
		send = transport.ExchangeTCP
	}
	got, err := send(ctx, conn, p)
	if err != nil {
		return transport.Received{}, fmt.Errorf("%s %s: %w", network, server, err)
	}
	return got, nil
}

// result returns the result for the matched response got. It returns a DNSError for
// the TC bit and for an RCODE other than NOERROR. The TC bit comes first, because a
// truncated response holds no answer RR whatever its RCODE.
func result(p transport.Packet, got transport.Received) (*Result, error) {
	if got.Truncated {
		return nil, &DNSError{Kind: KindTruncation, Name: p.Name, Key: p.Key, Err: errTruncated}
	}
	if got.RCode != transport.RCodeNoError {
		return nil, &DNSError{Kind: KindRCode, RCode: int(got.RCode), Name: p.Name, Key: p.Key}
	}
	return &Result{Name: p.Name, Key: p.Key, RCode: int(got.RCode), Payload: got.Payload, TTL: got.TTL}, nil
}

// errTruncated is the cause of a truncation DNSError, because the transport reports the
// TC bit as a field and not as an error.
var errTruncated = errors.New("udp response has the TC bit")

// qname returns the fully qualified QNAME of q, with the version label when q.Version
// is not 0.
func qname(q Query) string {
	name := q.Name
	if !strings.HasSuffix(name, ".") {
		name += "."
	}
	if q.Version != 0 {
		name = "_v" + strconv.FormatUint(uint64(q.Version), 10) + "." + name
	}
	return name
}
