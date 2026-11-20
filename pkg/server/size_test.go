package server

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/transport"
	"github.com/Crank-Git/qmux/pkg/transport/transporttest"
)

// sizeQName is the QNAME of the size tests. Its wire form has 24 bytes.
const sizeQName = "device42.fleet.example."

// responseOverhead is the size of a NOERROR response to a query for sizeQName without EDNS(0),
// less the envelope. It is 12 bytes of header, 28 bytes of question, and 12 bytes of
// answer RR: a 2-byte compressed name and a 10-byte fixed part.
const responseOverhead = 12 + 28 + 12

// payloadForEnvelope returns a payload of 0xab bytes whose envelope for testInfo is
// exactly size bytes. The length of the CBOR byte string head depends on the payload
// length, so the function searches for the length.
func payloadForEnvelope(t *testing.T, size int) []byte {
	t.Helper()
	for n := size; n >= 0; n-- {
		payload := bytes.Repeat([]byte{0xab}, n)
		b, err := codec.Encode(codec.Envelope{Format: 1, ProtocolID: testInfo.ID, Version: 1,
			MessageType: uint64(protocol.MessageRecord), Payload: payload}, codec.Limits{MaxEnvelope: 65000})
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		if len(b) == size {
			return payload
		}
	}
	t.Fatalf("no payload gives a %d-byte envelope", size)
	return nil
}

// sizeQuery returns a query for sizeQName. advertised 0 means a query without EDNS(0).
func sizeQuery(id, advertised uint16) transporttest.Message {
	return transporttest.Message{
		ID:       id,
		Question: []transporttest.RR{{Name: sizeQName, Key: testKey}},
		EDNS:     advertised != 0,
		UDPSize:  advertised,
	}
}

// exchangeMessage sends m to addr over network and returns the parsed response.
func exchangeMessage(t *testing.T, network, addr string, m transporttest.Message) transporttest.Message {
	t.Helper()
	wire, err := transporttest.Pack(m)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := transporttest.Exchange(ctx, network, addr, wire)
	if err != nil {
		t.Fatalf("%s exchange: %v", network, err)
	}
	got, err := transporttest.Unpack(resp)
	if err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	return got
}

// startPayloadServer starts a server with cfg that answers every query with payload,
// and returns its address.
func startPayloadServer(t *testing.T, cfg ServerConfig, payload []byte) string {
	t.Helper()
	_, addr, _ := startServer(t, newTestServer(t, cfg, payloadHandler(payload)))
	return addr
}

// checkTruncated fails the test when m is not a truncated NOERROR response with no
// answer RR.
func checkTruncated(t *testing.T, m transporttest.Message, id uint16) {
	t.Helper()
	if m.ID != id || m.RCode != transport.RCodeNoError || !m.Truncated || len(m.Answer) != 0 {
		t.Fatalf("got ID %d, RCODE %v, TC %t, %d answers; want ID %d, NOERROR, TC, no answer",
			m.ID, m.RCode, m.Truncated, len(m.Answer), id)
	}
}

// checkOPT fails the test when the presence or the UDP size of the OPT RR in m is wrong.
// udpSize 0 means that m must hold no OPT RR.
func checkOPT(t *testing.T, m transporttest.Message, udpSize uint16) {
	t.Helper()
	if udpSize == 0 {
		if m.EDNS {
			t.Fatalf("response holds an OPT RR with UDP size %d, want no OPT RR", m.UDPSize)
		}
		return
	}
	if !m.EDNS || m.UDPSize != udpSize {
		t.Fatalf("got OPT RR %t with UDP size %d, want an OPT RR with UDP size %d", m.EDNS, m.UDPSize, udpSize)
	}
}

func TestServeTruncatesA600BytePayloadToAUDPQueryWithoutEDNS(t *testing.T) {
	addr := startPayloadServer(t, ServerConfig{}, bytes.Repeat([]byte{0xab}, 600))

	m := exchangeMessage(t, transport.UDP, addr, sizeQuery(1, 0))
	checkTruncated(t, m, 1)
	checkOPT(t, m, 0)
}

func TestServeUsesA512ByteLimitForAUDPQueryWithoutEDNS(t *testing.T) {
	tests := []struct {
		name      string
		envelope  int
		truncated bool
	}{
		{"a 512-byte response", 512 - responseOverhead, false},
		{"a 513-byte response", 513 - responseOverhead, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := payloadForEnvelope(t, tt.envelope)
			addr := startPayloadServer(t, ServerConfig{}, payload)

			m := exchangeMessage(t, transport.UDP, addr, sizeQuery(2, 0))
			if tt.truncated {
				checkTruncated(t, m, 2)
				return
			}
			checkAnswer(t, transport.UDP, m, 2, payload)
		})
	}
}

func TestServeTruncatesA1500BytePayloadToAQueryThatAdvertises4096WithTheDefaultUDPSize(t *testing.T) {
	addr := startPayloadServer(t, ServerConfig{}, bytes.Repeat([]byte{0xab}, 1500))

	m := exchangeMessage(t, transport.UDP, addr, sizeQuery(3, 4096))
	checkTruncated(t, m, 3)
	checkOPT(t, m, 1232)
}

