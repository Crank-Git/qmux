package transport_test

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/transport"
	"github.com/Crank-Git/qmux/pkg/transport/transporttest"
)

func TestServeMessageSetsTheQueryFields(t *testing.T) {
	m := transporttest.Message{
		ID:               77,
		RecursionDesired: true,
		Question:         []transporttest.RR{{Name: "Node1.Example.", Key: testKey}},
		EDNS:             true,
		EDNSVersion:      1,
		UDPSize:          1400,
	}
	wire, err := transporttest.Pack(m)
	if err != nil {
		t.Fatalf("pack query: %v", err)
	}
	before := time.Now()

	r := serveAnswer(t, wire, transport.TCP, transport.Answer{RCode: transport.RCodeNoError})
	q := *r.query
	if q.ReceivedAt.Before(before) {
		t.Errorf("ReceivedAt = %v, want a time after %v", q.ReceivedAt, before)
	}
	q.ReceivedAt = time.Time{}
	want := transport.Query{
		ID:            77,
		QuestionCount: 1,
		Name:          "Node1.Example.",
		Key:           testKey,
		EDNS:          true,
		EDNSVersion:   1,
		UDPSize:       1400,
		WireSize:      len(wire),
		Source:        testSource,
		Transport:     transport.TCP,
	}
	if !reflect.DeepEqual(q, want) {
		t.Errorf("Query = %+v, want %+v", q, want)
	}
}

func TestServeMessageReportsUDPSizeZeroWithoutEDNS(t *testing.T) {
	r := serveAnswer(t, queryWire(t, "a.example.", 0), transport.UDP, transport.Answer{})
	if r.query.EDNS || r.query.UDPSize != 0 {
		t.Errorf("EDNS=%v UDPSize=%d, want false 0", r.query.EDNS, r.query.UDPSize)
	}
}

func TestServeMessageGivesMalformedBodyToTheHandlerWithErrMalformed(t *testing.T) {
	wire := queryWire(t, "a.example.", 0)
	wire = wire[:len(wire)-3] // The question loses part of its class field.

	r := serveAnswer(t, wire, transport.UDP, transport.Answer{RCode: transport.RCodeFormErr})
	if !errors.Is(r.query.Err, transport.ErrMalformed) {
		t.Fatalf("Query.Err = %v, want ErrMalformed", r.query.Err)
	}
	if r.query.ID != 4242 || r.query.QuestionCount != 1 {
		t.Errorf("ID=%d QuestionCount=%d, want the header values 4242 1", r.query.ID, r.query.QuestionCount)
	}
	if r.err != nil {
		t.Fatalf("Write: %v", r.err)
	}
	m := unpack(t, r.resp)
	if m.ID != 4242 || m.RCode != transport.RCodeFormErr {
		t.Errorf("response ID=%d RCODE=%v, want 4242 FORMERR", m.ID, m.RCode)
	}
}

func TestServeMessageRejectsAMessageShorterThanTheHeader(t *testing.T) {
	called := false
	h := transport.HandlerFunc(func(context.Context, *transport.Query, transport.Writer) { called = true })
	resp, err := transport.ServeMessage(context.Background(), make([]byte, 11), testSource, transport.UDP, h, testConfig)
	if !errors.Is(err, transport.ErrMalformed) || resp != nil || called {
		t.Errorf("ServeMessage = %d bytes, %v, handler called %v; want no bytes, ErrMalformed, no call", len(resp), err, called)
	}
}

// startServe runs Serve on 127.0.0.1:0. It returns the address, the cancel function of
// the Serve context, and a channel that receives the Serve result.
func startServe(t *testing.T, h transport.Handler, cfg transport.ServeConfig) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	pc, l, err := transport.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		done <- transport.Serve(ctx, pc, l, h, cfg)
		close(finished)
	}()
	t.Cleanup(func() {
		cancel()
		<-finished
	})
	// Listen binds both sockets, so the kernel queues every query sent from here on.
	return pc.LocalAddr().String(), cancel, done
}

func echoHandler() transport.Handler {
	return transport.HandlerFunc(func(_ context.Context, q *transport.Query, w transport.Writer) {
		_, _ = w.Write(transport.Answer{RCode: transport.RCodeNoError, Payload: []byte(q.Transport)})
	})
}

func exchange(t *testing.T, network, addr string, query []byte) transporttest.Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := transporttest.Exchange(ctx, network, addr, query)
	if err != nil {
		t.Fatalf("Exchange %s: %v", network, err)
	}
	return unpack(t, resp)
}

func TestListenOpensTCPOnTheUDPPort(t *testing.T) {
	pc, l, err := transport.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer pc.Close() //nolint:errcheck // The test only reads the addresses.
	defer l.Close()  //nolint:errcheck // The test only reads the addresses.
	if got, want := l.Addr().(*net.TCPAddr).Port, pc.LocalAddr().(*net.UDPAddr).Port; got != want {
		t.Errorf("TCP port %d, want the UDP port %d", got, want)
	}
}

