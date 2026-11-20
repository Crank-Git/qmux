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
	// AttemptTimeout is the time that one attempt waits for a response. The default is
	// 1s.
	AttemptTimeout time.Duration
	// Attempts is the number of UDP attempts. The default is 3.
	Attempts int
	// UDPSize is the UDP size that the OPT RR of each query advertises. The default is
	// 1232.
	UDPSize uint16
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

// Query sends q to the resolver over UDP and returns the matching response.
//
// Each attempt uses a new query with a new random ID. If an attempt gets no response
// within AttemptTimeout, Query makes a new attempt, up to Attempts attempts. Query
// returns an error when the resolver is not an IP address and port, when ctx ends, and
// when every attempt times out. A response that does not match q also returns an
// error.
func (c *Client) Query(ctx context.Context, q Query) (*Result, error) {
	server, err := netip.ParseAddrPort(c.cfg.Resolver)
	if err != nil {
		return nil, fmt.Errorf("client: resolver %q is not an IP address and port: %w", c.cfg.Resolver, err)
	}
	name := qname(q)
	key := protocol.ProtocolKey{Class: q.Class, Type: q.Type}

	var lastErr error
	for range c.cfg.Attempts {
		p, err := transport.NewQuery(name, key, c.cfg.UDPSize, c.cfg.Recursion)
		if err != nil {
			return nil, fmt.Errorf("client: %w", err)
		}
		got, err := c.attempt(ctx, server, p)
		if err == nil {
			return &Result{Name: p.Name, Key: key, RCode: int(got.RCode), Payload: got.Payload, TTL: got.TTL}, nil
		}
		// Only the end of the attempt timeout starts a new attempt. The end of ctx and
		// every other error stop the query.
		if ctx.Err() != nil || !errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		lastErr = err
	}
	return nil, fmt.Errorf("client: no response after %d attempts to %s: %w", c.cfg.Attempts, server, lastErr)
}

// attempt sends p to server once over a new UDP socket. A new socket for each attempt
// is necessary, because ExchangeUDP sets the deadline of the socket to the past when
// its context ends.
func (c *Client) attempt(ctx context.Context, server netip.AddrPort, p transport.Packet) (transport.Received, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.AttemptTimeout)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(ctx, transport.UDP, server.String())
	if err != nil {
		return transport.Received{}, fmt.Errorf("client: dial udp %s: %w", server, err)
	}
	defer conn.Close() //nolint:errcheck // The exchange is complete, so a close error changes no result.

	got, err := transport.ExchangeUDP(ctx, conn, p)
	if err != nil {
		return transport.Received{}, fmt.Errorf("client: query %s %d/%d to %s: %w", p.Name, p.Key.Class, p.Key.Type, server, err)
	}
	return got, nil
}

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
