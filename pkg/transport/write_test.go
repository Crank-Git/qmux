package transport_test

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/transport"
	"github.com/Crank-Git/qmux/pkg/transport/transporttest"
)

var (
	testKey    = protocol.ProtocolKey{Class: 65280, Type: 65280}
	testSource = netip.MustParseAddrPort("127.0.0.1:53211")
	testConfig = transport.ServeConfig{UDPSize: 1232}
)

// queryWire returns a packed query for name. udpSize 0 means no EDNS(0).
func queryWire(t *testing.T, name string, udpSize uint16) []byte {
	t.Helper()
	m := transporttest.Message{
		ID:       4242,
		Question: []transporttest.RR{{Name: name, Key: testKey}},
		EDNS:     udpSize != 0,
		UDPSize:  udpSize,
	}
	wire, err := transporttest.Pack(m)
	if err != nil {
		t.Fatalf("pack query: %v", err)
	}
	return wire
}

// writeResult holds what the handler of serveAnswer saw.
type writeResult struct {
	sent  transport.Sent
	err   error
	resp  []byte
	query *transport.Query
}

// serveAnswer runs ServeMessage on query with a handler that writes a.
func serveAnswer(t *testing.T, query []byte, network string, a transport.Answer) writeResult {
	t.Helper()
	var r writeResult
	h := transport.HandlerFunc(func(_ context.Context, q *transport.Query, w transport.Writer) {
		r.query = q
		r.sent, r.err = w.Write(a)
	})
	resp, err := transport.ServeMessage(context.Background(), query, testSource, network, h, testConfig)
	if err != nil {
		t.Fatalf("ServeMessage: %v", err)
	}
	r.resp = resp
	return r
}

func unpack(t *testing.T, wire []byte) transporttest.Message {
	t.Helper()
	m, err := transporttest.Unpack(wire)
	if err != nil {
		t.Fatalf("unpack response: %v", err)
	}
	return m
}

func TestWriteSendsBinaryPayloadUnchangedInOneOpaqueRR(t *testing.T) {
	payload := make([]byte, 256)
	for i := range payload {
		payload[i] = byte(i)
	}
	const name = "Device42.Fleet.Example."

	r := serveAnswer(t, queryWire(t, name, 0), transport.UDP,
		transport.Answer{RCode: transport.RCodeNoError, TTL: 300, Payload: payload})
	if r.err != nil {
		t.Fatalf("Write: %v", r.err)
	}

	want := transporttest.Message{
		ID:            4242,
		Response:      true,
		Authoritative: true,
		Question:      []transporttest.RR{{Name: name, Key: testKey}},
		Answer:        []transporttest.RR{{Name: name, Key: testKey, TTL: 300, Data: payload}},
	}
	if m := unpack(t, r.resp); !reflect.DeepEqual(m, want) {
		t.Errorf("response = %+v, want %+v", m, want)
	}
	if r.sent.RCode != transport.RCodeNoError || r.sent.Truncated || r.sent.WireSize != len(r.resp) {
		t.Errorf("Sent = %+v, want NOERROR, not truncated, WireSize %d", r.sent, len(r.resp))
	}
}

func TestWriteSendsNoAnswerRRAndNoAAForAnErrorRCode(t *testing.T) {
	r := serveAnswer(t, queryWire(t, "a.example.", 0), transport.UDP,
		transport.Answer{RCode: transport.RCodeRefused, Payload: []byte{1}})
	if r.err != nil {
		t.Fatalf("Write: %v", r.err)
	}
	m := unpack(t, r.resp)
	if m.RCode != transport.RCodeRefused || len(m.Answer) != 0 || m.Authoritative {
		t.Errorf("response RCODE %v answers %d AA %v, want REFUSED, 0, false", m.RCode, len(m.Answer), m.Authoritative)
	}
}

func TestWriteReturnsThePackError(t *testing.T) {
	// BADVERS needs an OPT RR, and a query without EDNS(0) gets no OPT RR.
	r := serveAnswer(t, queryWire(t, "a.example.", 0), transport.UDP,
		transport.Answer{RCode: transport.RCodeBadVers})
	if r.err == nil {
		t.Fatal("Write returned nil, want a pack error")
	}
	if r.resp != nil {
		t.Errorf("ServeMessage returned %d bytes, want none after a pack error", len(r.resp))
	}
}

func TestSecondWriteReturnsErrAlreadyWritten(t *testing.T) {
	var first, second error
	h := transport.HandlerFunc(func(_ context.Context, _ *transport.Query, w transport.Writer) {
		_, first = w.Write(transport.Answer{RCode: transport.RCodeNoError})
		_, second = w.Write(transport.Answer{RCode: transport.RCodeNoError})
	})
	if _, err := transport.ServeMessage(context.Background(), queryWire(t, "a.example.", 0),
		testSource, transport.UDP, h, testConfig); err != nil {
		t.Fatalf("ServeMessage: %v", err)
	}
	if first != nil {
		t.Fatalf("first Write: %v", first)
	}
	if !errors.Is(second, transport.ErrAlreadyWritten) {
		t.Errorf("second Write = %v, want ErrAlreadyWritten", second)
	}
}

