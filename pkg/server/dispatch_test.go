package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/transport"
	"github.com/Crank-Git/qmux/pkg/transport/transporttest"
)

// syncBuffer is a log destination that handler goroutines and the test can share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// debugLogger returns a logger that writes every record, debug level included, to buf.
func debugLogger(buf *syncBuffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// countLevel returns the number of records at level in a text log.
func countLevel(log string, level slog.Level) int {
	return strings.Count(log, "level="+level.String())
}

// countingSchema records the version and the payload of each Validate call, and
// returns err.
type countingSchema struct {
	mu       sync.Mutex
	versions []uint16
	payloads [][]byte
	err      error
}

func (c *countingSchema) Validate(version uint16, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.versions = append(c.versions, version)
	c.payloads = append(c.payloads, payload)
	return c.err
}

func (c *countingSchema) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.versions)
}

// requestFor runs s.serveQuery for a valid query with name, and returns the request
// that the handler received.
func requestFor(t *testing.T, cfg ServerConfig, name string) *protocol.Request {
	t.Helper()
	var reqs []*protocol.Request
	s := newTestServer(t, cfg, recordingHandler(&reqs))
	q := validQuery()
	q.Name = name
	checkRCode(t, answer(s, q), transport.RCodeNoError)
	if len(reqs) != 1 {
		t.Fatalf("the handler received %d requests, want 1", len(reqs))
	}
	return reqs[0]
}

func TestServeQueryGivesTheHandlerTheQNameInLowercase(t *testing.T) {
	req := requestFor(t, ServerConfig{Zones: []string{"fleet.example."}}, "Device42.Fleet.Example.")
	if req.QName != "device42.fleet.example." {
		t.Fatalf("QName = %q, want %q", req.QName, "device42.fleet.example.")
	}
}

func TestServeQuerySetsTheVersionFromTheLeftmostVersionLabel(t *testing.T) {
	tests := []struct {
		name    string
		zones   []string
		qname   string
		version uint16
		labels  []string
	}{
		{"version 2", []string{"fleet.example."}, "_v2.device42.fleet.example.", 2, []string{"device42"}},
		{"no version label", []string{"fleet.example."}, "device42.fleet.example.", 0, []string{"device42"}},
		{"version 2 with no zones", nil, "_v2.device42.fleet.example.", 2, []string{"device42", "fleet", "example"}},
		{"uppercase version label", []string{"fleet.example."}, "_V7.device42.fleet.example.", 7, []string{"device42"}},
		{"highest version", []string{"fleet.example."}, "_v65535.a.fleet.example.", 65535, []string{"a"}},
		{"version 0 is a label", []string{"fleet.example."}, "_v0.a.fleet.example.", 0, []string{"_v0", "a"}},
		{"leading zero is a label", []string{"fleet.example."}, "_v02.a.fleet.example.", 0, []string{"_v02", "a"}},
		{"version above 65535 is a label", []string{"fleet.example."}, "_v65536.a.fleet.example.", 0, []string{"_v65536", "a"}},
		{"no digits is a label", []string{"fleet.example."}, "_v.a.fleet.example.", 0, []string{"_v", "a"}},
		{"letters are a label", []string{"fleet.example."}, "_v2x.a.fleet.example.", 0, []string{"_v2x", "a"}},
		{"version label not leftmost", []string{"fleet.example."}, "a._v2.fleet.example.", 0, []string{"a", "_v2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := requestFor(t, ServerConfig{Zones: tt.zones}, tt.qname)
			if req.Version != tt.version {
				t.Errorf("Version = %d, want %d", req.Version, tt.version)
			}
			if !slices.Equal(req.Labels, tt.labels) {
				t.Errorf("Labels = %q, want %q", req.Labels, tt.labels)
			}
		})
	}
}

