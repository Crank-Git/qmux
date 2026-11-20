package client

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/transport"
	"github.com/Crank-Git/qmux/pkg/transport/transporttest"
)

// These tests call attempt over TCP, because ClientConfig selects no TCP transport
// until #27 adds Network and the TCP fallback.

var internalKey = protocol.ProtocolKey{Class: 65280, Type: 65280}

// tcpAttempt sends one query over TCP to server and returns the error of attempt.
func tcpAttempt(t *testing.T, server string) error {
	t.Helper()
	p, err := transport.NewQuery("device42.fleet.example.", internalKey, 0, false)
	if err != nil {
		t.Fatalf("NewQuery: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := NewClient(ClientConfig{Resolver: server})
	res, err := c.attempt(ctx, transport.TCP, netip.MustParseAddrPort(server), p)
	if err == nil {
		t.Fatalf("attempt returned %+v and no error", res)
	}
	return err
}

func wantInternalKind(t *testing.T, err error, want ErrorKind) {
	t.Helper()
	var de *DNSError
	if !errors.As(err, &de) {
		t.Fatalf("error %v is not a DNSError", err)
	}
	if de.Kind != want {
		t.Fatalf("DNSError kind = %q, want %q (%v)", de.Kind, want, err)
	}
}

func TestQueryReturnsAMalformedErrorForATruncatedTCPResponse(t *testing.T) { // FR-client-22
	server := transporttest.StartServer(t, func(_ string, wire []byte) [][]byte {
		q, err := transporttest.Unpack(wire)
		if err != nil {
			return nil
		}
		resp, err := transporttest.Pack(transporttest.Message{
			ID:       q.ID,
			Response: true,
			Question: q.Question,
			Answer:   []transporttest.RR{{Name: q.Question[0].Name, Key: q.Question[0].Key, TTL: 60, Data: []byte{1, 2, 3, 4}}},
		})
		if err != nil {
			return nil
		}
		// The server frames the cut message with its own length, so the read succeeds
		// and the parse fails.
		return [][]byte{resp[:len(resp)-3]}
	})

	err := tcpAttempt(t, server)

	wantInternalKind(t, err, KindMalformed)
}

func TestQueryReturnsANetworkErrorForAClosedTCPPort(t *testing.T) { // FR-client-23
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	err = tcpAttempt(t, server)

	wantInternalKind(t, err, KindNetwork)
}
