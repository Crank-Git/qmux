package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/transport"
	"github.com/Crank-Git/qmux/pkg/transport/transporttest"
)

// observationLines returns each line of out, decoded as a JSON object. It fails the test
// when out does not end in a newline or when a line does not parse.
func observationLines(t *testing.T, out string) []map[string]any {
	t.Helper()
	if out == "" {
		return nil
	}
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("the observation log does not end in a newline: %q", out)
	}
	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("line %q does not parse as a JSON object: %v", l, err)
		}
		lines = append(lines, m)
	}
	return lines
}

// oneObservationLine fails the test when out does not hold exactly one line.
func oneObservationLine(t *testing.T, out string) map[string]any {
	t.Helper()
	lines := observationLines(t, out)
	if len(lines) != 1 {
		t.Fatalf("got %d observation lines, want 1: %q", len(lines), out)
	}
	return lines[0]
}

var fleetQuestion = []transporttest.RR{{Name: "Device42.fleet.example.", Key: testKey}}

func TestObservationLogWritesOneLineForEachOfTenQueries(t *testing.T) {
	var buf syncBuffer
	s := newTestServer(t, ServerConfig{ObservationLog: &buf}, payloadHandler([]byte("ok")))
	for i := range 10 {
		serveWireOn(t, s, transport.UDP, transporttest.Message{ID: uint16(i), Question: fleetQuestion})
	}
	if n := len(observationLines(t, buf.String())); n != 10 {
		t.Fatalf("got %d observation lines for 10 queries, want 10", n)
	}
}

