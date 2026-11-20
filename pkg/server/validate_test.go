package server

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"testing"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/transport"
	"github.com/Crank-Git/qmux/pkg/transport/transporttest"
)

// The DNS opcodes other than QUERY that the tests send.
const (
	opcodeIQuery = 1
	opcodeNotify = 4
)

// fakeWriter records each answer that the server writes.
type fakeWriter struct {
	answers []transport.Answer
}

func (w *fakeWriter) Write(a transport.Answer) (transport.Sent, error) {
	w.answers = append(w.answers, a)
	return transport.Sent{RCode: a.RCode}, nil
}

// validQuery returns a query that passes every check: one question for testKey, no
// EDNS(0), and a size within the limit.
func validQuery() *transport.Query {
	return &transport.Query{
		ID:            7,
		Opcode:        opcodeQuery,
		QuestionCount: 1,
		Name:          "device42.fleet.example.",
		Key:           testKey,
		WireSize:      51,
		Transport:     transport.UDP,
	}
}

// recordingHandler returns a handler that stores each request in got.
func recordingHandler(got *[]*protocol.Request) protocol.HandlerFunc {
	return func(_ context.Context, r *protocol.Request) (*protocol.Response, error) {
		*got = append(*got, r)
		return &protocol.Response{Payload: []byte("ok")}, nil
	}
}

// answer runs s.serveQuery for q and returns the answers that the server wrote.
func answer(s *Server, q *transport.Query) []transport.Answer {
	var w fakeWriter
	s.serveQuery(context.Background(), q, &w)
	return w.answers
}

// checkRCode fails the test when the server did not write exactly one answer with want.
func checkRCode(t *testing.T, got []transport.Answer, want transport.RCode) {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("got %d answers, want 1 with %v", len(got), want)
	}
	if got[0].RCode != want {
		t.Fatalf("RCODE = %v, want %v", got[0].RCode, want)
	}
}

func TestServeQueryIgnoresAMessageWithQRSet(t *testing.T) {
	var reqs []*protocol.Request
	s := newTestServer(t, ServerConfig{}, recordingHandler(&reqs))
	q := validQuery()
	q.QR = true
	if got := answer(s, q); len(got) != 0 {
		t.Fatalf("got %d answers, want none", len(got))
	}
	if len(reqs) != 0 {
		t.Fatalf("the handler received %d requests, want none", len(reqs))
	}
}

func TestServeQueryAnswersNotImpToAnIQueryAndANotify(t *testing.T) {
	s := newTestServer(t, ServerConfig{}, payloadHandler(nil))
	for _, op := range []uint16{opcodeIQuery, opcodeNotify} {
		t.Run(fmt.Sprintf("opcode %d", op), func(t *testing.T) {
			q := validQuery()
			q.Opcode = op
			checkRCode(t, answer(s, q), transport.RCodeNotImp)
		})
	}
}

func TestServeQueryAnswersFormErrToZeroAndToTwoQuestions(t *testing.T) {
	s := newTestServer(t, ServerConfig{}, payloadHandler(nil))
	for _, n := range []int{0, 2} {
		t.Run(fmt.Sprintf("%d questions", n), func(t *testing.T) {
			q := validQuery()
			q.QuestionCount = n
			checkRCode(t, answer(s, q), transport.RCodeFormErr)
		})
	}
}

func TestServeQueryAnswersFormErrToAQueryWithABodyThatDoesNotParse(t *testing.T) {
	s := newTestServer(t, ServerConfig{}, payloadHandler(nil))
	q := &transport.Query{
		ID:            7,
		QuestionCount: 1,
		WireSize:      20,
		Transport:     transport.UDP,
		Err:           transport.ErrMalformed,
	}
	checkRCode(t, answer(s, q), transport.RCodeFormErr)
}

