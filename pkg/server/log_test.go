package server

import (
	"context"
	"encoding/hex"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/transport"
	"github.com/Crank-Git/qmux/pkg/transport/transporttest"
)

// recordHandler is a slog.Handler that keeps every record, so that a test can read the
// attributes with their types.
type recordHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *recordHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *recordHandler) WithGroup(string) slog.Handler { return h }

// queryRecords returns the records with the message "query".
func (h *recordHandler) queryRecords() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []slog.Record
	for _, r := range h.records {
		if r.Message == "query" {
			out = append(out, r)
		}
	}
	return out
}

// attrs returns the attributes of r by key.
func attrs(r slog.Record) map[string]slog.Value {
	m := make(map[string]slog.Value)
	r.Attrs(func(a slog.Attr) bool {
		m[a.Key] = a.Value
		return true
	})
	return m
}

var testSource = netip.MustParseAddrPort("127.0.0.1:53211")

// serveWireOn runs s for the query m over network, and returns the query bytes and the
// response bytes.
func serveWireOn(t *testing.T, s *Server, network string, m transporttest.Message) ([]byte, []byte) {
	t.Helper()
	wire, err := transporttest.Pack(m)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	resp, err := transport.ServeMessage(context.Background(), wire, testSource, network,
		transport.HandlerFunc(s.serveQuery), transport.ServeConfig{UDPSize: s.cfg.UDPSize})
	if err != nil {
		t.Fatalf("ServeMessage: %v", err)
	}
	return wire, resp
}

// oneQueryRecord fails the test when h does not hold exactly one query record.
func oneQueryRecord(t *testing.T, h *recordHandler) map[string]slog.Value {
	t.Helper()
	recs := h.queryRecords()
	if len(recs) != 1 {
		t.Fatalf("got %d query records, want 1", len(recs))
	}
	if recs[0].Level != slog.LevelInfo {
		t.Fatalf("query record level = %v, want INFO", recs[0].Level)
	}
	return attrs(recs[0])
}

