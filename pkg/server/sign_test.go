package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"

	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/security"
	"github.com/Crank-Git/qmux/pkg/transport"
)

// signSeed is the fixed seed of the test signing key.
var signSeed = bytes.Repeat([]byte{0x24}, ed25519.SeedSize)

func testSigner() *security.Signer {
	return &security.Signer{
		Key: ed25519.NewKeyFromSeed(signSeed),
		Now: func() time.Time { return time.Unix(1700000000, 0) },
	}
}

// statusHandler returns a handler that answers every request with status.
func statusHandler(status protocol.Status) protocol.HandlerFunc {
	return func(context.Context, *protocol.Request) (*protocol.Response, error) {
		return &protocol.Response{Status: status}, nil
	}
}

// verifiesAgainst reports whether the signature block of e verifies over the
// signing input of e for qname and key. The test builds the signing input from
// docs/specs/spec.md § Data model, apart from the code in pkg/security.
func verifiesAgainst(t *testing.T, pub ed25519.PublicKey, e codec.Envelope, qname string, key protocol.ProtocolKey) bool {
	t.Helper()
	em, err := cbor.CoreDetEncOptions().EncMode()
	if err != nil {
		t.Fatalf("EncMode: %v", err)
	}
	sb := e.Signature
	// The payload is a CBOR byte string also when it is empty, and not CBOR null.
	payload := append([]byte{}, e.Payload...)
	msg, err := em.Marshal([]any{
		"qmux-sig/1", qname, key.Class, key.Type, e.Format, e.ProtocolID, e.Version,
		e.MessageType, e.Status, payload, sb.KeyID, sb.Alg, sb.IssuedAt, sb.ExpiresAt,
	})
	if err != nil {
		t.Fatalf("Marshal signing input: %v", err)
	}
	return ed25519.Verify(pub, msg, sb.Signature)
}

func TestServeQuerySignsTheEnvelopeOfEveryApplicationStatus(t *testing.T) { // FR-security-15
	cases := []struct {
		name   string
		qname  string
		status protocol.Status
	}{
		{"ok from the handler", "device42.fleet.example.", protocol.StatusOK},
		{"not-found from the handler", "device42.fleet.example.", protocol.StatusNotFound},
		{"unavailable from the handler", "device42.fleet.example.", protocol.StatusUnavailable},
		{"protocol-specific from the handler", "device42.fleet.example.", 300},
		{"bad-request from the server", "_vx.device42.fleet.example.", protocol.StatusBadRequest},
		{"version-unsupported from the server", "_v9.device42.fleet.example.", protocol.StatusVersionUnsupported},
	}
	signer := testSigner()
	pub := signer.Key.Public().(ed25519.PublicKey)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewServer(ServerConfig{Zones: []string{"fleet.example."}, Signer: signer, Logger: quietLogger()})
			if err := s.RegisterProtocol(testKey, testInfo, statusHandler(tc.status)); err != nil {
				t.Fatalf("RegisterProtocol: %v", err)
			}
			q := validQuery()
			q.Name = tc.qname
			e := decodeAnswer(t, answer(s, q))
			if e.Status != uint64(tc.status) {
				t.Fatalf("status = %d, want %d", e.Status, tc.status)
			}
			if e.Signature == nil {
				t.Fatal("the envelope has no signature block")
			}
			if !verifiesAgainst(t, pub, e, tc.qname, testKey) {
				t.Fatal("the signature does not verify")
			}
		})
	}
}

func TestServeQuerySignsWithTheFullQNameIncludingTheVersionLabel(t *testing.T) { // FR-security-16
	signer := testSigner()
	s := NewServer(ServerConfig{Zones: []string{"fleet.example."}, Signer: signer, Logger: quietLogger()})
	if err := s.RegisterProtocol(testKey, testInfo, payloadHandler([]byte("x"))); err != nil {
		t.Fatalf("RegisterProtocol: %v", err)
	}
	q := validQuery()
	q.Name = "_v1.Device42.Fleet.Example."
	e := decodeAnswer(t, answer(s, q))
	pub := signer.Key.Public().(ed25519.PublicKey)
	if !verifiesAgainst(t, pub, e, "_v1.device42.fleet.example.", testKey) {
		t.Fatal("the signature does not verify against the full lowercase QNAME")
	}
	for _, other := range []string{"device42.fleet.example.", "_v1.Device42.Fleet.Example."} {
		if verifiesAgainst(t, pub, e, other, testKey) {
			t.Fatalf("the signature verifies against %q", other)
		}
	}
}

func TestServeQueryAnswersServFailWhenSigningFails(t *testing.T) { // FR-security-17
	for _, name := range []string{"device42.fleet.example.", "_v9.device42.fleet.example."} {
		t.Run(name, func(t *testing.T) {
			s := NewServer(ServerConfig{Signer: &security.Signer{}, Logger: quietLogger()})
			if err := s.RegisterProtocol(testKey, testInfo, payloadHandler([]byte("x"))); err != nil {
				t.Fatalf("RegisterProtocol: %v", err)
			}
			q := validQuery()
			q.Name = name
			checkRCode(t, answer(s, q), transport.RCodeServFail)
		})
	}
}

func TestServeQueryReturnsAnUnsignedEnvelopeWhenSignerIsNil(t *testing.T) {
	e := decodeAnswer(t, serveResponse(t, ServerConfig{}, testInfo, protocol.Response{Payload: []byte("x")}))
	if e.Signature != nil {
		t.Fatalf("signature block = %+v, want none", e.Signature)
	}
}