func TestServeQueryAnswersFormErrToA600ByteQueryWithoutEDNS(t *testing.T) {
	s := newTestServer(t, ServerConfig{}, payloadHandler(nil))
	for _, network := range []string{transport.UDP, transport.TCP} {
		t.Run(network, func(t *testing.T) {
			q := validQuery()
			q.Transport = network
			q.WireSize = 600
			checkRCode(t, answer(s, q), transport.RCodeFormErr)
		})
	}
}

func TestServeQueryAcceptsA512ByteQueryWithoutEDNS(t *testing.T) {
	s := newTestServer(t, ServerConfig{}, payloadHandler(nil))
	q := validQuery()
	q.WireSize = 512
	checkRCode(t, answer(s, q), transport.RCodeNoError)
}

func TestServeQueryAnswersFormErrToA1300ByteQueryWithEDNS(t *testing.T) {
	s := newTestServer(t, ServerConfig{}, payloadHandler(nil))
	q := validQuery()
	q.EDNS = true
	q.UDPSize = 4096
	q.WireSize = 1300
	checkRCode(t, answer(s, q), transport.RCodeFormErr)
}

func TestServeQueryAcceptsA1232ByteQueryWithEDNS(t *testing.T) {
	s := newTestServer(t, ServerConfig{}, payloadHandler(nil))
	q := validQuery()
	q.EDNS = true
	q.UDPSize = 4096
	q.WireSize = 1232
	checkRCode(t, answer(s, q), transport.RCodeNoError)
}

func TestServeQueryUsesTheConfiguredUDPSizeAsTheEDNSQueryLimit(t *testing.T) {
	s := newTestServer(t, ServerConfig{UDPSize: 2000}, payloadHandler(nil))
	q := validQuery()
	q.EDNS = true
	q.UDPSize = 4096
	q.WireSize = 1300
	checkRCode(t, answer(s, q), transport.RCodeNoError)

	q.WireSize = 2001
	checkRCode(t, answer(s, q), transport.RCodeFormErr)
}

func TestServeQueryAnswersBadVersToEDNSVersion1(t *testing.T) {
	s := newTestServer(t, ServerConfig{}, payloadHandler(nil))
	q := validQuery()
	q.EDNS = true
	q.EDNSVersion = 1
	q.UDPSize = 1232
	checkRCode(t, answer(s, q), transport.RCodeBadVers)
}

func TestServeQueryChecksTheSizeBeforeTheEDNSVersion(t *testing.T) {
	s := newTestServer(t, ServerConfig{}, payloadHandler(nil))
	q := validQuery()
	q.EDNS = true
	q.EDNSVersion = 1
	q.UDPSize = 1232
	q.WireSize = 1300
	checkRCode(t, answer(s, q), transport.RCodeFormErr)
}

func TestServeQueryChecksTheOpcodeBeforeTheQuestionCount(t *testing.T) {
	s := newTestServer(t, ServerConfig{}, payloadHandler(nil))
	q := validQuery()
	q.Opcode = opcodeNotify
	q.QuestionCount = 0
	checkRCode(t, answer(s, q), transport.RCodeNotImp)
}

func TestServeQueryChecksTheZoneBeforeTheRegistration(t *testing.T) {
	s := newTestServer(t, ServerConfig{Zones: []string{"fleet.example."}}, payloadHandler(nil))
	q := validQuery()
	q.Name = "a.other.example."
	q.Key = protocol.ProtocolKey{Class: 255, Type: 65280}
	checkRCode(t, answer(s, q), transport.RCodeRefused)
}

func TestServeQueryAnswersRefusedToAQNameOutsideEveryZone(t *testing.T) {
	var reqs []*protocol.Request
	s := newTestServer(t, ServerConfig{Zones: []string{"fleet.example."}}, recordingHandler(&reqs))
	q := validQuery()
	q.Name = "a.other.example."
	checkRCode(t, answer(s, q), transport.RCodeRefused)
	if len(reqs) != 0 {
		t.Fatalf("the handler received %d requests, want none", len(reqs))
	}
}

