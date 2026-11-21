package integration

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Crank-Git/qmux"
	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/transport"
)

// The server default HandlerTimeout must be shorter than the client default
// AttemptTimeout. Otherwise a slow handler gives the client a timeout instead of
// SERVFAIL, and each new attempt calls the handler again (review #193).
func TestDefaultTimeoutsGiveServFailOnTheFirstAttemptForASlowHandler(t *testing.T) {
	var calls atomic.Int32
	h := protocol.HandlerFunc(func(context.Context, *protocol.Request) (*protocol.Response, error) {
		calls.Add(1)
		// The handler ignores its context, so only the server timeout can answer.
		time.Sleep(1500 * time.Millisecond)
		return &protocol.Response{}, nil
	})
	s := qmux.NewServer(qmux.ServerConfig{Address: "127.0.0.1:0", Logger: slog.New(slog.DiscardHandler)})
	if err := s.RegisterProtocol(testKey, protocol.Info{ID: "test.slow", Versions: []uint16{1}}, h); err != nil {
		t.Fatalf("RegisterProtocol: %v", err)
	}
	addr, _ := startServer(t, s)

	c := qmux.NewClient(qmux.ClientConfig{Resolver: addr})
	begin := time.Now()
	_, err := c.Query(context.Background(), client.Query{Name: "device42.fleet.example.", Class: testKey.Class, Type: testKey.Type, Version: 1})
	elapsed := time.Since(begin)

	var dnsErr *client.DNSError
	if !errors.As(err, &dnsErr) || dnsErr.Kind != client.KindRCode || dnsErr.RCode != int(transport.RCodeServFail) {
		t.Fatalf("Query error = %v, want a DNSError with RCODE SERVFAIL", err)
	}
	// One attempt waits at most the default AttemptTimeout of 1s.
	if elapsed >= time.Second {
		t.Errorf("SERVFAIL arrived %v after the query, want less than the 1s attempt timeout", elapsed)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("the handler ran %d times, want 1", n)
	}
}