func TestWriteAddsOPTRRWithServerUDPSizeForEDNSQuery(t *testing.T) {
	r := serveAnswer(t, queryWire(t, "a.example.", 4096), transport.UDP,
		transport.Answer{RCode: transport.RCodeNoError, Payload: []byte{1}})
	m := unpack(t, r.resp)
	if !m.EDNS || m.UDPSize != 1232 || m.EDNSVersion != 0 {
		t.Errorf("response EDNS=%v UDPSize=%d version=%d, want true 1232 0", m.EDNS, m.UDPSize, m.EDNSVersion)
	}
}

func TestWriteAddsNoOPTRRForQueryWithoutEDNS(t *testing.T) {
	r := serveAnswer(t, queryWire(t, "a.example.", 0), transport.UDP,
		transport.Answer{RCode: transport.RCodeNoError, Payload: []byte{1}})
	if m := unpack(t, r.resp); m.EDNS {
		t.Error("response has an OPT RR, want none")
	}
}

func TestWritePutsBADVERSInTheExtendedRCODE(t *testing.T) {
	r := serveAnswer(t, queryWire(t, "a.example.", 1232), transport.UDP,
		transport.Answer{RCode: transport.RCodeBadVers})
	if r.err != nil {
		t.Fatalf("Write: %v", r.err)
	}
	// BADVERS is 16, so the 4-bit header RCODE is 0 and the OPT RR holds the upper bits.
	if got := r.resp[3] & 0x0f; got != 0 {
		t.Errorf("header RCODE bits = %d, want 0", got)
	}
	m := unpack(t, r.resp)
	if !m.EDNS || m.RCode != transport.RCodeBadVers {
		t.Errorf("response EDNS=%v RCODE=%v, want true BADVERS", m.EDNS, m.RCode)
	}
	if r.sent.RCode != transport.RCodeBadVers {
		t.Errorf("Sent.RCode = %v, want BADVERS", r.sent.RCode)
	}
}

// baseSize returns the response size for an empty payload, so a test can pick a
// payload that makes the response exactly a given size.
func baseSize(t *testing.T, query []byte, network string) int {
	t.Helper()
	r := serveAnswer(t, query, network, transport.Answer{RCode: transport.RCodeNoError})
	if r.err != nil {
		t.Fatalf("Write: %v", r.err)
	}
	return len(r.resp)
}

func TestWriteTruncatesUDPResponseAboveTheLimit(t *testing.T) {
	tests := []struct {
		name       string
		advertised uint16 // 0 means no EDNS(0)
		limit      int
	}{
		{"without EDNS the limit is 512", 0, 512},
		{"with EDNS the advertised size is smaller", 1000, 1000},
		{"with EDNS the server size is smaller", 4096, 1232},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query := queryWire(t, "a.example.", tt.advertised)
			fit := tt.limit - baseSize(t, query, transport.UDP)

			r := serveAnswer(t, query, transport.UDP,
				transport.Answer{RCode: transport.RCodeNoError, Payload: make([]byte, fit)})
			if m := unpack(t, r.resp); len(r.resp) != tt.limit || m.Truncated || len(m.Answer) != 1 {
				t.Errorf("at the limit: %d bytes TC=%v answers=%d, want %d bytes, no TC, 1 answer",
					len(r.resp), m.Truncated, len(m.Answer), tt.limit)
			}

			r = serveAnswer(t, query, transport.UDP,
				transport.Answer{RCode: transport.RCodeNoError, Payload: make([]byte, fit+1)})
			if r.err != nil {
				t.Fatalf("Write: %v", r.err)
			}
			m := unpack(t, r.resp)
			if !m.Truncated || len(m.Answer) != 0 || len(r.resp) > tt.limit {
				t.Errorf("above the limit: %d bytes TC=%v answers=%d, want TC and 0 answers within %d bytes",
					len(r.resp), m.Truncated, len(m.Answer), tt.limit)
			}
			if m.EDNS != (tt.advertised != 0) {
				t.Errorf("above the limit: EDNS=%v, want %v", m.EDNS, tt.advertised != 0)
			}
			if !r.sent.Truncated || r.sent.RCode != transport.RCodeNoError || r.sent.WireSize != len(r.resp) {
				t.Errorf("Sent = %+v, want truncated NOERROR with WireSize %d", r.sent, len(r.resp))
			}
		})
	}
}

