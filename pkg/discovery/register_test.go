package discovery

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"testing"

	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/server"
	"github.com/Crank-Git/qmux/pkg/transport"
)

const testZone = "fleet.example."

// okHandler answers every request with an empty payload.
var okHandler = protocol.HandlerFunc(func(context.Context, *protocol.Request) (*protocol.Response, error) {
	return &protocol.Response{}, nil
})

func newServer() *server.Server {
	return server.NewServer(server.ServerConfig{
		Zones:  []string{testZone},
		Logger: slog.New(slog.DiscardHandler),
	})
}

// serve runs srv on loopback until the test ends, and returns a client for it.
// The listeners exist before serve returns, so the first query needs no wait.
func serve(t *testing.T, srv *server.Server) *client.Client {
	t.Helper()
	pc, l, err := transport.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ctx, pc, l) }()
	t.Cleanup(func() {
		cancel()
		if err := <-errc; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return client.NewClient(client.ClientConfig{Resolver: pc.LocalAddr().String()})
}

// query sends a query for name at the discovery key.
func query(t *testing.T, c *client.Client, name string) (*client.Result, error) {
	t.Helper()
	return c.Query(context.Background(), client.Query{
		Name: name, Class: DefaultKey.Class, Type: DefaultKey.Type,
	})
}

// fetchManifest queries the discovery name of testZone and decodes the manifest.
func fetchManifest(t *testing.T, c *client.Client) *Manifest {
	t.Helper()
	res, err := query(t, c, "_qmux."+testZone)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	var m Manifest
	if err := codec.Unmarshal(res.Payload, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	return &m
}

func TestRegisterWithZeroOptionsRegistersDiscoveryAtDefaultKey(t *testing.T) {
	srv := newServer()
	if err := Register(srv, testZone, Options{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	regs := srv.Registry().List()
	if len(regs) != 1 {
		t.Fatalf("registry lists %d registrations, want 1", len(regs))
	}
	got := regs[0]
	if got.Key != (protocol.ProtocolKey{Class: 65280, Type: 65534}) {
		t.Errorf("key = %s, want 65280/65534", got.Key)
	}
	if got.Info.ID != "org.qmux.discovery" || !reflect.DeepEqual(got.Info.Versions, []uint16{1}) {
		t.Errorf("info = %+v, want ID org.qmux.discovery and versions [1]", got.Info)
	}
}

func TestRegisterUsesTheKeyOfTheOptions(t *testing.T) {
	srv := newServer()
	key := protocol.ProtocolKey{Class: 65290, Type: 65300}
	if err := Register(srv, testZone, Options{Key: key}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	reg, ok := srv.Registry().LookupID(ProtocolID)
	if !ok || reg.Key != key {
		t.Fatalf("LookupID = %s, %v, want %s, true", reg.Key, ok, key)
	}
}

func TestRegisterWithInClassRegistersDiscoveryOnlyAtINKey(t *testing.T) {
	for _, key := range []protocol.ProtocolKey{{}, {Class: 65290, Type: 65300}} {
		srv := newServer()
		if err := Register(srv, testZone, Options{Key: key, InClass: true}); err != nil {
			t.Fatalf("Register with Key %s: %v", key, err)
		}
		regs := srv.Registry().List()
		want := protocol.ProtocolKey{Class: 1, Type: 65534}
		if len(regs) != 1 || regs[0].Key != want || regs[0].Info.ID != ProtocolID {
			t.Fatalf("Register with Key %s: registry lists %+v, want only %s at %s", key, regs, ProtocolID, want)
		}
	}
}

func TestINKeyQueryReturnsManifestWithDiscoveryAtINKey(t *testing.T) {
	srv := newServer()
	if err := Register(srv, testZone, Options{InClass: true}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	res, err := serve(t, srv).Query(context.Background(), client.Query{
		Name: "_qmux." + testZone, Class: INKey.Class, Type: INKey.Type,
	})
	if err != nil {
		t.Fatalf("Query at %s: %v", INKey, err)
	}
	if res.MessageType != protocol.MessageManifest {
		t.Fatalf("message type = %d, want %d", res.MessageType, protocol.MessageManifest)
	}
	var m Manifest
	if err := codec.Unmarshal(res.Payload, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if err := m.validate(testZone); err != nil {
		t.Fatalf("validate: %v", err)
	}
	want := []Entry{{ID: ProtocolID, Versions: []uint16{1}, Class: 1, Type: 65534}}
	if !reflect.DeepEqual(m.Protocols, want) {
		t.Fatalf("manifest entries = %+v, want %+v", m.Protocols, want)
	}
}

func TestRegisterReturnsTheRegistryError(t *testing.T) {
	srv := newServer()
	if err := srv.RegisterProtocol(DefaultKey, protocol.Info{ID: "other.qmux", Versions: []uint16{1}}, okHandler); err != nil {
		t.Fatalf("RegisterProtocol: %v", err)
	}
	if err := Register(srv, testZone, Options{}); err == nil {
		t.Fatal("Register at a held key: got nil, want an error")
	}
}

func TestDiscoveryNameQueryReturnsManifestEnvelope(t *testing.T) {
	srv := newServer()
	if err := Register(srv, testZone, Options{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	res, err := query(t, serve(t, srv), "_qmux.fleet.example.")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.MessageType != 2 || res.ProtocolID != ProtocolID || res.Version != 1 || res.Status != protocol.StatusOK {
		t.Fatalf("envelope = type %d, ID %q, version %d, status %d; want 2, %q, 1, 0",
			res.MessageType, res.ProtocolID, res.Version, res.Status, ProtocolID)
	}
	var m Manifest
	if err := codec.Unmarshal(res.Payload, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if err := m.validate(testZone); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestDiscoveryNameWithVersionLabelReturnsManifestEnvelope(t *testing.T) {
	srv := newServer()
	if err := Register(srv, testZone, Options{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	res, err := serve(t, srv).Query(context.Background(), client.Query{
		Name: "_qmux." + testZone, Class: DefaultKey.Class, Type: DefaultKey.Type, Version: 1,
	})
	if err != nil {
		t.Fatalf("Query for _v1._qmux.%s: %v", testZone, err)
	}
	if res.MessageType != protocol.MessageManifest {
		t.Fatalf("message type = %d, want %d", res.MessageType, protocol.MessageManifest)
	}
}

func TestDiscoveryNameMatchIgnoresTheCaseOfTheRegisteredZone(t *testing.T) {
	srv := newServer()
	if err := Register(srv, "FLEET.Example.", Options{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	m := fetchManifest(t, serve(t, srv))
	if m.Zone != testZone {
		t.Errorf("manifest zone = %q, want %q", m.Zone, testZone)
	}
}

func TestManifestListsProtocolRegisteredAfterStart(t *testing.T) {
	srv := newServer()
	if err := Register(srv, testZone, Options{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	c := serve(t, srv)
	if m := fetchManifest(t, c); len(m.Protocols) != 1 {
		t.Fatalf("first manifest has %d entries, want 1", len(m.Protocols))
	}
	key := protocol.ProtocolKey{Class: 65281, Type: 65280}
	if err := srv.RegisterProtocol(key, protocol.Info{ID: "org.qmux.firmware", Versions: []uint16{1, 2}}, okHandler); err != nil {
		t.Fatalf("RegisterProtocol: %v", err)
	}
	e, ok := fetchManifest(t, c).Lookup("org.qmux.firmware")
	want := Entry{ID: "org.qmux.firmware", Versions: []uint16{1, 2}, Class: 65281, Type: 65280}
	if !ok || !reflect.DeepEqual(e, want) {
		t.Fatalf("next manifest entry = %+v, %v; want %+v, true", e, ok, want)
	}
}

func TestManifestListsEveryRegistrationInClassThenTypeOrder(t *testing.T) {
	srv := newServer()
	for _, r := range []struct {
		id  string
		key protocol.ProtocolKey
	}{
		{"org.qmux.firmware", protocol.ProtocolKey{Class: 65282, Type: 65280}},
		{"org.qmux.configuration", protocol.ProtocolKey{Class: 65281, Type: 65280}},
		{"org.qmux.bootstrap", protocol.ProtocolKey{Class: 65280, Type: 65280}},
	} {
		if err := srv.RegisterProtocol(r.key, protocol.Info{ID: r.id, Versions: []uint16{1}}, okHandler); err != nil {
			t.Fatalf("RegisterProtocol %s: %v", r.id, err)
		}
	}
	if err := Register(srv, testZone, Options{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	want := []Entry{
		{ID: "org.qmux.bootstrap", Versions: []uint16{1}, Class: 65280, Type: 65280},
		{ID: "org.qmux.discovery", Versions: []uint16{1}, Class: 65280, Type: 65534},
		{ID: "org.qmux.configuration", Versions: []uint16{1}, Class: 65281, Type: 65280},
		{ID: "org.qmux.firmware", Versions: []uint16{1}, Class: 65282, Type: 65280},
	}
	if got := fetchManifest(t, serve(t, srv)).Protocols; !reflect.DeepEqual(got, want) {
		t.Fatalf("manifest entries =\n%+v\nwant\n%+v", got, want)
	}
}

func TestOtherQNameAtDiscoveryKeyReturnsNotFound(t *testing.T) {
	srv := newServer()
	if err := Register(srv, testZone, Options{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	res, err := query(t, serve(t, srv), "x.fleet.example.")
	var appErr *client.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("Query: got %v, want an AppError", err)
	}
	if res.Status != protocol.StatusNotFound || res.ProtocolID != ProtocolID {
		t.Fatalf("envelope = status %d, ID %q; want %d, %q", res.Status, res.ProtocolID, protocol.StatusNotFound, ProtocolID)
	}
}