func TestServeQuerySetsTheLabelsBelowTheZone(t *testing.T) {
	tests := []struct {
		name   string
		zones  []string
		qname  string
		labels []string
	}{
		{"one label below the zone", []string{"fleet.example."}, "_v2.device42.fleet.example.", []string{"device42"}},
		{"two labels below the zone", []string{"fleet.example."}, "sensor.device42.fleet.example.", []string{"sensor", "device42"}},
		{"the QNAME is the zone", []string{"fleet.example."}, "fleet.example.", []string{}},
		{"every label with no zones", nil, "device42.fleet.example.", []string{"device42", "fleet", "example"}},
		{"an escaped dot stays in its label", nil, `a\.b.example.`, []string{`a\.b`, "example"}},
		{"an escaped dot below the zone", []string{"example."}, `a\.b.example.`, []string{`a\.b`}},
		{"an escaped octet stays in its label", []string{"example."}, `a\046b.example.`, []string{`a\046b`}},
		{"labels below a zone with an escaped dot", []string{`a\.b.example.`}, `x.y.a\.b.example.`, []string{"x", "y"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := requestFor(t, ServerConfig{Zones: tt.zones}, tt.qname)
			if !slices.Equal(req.Labels, tt.labels) {
				t.Fatalf("Labels = %q, want %q", req.Labels, tt.labels)
			}
		})
	}
}

func TestServeQueryCopiesTheQueryFieldsIntoTheRequest(t *testing.T) {
	var reqs []*protocol.Request
	s := newTestServer(t, ServerConfig{}, recordingHandler(&reqs))
	q := validQuery()
	q.Source = netip.MustParseAddrPort("192.0.2.1:40000")
	q.Transport = transport.TCP
	q.UDPSize = 1400
	q.EDNS = true
	q.ID = 4242
	q.ReceivedAt = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	checkRCode(t, answer(s, q), transport.RCodeNoError)

	r := reqs[0]
	if r.Source != q.Source || r.Transport != q.Transport || r.UDPSize != q.UDPSize ||
		r.ID != q.ID || !r.ReceivedAt.Equal(q.ReceivedAt) || r.Key != q.Key {
		t.Fatalf("request = %+v, want the fields of query %+v", r, q)
	}
}

func TestServeGivesTheHandlerTheSourceTransportIDAndReceiveTime(t *testing.T) {
	reqc := make(chan *protocol.Request, 2)
	h := protocol.HandlerFunc(func(_ context.Context, r *protocol.Request) (*protocol.Response, error) {
		reqc <- r
		return &protocol.Response{Payload: []byte("ok")}, nil
	})
	s := newTestServer(t, ServerConfig{}, h)
	_, addr, _ := startServer(t, s)

	for i, network := range []string{transport.UDP, transport.TCP} {
		id := uint16(500 + i)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		m, err := exchange(ctx, network, addr, id)
		cancel()
		if err != nil {
			t.Fatalf("%s: exchange: %v", network, err)
		}
		checkAnswer(t, network, m, id, []byte("ok"))
		r := <-reqc
		if !r.Source.Addr().IsLoopback() || r.Source.Port() == 0 {
			t.Errorf("%s: Source = %v, want the loopback address of the client", network, r.Source)
		}
		if r.Transport != network || r.ID != id || r.ReceivedAt.IsZero() {
			t.Errorf("%s: got Transport %q, ID %d, ReceivedAt %v; want %q, %d, non-zero",
				network, r.Transport, r.ID, r.ReceivedAt, network, id)
		}
	}
}

func TestServeQueryGivesTheHandlerADeadlineOfTheDefaultHandlerTimeout(t *testing.T) {
	var left time.Duration
	var ok bool
	h := protocol.HandlerFunc(func(ctx context.Context, _ *protocol.Request) (*protocol.Response, error) {
		var deadline time.Time
		deadline, ok = ctx.Deadline()
		left = time.Until(deadline)
		return &protocol.Response{}, nil
	})
	s := newTestServer(t, ServerConfig{}, h)
	checkRCode(t, answer(s, validQuery()), transport.RCodeNoError)
	if !ok {
		t.Fatal("the handler context has no deadline")
	}
	if left <= 1500*time.Millisecond || left > 2*time.Second {
		t.Fatalf("the deadline is %v after the call, want about 2s", left)
	}
}

