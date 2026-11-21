package baseline

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/udp"

	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/security"
	"github.com/Crank-Git/qmux/tests/interoperability/baseline/fixture"
)

// maxEnvelope is the largest envelope that a lookup reads, the default of codec.Limits.
const maxEnvelope = fixture.MaxEnvelope

// Client does lookups against one set of baseline servers. Each lookup verifies the
// envelope signature before it returns the envelope bytes (FR-bench-11).
type Client struct {
	target   Target
	verifier *security.Verifier
	dns      *client.Client
	cold     *http.Client
	reuse    *http.Client
}

// NewClient returns a client for the baseline servers at t. It trusts only
// TrustAnchor. The error is the error of TLSClientConfig.
//
// The https-cold lookups open a new TCP connection and a new TLS session each time.
// The https-reuse lookups share one connection until Close.
func NewClient(t Target) (*Client, error) {
	tlsConfig, err := TLSClientConfig()
	if err != nil {
		return nil, err
	}
	verifier := &security.Verifier{Anchors: []security.TrustAnchor{TrustAnchor()}}
	return &Client{
		target:   t,
		verifier: verifier,
		dns:      client.NewClient(client.ClientConfig{Resolver: t.DNS, TrustAnchors: verifier}),
		// A client with no ClientSessionCache never resumes a TLS session.
		cold:  &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig.Clone(), DisableKeepAlives: true}},
		reuse: &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig.Clone()}},
	}, nil
}

// Close closes the kept connection of https-reuse.
func (c *Client) Close() {
	c.reuse.CloseIdleConnections()
}

// Lookup does one lookup of the baseline name and returns the envelope bytes after
// the signature verifies.
//
// It returns an error that wraps ErrUnknownBaseline for a name that is not in Names.
// A failed verification gives an error that wraps the security sentinel, for example
// security.ErrBadSignature. Every other failure of the baseline is a wrapped error.
func (c *Client) Lookup(ctx context.Context, name string) ([]byte, error) {
	switch name {
	case QMuxPrivate:
		return c.lookupDNS(ctx, PrivateName, PrivateKey, PrivateID)
	case DNSIn:
		return c.lookupDNS(ctx, INName, INKey, INID)
	case DNSInTXT:
		return c.lookupDNS(ctx, INName, TXTKey, TXTID)
	case CoAP:
		return c.lookupCoAP(ctx)
	case HTTPSCold:
		return c.lookupHTTPS(ctx, c.cold)
	case HTTPSReuse:
		return c.lookupHTTPS(ctx, c.reuse)
	}
	return nil, fmt.Errorf("%w: %q", ErrUnknownBaseline, name)
}

// lookupDNS queries the QNAME name at key. pkg/client verifies the envelope, because
// the client has trust anchors.
func (c *Client) lookupDNS(ctx context.Context, name string, key protocol.ProtocolKey, id string) ([]byte, error) {
	res, err := c.dns.Query(ctx, client.Query{Name: name, Class: key.Class, Type: key.Type, ExpectedProtocolID: id})
	if err != nil {
		return nil, fmt.Errorf("baseline: dns lookup %s %d/%d: %w", name, key.Class, key.Type, err)
	}
	return res.Raw, nil
}

// lookupCoAP sends a confirmable GET on Path from a new UDP socket.
func (c *Client) lookupCoAP(ctx context.Context) ([]byte, error) {
	conn, err := udp.Dial(c.target.CoAP)
	if err != nil {
		return nil, fmt.Errorf("baseline: coap dial %s: %w", c.target.CoAP, err)
	}
	defer conn.Close() //nolint:errcheck // The lookup reads nothing more from the socket.
	resp, err := conn.Get(ctx, Path)
	if err != nil {
		return nil, fmt.Errorf("baseline: coap GET %s: %w", Path, err)
	}
	if resp.Code() != codes.Content {
		return nil, fmt.Errorf("baseline: coap GET %s: code %v", Path, resp.Code())
	}
	if cf, err := resp.ContentFormat(); err != nil || cf != message.AppCBOR {
		return nil, fmt.Errorf("baseline: coap GET %s: content-format %v, want 60", Path, cf)
	}
	raw, err := resp.ReadBody()
	if err != nil {
		return nil, fmt.Errorf("baseline: coap read %s: %w", Path, err)
	}
	return c.verifyPrivate(raw)
}

// lookupHTTPS sends GET Path with hc.
func (c *Client) lookupHTTPS(ctx context.Context, hc *http.Client) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+c.target.HTTPS+Path, nil)
	if err != nil {
		return nil, fmt.Errorf("baseline: https request: %w", err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("baseline: https GET %s: %w", Path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // The body is read-only.
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("baseline: https GET %s: status %d", Path, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxEnvelope+1))
	if err != nil {
		return nil, fmt.Errorf("baseline: https read %s: %w", Path, err)
	}
	if len(raw) > maxEnvelope {
		return nil, fmt.Errorf("baseline: https GET %s: %w", Path, codec.ErrTooLarge)
	}
	return c.verifyPrivate(raw)
}

// verifyPrivate verifies raw as the qmux-private envelope, because the CoAP and HTTPS
// baselines carry its exact bytes (FR-bench-10).
func (c *Client) verifyPrivate(raw []byte) ([]byte, error) {
	if _, err := c.verifier.Verify(raw, PrivateName, PrivateKey); err != nil {
		return nil, fmt.Errorf("baseline: %w", err)
	}
	return raw, nil
}
