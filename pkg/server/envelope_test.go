package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/transport"
)

// twoVersionInfo is a registration with the versions 1 and 2.
var twoVersionInfo = protocol.Info{ID: "org.qmux.firmware", Versions: []uint16{1, 2}}

// responseHandler returns a handler that answers every request with resp.
func responseHandler(resp protocol.Response) protocol.HandlerFunc {
	return func(context.Context, *protocol.Request) (*protocol.Response, error) {
		return &resp, nil
	}
}

// serveResponse registers info for testKey with a handler that returns resp, and
// returns the answers that the server wrote for one valid query.
func serveResponse(t *testing.T, cfg ServerConfig, info protocol.Info, resp protocol.Response) []transport.Answer {
	t.Helper()
	if cfg.Logger == nil {
		cfg.Logger = quietLogger()
	}
	s := NewServer(cfg)
	if err := s.RegisterProtocol(testKey, info, responseHandler(resp)); err != nil {
		t.Fatalf("RegisterProtocol: %v", err)
	}
	return answer(s, validQuery())
}

// decodeAnswer fails the test when got is not one NOERROR answer that holds a valid
// envelope, and returns the envelope.
func decodeAnswer(t *testing.T, got []transport.Answer) codec.Envelope {
	t.Helper()
	checkRCode(t, got, transport.RCodeNoError)
	e, err := codec.Decode(got[0].Payload, codec.Limits{})
	if err != nil {
		t.Fatalf("Decode(RDATA): %v", err)
	}
	return e
}

func TestDispatchWrapsTheResponseInAnEnvelopeWithTheRegistrationProtocolID(t *testing.T) { // FR-codec-29, FR-codec-30
	e := decodeAnswer(t, serveResponse(t, ServerConfig{}, twoVersionInfo,
		protocol.Response{Payload: []byte("hello"), Version: 1, Status: protocol.StatusOK}))
	want := codec.Envelope{Format: 1, ProtocolID: "org.qmux.firmware", Version: 1, MessageType: 1,
		Status: 0, Payload: []byte("hello")}
	if e.Format != want.Format || e.ProtocolID != want.ProtocolID || e.Version != want.Version ||
		e.MessageType != want.MessageType || e.Status != want.Status || !bytes.Equal(e.Payload, want.Payload) {
		t.Fatalf("envelope = %+v, want %+v", e, want)
	}
}

func TestDispatchUsesTheHighestRegistrationVersionForResponseVersion0(t *testing.T) { // FR-codec-31
	e := decodeAnswer(t, serveResponse(t, ServerConfig{}, twoVersionInfo, protocol.Response{Payload: []byte("x")}))
	if e.Version != 2 {
		t.Fatalf("envelope version = %d, want 2", e.Version)
	}
}

func TestDispatchUsesTheResponseVersionWhenTheRegistrationHasIt(t *testing.T) { // FR-codec-31
	e := decodeAnswer(t, serveResponse(t, ServerConfig{}, twoVersionInfo, protocol.Response{Version: 1}))
	if e.Version != 1 {
		t.Fatalf("envelope version = %d, want 1", e.Version)
	}
}

func TestDispatchAnswersServFailForAVersionOutsideTheRegistration(t *testing.T) { // FR-codec-32
	checkRCode(t, serveResponse(t, ServerConfig{}, twoVersionInfo, protocol.Response{Version: 3}),
		transport.RCodeServFail)
}

func TestDispatchUsesMessageTypeRecordForResponseMessageType0(t *testing.T) { // FR-codec-33
	e := decodeAnswer(t, serveResponse(t, ServerConfig{}, twoVersionInfo, protocol.Response{}))
	if e.MessageType != uint64(protocol.MessageRecord) {
		t.Fatalf("envelope message type = %d, want 1", e.MessageType)
	}
}

func TestDispatchKeepsAProtocolSpecificMessageTypeAndStatus(t *testing.T) { // FR-codec-29
	e := decodeAnswer(t, serveResponse(t, ServerConfig{}, twoVersionInfo,
		protocol.Response{MessageType: 256, Status: 300}))
	if e.MessageType != 256 || e.Status != 300 {
		t.Fatalf("envelope message type, status = %d, %d, want 256, 300", e.MessageType, e.Status)
	}
}

func TestDispatchAnswersServFailForMessageType7(t *testing.T) { // FR-codec-36
	checkRCode(t, serveResponse(t, ServerConfig{}, twoVersionInfo, protocol.Response{MessageType: 7}),
		transport.RCodeServFail)
}

func TestDispatchAnswersServFailForA20000BytePayload(t *testing.T) { // FR-codec-36
	checkRCode(t, serveResponse(t, ServerConfig{}, twoVersionInfo, protocol.Response{Payload: make([]byte, 20000)}),
		transport.RCodeServFail)
}

func TestDispatchValidatesTheResolvedVersionWithTheSchema(t *testing.T) { // FR-codec-34
	schema := &countingSchema{}
	info := twoVersionInfo
	info.Schema = schema
	decodeAnswer(t, serveResponse(t, ServerConfig{}, info, protocol.Response{Payload: []byte("p")}))
	if schema.calls() != 1 || schema.versions[0] != 2 || string(schema.payloads[0]) != "p" {
		t.Fatalf("Validate calls = %v %q, want one call with version 2 and payload %q",
			schema.versions, schema.payloads, "p")
	}
}

func TestServeLogsTheApplicationStatusOfTheEnvelope(t *testing.T) {
	h := &recordHandler{}
	s := NewServer(ServerConfig{Logger: slog.New(h)})
	if err := s.RegisterProtocol(testKey, twoVersionInfo, responseHandler(protocol.Response{Status: 300})); err != nil {
		t.Fatalf("RegisterProtocol: %v", err)
	}
	answer(s, validQuery())
	if got := oneQueryRecord(t, h)["status"]; !got.Equal(slog.StringValue("300")) {
		t.Fatalf("status = %v, want 300", got)
	}
}

func TestServeLogsAnEmptyStatusForAServFailAnswer(t *testing.T) {
	h := &recordHandler{}
	s := NewServer(ServerConfig{Logger: slog.New(h)})
	if err := s.RegisterProtocol(testKey, twoVersionInfo, responseHandler(protocol.Response{Version: 3})); err != nil {
		t.Fatalf("RegisterProtocol: %v", err)
	}
	answer(s, validQuery())
	if got := oneQueryRecord(t, h)["status"]; !got.Equal(slog.StringValue("")) {
		t.Fatalf("status = %v, want the empty string", got)
	}
}

func TestDispatchAnswersServFailAndLogsOneErrorWhenTheSchemaRejectsThePayload(t *testing.T) { // FR-codec-34, FR-codec-35
	var buf syncBuffer
	info := twoVersionInfo
	info.Schema = &countingSchema{err: errors.New("bad payload")}
	got := serveResponse(t, ServerConfig{Logger: debugLogger(&buf)}, info, protocol.Response{Payload: []byte("p")})
	checkRCode(t, got, transport.RCodeServFail)
	if n := countLevel(buf.String(), slog.LevelError); n != 1 {
		t.Fatalf("the log holds %d error records, want 1:\n%s", n, buf.String())
	}
}