// The ts field has nine fraction digits, as in the example line of the spec.
var nanoTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{9}Z$`)

func TestObservationLogLineHoldsTheElevenFieldsOfTheSpec(t *testing.T) {
	var buf syncBuffer
	s := newTestServer(t, ServerConfig{ObservationLog: &buf}, payloadHandler([]byte("ok")))
	before := time.Now()
	wire, resp := serveWireOn(t, s, transport.UDP, transporttest.Message{ID: 9, Question: fleetQuestion})

	got := oneObservationLine(t, buf.String())
	want := map[string]any{
		"transport": "udp",
		"source":    "127.0.0.1:53211",
		"qname":     "Device42.fleet.example.",
		"qclass":    float64(testKey.Class),
		"qtype":     float64(testKey.Type),
		"rcode":     "NOERROR",
		"status":    "ok",
		"bytes_in":  float64(len(wire)),
		"bytes_out": float64(len(resp)),
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %#v, want %#v", k, got[k], v)
		}
	}
	ts, _ := got["ts"].(string)
	if !nanoTimestamp.MatchString(ts) {
		t.Errorf("ts = %q, want RFC 3339 in UTC with nine fraction digits", ts)
	}
	if parsed, err := time.Parse(time.RFC3339Nano, ts); err != nil || parsed.Before(before.Add(-time.Second)) {
		t.Errorf("ts = %q does not parse as a time of this query: %v", ts, err)
	}
	if d, ok := got["duration_us"].(float64); !ok || d < 0 || d != float64(int64(d)) {
		t.Errorf("duration_us = %#v, want an integer of 0 or more", got["duration_us"])
	}
	if len(got) != 11 {
		t.Errorf("the observation line has %d fields, want 11: %v", len(got), got)
	}
}

func TestObservationLogWritesTheStatusNameOfTheEnvelope(t *testing.T) {
	tests := []struct {
		status protocol.Status
		want   string
	}{
		{protocol.StatusNotFound, "not-found"},
		{protocol.StatusBadRequest, "bad-request"},
		{protocol.StatusVersionUnsupported, "version-unsupported"},
		{protocol.StatusUnavailable, "unavailable"},
		{300, "300"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			var buf syncBuffer
			h := protocol.HandlerFunc(func(context.Context, *protocol.Request) (*protocol.Response, error) {
				return &protocol.Response{Status: tt.status}, nil
			})
			s := newTestServer(t, ServerConfig{ObservationLog: &buf}, h)
			serveWireOn(t, s, transport.UDP, transporttest.Message{Question: fleetQuestion})
			if got := oneObservationLine(t, buf.String())["status"]; got != tt.want {
				t.Fatalf("status = %#v, want %q", got, tt.want)
			}
		})
	}
}

func TestObservationLogWritesStatusNullForAQueryThatGetsNoEnvelope(t *testing.T) {
	failing := protocol.HandlerFunc(func(context.Context, *protocol.Request) (*protocol.Response, error) {
		return nil, context.Canceled
	})
	tests := []struct {
		name  string
		zones []string
		h     protocol.Handler
		m     transporttest.Message
		want  string
	}{
		{"refused", []string{"other.example."}, payloadHandler(nil), transporttest.Message{Question: fleetQuestion}, "REFUSED"},
		{"formerr", nil, payloadHandler(nil), transporttest.Message{}, "FORMERR"},
		{"servfail", nil, failing, transporttest.Message{Question: fleetQuestion}, "SERVFAIL"},
		{"notimp", nil, payloadHandler(nil), transporttest.Message{Opcode: opcodeNotify, Question: fleetQuestion}, "NOTIMP"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf syncBuffer
			s := newTestServer(t, ServerConfig{ObservationLog: &buf, Zones: tt.zones}, tt.h)
			serveWireOn(t, s, transport.UDP, tt.m)
			got := oneObservationLine(t, buf.String())
			if got["rcode"] != tt.want {
				t.Errorf("rcode = %#v, want %q", got["rcode"], tt.want)
			}
			if v, ok := got["status"]; !ok || v != nil {
				t.Errorf("status = %#v (present %v), want null", v, ok)
			}
		})
	}
}

func TestObservationLogWritesNoLineForAMessageItIgnores(t *testing.T) {
	var buf syncBuffer
	s := newTestServer(t, ServerConfig{ObservationLog: &buf}, payloadHandler(nil))
	serveWireOn(t, s, transport.UDP, transporttest.Message{Response: true, Question: fleetQuestion})
	if out := buf.String(); out != "" {
		t.Fatalf("the observation log holds %q for a message with QR set, want nothing", out)
	}
}

// overlapWriter fails the test when two Write calls run at the same time. It writes
// each line in two parts, so that two writers without a lock can mix their bytes.
type overlapWriter struct {
	t      *testing.T
	active atomic.Int32
	mu     sync.Mutex
	buf    bytes.Buffer
}

func (w *overlapWriter) Write(p []byte) (int, error) {
	if w.active.Add(1) != 1 {
		w.t.Error("two Write calls to the observation log overlap")
	}
	defer w.active.Add(-1)
	half := len(p) / 2
	for _, part := range [][]byte{p[:half], p[half:]} {
		w.mu.Lock()
		w.buf.Write(part)
		w.mu.Unlock()
	}
	return len(p), nil
}

func (w *overlapWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestObservationLogSerializesTheLinesOfOneHundredConcurrentQueries(t *testing.T) {
	w := &overlapWriter{t: t}
	s := newTestServer(t, ServerConfig{ObservationLog: w}, payloadHandler([]byte("ok")))
	wire, err := transporttest.Pack(transporttest.Message{Question: fleetQuestion})
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 100 {
		wg.Go(func() {
			<-start
			_, err := transport.ServeMessage(context.Background(), wire, testSource, transport.UDP,
				transport.HandlerFunc(s.serveQuery), transport.ServeConfig{UDPSize: s.cfg.UDPSize})
			if err != nil {
				t.Errorf("ServeMessage: %v", err)
			}
		})
	}
	close(start)
	wg.Wait()
	if n := len(observationLines(t, w.String())); n != 100 {
		t.Fatalf("got %d observation lines for 100 concurrent queries, want 100", n)
	}
}

// failWriter returns an error for every write, as a full disk does.
type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("no space left on device") }

func TestObservationLogLogsOneErrorPerMinuteAndKeepsAnswering(t *testing.T) {
	h := &recordHandler{}
	s := newTestServer(t, ServerConfig{Logger: slog.New(h), ObservationLog: failWriter{}}, payloadHandler([]byte("ok")))
	for range 3 {
		_, resp := serveWireOn(t, s, transport.UDP, transporttest.Message{Question: fleetQuestion})
		m, err := transporttest.Unpack(resp)
		if err != nil || m.RCode != transport.RCodeNoError || len(m.Answer) != 1 {
			t.Fatalf("response = %+v, %v, want NOERROR with one answer RR", m, err)
		}
	}
	var errs int
	h.mu.Lock()
	for _, r := range h.records {
		if r.Level == slog.LevelError && r.Message == "server: write observation log" {
			errs++
		}
	}
	h.mu.Unlock()
	if errs != 1 {
		t.Fatalf("got %d error records for 3 failed writes in one minute, want 1", errs)
	}
}