func TestListenReturnsTheBindError(t *testing.T) {
	pc, l, err := transport.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer pc.Close() //nolint:errcheck // The test only holds the port.
	defer l.Close()  //nolint:errcheck // The test only holds the port.
	if _, _, err := transport.Listen(pc.LocalAddr().String()); err == nil {
		t.Error("Listen on a port in use returned nil, want the bind error")
	}
}

func TestServeAnswersOverUDPAndOverTCPOnTheSamePort(t *testing.T) {
	addr, _, _ := startServe(t, echoHandler(), testConfig)
	for _, network := range []string{transport.UDP, transport.TCP} {
		m := exchange(t, network, addr, queryWire(t, "a.example.", 0))
		if len(m.Answer) != 1 || string(m.Answer[0].Data) != network {
			t.Errorf("%s answer = %+v, want one RR with payload %q", network, m.Answer, network)
		}
	}
}

func TestServeReadsSeveralLengthFramedQueriesOnOneTCPConnection(t *testing.T) {
	addr, _, _ := startServe(t, echoHandler(), testConfig)
	c, err := net.Dial(transport.TCP, addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close() //nolint:errcheck // The test reads all it needs before the close.
	if err := c.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}

	// Two framed queries in one write show that the server reads the frame length.
	var out []byte
	for _, id := range []uint16{1, 2} {
		q, err := transporttest.Pack(transporttest.Message{ID: id, Question: []transporttest.RR{{Name: "a.example.", Key: testKey}}})
		if err != nil {
			t.Fatalf("pack: %v", err)
		}
		out = binary.BigEndian.AppendUint16(out, uint16(len(q)))
		out = append(out, q...)
	}
	if _, err := c.Write(out); err != nil {
		t.Fatalf("write: %v", err)
	}

	for _, want := range []uint16{1, 2} {
		var size [2]byte
		if _, err := io.ReadFull(c, size[:]); err != nil {
			t.Fatalf("read length of response %d: %v", want, err)
		}
		resp := make([]byte, binary.BigEndian.Uint16(size[:]))
		if _, err := io.ReadFull(c, resp); err != nil {
			t.Fatalf("read response %d: %v", want, err)
		}
		if m := unpack(t, resp); m.ID != want {
			t.Errorf("response ID = %d, want %d", m.ID, want)
		}
	}
}

func TestQueryWireSizeEqualsTheBytesSentForAPaddedQuery(t *testing.T) {
	sizes := make(chan int, 1)
	h := transport.HandlerFunc(func(_ context.Context, q *transport.Query, w transport.Writer) {
		sizes <- q.WireSize
		_, _ = w.Write(transport.Answer{RCode: transport.RCodeFormErr})
	})
	addr, _, _ := startServe(t, h, testConfig)

	tests := []struct {
		network string
		udpSize uint16
		padTo   int
	}{
		{transport.UDP, 0, 513},
		{transport.UDP, 1232, 1233},
		{transport.TCP, 0, 4000},
	}
	for _, tt := range tests {
		m := transporttest.Message{ID: 9, Question: []transporttest.RR{{Name: "a.example.", Key: testKey}},
			EDNS: tt.udpSize != 0, UDPSize: tt.udpSize}
		m.PadTo(tt.padTo)
		wire, err := transporttest.Pack(m)
		if err != nil {
			t.Fatalf("pack: %v", err)
		}
		if len(wire) != tt.padTo {
			t.Fatalf("padded query is %d bytes, want %d", len(wire), tt.padTo)
		}
		exchange(t, tt.network, addr, wire)
		if got := <-sizes; got != len(wire) {
			t.Errorf("%s: WireSize = %d, want %d", tt.network, got, len(wire))
		}
	}
}

// recordHandler keeps every log record so that a test can read them.
type recordHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *recordHandler) WithGroup(string) slog.Handler            { return h }
func (h *recordHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}

func (h *recordHandler) debugRecords() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.records {
		if r.Level == slog.LevelDebug {
			n++
		}
	}
	return n
}

