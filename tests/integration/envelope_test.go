package integration

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/Crank-Git/qmux"
	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/transport"
)

// firmwareInfo is the registration of the envelope tests.
var firmwareInfo = protocol.Info{ID: "org.qmux.firmware", Versions: []uint16{1, 2}}

// startEnvelopeServer starts a server that answers every query for testKey with resp,
// and returns its address.
func startEnvelopeServer(t *testing.T, resp protocol.Response) string {
	t.Helper()
	s := qmux.NewServer(qmux.ServerConfig{
		Address: "127.0.0.1:0",
		Zones:   []string{"fleet.example."},
		Logger:  slog.New(slog.DiscardHandler),
	})
	h := protocol.HandlerFunc(func(context.Context, *protocol.Request) (*protocol.Response, error) {
		return &resp, nil
	})
	if err := s.RegisterProtocol(testKey, firmwareInfo, h); err != nil {
		t.Fatalf("RegisterProtocol: %v", err)
	}
	addr, _ := startServer(t, s)
	return addr
}

func TestAnswerRRDataDecodesToTheRegistrationProtocolID(t *testing.T) { // FR-codec-29, FR-codec-30
	addr := startEnvelopeServer(t, protocol.Response{Payload: []byte("firmware 1.4.2")})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for i, network := range []string{transport.UDP, transport.TCP} {
		rdata, err := query(ctx, network, addr, uint16(i+1), "device42.fleet.example.")
		if err != nil {
			t.Fatalf("%s: %v", network, err)
		}
		e, err := codec.Decode(rdata, codec.Limits{})
		if err != nil {
			t.Fatalf("%s: Decode(RDATA): %v", network, err)
		}
		if e.ProtocolID != "org.qmux.firmware" || e.Version != 2 || string(e.Payload) != "firmware 1.4.2" {
			t.Fatalf("%s: envelope = %+v, want protocol ID org.qmux.firmware, version 2, the handler payload", network, e)
		}
	}
}

func TestClientReturnsTheResultAndAnAppErrorForANotFoundAnswer(t *testing.T) { // FR-codec-39
	addr := startEnvelopeServer(t, protocol.Response{Status: protocol.StatusNotFound})
	c := qmux.NewClient(qmux.ClientConfig{Resolver: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.Query(ctx, qmux.Query{Name: "device42.fleet.example.", Class: testKey.Class, Type: testKey.Type})
	var ae *client.AppError
	if res == nil || !errors.As(err, &ae) || ae.Status != protocol.StatusNotFound || ae.ProtocolID != "org.qmux.firmware" {
		t.Fatalf("result %+v, error %v; want a result and an AppError with not-found from org.qmux.firmware", res, err)
	}
}