func TestServeQueryCallsTheSchemaOncePerAnsweredQuery(t *testing.T) {
	schema := &countingSchema{}
	info := protocol.Info{ID: "test.qmux", Versions: []uint16{1, 3, 2}, Schema: schema}
	payload := []byte{0x01, 0x02}
	s := NewServer(ServerConfig{Logger: quietLogger()})
	if err := s.RegisterProtocol(testKey, info, payloadHandler(payload)); err != nil {
		t.Fatalf("RegisterProtocol: %v", err)
	}
	for range 3 {
		checkRCode(t, answer(s, validQuery()), transport.RCodeNoError)
	}
	if n := schema.calls(); n != 3 {
		t.Fatalf("the schema recorded %d calls, want 3", n)
	}
	// Response.Version 0 means the highest version of the registration.
	if schema.versions[0] != 3 || !bytes.Equal(schema.payloads[0], payload) {
		t.Fatalf("Validate got version %d and payload %x, want 3 and %x",
			schema.versions[0], schema.payloads[0], payload)
	}
}

func TestServeQueryGivesTheSchemaTheVersionOfTheResponse(t *testing.T) {
	schema := &countingSchema{}
	info := protocol.Info{ID: "test.qmux", Versions: []uint16{1, 2}, Schema: schema}
	h := protocol.HandlerFunc(func(context.Context, *protocol.Request) (*protocol.Response, error) {
		return &protocol.Response{Version: 1}, nil
	})
	s := NewServer(ServerConfig{Logger: quietLogger()})
	if err := s.RegisterProtocol(testKey, info, h); err != nil {
		t.Fatalf("RegisterProtocol: %v", err)
	}
	checkRCode(t, answer(s, validQuery()), transport.RCodeNoError)
	if schema.versions[0] != 1 {
		t.Fatalf("Validate got version %d, want 1", schema.versions[0])
	}
}

func TestServeQueryAnswersServFailWhenTheSchemaRejectsThePayload(t *testing.T) {
	schema := &countingSchema{err: errors.New("bad payload")}
	info := protocol.Info{ID: "test.qmux", Versions: []uint16{1}, Schema: schema}
	s := NewServer(ServerConfig{Logger: quietLogger()})
	if err := s.RegisterProtocol(testKey, info, payloadHandler([]byte("x"))); err != nil {
		t.Fatalf("RegisterProtocol: %v", err)
	}
	checkRCode(t, answer(s, validQuery()), transport.RCodeServFail)
}

// wireAnswer sends one query for name and key through the request path of s with no
// socket, and returns the parsed response.
func wireAnswer(t *testing.T, s *Server, name string, key protocol.ProtocolKey) transporttest.Message {
	t.Helper()
	m, err := transporttest.Unpack(serveWire(t, s, transporttest.Message{
		ID:               99,
		RecursionDesired: true,
		Question:         []transporttest.RR{{Name: name, Key: key}},
	}))
	if err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	return m
}

