package integration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/Crank-Git/qmux"
	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/discovery"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/server"
	"github.com/Crank-Git/qmux/pkg/transport"
)

const phase2Zone = "fleet.example."

// phase2Protocols are the three registrations of FR-discovery-39. Each one is in a
// different private class, so only the manifest tells the client where each one is.
var phase2Protocols = []struct {
	id       string
	key      qmux.ProtocolKey
	versions []uint16
}{
	{"org.qmux.bootstrap", qmux.ProtocolKey{Class: 65280, Type: 65280}, []uint16{1}},
	{"org.qmux.configuration", qmux.ProtocolKey{Class: 65281, Type: 65280}, []uint16{1, 2}},
	{"org.qmux.firmware", qmux.ProtocolKey{Class: 65282, Type: 65280}, []uint16{1}},
}

// clientVersions is the version list of the client. It holds 3, which no registration
// has, so the test also proves that the negotiation picks the highest common version.
var clientVersions = []uint16{1, 2, 3}

// echoRequest returns a handler that writes the protocol ID, the version and the name
// of the request into the payload. The payload then proves which handler answered and
// what the server read from the version label.
func echoRequest(id string) protocol.Handler {
	return protocol.HandlerFunc(func(_ context.Context, req *protocol.Request) (*protocol.Response, error) {
		return &protocol.Response{Payload: []byte(echoPayload(id, req.Version, req.Name))}, nil
	})
}

func echoPayload(id string, version uint16, name string) string {
	return fmt.Sprintf("%s v%d %s", id, version, name)
}

// startPhase2Server starts a server with the three Phase 2 registrations and the
// discovery protocol registered with opts. It returns the server and its address.
func startPhase2Server(t *testing.T, opts discovery.Options) (*server.Server, string) {
	t.Helper()
	s := qmux.NewServer(qmux.ServerConfig{
		Address: "127.0.0.1:0",
		Zones:   []string{phase2Zone},
		Logger:  slog.New(slog.DiscardHandler),
	})
	for _, p := range phase2Protocols {
		info := protocol.Info{ID: p.id, Versions: p.versions}
		if err := s.RegisterProtocol(p.key, info, echoRequest(p.id)); err != nil {
			t.Fatalf("RegisterProtocol %s: %v", p.id, err)
		}
	}
	if err := discovery.Register(s, phase2Zone, opts); err != nil {
		t.Fatalf("discovery.Register: %v", err)
	}
	addr, _ := startServer(t, s)
	return s, addr
}

// newDiscoveryClient returns a discovery client for addr over network. Epic 5 has no
// manifest verification, so the client must set InsecureSkipVerify.
func newDiscoveryClient(addr, network string, key qmux.ProtocolKey) *discovery.Client {
	return &discovery.Client{
		Client:             qmux.NewClient(qmux.ClientConfig{Resolver: addr, Network: network}),
		Key:                key,
		InsecureSkipVerify: true,
	}
}

// wantEntry returns an error when m has no entry for id at key.
func wantEntry(m *discovery.Manifest, id string, key qmux.ProtocolKey) error {
	e, ok := m.Lookup(id)
	if !ok {
		return fmt.Errorf("manifest has no entry for %s", id)
	}
	if got := (qmux.ProtocolKey{Class: e.Class, Type: e.Type}); got != key {
		return fmt.Errorf("manifest entry %s is at %v, want %v", id, got, key)
	}
	return nil
}

// wantRefused returns an error when err is not a *client.DNSError with RCODE REFUSED.
func wantRefused(err error) error {
	var dnsErr *client.DNSError
	if !errors.As(err, &dnsErr) || dnsErr.Kind != client.KindRCode || dnsErr.RCode != int(transport.RCodeRefused) {
		return fmt.Errorf("error = %v, want a DNSError with RCODE REFUSED", err)
	}
	return nil
}

