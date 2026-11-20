package schema_test

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/examples/embedded-bootstrap/schema"
	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/server"
)

// rcodeServFail is the DNS RCODE SERVFAIL. The demonstration never imports
// pkg/transport, so the test states the value.
const rcodeServFail = 2

var configurationKey = protocol.ProtocolKey{Class: 65281, Type: 65280}

// startConfigurationServer starts a server that registers the configuration protocol
// with its schema, and whose handler returns payload. It returns the UDP address.
func startConfigurationServer(t *testing.T, payload []byte) string {
	t.Helper()
	s := server.NewServer(server.ServerConfig{
		Zones:  []string{"fleet.example."},
		Logger: slog.New(slog.DiscardHandler),
	})
	info := protocol.Info{ID: "org.qmux.configuration", Versions: []uint16{1}, Schema: schema.Configuration{}}
	h := protocol.HandlerFunc(func(context.Context, *protocol.Request) (*protocol.Response, error) {
		return &protocol.Response{Payload: payload}, nil
	})
	if err := s.RegisterProtocol(configurationKey, info, h); err != nil {
		t.Fatalf("RegisterProtocol: %v", err)
	}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- s.Serve(ctx, pc, l) }()
	t.Cleanup(func() {
		cancel()
		if err := <-errc; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return pc.LocalAddr().String()
}

// queryConfiguration sends one configuration query for device42 to addr.
func queryConfiguration(t *testing.T, addr string) (*client.Result, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := client.NewClient(client.ClientConfig{Resolver: addr})
	return c.Query(ctx, client.Query{
		Name:    "device42.fleet.example.",
		Class:   configurationKey.Class,
		Type:    configurationKey.Type,
		Version: 1,
	})
}

func TestServerAnswersServFailWhenTheHandlerReturnsAnInvalidConfiguration(t *testing.T) { // FR-demo-6
	invalid := mustMarshal(t, struct {
		Generation uint64 `cbor:"1,keyasint"`
		Digest     []byte `cbor:"2,keyasint"`
		URL        string `cbor:"3,keyasint"`
	}{7, digest()[:31], "u"})
	_, err := queryConfiguration(t, startConfigurationServer(t, invalid))
	var dnsErr *client.DNSError
	if !errors.As(err, &dnsErr) || dnsErr.Kind != client.KindRCode || dnsErr.RCode != rcodeServFail {
		t.Fatalf("got %v, want a DNSError with RCODE SERVFAIL", err)
	}
}

func TestServerAnswersAValidConfiguration(t *testing.T) { // FR-demo-6
	payload, err := configuration().Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	res, err := queryConfiguration(t, startConfigurationServer(t, payload))
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	got, err := schema.UnmarshalConfiguration(res.Payload)
	if err != nil {
		t.Fatalf("UnmarshalConfiguration: %v", err)
	}
	if got.Generation != 7 {
		t.Fatalf("generation = %d, want 7", got.Generation)
	}
}
