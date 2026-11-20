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

	"github.com/Crank-Git/qmux/pkg/codec"
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
	// Version adds the label "_v<Version>" before Name when it is not 0. The envelope
	// version must then equal Version, except in a version-unsupported envelope.
	Version uint16
	// ExpectedProtocolID is optional. When it is set, the envelope protocol ID must
	// equal it.
	ExpectedProtocolID string
}

// Result is the answer to a query.
type Result struct {
	// Name is the QNAME that the client sent, with the version label.
	Name  string
	Key   protocol.ProtocolKey
	RCode int
	// ProtocolID, Version, MessageType, Status and Payload come from the envelope in the
	// RDATA of the answer RR.
	ProtocolID  string
	Version     uint16
	MessageType protocol.MessageType
	Status      protocol.Status
	Payload     []byte
	// TTL is the TTL of the answer RR. It is informational and never a security expiry.
	TTL uint32
	// Transport is "udp" or "tcp", the transport of the final attempt.
	Transport string
	// FellBack is true when the client repeated the query over TCP after a UDP response
	// with the TC bit.
	FellBack bool
	// RTT is the time of the final attempt, from the dial to the matched response.
	// Earlier attempts that timed out do not count.
	RTT time.Duration
	// QueryBytes and ResponseBytes are the wire sizes of the final attempt, without the
	// TCP length prefix.
	QueryBytes    int
	ResponseBytes int
	// Verified is true when the client verified the signed envelope of the payload. The
	// client does not verify envelopes yet, so Verified is always false.
	Verified bool
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
//   - KindMismatch when the response does not answer q, when the envelope protocol ID
//     differs from q.ExpectedProtocolID, or when the envelope version differs from
//     q.Version. A version-unsupported envelope gives the *AppError and not this
//     error, because it carries the highest version of the registration.
//   - KindMalformed when the response does not parse, when a TCP response has the TC
//     bit, or when codec.Decode rejects the RDATA of the answer RR.
//   - KindRCode when the RCODE is not NOERROR.
//
// When the envelope has an application status other than protocol.StatusOK, Query
// returns the result and a *AppError together.
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
	for i := range attempts {
		p, err := transport.NewQuery(name, key, c.cfg.UDPSize, c.cfg.Recursion)
		if err != nil {
			return nil, fmt.Errorf("client: %w", err)
		}
		res, err := c.attempt(ctx, q, network, server, p)
		// Only a UDP attempt can return a truncation error, because a TCP response with
		// the TC bit is malformed.
		if errors.As(err, &last) && last.Kind == KindTruncation && !c.cfg.DisableTCPFallback {
			return c.fallback(ctx, q, server, p, i+2)
		}
		// Only the end of the attempt timeout starts a new attempt. A result, the end of
		// ctx and every other error stop the query.
		if !errors.As(err, &last) || last.Kind != KindTimeout || ctx.Err() != nil {
			return res, counted(err, i+1, server)
		}
	}
	return nil, counted(last, attempts, server)
}

// fallback repeats p once over TCP after a UDP response with the TC bit. This TCP
// attempt is one extra attempt beyond Attempts, so n counts the UDP attempts and the
// TCP attempt.
func (c *Client) fallback(ctx context.Context, q Query, server netip.AddrPort, p transport.Packet, n int) (*Result, error) {
	res, err := c.attempt(ctx, q, transport.TCP, server, p)
	// An AppError comes with a result, so the result can be non-nil with an error.
	if res != nil {
		res.FellBack = true
	}
	return res, counted(err, n, server)
}

// counted returns err with n attempts and server recorded, when err is a timeout
// DNSError. The text of a timeout DNSError names them.
func counted(err error, n int, server netip.AddrPort) error {
	var de *DNSError
	if errors.As(err, &de) && de.Kind == KindTimeout {
		de.Attempts, de.Resolver = n, server.String()
	}
	return err
}

// attempt sends p to server once over a new socket. network is transport.UDP or
// transport.TCP. A new socket for each attempt is necessary, because the exchange sets
// the deadline of the socket to the past when its context ends.
//
// attempt returns a *DNSError for every error, except an error that wraps
// context.Canceled and the *AppError that comes with a result.
func (c *Client) attempt(ctx context.Context, q Query, network string, server netip.AddrPort, p transport.Packet) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.AttemptTimeout)
	defer cancel()

	start := time.Now()
	got, err := exchange(ctx, network, server, p)
	rtt := time.Since(start)
	if err == nil {
		res, err := result(q, p, got)
		if res != nil {
			res.Transport, res.RTT = network, rtt
		}
		return res, err
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

// result returns the result for the matched response got to q. It returns a DNSError
// for the TC bit, for an RCODE other than NOERROR, and for an envelope that fails to
// decode or does not match q. The TC bit comes first, because a truncated response
// holds no answer RR whatever its RCODE.
//
// result returns the result and a *AppError when the application status is not
// protocol.StatusOK. For the status protocol.StatusVersionUnsupported, result skips the
// version check, so the caller gets the *AppError.
func result(q Query, p transport.Packet, got transport.Received) (*Result, error) {
	if got.Truncated {
		return nil, &DNSError{Kind: KindTruncation, Name: p.Name, Key: p.Key, Err: errTruncated}
	}
	if got.RCode != transport.RCodeNoError {
		return nil, &DNSError{Kind: KindRCode, RCode: int(got.RCode), Name: p.Name, Key: p.Key}
	}
	e, err := codec.Decode(got.Payload, codec.Limits{})
	if err != nil {
		return nil, &DNSError{Kind: KindMalformed, Name: p.Name, Key: p.Key,
			Err: fmt.Errorf("%w: %w", transport.ErrMalformed, err)}
	}
	if q.ExpectedProtocolID != "" && e.ProtocolID != q.ExpectedProtocolID {
		return nil, &DNSError{Kind: KindMismatch, Name: p.Name, Key: p.Key,
			Err: fmt.Errorf("%w: envelope protocol ID %q, want %q", transport.ErrMismatch, e.ProtocolID, q.ExpectedProtocolID)}
	}
	// A version-unsupported envelope carries the highest version of the registration,
	// not the label. A version check on it would hide the status from the caller.
	unsupported := e.Status == uint64(protocol.StatusVersionUnsupported)
	if q.Version != 0 && e.Version != uint64(q.Version) && !unsupported {
		return nil, &DNSError{Kind: KindMismatch, Name: p.Name, Key: p.Key,
			Err: fmt.Errorf("%w: envelope version %d, want %d", transport.ErrMismatch, e.Version, q.Version)}
	}
	// codec.Decode rejects a version, a message type and a status above 65535, so the
	// conversions keep every value.
	res := &Result{
		Name: p.Name, Key: p.Key, RCode: int(got.RCode), TTL: got.TTL,
		ProtocolID: e.ProtocolID, Version: uint16(e.Version), MessageType: protocol.MessageType(e.MessageType),
		Status: protocol.Status(e.Status), Payload: e.Payload,
		QueryBytes: len(p.Wire), ResponseBytes: got.WireSize,
	}
	if res.Status != protocol.StatusOK {
		return res, &AppError{Status: res.Status, ProtocolID: res.ProtocolID, Version: res.Version}
	}
	return res, nil
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