func TestServeLogsOneQueryRecordWithTheTenFieldsOfTheSpec(t *testing.T) {
	h := &recordHandler{}
	s := newTestServer(t, ServerConfig{Logger: slog.New(h)}, payloadHandler([]byte("ok")))
	wire, resp := serveWireOn(t, s, transport.UDP, transporttest.Message{
		ID:       9,
		Question: []transporttest.RR{{Name: "Device42.fleet.example.", Key: testKey}},
	})

	got := oneQueryRecord(t, h)
	want := map[string]slog.Value{
		"qname":     slog.StringValue("Device42.fleet.example."),
		"qclass":    slog.Int64Value(int64(testKey.Class)),
		"qtype":     slog.Int64Value(int64(testKey.Type)),
		"transport": slog.StringValue("udp"),
		"source":    slog.StringValue("127.0.0.1:53211"),
		"rcode":     slog.StringValue("NOERROR"),
		"status":    slog.StringValue(""),
		"bytes_in":  slog.Int64Value(int64(len(wire))),
		"bytes_out": slog.Int64Value(int64(len(resp))),
	}
	for k, v := range want {
		if !got[k].Equal(v) {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	d, ok := got["duration_ms"]
	if !ok || d.Kind() != slog.KindFloat64 || d.Float64() < 0 {
		t.Errorf("duration_ms = %v, want a float64 of 0 or more", d)
	}
	if len(got) != 10 {
		t.Errorf("the query record has %d fields, want 10: %v", len(got), got)
	}
}

func TestServeLogsTheRCodeAndSizeThatTheTransportSent(t *testing.T) {
	h := &recordHandler{}
	// RDLENGTH is 16 bits, so the transport answers SERVFAIL to a 70000-byte payload.
	s := newTestServer(t, ServerConfig{Logger: slog.New(h)}, payloadHandler(make([]byte, 70000)))
	_, resp := serveWireOn(t, s, transport.TCP, transporttest.Message{
		Question: []transporttest.RR{{Name: "device42.fleet.example.", Key: testKey}},
	})

	got := oneQueryRecord(t, h)
	if got["rcode"].String() != "SERVFAIL" {
		t.Errorf("rcode = %v, want SERVFAIL", got["rcode"])
	}
	if got["bytes_out"].Int64() != int64(len(resp)) {
		t.Errorf("bytes_out = %v, want %d", got["bytes_out"], len(resp))
	}
}

func TestServeQueryLogsOneQueryRecordForEachAnsweredRCode(t *testing.T) {
	failing := protocol.HandlerFunc(func(context.Context, *protocol.Request) (*protocol.Response, error) {
		return nil, context.Canceled
	})
	tests := []struct {
		name  string
		zones []string
		h     protocol.Handler
		edit  func(q *transport.Query)
		want  string
	}{
		{"noerror", nil, payloadHandler([]byte("ok")), func(*transport.Query) {}, "NOERROR"},
		{"notimp", nil, payloadHandler(nil), func(q *transport.Query) { q.Opcode = opcodeNotify }, "NOTIMP"},
		{"formerr", nil, payloadHandler(nil), func(q *transport.Query) { q.QuestionCount = 2 }, "FORMERR"},
		{"badvers", nil, payloadHandler(nil), func(q *transport.Query) { q.EDNS, q.EDNSVersion = true, 1 }, "BADVERS"},
		{"refused", []string{"other.example."}, payloadHandler(nil), func(*transport.Query) {}, "REFUSED"},
		{"servfail", nil, failing, func(*transport.Query) {}, "SERVFAIL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &recordHandler{}
			s := newTestServer(t, ServerConfig{Logger: slog.New(h), Zones: tt.zones}, tt.h)
			q := validQuery()
			tt.edit(q)
			answer(s, q)
			if got := oneQueryRecord(t, h)["rcode"].String(); got != tt.want {
				t.Fatalf("rcode = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestServeQueryLogsNoQueryRecordForAMessageItIgnores(t *testing.T) {
	h := &recordHandler{}
	s := newTestServer(t, ServerConfig{Logger: slog.New(h)}, payloadHandler(nil))
	q := validQuery()
	q.QR = true
	answer(s, q)
	if n := len(h.queryRecords()); n != 0 {
		t.Fatalf("got %d query records for a message with QR set, want 0", n)
	}
}

func TestServeLogsNoPayloadBytes(t *testing.T) {
	const marker = "QMUX-TEST-PAYLOAD-MARKER-7f3a"
	opts := &slog.HandlerOptions{Level: slog.LevelDebug}
	formats := []struct {
		name   string
		new    func(w *syncBuffer) slog.Handler
		record string
	}{
		{"text", func(w *syncBuffer) slog.Handler { return slog.NewTextHandler(w, opts) }, "msg=query"},
		{"json", func(w *syncBuffer) slog.Handler { return slog.NewJSONHandler(w, opts) }, `"msg":"query"`},
	}
	for _, f := range formats {
		t.Run(f.name, func(t *testing.T) {
			var buf syncBuffer
			s := newTestServer(t, ServerConfig{Logger: slog.New(f.new(&buf))}, payloadHandler([]byte(marker)))
			serveWireOn(t, s, transport.UDP, transporttest.Message{
				Question: []transporttest.RR{{Name: "device42.fleet.example.", Key: testKey}},
			})

			out := buf.String()
			// A log without the query record proves nothing about the payload.
			if strings.Count(out, f.record) != 1 {
				t.Fatalf("the log holds no single query record:\n%s", out)
			}
			for _, form := range []string{marker, hex.EncodeToString([]byte(marker))} {
				if strings.Contains(out, form) {
					t.Fatalf("the log holds the payload marker %q:\n%s", form, out)
				}
			}
		})
	}
}
