package client_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/security"
	"github.com/Crank-Git/qmux/pkg/transport"
	"github.com/Crank-Git/qmux/pkg/transport/transporttest"
)

// signTime is the clock of the test signer and of the test verifier, so that each
// signed answer is fresh.
var signTime = time.Unix(1_800_000_000, 0)

// signingKey is the test key. A fixed seed makes each run sign the same bytes.
var signingKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x46}, ed25519.SeedSize))

// otherKey is a key that no test trust anchor holds.
var otherKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x47}, ed25519.SeedSize))

// newVerifier returns a verifier that trusts signingKey, with the clock at now.
func newVerifier(now time.Time) *security.Verifier {
	pub := signingKey.Public().(ed25519.PublicKey)
	return &security.Verifier{
		Anchors: []security.TrustAnchor{{KeyID: security.KeyID(pub), PublicKey: pub}},
		Now:     func() time.Time { return now },
	}
}

// signedWith returns a recorder edit that sends e, signed by key for the question of
// the response, as the RDATA of the answer RR. Each edit signs again, so that the
// signature binds the QNAME of each query.
func signedWith(t *testing.T, key ed25519.PrivateKey, e codec.Envelope) func(string, *transporttest.Message) {
	t.Helper()
	s := &security.Signer{Key: key, Now: func() time.Time { return signTime }}
	return func(_ string, m *transporttest.Message) {
		q := m.Question[0]
		signed := e
		if err := s.Sign(&signed, q.Name, q.Key); err != nil {
			t.Errorf("Sign: %v", err)
			return
		}
		if len(m.Answer) > 0 {
			m.Answer[0].Data = mustEncode(signed)
		}
	}
}

// withFallback returns edit followed by truncateUDP, so that the client gets the
// edited answer over TCP only.
func withFallback(edit func(string, *transporttest.Message)) func(string, *transporttest.Message) {
	return func(network string, m *transporttest.Message) {
		edit(network, m)
		truncateUDP(network, m)
	}
}

// verifyQuery sends q through a client with the trust anchors v to a recorder with
// edit, and returns the result and the error of Query.
func verifyQuery(t *testing.T, v *security.Verifier, cfg client.ClientConfig, edit func(string, *transporttest.Message), q client.Query) (*client.Result, error) {
	t.Helper()
	r := recorder{edit: edit}
	cfg.Resolver, cfg.TrustAnchors = r.start(t), v
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return client.NewClient(cfg).Query(ctx, q)
}

// wantVerifyError fails the test when res is not nil, or when err is not a
// *client.VerifyError with the reason want. A nil want requires a non-nil reason.
func wantVerifyError(t *testing.T, res *client.Result, err error, want error) {
	t.Helper()
	if res != nil {
		t.Errorf("result = %+v, want nil", res)
	}
	var ve *client.VerifyError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a *client.VerifyError", err)
	}
	if ve.Reason == nil || want != nil && (ve.Reason != want || !errors.Is(err, want)) {
		t.Fatalf("VerifyError.Reason = %v, error %v; want the reason %v", ve.Reason, err, want)
	}
	var ae *client.AppError
	var de *client.DNSError
	if errors.As(err, &ae) || errors.As(err, &de) {
		t.Fatalf("error = %v, want no AppError and no DNSError in it", err)
	}
}

func TestQueryReturnsAVerifiedResultForASignedAnswer(t *testing.T) { // FR-security-34, FR-security-35, FR-security-37
	for _, tc := range []struct {
		name    string
		network string
		edit    func(func(string, *transporttest.Message)) func(string, *transporttest.Message)
	}{
		{name: "udp"},
		{name: "Network tcp", network: transport.TCP},
		{name: "tcp fallback", edit: withFallback},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edit := signedWith(t, signingKey, testEnvelope)
			if tc.edit != nil {
				edit = tc.edit(edit)
			}
			res, err := verifyQuery(t, newVerifier(signTime), client.ClientConfig{Network: tc.network}, edit, testQuery)
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if !res.Verified {
				t.Error("Verified = false, want true for a signed answer")
			}
			if !bytes.Equal(res.Payload, testPayload) || res.ProtocolID != testEnvelope.ProtocolID {
				t.Errorf("Payload, ProtocolID = %x, %q, want %x, %q", res.Payload, res.ProtocolID, testPayload, testEnvelope.ProtocolID)
			}
		})
	}
}

func TestQueryReturnsAVerifyErrorAndNoResultForAnUnsignedAnswer(t *testing.T) { // FR-security-36
	for _, tc := range []struct {
		name    string
		network string
		edit    func(string, *transporttest.Message)
	}{
		{name: "udp"},
		{name: "Network tcp", network: transport.TCP},
		{name: "tcp fallback", edit: truncateUDP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := verifyQuery(t, newVerifier(signTime), client.ClientConfig{Network: tc.network}, tc.edit, testQuery)
			wantVerifyError(t, res, err, security.ErrUnsigned)
		})
	}
}

func TestQueryReturnsTheSentinelOfTheFailedCheckAsTheReason(t *testing.T) { // FR-security-36
	signed := signedWith(t, signingKey, testEnvelope)
	for _, tc := range []struct {
		name string
		now  time.Time
		edit func(string, *transporttest.Message)
		want error
	}{
		{name: "unknown key", now: signTime, edit: signedWith(t, otherKey, testEnvelope), want: security.ErrUnknownKey},
		{name: "expired", now: signTime.Add(3 * time.Hour), edit: signed, want: security.ErrExpired},
		{name: "not yet valid", now: signTime.Add(-time.Hour), edit: signed, want: security.ErrNotYetValid},
		{name: "bad signature", now: signTime, edit: func(network string, m *transporttest.Message) {
			signed(network, m)
			m.Answer[0].Data = mustEncode(withBadSignature(t, m.Answer[0].Data))
		}, want: security.ErrBadSignature},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := verifyQuery(t, newVerifier(tc.now), client.ClientConfig{}, tc.edit, testQuery)
			wantVerifyError(t, res, err, tc.want)
		})
	}
}