func TestServeQueryRefusesAQNameThatOnlyEndsWithTheZoneText(t *testing.T) {
	s := newTestServer(t, ServerConfig{Zones: []string{"fleet.example."}}, payloadHandler(nil))
	q := validQuery()
	q.Name = "otherfleet.example."
	checkRCode(t, answer(s, q), transport.RCodeRefused)
}

func TestServeQueryRefusesAQNameWhoseEscapedDotLooksLikeTheZone(t *testing.T) {
	// The labels of a\.b.example. are a.b and example, so the name is outside b.example.
	s := newTestServer(t, ServerConfig{Zones: []string{"b.example."}}, payloadHandler(nil))
	q := validQuery()
	q.Name = `a\.b.example.`
	checkRCode(t, answer(s, q), transport.RCodeRefused)
}

func TestServeQueryMatchesAZoneByItsLabels(t *testing.T) {
	tests := []struct {
		name  string
		zones []string
		qname string
		zone  string
	}{
		{"a name below the zone", []string{"b.example."}, "x.b.example.", "b.example."},
		{"an uppercase name below the zone", []string{"b.example."}, "X.B.EXAMPLE.", "b.example."},
		{"the zone itself", []string{"b.example."}, "b.example.", "b.example."},
		{"the zone with the most labels", []string{"example.", "b.example."}, "x.b.example.", "b.example."},
		{"a zone with an escaped dot", []string{`a\.b.example.`}, `x.a\.b.example.`, `a\.b.example.`},
		{"the shorter zone past an escaped dot", []string{"example.", "b.example."}, `a\.b.example.`, "example."},
		{"a zone with no final dot", []string{"b.example"}, "x.b.example.", "b.example"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := requestFor(t, ServerConfig{Zones: tt.zones}, tt.qname)
			if req.Zone != tt.zone {
				t.Fatalf("Zone = %q, want %q", req.Zone, tt.zone)
			}
		})
	}
}

func TestServeQueryMatchesZonesWithoutRegardToCase(t *testing.T) {
	var reqs []*protocol.Request
	s := newTestServer(t, ServerConfig{Zones: []string{"Fleet.Example."}}, recordingHandler(&reqs))
	for _, name := range []string{"DEVICE42.fleet.EXAMPLE.", "fleet.example."} {
		q := validQuery()
		q.Name = name
		checkRCode(t, answer(s, q), transport.RCodeNoError)
	}
	if len(reqs) != 2 {
		t.Fatalf("the handler received %d requests, want 2", len(reqs))
	}
}

func TestServeQuerySetsTheLongestMatchingZoneInTheRequest(t *testing.T) {
	var reqs []*protocol.Request
	zones := []string{"example.", "a.fleet.example.", "fleet.example."}
	s := newTestServer(t, ServerConfig{Zones: zones}, recordingHandler(&reqs))
	q := validQuery()
	q.Name = "device42.a.fleet.example."
	checkRCode(t, answer(s, q), transport.RCodeNoError)
	if len(reqs) != 1 || reqs[0].Zone != "a.fleet.example." {
		t.Fatalf("got requests %+v, want one with Zone a.fleet.example.", reqs)
	}
}

func TestServeQueryPassesEveryQNameToTheHandlerWithNoZones(t *testing.T) {
	var reqs []*protocol.Request
	s := newTestServer(t, ServerConfig{}, recordingHandler(&reqs))
	q := validQuery()
	q.Name = "a.other.example."
	checkRCode(t, answer(s, q), transport.RCodeNoError)
	if len(reqs) != 1 || reqs[0].Zone != "." {
		t.Fatalf("got requests %+v, want one with Zone .", reqs)
	}
}

func TestServeQueryAnswersRefusedToAnUnregisteredProtocolKey(t *testing.T) {
	s := newTestServer(t, ServerConfig{}, payloadHandler(nil))
	q := validQuery()
	q.Key = protocol.ProtocolKey{Class: 65281, Type: 65280}
	checkRCode(t, answer(s, q), transport.RCodeRefused)
}