func TestServeUsesTheSmallerOfTheAdvertisedAndTheConfiguredUDPSize(t *testing.T) {
	tests := []struct {
		name       string
		cfgSize    uint16
		advertised uint16
		payload    int
		truncated  bool
	}{
		{"advertised 4096 and the default 1232: 1100 bytes fit", 0, 4096, 1100, false},
		{"advertised 4096 and 2048 configured: 1500 bytes fit", 2048, 4096, 1500, false},
		{"advertised 800 and the default 1232: 1000 bytes do not fit", 0, 800, 1000, true},
		{"advertised 800 and the default 1232: 700 bytes fit", 0, 800, 700, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := bytes.Repeat([]byte{0xab}, tt.payload)
			addr := startPayloadServer(t, ServerConfig{UDPSize: tt.cfgSize}, payload)

			m := exchangeMessage(t, transport.UDP, addr, sizeQuery(4, tt.advertised))
			if tt.truncated {
				checkTruncated(t, m, 4)
				return
			}
			checkAnswer(t, transport.UDP, m, 4, payload)
		})
	}
}

func TestServeAddsAnOPTRRWithTheConfiguredUDPSizeToEachEDNSResponse(t *testing.T) {
	other := protocol.ProtocolKey{Class: 65281, Type: 65281}
	tests := []struct {
		name       string
		cfgSize    uint16
		advertised uint16
		key        protocol.ProtocolKey
		wantRCode  transport.RCode
		wantSize   uint16
	}{
		{"NOERROR with the default UDP size", 0, 4096, testKey, transport.RCodeNoError, 1232},
		{"NOERROR to a query that advertises 512", 0, 512, testKey, transport.RCodeNoError, 1232},
		{"NOERROR with 4096 configured", 4096, 1232, testKey, transport.RCodeNoError, 4096},
		{"REFUSED to an unregistered protocol key", 0, 4096, other, transport.RCodeRefused, 1232},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := startPayloadServer(t, ServerConfig{UDPSize: tt.cfgSize}, []byte("ok"))
			q := sizeQuery(5, tt.advertised)
			q.Question[0].Key = tt.key

			for _, network := range []string{transport.UDP, transport.TCP} {
				m := exchangeMessage(t, network, addr, q)
				if m.RCode != tt.wantRCode {
					t.Fatalf("%s: RCODE = %v, want %v", network, m.RCode, tt.wantRCode)
				}
				checkOPT(t, m, tt.wantSize)
			}
		})
	}
}

func TestServeSendsNoOPTRRToAQueryWithoutEDNS(t *testing.T) {
	addr := startPayloadServer(t, ServerConfig{}, []byte("ok"))

	for _, network := range []string{transport.UDP, transport.TCP} {
		m := exchangeMessage(t, network, addr, sizeQuery(6, 0))
		checkAnswer(t, network, m, 6, []byte("ok"))
		checkOPT(t, m, 0)
	}
}

func TestServeKeepsTheOPTRRAndRemovesTheAnswerRRFromATruncatedResponse(t *testing.T) {
	addr := startPayloadServer(t, ServerConfig{}, bytes.Repeat([]byte{0xab}, 2000))

	m := exchangeMessage(t, transport.UDP, addr, sizeQuery(7, 1232))
	checkTruncated(t, m, 7)
	checkOPT(t, m, 1232)
	if len(m.Question) != 1 || m.Question[0].Name != sizeQName || m.Question[0].Key != testKey {
		t.Fatalf("question = %+v, want one question for %q and %v", m.Question, sizeQName, testKey)
	}
}

func TestServeSendsTheFullAnswerOverTCPAfterATruncatedUDPResponse(t *testing.T) {
	payload := bytes.Repeat([]byte{0xab}, 1500)
	addr := startPayloadServer(t, ServerConfig{}, payload)

	for _, advertised := range []uint16{0, 4096} {
		q := sizeQuery(8, advertised)
		checkTruncated(t, exchangeMessage(t, transport.UDP, addr, q), 8)

		m := exchangeMessage(t, transport.TCP, addr, q)
		checkAnswer(t, transport.TCP, m, 8, payload)
		if m.Truncated {
			t.Fatalf("advertised %d: the TCP response has the TC bit", advertised)
		}
	}
}

func TestServeAnswersServFailToA70000BytePayloadOverTCP(t *testing.T) {
	addr := startPayloadServer(t, ServerConfig{}, bytes.Repeat([]byte{0xab}, 70000))

	for _, advertised := range []uint16{0, 4096} {
		m := exchangeMessage(t, transport.TCP, addr, sizeQuery(9, advertised))
		if m.ID != 9 || m.RCode != transport.RCodeServFail || m.Truncated || len(m.Answer) != 0 {
			t.Fatalf("advertised %d: got ID %d, RCODE %v, TC %t, %d answers; want ID 9, SERVFAIL, no TC, no answer",
				advertised, m.ID, m.RCode, m.Truncated, len(m.Answer))
		}
	}
}

func TestServeAnswersTheLargestEnvelopeOverTCP(t *testing.T) { // FR-codec-36
	tests := []struct {
		name     string
		envelope int
		servFail bool
	}{
		{"a 16384-byte envelope", 16384, false},
		{"a 16385-byte envelope", 16385, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := payloadForEnvelope(t, tt.envelope)
			addr := startPayloadServer(t, ServerConfig{}, payload)

			m := exchangeMessage(t, transport.TCP, addr, sizeQuery(10, 0))
			if tt.servFail {
				if m.RCode != transport.RCodeServFail || len(m.Answer) != 0 {
					t.Fatalf("got RCODE %v, %d answers; want SERVFAIL, no answer", m.RCode, len(m.Answer))
				}
				return
			}
			checkAnswer(t, transport.TCP, m, 10, payload)
		})
	}
}