func TestServeAnswersNoErrorWithAAAndOneOpaqueRR(t *testing.T) {
	key := protocol.ProtocolKey{Class: 65281, Type: 65280}
	payloads := map[string][]byte{
		"text":            []byte("device42 is online"),
		"binary":          {0x00, 0xff, 0x80, 0x0a, 0x00, 0x5c, 0x22, 0x3b},
		"every byte":      allBytes(),
		"one zero byte":   {0x00},
		"no payload byte": {},
	}
	for name, payload := range payloads {
		t.Run(name, func(t *testing.T) {
			s := NewServer(ServerConfig{Zones: []string{"fleet.example."}, Logger: quietLogger()})
			if err := s.RegisterProtocol(key, testInfo, payloadHandler(payload)); err != nil {
				t.Fatalf("RegisterProtocol: %v", err)
			}
			m := wireAnswer(t, s, "Device42.fleet.example.", key)
			if m.RCode != transport.RCodeNoError || !m.Authoritative || m.RecursionAvailable || len(m.Answer) != 1 {
				t.Fatalf("got RCODE %v, AA %t, RA %t, %d answers; want NOERROR, AA, no RA, 1 answer",
					m.RCode, m.Authoritative, m.RecursionAvailable, len(m.Answer))
			}
			rr := m.Answer[0]
			if rr.Name != "Device42.fleet.example." || rr.Key != key {
				t.Fatalf("answer RR has name %q and key %v, want %q and %v",
					rr.Name, rr.Key, "Device42.fleet.example.", key)
			}
			if !bytes.Equal(rr.Data, payload) {
				t.Fatalf("RDATA = %x, want %x", rr.Data, payload)
			}
		})
	}
}

// allBytes returns the 256 byte values in order.
func allBytes() []byte {
	b := make([]byte, 256)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

func TestServeUsesTheConfiguredTTLWhenTheResponseTTLIsZero(t *testing.T) {
	tests := []struct {
		name    string
		cfgTTL  uint32
		respTTL uint32
		wantTTL uint32
	}{
		{"response TTL 0 with the default", 0, 0, 60},
		{"response TTL 5", 0, 5, 5},
		{"response TTL 0 with TTL 30 configured", 30, 0, 30},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := protocol.HandlerFunc(func(context.Context, *protocol.Request) (*protocol.Response, error) {
				return &protocol.Response{Payload: []byte("ok"), TTL: tt.respTTL}, nil
			})
			s := newTestServer(t, ServerConfig{TTL: tt.cfgTTL}, h)
			m := wireAnswer(t, s, "device42.fleet.example.", testKey)
			if len(m.Answer) != 1 || m.Answer[0].TTL != tt.wantTTL {
				t.Fatalf("answers %+v, want one RR with TTL %d", m.Answer, tt.wantTTL)
			}
		})
	}
}

func TestServeQueryAnswersServFailWhenTheHandlerReturnsAnError(t *testing.T) {
	h := protocol.HandlerFunc(func(context.Context, *protocol.Request) (*protocol.Response, error) {
		return nil, errors.New("no resource store")
	})
	s := newTestServer(t, ServerConfig{}, h)
	checkRCode(t, answer(s, validQuery()), transport.RCodeServFail)
}

func TestServeQueryAnswersServFailAndLogsANilResponse(t *testing.T) {
	var log syncBuffer
	h := protocol.HandlerFunc(func(context.Context, *protocol.Request) (*protocol.Response, error) {
		return nil, nil
	})
	s := newTestServer(t, ServerConfig{Logger: debugLogger(&log)}, h)
	checkRCode(t, answer(s, validQuery()), transport.RCodeServFail)
	if n := countLevel(log.String(), slog.LevelError); n != 1 {
		t.Fatalf("the log holds %d error records, want 1:\n%s", n, log.String())
	}
}

func TestServeQueryAnswersServFailAtTheTimeoutToAHandlerThatIgnoresItsContext(t *testing.T) {
	release := make(chan struct{})
	returned := make(chan struct{})
	h := protocol.HandlerFunc(func(context.Context, *protocol.Request) (*protocol.Response, error) {
		defer close(returned)
		<-release
		return &protocol.Response{Payload: []byte("late")}, nil
	})
	s := newTestServer(t, ServerConfig{HandlerTimeout: 50 * time.Millisecond}, h)

	var w fakeWriter
	begin := time.Now()
	s.serveQuery(context.Background(), validQuery(), &w)
	took := time.Since(begin)
	checkRCode(t, w.answers, transport.RCodeServFail)
	if took < 50*time.Millisecond {
		t.Fatalf("serveQuery returned after %v, before the 50ms timeout", took)
	}

	close(release)
	<-returned
	// The race detector also reports a late write to w, because w has no lock.
	checkRCode(t, w.answers, transport.RCodeServFail)
}