func TestServeQueryAnswersRefusedToQClassAny(t *testing.T) {
	s := newTestServer(t, ServerConfig{}, payloadHandler(nil))
	q := validQuery()
	q.Key = protocol.ProtocolKey{Class: 255, Type: testKey.Type}
	checkRCode(t, answer(s, q), transport.RCodeRefused)
}

// serveWire sends m through the request path of s with no socket, and returns the
// response bytes.
func serveWire(t *testing.T, s *Server, m transporttest.Message) []byte {
	t.Helper()
	wire, err := transporttest.Pack(m)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	resp, err := transport.ServeMessage(context.Background(), wire, netip.MustParseAddrPort("127.0.0.1:5300"),
		transport.UDP, transport.HandlerFunc(s.serveQuery), transport.ServeConfig{UDPSize: s.cfg.UDPSize})
	if err != nil {
		t.Fatalf("ServeMessage: %v", err)
	}
	return resp
}

func TestServeMessageAppliesTheChecksToReceivedBytes(t *testing.T) {
	s := newTestServer(t, ServerConfig{Zones: []string{"fleet.example."}}, payloadHandler([]byte("ok")))
	question := []transporttest.RR{{Name: "device42.fleet.example.", Key: testKey}}

	if resp := serveWire(t, s, transporttest.Message{ID: 1, Response: true, Question: question}); resp != nil {
		t.Fatalf("QR set: got %d response bytes, want no response", len(resp))
	}

	tests := []struct {
		name string
		msg  transporttest.Message
		want transport.RCode
	}{
		{"valid", transporttest.Message{Question: question}, transport.RCodeNoError},
		{"notify", transporttest.Message{Opcode: opcodeNotify, Question: question}, transport.RCodeNotImp},
		{"no question", transporttest.Message{}, transport.RCodeFormErr},
		{"600 bytes without EDNS", padded(transporttest.Message{Question: question}, 600), transport.RCodeFormErr},
		{"1300 bytes with EDNS", padded(transporttest.Message{Question: question, EDNS: true, UDPSize: 4096}, 1300), transport.RCodeFormErr},
		{"EDNS version 1", transporttest.Message{Question: question, EDNS: true, EDNSVersion: 1, UDPSize: 1232}, transport.RCodeBadVers},
		{"outside the zone", transporttest.Message{Question: []transporttest.RR{{Name: "a.other.example.", Key: testKey}}}, transport.RCodeRefused},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := transporttest.Unpack(serveWire(t, s, tt.msg))
			if err != nil {
				t.Fatalf("Unpack: %v", err)
			}
			if got.RCode != tt.want {
				t.Fatalf("RCODE = %v, want %v", got.RCode, tt.want)
			}
		})
	}
}

// padded returns m with padding that makes it exactly size bytes on the wire.
func padded(m transporttest.Message, size int) transporttest.Message {
	m.PadTo(size)
	return m
}

// countWarnings returns the number of warning records in a text log.
func countWarnings(log string) int {
	return strings.Count(log, "level=WARN")
}

func TestServeLogsOneWarningAtStartWithNoZones(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	s := newTestServer(t, ServerConfig{Logger: logger}, payloadHandler(nil))
	if n := countWarnings(buf.String()); n != 0 {
		t.Fatalf("NewServer logged %d warnings, want none before the start", n)
	}

	cancel, _, errc := serve(t, s)
	cancel()
	if err := <-errc; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if n := countWarnings(buf.String()); n != 1 {
		t.Fatalf("the log holds %d warnings, want 1:\n%s", n, buf.String())
	}
}

func TestServeLogsNoWarningWithZones(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	s := newTestServer(t, ServerConfig{Logger: logger, Zones: []string{"fleet.example."}}, payloadHandler(nil))

	cancel, _, errc := serve(t, s)
	cancel()
	if err := <-errc; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if n := countWarnings(buf.String()); n != 0 {
		t.Fatalf("the log holds %d warnings, want none:\n%s", n, buf.String())
	}
}
