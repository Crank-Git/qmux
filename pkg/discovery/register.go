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
	// Key is the discovery key. The zero value selects DefaultKey.
	Key protocol.ProtocolKey
	// InClass also registers discovery at INKey.
	InClass bool
}

// Register registers the discovery protocol, version 1, on srv at opts.Key. The
// discovery handler answers the discovery name "_qmux.<zone>" with the manifest of
// srv for zone. It returns status not-found for every other QNAME.
//
// When opts.InClass is true, Register also registers the discovery protocol at INKey.
// Errors wrap the error of server.RegisterProtocol.
func Register(srv *server.Server, zone string, opts Options) error {
	key := opts.Key
	if key == (protocol.ProtocolKey{}) {
		key = DefaultKey
	}
	zone = strings.ToLower(zone)
	if !strings.HasSuffix(zone, ".") {
		zone += "."
	}
	h := &handler{srv: srv, zone: zone, name: "_qmux." + zone}
	info := protocol.Info{ID: ProtocolID, Versions: []uint16{1}}
	if err := srv.RegisterProtocol(key, info, h); err != nil {
		return fmt.Errorf("discovery: %w", err)
	}
	if opts.InClass {
		if err := srv.RegisterProtocol(INKey, info, h); err != nil {
			return fmt.Errorf("discovery: %w", err)
		}
	}
	return nil
}

// handler is the discovery handler for one zone of one server.
type handler struct {
	srv *server.Server
	// zone is lowercase and fully qualified.
	zone string
	// name is the discovery name of zone.
	name string
}

// Handle returns the manifest envelope for the discovery name, and status not-found
// for every other QNAME. It returns the error of codec.Marshal when the manifest does
// not encode.
func (h *handler) Handle(_ context.Context, req *protocol.Request) (*protocol.Response, error) {
	// Name has no version label, so "_v1._qmux.<zone>" also gets the manifest.
	if req.Name != h.name {
		return &protocol.Response{Status: protocol.StatusNotFound}, nil
	}
	// The handler reads the registry at each request, so a registration made after the
	// start appears in the next manifest. List returns the class-then-type order.
	regs := h.srv.Registry().List()
	m := Manifest{FrameworkVersion: frameworkVersion, Zone: h.zone, Protocols: make([]Entry, len(regs))}
	for i, r := range regs {
		m.Protocols[i] = Entry{ID: r.Info.ID, Versions: r.Info.Versions, Class: r.Key.Class, Type: r.Key.Type}
	}
	payload, err := codec.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("discovery: encode the manifest: %w", err)
	}
	return &protocol.Response{Payload: payload, MessageType: protocol.MessageManifest}, nil
}
