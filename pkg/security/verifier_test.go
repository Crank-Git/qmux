package security

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/protocol"
)

const (
	testProtocolID = "org.qmux.firmware"
	testQName      = "_v1.device42.fleet.example."
)

var (
	testProtocolKey = protocol.ProtocolKey{Class: 65280, Type: 65281}
	// verifyNow is the fixed clock of the verifier tests.
	verifyNow = time.Unix(1_800_000_000, 0)
)

// signedEnvelope is one envelope and the inputs that sign it. A test changes one
// field, then calls raw to sign and encode the envelope.
type signedEnvelope struct {
	env   codec.Envelope
	key   ed25519.PrivateKey
	qname string
	pk    protocol.ProtocolKey
}

func newSignedEnvelope(t *testing.T) *signedEnvelope {
	t.Helper()
	key := testKey(t)
	id := KeyID(key.Public().(ed25519.PublicKey))
	return &signedEnvelope{
		env: codec.Envelope{
			Format:      1,
			ProtocolID:  testProtocolID,
			Version:     1,
			MessageType: 1,
			Status:      0,
			Payload:     []byte{0xa1, 0x01, 0x02},
			Signature: &codec.SignatureBlock{
				KeyID:     id[:],
				Alg:       -8,
				IssuedAt:  uint64(verifyNow.Unix()),
				ExpiresAt: uint64(verifyNow.Add(time.Hour).Unix()),
			},
		},
		key:   key,
		qname: testQName,
		pk:    testProtocolKey,
	}
}

