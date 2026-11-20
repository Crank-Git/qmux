package integration

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/server"
	"github.com/Crank-Git/qmux/pkg/transport"
	"github.com/Crank-Git/qmux/pkg/transport/transporttest"
)

const concurrentQueries = 100

var testKey = protocol.ProtocolKey{Class: 65280, Type: 65280}

// lockedBuffer is a log destination that handler goroutines and the test can share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startServer runs s.ListenAndServe, and returns the server address and a stop function.
// stop ends the server and returns after ListenAndServe returns, so every handler and
// every log record is complete. The test cleanup calls stop when the test did not.
func startServer(t *testing.T, s *server.Server) (string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- s.ListenAndServe(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			if err := <-errc; err != nil {
				t.Errorf("ListenAndServe: %v", err)
			}
		})
	}
	t.Cleanup(stop)

	// Addr is non-nil only after both listeners are bound.
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for s.Addr() == nil {
		select {
		case err := <-errc:
			t.Fatalf("ListenAndServe returned before its listeners started: %v", err)
		case <-deadline:
			t.Fatal("Addr is nil 2s after the start")
		case <-tick.C:
		}
	}
	return s.Addr().String(), stop
}

// query sends one query for name over network to addr, and returns the RDATA of the
// single answer RR.
func query(ctx context.Context, network, addr string, id uint16, name string) ([]byte, error) {
	wire, err := transporttest.Pack(transporttest.Message{ID: id, Question: []transporttest.RR{{Name: name, Key: testKey}}})
	if err != nil {
		return nil, fmt.Errorf("pack: %w", err)
	}
	resp, err := transporttest.Exchange(ctx, network, addr, wire)
	if err != nil {
		return nil, fmt.Errorf("exchange: %w", err)
	}
	m, err := transporttest.Unpack(resp)
	if err != nil {
		return nil, fmt.Errorf("unpack: %w", err)
	}
	if m.ID != id || m.RCode != transport.RCodeNoError || len(m.Answer) != 1 {
		return nil, fmt.Errorf("got ID %d, RCODE %v, %d answers; want ID %d, NOERROR, 1 answer", m.ID, m.RCode, len(m.Answer), id)
	}
	return m.Answer[0].Data, nil
}

func TestServerAnswers100ConcurrentQueriesOverUDPAndTCP(t *testing.T) {
	// The handler holds each call until every query is in a handler, so the server
	// must run the 100 handlers at the same time.
	var mu sync.Mutex
	entered := 0
	all := make(chan struct{})
	h := protocol.HandlerFunc(func(ctx context.Context, r *protocol.Request) (*protocol.Response, error) {
		mu.Lock()
		entered++
		if entered == concurrentQueries {
			close(all)
		}
		mu.Unlock()
		select {
		case <-all:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &protocol.Response{Payload: []byte(r.Labels[0])}, nil
	})

	var log lockedBuffer
	s := server.NewServer(server.ServerConfig{
		Address:        "127.0.0.1:0",
		Zones:          []string{"fleet.example."},
		HandlerTimeout: 10 * time.Second,
		Logger:         slog.New(slog.NewTextHandler(&log, nil)),
	})
	if err := s.RegisterProtocol(testKey, protocol.Info{ID: "test.qmux", Versions: []uint16{1}}, h); err != nil {
		t.Fatalf("RegisterProtocol: %v", err)
	}
	addr, stop := startServer(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	errs := make(chan error, concurrentQueries)
	var wg sync.WaitGroup
	for i := range concurrentQueries {
		network := transport.UDP
		if i%2 == 1 {
			network = transport.TCP
		}
		label := fmt.Sprintf("device%d", i)
		wg.Go(func() {
			data, err := query(ctx, network, addr, uint16(i+1), label+".fleet.example.")
			if err == nil && string(data) != label {
				err = fmt.Errorf("RDATA = %q, want %q", data, label)
			}
			if err != nil {
				errs <- fmt.Errorf("query %d over %s: %w", i, network, err)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	// A client can read its response before the server writes the log record.
	stop()
	if n := strings.Count(log.String(), "msg=query"); n != concurrentQueries {
		t.Errorf("the log holds %d query records, want %d", n, concurrentQueries)
	}
}
