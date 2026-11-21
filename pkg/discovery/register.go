package discovery

import (
	"context"
	"fmt"
	"strings"

	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/server"
)

// Options holds the settings of Register.
type Options struct {
	// Key is the discovery key. The zero value selects DefaultKey. Register ignores
	// Key when InClass is true.
	Key protocol.ProtocolKey
	// InClass registers discovery at INKey in place of Key. The registry holds one
	// protocol key for each protocol ID, so discovery cannot hold both keys.
	InClass bool
}

// Register registers the discovery protocol, version 1, on srv at opts.Key. The
// discovery handler answers the discovery name "_qmux.<zone>" with the manifest of
// srv for zone. It returns status not-found for every other QNAME.
//
// When opts.InClass is true, Register registers the discovery protocol at INKey and
// not at opts.Key. Errors wrap the error of server.RegisterProtocol.
func Register(srv *server.Server, zone string, opts Options) error {
	return RegisterZones(srv, []string{zone}, opts)
}

// RegisterZones registers the discovery protocol, version 1, on srv at opts.Key one
// time for all zones. The discovery handler answers the discovery name
// "_qmux.<zone>" of each zone with the manifest of srv for that zone. It returns
// status not-found for every other QNAME.
//
// The registry holds one protocol key for each protocol ID, so a server with two or
// more zones calls RegisterZones and not Register for each zone. opts.InClass has the
// same effect as in Register. Errors wrap the error of server.RegisterProtocol.
func RegisterZones(srv *server.Server, zones []string, opts Options) error {
	key := opts.Key
	switch {
	case opts.InClass:
		key = INKey
	case key == (protocol.ProtocolKey{}):
		key = DefaultKey
	}
	h := &handler{srv: srv, zones: make(map[string]string, len(zones))}
	for _, zone := range zones {
		zone = protocol.FoldName(zone)
		if !strings.HasSuffix(zone, ".") {
			zone += "."
		}
		h.zones["_qmux."+zone] = zone
	}
	info := protocol.Info{ID: ProtocolID, Versions: []uint16{1}}
	if err := srv.RegisterProtocol(key, info, h); err != nil {
		return fmt.Errorf("discovery: %w", err)
	}
	return nil
}

// handler is the discovery handler for the zones of one server.
type handler struct {
	srv *server.Server
	// zones maps the discovery name of each zone to the zone. Both are lowercase and
	// fully qualified.
	zones map[string]string
}

// Handle returns the manifest envelope for the discovery name of a zone, and status
// not-found for every other QNAME. It returns the error of codec.Marshal when the
// manifest does not encode.
func (h *handler) Handle(_ context.Context, req *protocol.Request) (*protocol.Response, error) {
	// Name has no version label, so "_v1._qmux.<zone>" also gets the manifest.
	zone, ok := h.zones[req.Name]
	if !ok {
		return &protocol.Response{Status: protocol.StatusNotFound}, nil
	}
	// The handler reads the registry at each request, so a registration made after the
	// start appears in the next manifest. List returns the class-then-type order.
	regs := h.srv.Registry().List()
	m := Manifest{FrameworkVersion: frameworkVersion, Zone: zone, Protocols: make([]Entry, len(regs))}
	for i, r := range regs {
		m.Protocols[i] = Entry{ID: r.Info.ID, Versions: r.Info.Versions, Class: r.Key.Class, Type: r.Key.Type}
	}
	payload, err := codec.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("discovery: encode the manifest: %w", err)
	}
	return &protocol.Response{Payload: payload, MessageType: protocol.MessageManifest}, nil
}