// raw signs the envelope over the signing input of s.qname and s.pk, and returns
// the encoded envelope.
func (s *signedEnvelope) raw(t *testing.T) []byte {
	t.Helper()
	if s.env.Signature != nil {
		in, err := signingInput(s.env, s.qname, s.pk)
		if err != nil {
			t.Fatal(err)
		}
		s.env.Signature.Signature = ed25519.Sign(s.key, in)
	}
	b, err := codec.Encode(s.env, codec.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func testVerifier(t *testing.T) *Verifier {
	t.Helper()
	pub := testKey(t).Public().(ed25519.PublicKey)
	return &Verifier{
		Anchors: []TrustAnchor{{KeyID: KeyID(pub), PublicKey: pub}},
		Now:     func() time.Time { return verifyNow },
	}
}

// nonDeterministic returns raw with the value of key 0 encoded in two bytes. The
// envelope decodes to the same fields, but it does not encode back to raw.
func nonDeterministic(t *testing.T, raw []byte) []byte {
	t.Helper()
	// raw[0] is the map head, raw[1] is key 0 and raw[2] is the format 1.
	if raw[1] != 0x00 || raw[2] != 0x01 {
		t.Fatalf("envelope starts % x, want key 0 with value 1", raw[:3])
	}
	out := append([]byte{}, raw[:2]...)
	out = append(out, 0x18, 0x01)
	return append(out, raw[3:]...)
}

func TestVerifyAcceptsAValidSignedEnvelope(t *testing.T) {
	s := newSignedEnvelope(t)
	got, err := testVerifier(t).Verify(s.raw(t), testQName, testProtocolKey)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProtocolID != testProtocolID || string(got.Payload) != string(s.env.Payload) {
		t.Fatalf("Verify returned %+v, want the signed envelope", got)
	}
}

func TestVerifyReturnsOneSentinelForEachFailedCheck(t *testing.T) {
	other := ed25519.NewKeyFromSeed(bytes32(0x42))
	otherID := KeyID(other.Public().(ed25519.PublicKey))
	cases := []struct {
		name string
		// prepare changes the envelope or the verifier and returns the raw bytes.
		prepare func(t *testing.T, s *signedEnvelope, v *Verifier) []byte
		want    error
	}{
		{"FR-security-19 no signature block", func(t *testing.T, s *signedEnvelope, _ *Verifier) []byte {
			s.env.Signature = nil
			return s.raw(t)
		}, ErrUnsigned},
		{"FR-security-20 algorithm is not -8", func(t *testing.T, s *signedEnvelope, _ *Verifier) []byte {
			s.env.Signature.Alg = -7
			return s.raw(t)
		}, ErrAlgorithm},
		{"FR-security-21 key ID is not an anchor", func(t *testing.T, s *signedEnvelope, _ *Verifier) []byte {
			s.key, s.env.Signature.KeyID = other, otherID[:]
			return s.raw(t)
		}, ErrUnknownKey},
		{"FR-security-22 anchor does not allow the protocol ID", func(t *testing.T, s *signedEnvelope, v *Verifier) []byte {
			v.Anchors[0].Protocols = []string{"org.qmux.configuration"}
			return s.raw(t)
		}, ErrProtocolNotAllowed},
		{"FR-security-23 tampered payload byte", func(t *testing.T, s *signedEnvelope, _ *Verifier) []byte {
			raw := s.raw(t)
			s.env.Payload[0] ^= 0x01
			b, err := codec.Encode(s.env, codec.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if len(b) != len(raw) {
				t.Fatal("the tampered envelope changed length")
			}
			return b
		}, ErrBadSignature},
		{"FR-security-23 envelope copied to another QNAME", func(t *testing.T, s *signedEnvelope, _ *Verifier) []byte {
			s.qname = "_v1.device43.fleet.example."
			return s.raw(t)
		}, ErrBadSignature},
		{"FR-security-23 envelope copied to another protocol key", func(t *testing.T, s *signedEnvelope, _ *Verifier) []byte {
			s.pk = protocol.ProtocolKey{Class: 65280, Type: 65282}
			return s.raw(t)
		}, ErrBadSignature},
		{"FR-security-24 issued_at later than now + skew", func(t *testing.T, s *signedEnvelope, _ *Verifier) []byte {
			s.env.Signature.IssuedAt = uint64(verifyNow.Add(10 * time.Minute).Unix())
			s.env.Signature.ExpiresAt = uint64(verifyNow.Add(time.Hour).Unix())
			return s.raw(t)
		}, ErrNotYetValid},
		{"FR-security-25 expires_at earlier than now - skew", func(t *testing.T, s *signedEnvelope, _ *Verifier) []byte {
			s.env.Signature.IssuedAt = uint64(verifyNow.Add(-2 * time.Hour).Unix())
			s.env.Signature.ExpiresAt = uint64(verifyNow.Add(-time.Hour).Unix())
			return s.raw(t)
		}, ErrExpired},
		{"FR-security-26 window longer than MaxWindow", func(t *testing.T, s *signedEnvelope, _ *Verifier) []byte {
			s.env.Signature.ExpiresAt = uint64(verifyNow.Add(48 * time.Hour).Unix())
			return s.raw(t)
		}, ErrWindowTooLong},
		{"FR-security-26 expires_at equal to issued_at", func(t *testing.T, s *signedEnvelope, _ *Verifier) []byte {
			s.env.Signature.ExpiresAt = s.env.Signature.IssuedAt
			return s.raw(t)
		}, ErrWindowTooLong},
		{"FR-security-27 issued_at older than the store", func(t *testing.T, s *signedEnvelope, v *Verifier) []byte {
			v.Store = &FreshnessStore{}
			v.Store.admit(freshnessKey{testProtocolID, "device42.fleet.example."}, s.env.Signature.IssuedAt+1)
			return s.raw(t)
		}, ErrReplay},
		{"FR-security-28 bytes do not encode back to the same bytes", func(t *testing.T, s *signedEnvelope, _ *Verifier) []byte {
			return nonDeterministic(t, s.raw(t))
		}, ErrNotDeterministic},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, v := newSignedEnvelope(t), testVerifier(t)
			raw := tc.prepare(t, s, v)
			if _, err := v.Verify(raw, testQName, testProtocolKey); !errors.Is(err, tc.want) {
				t.Fatalf("Verify = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestVerifyChecksDeterminismBeforeTheSignatureBlock(t *testing.T) { // FR-security-29
	s := newSignedEnvelope(t)
	s.env.Signature = nil
	_, err := testVerifier(t).Verify(nonDeterministic(t, s.raw(t)), testQName, testProtocolKey)
	if !errors.Is(err, ErrNotDeterministic) {
		t.Fatalf("Verify = %v, want ErrNotDeterministic", err)
	}
}

func TestVerifyReturnsErrBadSignatureForAnExpiredEnvelopeWithABadSignature(t *testing.T) { // FR-security-29
	s := newSignedEnvelope(t)
	s.env.Signature.IssuedAt = uint64(verifyNow.Add(-3 * time.Hour).Unix())
	s.env.Signature.ExpiresAt = uint64(verifyNow.Add(-2 * time.Hour).Unix())
	s.qname = "_v1.other.fleet.example."
	_, err := testVerifier(t).Verify(s.raw(t), testQName, testProtocolKey)
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("Verify = %v, want ErrBadSignature", err)
	}
}

func TestZeroVerifierUses60SecondSkewAnd24HourWindow(t *testing.T) { // FR-security-18
	cases := []struct {
		name     string
		issued   time.Duration
		lifetime time.Duration
		want     error
	}{
		{"issued 60 s in the future", 60 * time.Second, time.Hour, nil},
		{"issued 61 s in the future", 61 * time.Second, time.Hour, ErrNotYetValid},
		{"expired 60 s ago", -2 * time.Hour, 2*time.Hour - 60*time.Second, nil},
		{"expired 61 s ago", -2 * time.Hour, 2*time.Hour - 61*time.Second, ErrExpired},
		{"window of 24 h", 0, 24 * time.Hour, nil},
		{"window of 24 h and 1 s", 0, 24*time.Hour + time.Second, ErrWindowTooLong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSignedEnvelope(t)
			s.env.Signature.IssuedAt = uint64(verifyNow.Add(tc.issued).Unix())
			s.env.Signature.ExpiresAt = uint64(verifyNow.Add(tc.issued + tc.lifetime).Unix())
			v := testVerifier(t)
			if _, err := v.Verify(s.raw(t), testQName, testProtocolKey); !errors.Is(err, tc.want) {
				t.Fatalf("Verify = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestZeroVerifierUsesTheSystemClock(t *testing.T) { // FR-security-18
	s := newSignedEnvelope(t)
	now := time.Now()
	s.env.Signature.IssuedAt = uint64(now.Unix())
	s.env.Signature.ExpiresAt = uint64(now.Add(time.Hour).Unix())
	v := testVerifier(t)
	v.Now = nil
	if _, err := v.Verify(s.raw(t), testQName, testProtocolKey); err != nil {
		t.Fatal(err)
	}
}

func TestZeroVerifierKeepsAnInMemoryStoreAcrossCalls(t *testing.T) { // FR-security-18
	v := testVerifier(t)
	newer := newSignedEnvelope(t)
	newer.env.Signature.IssuedAt++
	if _, err := v.Verify(newer.raw(t), testQName, testProtocolKey); err != nil {
		t.Fatal(err)
	}
	older := newSignedEnvelope(t)
	if _, err := v.Verify(older.raw(t), testQName, testProtocolKey); !errors.Is(err, ErrReplay) {
		t.Fatalf("Verify of the older envelope = %v, want ErrReplay", err)
	}
}

func TestVerifyAcceptsAnIssuedAtEqualToTheStore(t *testing.T) { // FR-security-27
	v := testVerifier(t)
	for i := range 2 {
		s := newSignedEnvelope(t)
		if _, err := v.Verify(s.raw(t), testQName, testProtocolKey); err != nil {
			t.Fatalf("Verify %d: %v", i, err)
		}
	}
}

func TestVerifyKeepsFreshnessAcrossAVersionChangeAndLetterCase(t *testing.T) {
	v := testVerifier(t)
	newer := newSignedEnvelope(t)
	newer.env.Signature.IssuedAt++
	if _, err := v.Verify(newer.raw(t), testQName, testProtocolKey); err != nil {
		t.Fatal(err)
	}
	for _, qname := range []string{"_v2.device42.fleet.example.", "Device42.FLEET.example."} {
		s := newSignedEnvelope(t)
		s.qname = qname
		if _, err := v.Verify(s.raw(t), qname, testProtocolKey); !errors.Is(err, ErrReplay) {
			t.Fatalf("Verify for %s = %v, want ErrReplay", qname, err)
		}
	}
}

func TestVerifySignsTheLowercaseQName(t *testing.T) { // FR-security-13
	s := newSignedEnvelope(t)
	raw := s.raw(t)
	if _, err := testVerifier(t).Verify(raw, strings.ToUpper(testQName), testProtocolKey); err != nil {
		t.Fatal(err)
	}
}

func TestFailedVerifyLeavesTheStoreUnchanged(t *testing.T) { // FR-security-30
	failures := map[string]func(s *signedEnvelope){
		"bad signature": func(s *signedEnvelope) { s.qname = "_v1.other.fleet.example." },
		"window too long": func(s *signedEnvelope) {
			s.env.Signature.ExpiresAt = uint64(verifyNow.Add(48 * time.Hour).Unix())
		},
	}
	for name, change := range failures {
		t.Run(name, func(t *testing.T) {
			v := testVerifier(t)
			v.Store = &FreshnessStore{}
			if _, err := v.Verify(newSignedEnvelope(t).raw(t), testQName, testProtocolKey); err != nil {
				t.Fatal(err)
			}
			before := maps.Clone(v.Store.newest)
			s := newSignedEnvelope(t)
			s.env.Signature.IssuedAt += 10
			change(s)
			if _, err := v.Verify(s.raw(t), testQName, testProtocolKey); err == nil {
				t.Fatal("Verify returned nil")
			}
			if !maps.Equal(v.Store.newest, before) {
				t.Fatalf("store = %v, want %v", v.Store.newest, before)
			}
		})
	}
}

func TestVerifyIsSafeFor100ConcurrentCalls(t *testing.T) { // FR-security-31
	v := testVerifier(t)
	raws := make([][]byte, 100)
	qnames := make([]string, 100)
	for i := range raws {
		s := newSignedEnvelope(t)
		// Half of the calls share one QNAME, so they update one store entry.
		s.qname = fmt.Sprintf("_v1.device%d.fleet.example.", i%2*i)
		s.env.Signature.IssuedAt += uint64(i % 3)
		raws[i], qnames[i] = s.raw(t), s.qname
	}
	errs := make([]error, len(raws))
	var wg sync.WaitGroup
	for i := range raws {
		wg.Go(func() {
			_, errs[i] = v.Verify(raws[i], qnames[i], testProtocolKey)
		})
	}
	wg.Wait()
	for i, err := range errs {
		// Envelopes for one QNAME arrive in any order, so an older one can be a replay.
		if err != nil && !errors.Is(err, ErrReplay) {
			t.Errorf("Verify %d: %v", i, err)
		}
	}
	got := v.store().newest[freshnessKey{testProtocolID, "device0.fleet.example."}]
	if want := uint64(verifyNow.Unix()) + 2; got != want {
		t.Fatalf("store holds issued_at %d for device0, want the newest %d", got, want)
	}
}

func TestVerifyErrorNamesTheProtocolIDAndTheQName(t *testing.T) { // FR-security-33
	s := newSignedEnvelope(t)
	s.env.Signature.IssuedAt = uint64(verifyNow.Add(-2 * time.Hour).Unix())
	s.env.Signature.ExpiresAt = uint64(verifyNow.Add(-time.Hour).Unix())
	_, err := testVerifier(t).Verify(s.raw(t), testQName, testProtocolKey)
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("Verify = %v, want ErrExpired", err)
	}
	for _, want := range []string{testProtocolID, testQName} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestVerifyErrorNamesTheQNameForANonDeterministicEnvelope(t *testing.T) { // FR-security-33
	_, err := testVerifier(t).Verify(nonDeterministic(t, newSignedEnvelope(t).raw(t)), testQName, testProtocolKey)
	if !errors.Is(err, ErrNotDeterministic) || !strings.Contains(err.Error(), testQName) {
		t.Fatalf("Verify = %v, want ErrNotDeterministic that names %s", err, testQName)
	}
}

func TestVerifyDoesNotPanicForAnAnchorWithAShortPublicKey(t *testing.T) {
	v := testVerifier(t)
	v.Anchors[0].PublicKey = v.Anchors[0].PublicKey[:31]
	if _, err := v.Verify(newSignedEnvelope(t).raw(t), testQName, testProtocolKey); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("Verify = %v, want ErrBadSignature", err)
	}
}

func TestSavedFreshnessStoreLoadsToAnEqualStore(t *testing.T) { // FR-security-32
	var s FreshnessStore
	s.admit(freshnessKey{testProtocolID, "device42.fleet.example."}, 1_800_000_000)
	s.admit(freshnessKey{"org.qmux.configuration", "device42.fleet.example."}, 1_800_000_100)
	path := filepath.Join(t.TempDir(), "freshness.json")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFreshnessStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(got.newest, s.newest) {
		t.Fatalf("loaded store %v, want %v", got.newest, s.newest)
	}
}

func TestLoadedFreshnessStoreRejectsAReplay(t *testing.T) { // FR-security-32
	path := filepath.Join(t.TempDir(), "freshness.json")
	v := testVerifier(t)
	v.Store = &FreshnessStore{}
	newer := newSignedEnvelope(t)
	newer.env.Signature.IssuedAt++
	if _, err := v.Verify(newer.raw(t), testQName, testProtocolKey); err != nil {
		t.Fatal(err)
	}
	if err := v.Store.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadFreshnessStore(path)
	if err != nil {
		t.Fatal(err)
	}
	restarted := testVerifier(t)
	restarted.Store = loaded
	if _, err := restarted.Verify(newSignedEnvelope(t).raw(t), testQName, testProtocolKey); !errors.Is(err, ErrReplay) {
		t.Fatalf("Verify after a restart = %v, want ErrReplay", err)
	}
}

func TestLoadFreshnessStoreRejectsAMalformedFile(t *testing.T) {
	cases := map[string]string{
		"not JSON":      `{"entries":`,
		"unknown field": `{"entries":[{"protocol_id":"a","qname":"b.","issued_at":1,"extra":1}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadFreshnessStore(writeAnchorFile(t, body)); err == nil {
				t.Fatal("LoadFreshnessStore returned nil")
			}
		})
	}
}