// queryEach sends one QueryProtocol query for each Phase 2 registration and checks that
// the handler of that registration answered at the negotiated version.
func queryEach(ctx context.Context, t *testing.T, dc *discovery.Client, m *discovery.Manifest, network string) {
	t.Helper()
	const name = "device42.fleet.example."
	for _, p := range phase2Protocols {
		res, err := dc.QueryProtocol(ctx, m, p.id, name, clientVersions)
		if err != nil {
			t.Fatalf("QueryProtocol %s over %s: %v", p.id, network, err)
		}
		highest := p.versions[len(p.versions)-1]
		if want := echoPayload(p.id, highest, name); string(res.Payload) != want {
			t.Errorf("QueryProtocol %s over %s: payload = %q, want %q", p.id, network, res.Payload, want)
		}
		if res.Key != p.key {
			t.Errorf("QueryProtocol %s over %s: Key = %v, want %v", p.id, network, res.Key, p.key)
		}
		if res.Transport != network {
			t.Errorf("QueryProtocol %s over %s: Transport = %q", p.id, network, res.Transport)
		}
	}
}

func TestPhase2DiscoversAndQueriesThreePrivateClassesOverUDPAndTCP(t *testing.T) {
	_, addr := startPhase2Server(t, discovery.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, network := range []string{"udp", "tcp"} {
		dc := newDiscoveryClient(addr, network, qmux.ProtocolKey{})
		m, err := dc.Discover(ctx, phase2Zone)
		if err != nil {
			t.Fatalf("Discover over %s: %v", network, err)
		}
		// Three registrations and the discovery registration.
		if len(m.Protocols) != len(phase2Protocols)+1 {
			t.Errorf("Discover over %s: %d entries, want %d", network, len(m.Protocols), len(phase2Protocols)+1)
		}
		if err := wantEntry(m, discovery.ProtocolID, discovery.DefaultKey); err != nil {
			t.Errorf("Discover over %s: %v", network, err)
		}
		for _, p := range phase2Protocols {
			if err := wantEntry(m, p.id, p.key); err != nil {
				t.Errorf("Discover over %s: %v", network, err)
			}
		}
		queryEach(ctx, t, dc, m, network)
	}
}

func TestPhase2DiscoversThroughTheINOption(t *testing.T) {
	// The operator ruling on FR-discovery-12: InClass registers discovery only at INKey.
	_, addr := startPhase2Server(t, discovery.Options{InClass: true})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	dc := newDiscoveryClient(addr, "udp", discovery.INKey)
	m, err := dc.Discover(ctx, phase2Zone)
	if err != nil {
		t.Fatalf("Discover at %v: %v", discovery.INKey, err)
	}
	if err := wantEntry(m, discovery.ProtocolID, discovery.INKey); err != nil {
		t.Error(err)
	}
	queryEach(ctx, t, dc, m, "udp")

	// The default discovery key holds no registration, so the server refuses it.
	_, err = newDiscoveryClient(addr, "udp", qmux.ProtocolKey{}).Discover(ctx, phase2Zone)
	if err := wantRefused(err); err != nil {
		t.Errorf("Discover at %v: %v", discovery.DefaultKey, err)
	}
}

func TestPhase2RemapMovesTheProtocolKeyInTheNextDiscovery(t *testing.T) {
	s, addr := startPhase2Server(t, discovery.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const id = "org.qmux.firmware"
	const name = "device42.fleet.example."
	newKey := qmux.ProtocolKey{Class: 65283, Type: 65280}
	dc := newDiscoveryClient(addr, "udp", qmux.ProtocolKey{})

	before, err := dc.Discover(ctx, phase2Zone)
	if err != nil {
		t.Fatalf("Discover before the remap: %v", err)
	}
	if err := s.Registry().Remap(id, newKey); err != nil {
		t.Fatalf("Remap %s to %v: %v", id, newKey, err)
	}

	// The old manifest names a protocol key that holds no registration now.
	_, err = dc.QueryProtocol(ctx, before, id, name, clientVersions)
	if err := wantRefused(err); err != nil {
		t.Errorf("QueryProtocol with the old manifest: %v", err)
	}

	after, err := dc.Discover(ctx, phase2Zone)
	if err != nil {
		t.Fatalf("Discover after the remap: %v", err)
	}
	if err := wantEntry(after, id, newKey); err != nil {
		t.Fatal(err)
	}
	res, err := dc.QueryProtocol(ctx, after, id, name, clientVersions)
	if err != nil {
		t.Fatalf("QueryProtocol after the remap: %v", err)
	}
	if want := echoPayload(id, 1, name); string(res.Payload) != want {
		t.Errorf("QueryProtocol after the remap: payload = %q, want %q", res.Payload, want)
	}
	if res.Key != newKey {
		t.Errorf("QueryProtocol after the remap: Key = %v, want %v", res.Key, newKey)
	}
}
