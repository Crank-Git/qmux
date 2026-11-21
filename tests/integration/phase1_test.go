package integration

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/Crank-Git/qmux"
	"github.com/Crank-Git/qmux/pkg/protocol"
)

// keyA and keyB have the same QTYPE and a different QCLASS, so only the QCLASS selects
// the protocol. keyL holds the large answer.
var (
	keyA = qmux.ProtocolKey{Class: 65280, Type: 65280}
	keyB = qmux.ProtocolKey{Class: 65281, Type: 65280}
	keyL = qmux.ProtocolKey{Class: 65280, Type: 65281}
)

// largePayloadSize is above the default UDP size of 1232, so the server truncates the
// UDP response and the client must repeat the query over TCP.
const largePayloadSize = 4000

var (
	payloadA     = []byte("payload A from class 65280")
	payloadB     = []byte("payload B from class 65281")
	payloadLarge = bytes.Repeat([]byte{0xa5}, largePayloadSize)
)

// fixedPayload returns a handler that answers every request with payload.
func fixedPayload(payload []byte) protocol.Handler {
	return protocol.HandlerFunc(func(context.Context, *protocol.Request) (*protocol.Response, error) {
		return &protocol.Response{Payload: payload}, nil
	})
}

// startPhase1Server starts a server through the qmux facade with the three Phase 1
// protocols, and returns its address.
func startPhase1Server(t *testing.T) string {
	t.Helper()
	s := qmux.NewServer(qmux.ServerConfig{
		Address: "127.0.0.1:0",
		Zones:   []string{"fleet.example."},
		Logger:  slog.New(slog.DiscardHandler),
	})
	protocols := []struct {
		key     qmux.ProtocolKey
		id      string
		payload []byte
	}{
		{keyA, "test.phase1.a", payloadA},
		{keyB, "test.phase1.b", payloadB},
		{keyL, "test.phase1.large", payloadLarge},
	}
	for _, p := range protocols {
		info := protocol.Info{ID: p.id, Versions: []uint16{1}}
		if err := s.RegisterProtocol(p.key, info, fixedPayload(p.payload)); err != nil {
			t.Fatalf("RegisterProtocol %v: %v", p.key, err)
		}
	}
	addr, _ := startServer(t, s)
	return addr
}

func TestPhase1TwoClassesAnswerDifferentPayloadsOverUDPAndTCP(t *testing.T) {
	addr := startPhase1Server(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, network := range []string{"udp", "tcp"} {
		c := qmux.NewClient(qmux.ClientConfig{Resolver: addr, Network: network})
		for _, tc := range []struct {
			key  qmux.ProtocolKey
			want []byte
		}{
			{keyA, payloadA},
			{keyB, payloadB},
		} {
			q := qmux.Query{Name: "device42.fleet.example", Class: tc.key.Class, Type: tc.key.Type}
			res, err := c.Query(ctx, q)
			if err != nil {
				t.Fatalf("Query %v over %s: %v", tc.key, network, err)
			}
			if !bytes.Equal(res.Payload, tc.want) {
				t.Errorf("Query %v over %s: payload = %q, want %q", tc.key, network, res.Payload, tc.want)
			}
			if res.Transport != network {
				t.Errorf("Query %v over %s: Transport = %q", tc.key, network, res.Transport)
			}
		}
	}
}

func TestPhase1LargeAnswerArrivesThroughTCPFallback(t *testing.T) {
	addr := startPhase1Server(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c := qmux.NewClient(qmux.ClientConfig{Resolver: addr})
	res, err := c.Query(ctx, qmux.Query{Name: "device42.fleet.example", Class: keyL.Class, Type: keyL.Type})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if !res.FellBack {
		t.Error("FellBack = false, want true")
	}
	if res.Transport != "tcp" {
		t.Errorf("Transport = %q, want %q", res.Transport, "tcp")
	}
	if !bytes.Equal(res.Payload, payloadLarge) {
		t.Errorf("payload has %d bytes, want the %d bytes of the handler", len(res.Payload), largePayloadSize)
	}
}