// withBadSignature returns the envelope that rdata encodes, with one bit of the
// signature changed.
func withBadSignature(t *testing.T, rdata []byte) codec.Envelope {
	t.Helper()
	e, err := codec.Decode(rdata, codec.Limits{})
	if err != nil || e.Signature == nil {
		t.Fatalf("decode the signed RDATA: %v", err)
	}
	sb := *e.Signature
	sb.Signature = bytes.Clone(sb.Signature)
	sb.Signature[0] ^= 0x01
	e.Signature = &sb
	return e
}

func TestQueryReturnsAVerifyErrorForRDATAThatIsNotAValidEnvelope(t *testing.T) { // FR-security-36
	// testRDATA[1] is key 0 and testRDATA[2] is the format 1. The format in two bytes
	// decodes to the same envelope, but it does not encode back to the same bytes.
	nonDeterministic := append([]byte{testRDATA[0], testRDATA[1], 0x18, 0x01}, testRDATA[3:]...)
	for _, tc := range []struct {
		name  string
		rdata []byte
		want  error
	}{
		{name: "the byte 0xff", rdata: []byte{0xff}},
		{name: "not deterministic", rdata: nonDeterministic, want: security.ErrNotDeterministic},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := verifyQuery(t, newVerifier(signTime), client.ClientConfig{}, answerWith(tc.rdata), testQuery)
			wantVerifyError(t, res, err, tc.want)
		})
	}
}

func TestQueryVerifiesBeforeItComparesTheProtocolIDAndTheVersion(t *testing.T) { // FR-security-35
	unsigned := envelopeWith(func(e *codec.Envelope) { e.ProtocolID, e.Version = "org.qmux.other", 9 })
	for name, q := range map[string]client.Query{
		"protocol ID": {Name: testName, Class: testKey.Class, Type: testKey.Type, ExpectedProtocolID: "org.qmux.firmware"},
		"version":     {Name: testName, Class: testKey.Class, Type: testKey.Type, Version: 1},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := verifyQuery(t, newVerifier(signTime), client.ClientConfig{}, answerWith(mustEncode(unsigned)), q)
			wantVerifyError(t, res, err, security.ErrUnsigned)
		})
	}
}

func TestQueryReturnsAVerifyErrorAndNoAppErrorForANotFoundAnswerWithABadSignature(t *testing.T) { // FR-security-39
	notFound := envelopeWith(func(e *codec.Envelope) { e.Status = uint64(protocol.StatusNotFound) })
	signed := signedWith(t, signingKey, notFound)
	for name, wrap := range map[string]func(func(string, *transporttest.Message)) func(string, *transporttest.Message){
		"udp":          func(e func(string, *transporttest.Message)) func(string, *transporttest.Message) { return e },
		"tcp fallback": withFallback,
	} {
		t.Run(name, func(t *testing.T) {
			edit := wrap(func(network string, m *transporttest.Message) {
				signed(network, m)
				if len(m.Answer) > 0 {
					m.Answer[0].Data = mustEncode(withBadSignature(t, m.Answer[0].Data))
				}
			})
			res, err := verifyQuery(t, newVerifier(signTime), client.ClientConfig{}, edit, testQuery)
			wantVerifyError(t, res, err, security.ErrBadSignature)
		})
	}
}

func TestQueryReturnsAVerifiedResultAndAnAppErrorForASignedNotFoundAnswer(t *testing.T) { // FR-security-39
	notFound := envelopeWith(func(e *codec.Envelope) { e.Status = uint64(protocol.StatusNotFound) })
	res, err := verifyQuery(t, newVerifier(signTime), client.ClientConfig{}, signedWith(t, signingKey, notFound), testQuery)
	var ae *client.AppError
	if res == nil || !res.Verified || !errors.As(err, &ae) {
		t.Fatalf("result %+v, error %v; want a verified result and an AppError", res, err)
	}
}

func TestQueryReturnsAnUnverifiedResultForASignedAnswerWithoutTrustAnchors(t *testing.T) { // FR-security-38
	res, err := verifyQuery(t, nil, client.ClientConfig{}, signedWith(t, signingKey, testEnvelope), testQuery)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.Verified {
		t.Error("Verified = true, want false for a client with no trust anchors")
	}
}

func TestTrustAnchorsReturnsTheVerifierOfTheConfiguration(t *testing.T) { // FR-security-34
	v := newVerifier(signTime)
	if got := client.NewClient(client.ClientConfig{TrustAnchors: v}).TrustAnchors(); got != v {
		t.Errorf("TrustAnchors() = %p, want %p", got, v)
	}
	if got := client.NewClient(client.ClientConfig{}).TrustAnchors(); got != nil {
		t.Errorf("TrustAnchors() = %p, want nil", got)
	}
}

func TestResultRawHoldsTheRDATAThatTheVerifierChecked(t *testing.T) { // FR-security-43
	res, err := verifyQuery(t, newVerifier(signTime), client.ClientConfig{}, signedWith(t, signingKey, testEnvelope), testQuery)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	// A second verifier has its own freshness store, so it checks the bytes again.
	e, err := newVerifier(signTime).Verify(res.Raw, res.Name, res.Key)
	if err != nil {
		t.Fatalf("Verify(Raw) error = %v", err)
	}
	if !bytes.Equal(e.Payload, res.Payload) {
		t.Errorf("payload of Raw = %x, want %x", e.Payload, res.Payload)
	}
}