func TestServeAnswersServFailAndShutsDownWhenAHandlerIgnoresItsContext(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	h := protocol.HandlerFunc(func(context.Context, *protocol.Request) (*protocol.Response, error) {
		<-release
		return &protocol.Response{Payload: []byte("late")}, nil
	})
	s := newTestServer(t, ServerConfig{HandlerTimeout: 100 * time.Millisecond, ShutdownGrace: 5 * time.Second}, h)
	cancelServe, addr, errc := startServer(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	wire, err := transporttest.Pack(transporttest.Message{
		ID:       600,
		Question: []transporttest.RR{{Name: "device42.fleet.example.", Key: testKey}},
	})
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	resp, err := transporttest.Exchange(ctx, transport.UDP, addr, wire)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	m, err := transporttest.Unpack(resp)
	if err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if m.RCode != transport.RCodeServFail || len(m.Answer) != 0 {
		t.Fatalf("got RCODE %v with %d answers, want SERVFAIL with none", m.RCode, len(m.Answer))
	}

	// The handler still runs, but the server already answered, so the shutdown does
	// not wait for the 5s grace.
	begin := time.Now()
	cancelServe()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("ListenAndServe: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ListenAndServe did not return 2s after the cancel")
	}
	if wait := time.Since(begin); wait >= 2*time.Second {
		t.Fatalf("the shutdown took %v", wait)
	}
}

func TestServeAnswersServFailToAPanicAndKeepsAnswering(t *testing.T) {
	var log syncBuffer
	var mu sync.Mutex
	calls := 0
	h := protocol.HandlerFunc(func(context.Context, *protocol.Request) (*protocol.Response, error) {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if first {
			panic("handler bug")
		}
		return &protocol.Response{Payload: []byte("ok")}, nil
	})
	s := newTestServer(t, ServerConfig{Logger: debugLogger(&log)}, h)
	_, addr, _ := startServer(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	m, err := exchange(ctx, transport.UDP, addr, 700)
	if err != nil {
		t.Fatalf("first exchange: %v", err)
	}
	if m.RCode != transport.RCodeServFail {
		t.Fatalf("RCODE after the panic = %v, want SERVFAIL", m.RCode)
	}
	if n := countLevel(log.String(), slog.LevelError); n != 1 {
		t.Fatalf("the log holds %d error records, want 1:\n%s", n, log.String())
	}

	m, err = exchange(ctx, transport.UDP, addr, 701)
	if err != nil {
		t.Fatalf("second exchange: %v", err)
	}
	checkAnswer(t, transport.UDP, m, 701, []byte("ok"))
}

// errWriter is a Writer whose every write fails, as a closed TCP connection does.
type errWriter struct{}

func (errWriter) Write(transport.Answer) (transport.Sent, error) {
	return transport.Sent{}, errors.New("write tcp: broken pipe")
}

func TestServeQueryLogsAWriteErrorAtDebugLevel(t *testing.T) {
	var log syncBuffer
	s := newTestServer(t, ServerConfig{Logger: debugLogger(&log)}, payloadHandler([]byte("ok")))
	s.serveQuery(context.Background(), validQuery(), errWriter{})
	out := log.String()
	if countLevel(out, slog.LevelDebug) != 1 || !strings.Contains(out, "broken pipe") {
		t.Fatalf("the log holds %q, want one debug record with the write error", out)
	}
	if countLevel(out, slog.LevelError) != 0 {
		t.Fatalf("the log holds an error record for a write error: %q", out)
	}
}
