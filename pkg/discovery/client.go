package discovery

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/protocol"
)

// ProtocolID is the protocol ID of the discovery protocol.
const ProtocolID = "org.qmux.discovery"

// DefaultKey is the discovery key that a Client with a zero Key uses.
var DefaultKey = protocol.ProtocolKey{Class: 65280, Type: 65534}

// INKey is the discovery key in the IN class.
var INKey = protocol.ProtocolKey{Class: 1, Type: 65534}

var (
	// ErrInsecure is the error that Client.Discover returns when InsecureSkipVerify is
	// false. The client cannot verify a manifest before Epic 6.
	ErrInsecure = errors.New("discovery: manifest verification is not available, set InsecureSkipVerify")
	// ErrProtocolNotFound is the error that Negotiate returns when the manifest has no
	// entry for the protocol ID.
	ErrProtocolNotFound = errors.New("discovery: protocol not found in the manifest")
	// ErrVersionUnsupported is the error that Negotiate returns when the client and the
	// manifest entry have no common version.
	ErrVersionUnsupported = errors.New("discovery: no common protocol version")
)

// Client discovers the manifest of a server and sends queries at the negotiated
// protocol keys.
type Client struct {
	// Client sends every query.
	Client *client.Client
	// Key is the discovery key. The zero value means DefaultKey.
	Key protocol.ProtocolKey
	// InsecureSkipVerify lets Discover return a manifest that it did not verify. In
	// Epic 5, Discover returns ErrInsecure when it is false.
	InsecureSkipVerify bool
}

// Discover returns the validated manifest of zone. It sends one query for the
// discovery name "_qmux.<zone>" at c.Key.
//
// Discover returns:
//   - ErrInsecure, with no query, when c.InsecureSkipVerify is false.
//   - The error of client.Client.Query, for example a *client.DNSError or a
//     *client.AppError.
//   - An error that wraps ErrInvalidManifest when the envelope is not a manifest of
//     ProtocolID, or when the manifest fails manifest validation.
func (c *Client) Discover(ctx context.Context, zone string) (*Manifest, error) {
	if !c.InsecureSkipVerify {
		return nil, ErrInsecure
	}
	zone = strings.ToLower(zone)
	if !strings.HasSuffix(zone, ".") {
		zone += "."
	}
	key := c.Key
	if key == (protocol.ProtocolKey{}) {
		key = DefaultKey
	}
	res, err := c.Client.Query(ctx, client.Query{
		Name: "_qmux." + zone, Class: key.Class, Type: key.Type, ExpectedProtocolID: ProtocolID,
	})
	if err != nil {
		return nil, fmt.Errorf("discovery: discover %s: %w", zone, err)
	}
	if res.MessageType != protocol.MessageManifest {
		return nil, fmt.Errorf("%w: envelope message type %d, want %d",
			ErrInvalidManifest, res.MessageType, protocol.MessageManifest)
	}
	var m Manifest
	if err := codec.Unmarshal(res.Payload, &m); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidManifest, err)
	}
	if err := m.validate(zone); err != nil {
		return nil, err
	}
	return &m, nil
}

// QueryProtocol returns the result of a query for name with the protocol id. It
// negotiates the protocol key and the version against m and supported first. It then
// sends the query for "_v<N>.<name>" at the negotiated protocol key, and requires the
// envelope protocol ID to equal id.
//
// QueryProtocol returns the error of Negotiate, with no query, when the negotiation
// fails. Otherwise it returns the result and the error of client.Client.Query.
func (c *Client) QueryProtocol(ctx context.Context, m *Manifest, id, name string, supported []uint16) (*client.Result, error) {
	key, v, err := Negotiate(m, id, supported)
	if err != nil {
		return nil, err
	}
	res, err := c.Client.Query(ctx, client.Query{
		Name: name, Class: key.Class, Type: key.Type, Version: v, ExpectedProtocolID: id,
	})
	if err != nil {
		// A *client.AppError comes with a result, so the result stays.
		return res, fmt.Errorf("discovery: query %s: %w", id, err)
	}
	return res, nil
}

// Negotiate returns the protocol key of the manifest entry for id, and the highest
// version that is in both supported and the entry.
//
// Negotiate returns an error that wraps ErrProtocolNotFound when m has no entry for
// id. It returns an error that wraps ErrVersionUnsupported when no common version
// exists.
func Negotiate(m *Manifest, id string, supported []uint16) (protocol.ProtocolKey, uint16, error) {
	e, ok := m.Lookup(id)
	if !ok {
		return protocol.ProtocolKey{}, 0, fmt.Errorf("%w: %q", ErrProtocolNotFound, id)
	}
	var best uint16
	for _, v := range supported {
		if v > best && slices.Contains(e.Versions, v) {
			best = v
		}
	}
	if best == 0 {
		return protocol.ProtocolKey{}, 0, fmt.Errorf("%w: %q supports %v, the client supports %v",
			ErrVersionUnsupported, id, e.Versions, supported)
	}
	return protocol.ProtocolKey{Class: e.Class, Type: e.Type}, best, nil
}

// Static returns a manifest for zone with the entries, with no query. A device uses
// it in place of Discover when it holds a preconfigured mapping.
//
// Static returns an error that wraps ErrInvalidManifest when the manifest fails
// manifest validation.
func Static(zone string, entries []Entry) (*Manifest, error) {
	m := &Manifest{FrameworkVersion: frameworkVersion, Zone: zone, Protocols: slices.Clone(entries)}
	if err := m.validate(zone); err != nil {
		return nil, err
	}
	return m, nil
}
