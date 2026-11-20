package security

import (
	"bytes"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/codec"
)

// testSeed is the fixed seed of the test signing key.
var testSeed = bytes.Repeat([]byte{0x42}, ed25519.SeedSize)

// testNow is the fixed signing time of the signer tests.
var testNow = time.Unix(1700000000, 0)

// unsignedEnvelope returns a valid envelope with no signature block.
func unsignedEnvelope() codec.Envelope {
	e := vectorEnvelope()
	e.Signature = nil
	return e
}

func fixedSigner() *Signer {
	return &Signer{Key: ed25519.NewKeyFromSeed(testSeed), Now: func() time.Time { return testNow }}
}

func TestSignSetsTheKeyIDAlgorithmAndAOneHourWindowFromNow(t *testing.T) { // FR-security-11, FR-security-12
	s := fixedSigner()
	e := unsignedEnvelope()
	if err := s.Sign(&e, "device42.fleet.example.", vectorKey); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	sb := e.Signature
	if sb == nil {
		t.Fatal("Sign set no signature block")
	}
	id := KeyID(s.Key.Public().(ed25519.PublicKey))
	if !bytes.Equal(sb.KeyID, id[:]) {
		t.Errorf("key ID = %x, want %x", sb.KeyID, id)
	}
	if sb.Alg != -8 {
		t.Errorf("algorithm = %d, want -8", sb.Alg)
	}
	if sb.IssuedAt != 1700000000 {
		t.Errorf("issued_at = %d, want 1700000000", sb.IssuedAt)
	}
	if sb.ExpiresAt != 1700000000+3600 {
		t.Errorf("expires_at = %d, want %d", sb.ExpiresAt, 1700000000+3600)
	}
	if len(sb.Signature) != ed25519.SignatureSize {
		t.Errorf("signature length = %d, want %d", len(sb.Signature), ed25519.SignatureSize)
	}
}

func TestSignUsesTheConfiguredValidity(t *testing.T) { // FR-security-11
	s := fixedSigner()
	s.Validity = 48 * time.Hour
	e := unsignedEnvelope()
	if err := s.Sign(&e, "device42.fleet.example.", vectorKey); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if want := uint64(1700000000 + 48*3600); e.Signature.ExpiresAt != want {
		t.Fatalf("expires_at = %d, want %d", e.Signature.ExpiresAt, want)
	}
}

func TestSignUsesTheCurrentTimeWhenNowIsNil(t *testing.T) { // FR-security-11
	s := &Signer{Key: ed25519.NewKeyFromSeed(testSeed)}
	e := unsignedEnvelope()
	before := uint64(time.Now().Unix())
	if err := s.Sign(&e, "device42.fleet.example.", vectorKey); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	after := uint64(time.Now().Unix())
	if e.Signature.IssuedAt < before || e.Signature.IssuedAt > after {
		t.Fatalf("issued_at = %d, want %d–%d", e.Signature.IssuedAt, before, after)
	}
}

func TestSignComputesASignatureThatVerifiesOverTheLowercaseSigningInput(t *testing.T) { // FR-security-13
	s := fixedSigner()
	e := unsignedEnvelope()
	if err := s.Sign(&e, "_v1.Device42.Fleet.Example.", vectorKey); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	msg, err := signingInput(e, "_v1.device42.fleet.example.", vectorKey)
	if err != nil {
		t.Fatalf("signingInput: %v", err)
	}
	if !ed25519.Verify(s.Key.Public().(ed25519.PublicKey), msg, e.Signature.Signature) {
		t.Fatal("ed25519.Verify rejected the signature")
	}
}

func TestSignReturnsAnErrorAndLeavesTheEnvelopeUnchanged(t *testing.T) { // FR-security-17
	cases := []struct {
		name   string
		signer *Signer
	}{
		{"nil key", &Signer{Now: fixedSigner().Now}},
		{"short key", &Signer{Key: ed25519.PrivateKey{1, 2, 3}, Now: fixedSigner().Now}},
		{"negative validity", &Signer{Key: fixedSigner().Key, Validity: -time.Hour, Now: fixedSigner().Now}},
		{"validity under one second", &Signer{Key: fixedSigner().Key, Validity: time.Millisecond, Now: fixedSigner().Now}},
		{"time before 1970", &Signer{Key: fixedSigner().Key, Now: func() time.Time { return time.Unix(-10, 0) }}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := unsignedEnvelope()
			if err := tc.signer.Sign(&e, "device42.fleet.example.", vectorKey); err == nil {
				t.Fatal("Sign returned no error")
			}
			if e.Signature != nil {
				t.Fatalf("Sign set the signature block %+v after an error", e.Signature)
			}
		})
	}
}

func TestSignedEnvelopeEncodesAndDecodesWithTheSignatureBlock(t *testing.T) {
	s := fixedSigner()
	e := unsignedEnvelope()
	if err := s.Sign(&e, "device42.fleet.example.", vectorKey); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	b, err := codec.Encode(e, codec.Limits{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := codec.Decode(b, codec.Limits{})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Signature == nil || !bytes.Equal(got.Signature.Signature, e.Signature.Signature) {
		t.Fatalf("decoded signature block = %+v, want %+v", got.Signature, e.Signature)
	}
}