func TestWriteAnswersSERVFAILForTCPResponseAbove65535Bytes(t *testing.T) {
	query := queryWire(t, "a.example.", 1232)
	fit := 65535 - baseSize(t, query, transport.TCP)

	r := serveAnswer(t, query, transport.TCP,
		transport.Answer{RCode: transport.RCodeNoError, Payload: make([]byte, fit)})
	if m := unpack(t, r.resp); len(r.resp) != 65535 || m.RCode != transport.RCodeNoError || len(m.Answer) != 1 {
		t.Errorf("at 65535 bytes: %d bytes RCODE %v answers %d, want 65535 bytes NOERROR 1 answer",
			len(r.resp), m.RCode, len(m.Answer))
	}

	for _, size := range []int{fit + 1, 70000} {
		r = serveAnswer(t, query, transport.TCP,
			transport.Answer{RCode: transport.RCodeNoError, Payload: make([]byte, size)})
		if r.err != nil {
			t.Fatalf("Write with payload %d: %v", size, r.err)
		}
		m := unpack(t, r.resp)
		if m.RCode != transport.RCodeServFail || len(m.Answer) != 0 || m.Truncated {
			t.Errorf("payload %d: RCODE %v answers %d TC %v, want SERVFAIL 0 false", size, m.RCode, len(m.Answer), m.Truncated)
		}
		if r.sent.RCode != transport.RCodeServFail {
			t.Errorf("payload %d: Sent.RCode = %v, want SERVFAIL", size, r.sent.RCode)
		}
	}
}

func TestRCodeStringNamesTheRCode(t *testing.T) {
	tests := map[transport.RCode]string{
		transport.RCodeNoError:  "NOERROR",
		transport.RCodeFormErr:  "FORMERR",
		transport.RCodeServFail: "SERVFAIL",
		transport.RCodeNotImp:   "NOTIMP",
		transport.RCodeRefused:  "REFUSED",
		transport.RCodeBadVers:  "BADVERS",
		transport.RCode(3):      "NXDOMAIN",
		transport.RCode(6):      "YXDOMAIN",
		transport.RCode(7):      "YXRRSET",
		transport.RCode(8):      "NXRRSET",
		transport.RCode(9):      "NOTAUTH",
		transport.RCode(10):     "NOTZONE",
		transport.RCode(11):     "RCODE11",
		transport.RCode(15):     "RCODE15",
		transport.RCode(17):     "RCODE17",
		transport.RCode(4095):   "RCODE4095",
	}
	for rc, want := range tests {
		if got := rc.String(); got != want {
			t.Errorf("RCode(%d).String() = %q, want %q", uint16(rc), got, want)
		}
	}
}

// txtQuery returns a packed query for name at class 1, QTYPE 16, without EDNS(0).
func txtQuery(t *testing.T, name string) []byte {
	t.Helper()
	wire, err := transporttest.Pack(transporttest.Message{
		ID:       4242,
		Question: []transporttest.RR{{Name: name, Key: protocol.ProtocolKey{Class: 1, Type: 16}}},
	})
	if err != nil {
		t.Fatalf("pack query: %v", err)
	}
	return wire
}

func TestWriteSplitsAQTYPE16PayloadIntoCharacterStringsOfAtMost255Bytes(t *testing.T) { // FR-interop-7
	payload := make([]byte, 300)
	for i := range payload {
		payload[i] = byte(i)
	}
	r := serveAnswer(t, txtQuery(t, "abc.probe.fleet.example."), transport.TCP,
		transport.Answer{RCode: transport.RCodeNoError, TTL: 60, Payload: payload})
	if r.err != nil {
		t.Fatalf("Write: %v", r.err)
	}
	m := unpack(t, r.resp)
	if len(m.Answer) != 1 {
		t.Fatalf("response has %d answer RRs, want 1", len(m.Answer))
	}
	want := append(append([]byte{255}, payload[:255]...), append([]byte{45}, payload[255:]...)...)
	if got := m.Answer[0].Data; !bytes.Equal(got, want) {
		t.Errorf("RDATA = %x, want two character-strings %x", got, want)
	}
}

func TestWriteSendsOneEmptyCharacterStringForAnEmptyQTYPE16Payload(t *testing.T) { // FR-interop-7
	r := serveAnswer(t, txtQuery(t, "abc.probe.fleet.example."), transport.UDP,
		transport.Answer{RCode: transport.RCodeNoError, TTL: 60})
	if r.err != nil {
		t.Fatalf("Write: %v", r.err)
	}
	m := unpack(t, r.resp)
	if len(m.Answer) != 1 || !bytes.Equal(m.Answer[0].Data, []byte{0}) {
		t.Errorf("answer = %+v, want one RR with RDATA 00", m.Answer)
	}
}
