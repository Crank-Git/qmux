package qmux_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"testing"

	"github.com/Crank-Git/qmux"
	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/protocol"
)

// firmwareHandler answers the firmware metadata of a device.
type firmwareHandler struct{}

// Handle returns the firmware metadata for the device that req.QName names.
// It uses QName and not Labels, because a query for the zone itself has no labels.
func (firmwareHandler) Handle(ctx context.Context, req *protocol.Request) (*protocol.Response, error) {
	return &protocol.Response{Payload: []byte("firmware 1.4.2 for " + req.QName)}, nil
}

// Info returns the protocol ID and the protocol versions of the firmware protocol.
func (firmwareHandler) Info() protocol.Info {
	return protocol.Info{ID: "org.qmux.firmware", Versions: []uint16{1}}
}

// ExampleNewServer serves the firmware protocol at the protocol key 65280/65280.
// The example has no Output comment, because ListenAndServe runs until an interrupt.
func ExampleNewServer() {
	srv := qmux.NewServer(qmux.ServerConfig{
		Address: "127.0.0.1:5300",
		Zones:   []string{"fleet.example."},
	})
	key := qmux.ProtocolKey{Class: 65280, Type: 65280}
	if err := srv.Register(key, firmwareHandler{}); err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := srv.ListenAndServe(ctx); err != nil {
		log.Fatal(err)
	}
}

// ExampleNewClient asks the server of ExampleNewServer for the firmware metadata.
// The example has no Output comment, because it needs a running server.
func ExampleNewClient() {
	c := qmux.NewClient(qmux.ClientConfig{Resolver: "127.0.0.1:5300"})
	res, err := c.Query(context.Background(), qmux.Query{
		Name:  "device42.fleet.example.",
		Class: 65280,
		Type:  65280,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s\n", res.Payload)
}

// firmwareRecord is the payload of org.qmux.firmware, protocol version 1 (FR-demo-3).
type firmwareRecord struct {
	Version string `cbor:"1,keyasint"`
	Digest  []byte `cbor:"2,keyasint"`
	URL     string `cbor:"3,keyasint"`
	KeyID   []byte `cbor:"4,keyasint"`
}

func TestFirmwareHandlerEchoesTheRequestVersion(t *testing.T) {
	res, err := firmwareHandler{}.Handle(context.Background(), &protocol.Request{QName: "device42.fleet.example.", Version: 1})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if res.Version != 1 {
		t.Errorf("Version = %d, want 1", res.Version)
	}
}

func TestFirmwareHandlerPayloadDecodesAsAFirmwareRecord(t *testing.T) {
	res, err := firmwareHandler{}.Handle(context.Background(), &protocol.Request{QName: "device42.fleet.example.", Version: 1})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var fw firmwareRecord
	if err := codec.Unmarshal(res.Payload, &fw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if fw.Version == "" || fw.URL == "" {
		t.Errorf("record = %+v, want a version and a URL", fw)
	}
}

// The quick-start server is the server that the README client example runs against.
func TestExamplesUseTheFirmwareKeyOfTheQuickStartServer(t *testing.T) {
	b, err := os.ReadFile("examples/qmuxd.json")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var cfg struct {
		Protocols []struct {
			ID    string `json:"id"`
			Class uint16 `json:"class"`
			Type  uint16 `json:"type"`
		} `json:"protocols"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	var key *regexp.Regexp
	for _, p := range cfg.Protocols {
		if p.ID == "org.qmux.firmware" {
			key = regexp.MustCompile(fmt.Sprintf(`Class:\s*%d,\s*Type:\s*%d\b`, p.Class, p.Type))
		}
	}
	if key == nil {
		t.Fatal("examples/qmuxd.json has no org.qmux.firmware protocol")
	}
	bodies, err := exampleBodies("example_test.go")
	if err != nil {
		t.Fatalf("exampleBodies: %v", err)
	}
	for _, name := range []string{"ExampleNewServer", "ExampleNewClient"} {
		if !key.MatchString(strings.Join(bodies[name], "\n")) {
			t.Errorf("%s does not use the firmware key %s of examples/qmuxd.json", name, key)
		}
	}
}