func TestServeDropsAPacketShorterThan12BytesAndLogsAtDebug(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	h := transport.HandlerFunc(func(_ context.Context, _ *transport.Query, w transport.Writer) {
		mu.Lock()
		calls++
		mu.Unlock()
		_, _ = w.Write(transport.Answer{RCode: transport.RCodeNoError})
	})
	logs := &recordHandler{}
	cfg := testConfig
	cfg.Logger = slog.New(logs)
	addr, _, _ := startServe(t, h, cfg)

	c, err := net.Dial(transport.UDP, addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close() //nolint:errcheck // The test reads all it needs before the close.
	if err := c.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	// The server reads packets in order. The response to the full query proves that
	// the server read the short packet first.
	if _, err := c.Write(make([]byte, 11)); err != nil {
		t.Fatalf("write short packet: %v", err)
	}
	if _, err := c.Write(queryWire(t, "a.example.", 0)); err != nil {
		t.Fatalf("write query: %v", err)
	}
	buf := make([]byte, 65535)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if m := unpack(t, buf[:n]); m.ID != 4242 {
		t.Errorf("first response ID = %d, want 4242 from the full query", m.ID)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("handler calls = %d, want 1", calls)
	}
	if logs.debugRecords() != 1 {
		t.Errorf("debug records = %d, want 1", logs.debugRecords())
	}
}

func TestServeLetsAnInFlightHandlerFinishWithinShutdownGrace(t *testing.T) {
	started := make(chan context.Context)
	release := make(chan struct{})
	h := transport.HandlerFunc(func(ctx context.Context, _ *transport.Query, w transport.Writer) {
		started <- ctx
		<-release
		_, _ = w.Write(transport.Answer{RCode: transport.RCodeNoError, Payload: []byte("late")})
	})
	cfg := testConfig
	cfg.ShutdownGrace = time.Minute
	addr, cancel, done := startServe(t, h, cfg)

	c, err := net.Dial(transport.UDP, addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close() //nolint:errcheck // The test reads all it needs before the close.
	if _, err := c.Write(queryWire(t, "a.example.", 0)); err != nil {
		t.Fatalf("write query: %v", err)
	}
	handlerCtx := <-started

	cancel()
	// A context derived from the Serve context ends inside the cancel call.
	if handlerCtx.Err() != nil {
		t.Fatalf("handler context ended with the Serve context: %v", handlerCtx.Err())
	}
	// The handler waits for release, so Serve cannot return yet.
	select {
	case err := <-done:
		t.Fatalf("Serve returned %v before the in-flight handler finished", err)
	default:
	}

	close(release)
	if err := c.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	buf := make([]byte, 65535)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("read the response of the in-flight handler: %v", err)
	}
	if m := unpack(t, buf[:n]); len(m.Answer) != 1 || string(m.Answer[0].Data) != "late" {
		t.Errorf("response = %+v, want the answer of the in-flight handler", m)
	}
	if err := <-done; err != nil {
		t.Errorf("Serve = %v, want nil", err)
	}
}

func TestServeCancelsHandlerContextsAfterShutdownGrace(t *testing.T) {
	const grace = 100 * time.Millisecond
	started := make(chan struct{})
	ended := make(chan time.Time, 1)
	h := transport.HandlerFunc(func(ctx context.Context, _ *transport.Query, _ transport.Writer) {
		close(started)
		<-ctx.Done()
		ended <- time.Now()
	})
	cfg := testConfig
	cfg.ShutdownGrace = grace
	addr, cancel, done := startServe(t, h, cfg)

	c, err := net.Dial(transport.TCP, addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close() //nolint:errcheck // The test reads no response.
	q := queryWire(t, "a.example.", 0)
	if _, err := c.Write(append(binary.BigEndian.AppendUint16(nil, uint16(len(q))), q...)); err != nil {
		t.Fatalf("write query: %v", err)
	}
	<-started

	stopAt := time.Now()
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Serve = %v, want nil", err)
	}
	if waited := (<-ended).Sub(stopAt); waited < grace {
		t.Errorf("handler context ended %v after the stop, want at least %v", waited, grace)
	}
}

func TestServeClosesTheSocketsAfterTheContextEnds(t *testing.T) {
	pc, l, err := transport.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := transport.Serve(ctx, pc, l, echoHandler(), testConfig); err != nil {
		t.Fatalf("Serve = %v, want nil", err)
	}
	if _, _, err := pc.ReadFrom(make([]byte, 1)); !errors.Is(err, net.ErrClosed) {
		t.Errorf("UDP read after Serve = %v, want net.ErrClosed", err)
	}
	if _, err := l.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Errorf("TCP accept after Serve = %v, want net.ErrClosed", err)
	}
}

// failingPacketConn returns errWrite from every WriteTo.
type failingPacketConn struct {
	net.PacketConn
}

var errWrite = errors.New("write refused by the test")

func (failingPacketConn) WriteTo([]byte, net.Addr) (int, error) { return 0, errWrite }

func TestWriteReturnsTheWriteError(t *testing.T) {
	pc, l, err := transport.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	errs := make(chan error, 1)
	h := transport.HandlerFunc(func(_ context.Context, _ *transport.Query, w transport.Writer) {
		_, err := w.Write(transport.Answer{RCode: transport.RCodeNoError})
		errs <- err
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- transport.Serve(ctx, failingPacketConn{pc}, l, h, testConfig) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve = %v, want nil", err)
		}
	}()

	c, err := net.Dial(transport.UDP, pc.LocalAddr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close() //nolint:errcheck // The test reads no response.
	if _, err := c.Write(queryWire(t, "a.example.", 0)); err != nil {
		t.Fatalf("write query: %v", err)
	}
	if err := <-errs; !errors.Is(err, errWrite) {
		t.Errorf("Write = %v, want the wrapped write error", err)
	}
}
