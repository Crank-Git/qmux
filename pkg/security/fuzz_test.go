package security

import (
	"testing"
)

// FuzzVerify requires that Verify never panics on fuzzed envelope bytes. It also
// requires that Verify accepts no input other than a seed that the test key signed.
// Every byte of a signed envelope is a field of the signing input, a part of the
// signature, or a part of the deterministic encoding, so each modified envelope
// must fail a check. (FR-security-52)
func FuzzVerify(f *testing.F) {
	signed := map[string]bool{}
	seed := func(raw []byte) {
		f.Add(raw)
		signed[string(raw)] = true
	}

	seed(newSignedEnvelope(f).raw(f))
	notFound := newSignedEnvelope(f)
	notFound.env.Status = 1
	notFound.env.Payload = nil
	seed(notFound.raw(f))
	manifest := newSignedEnvelope(f)
	manifest.env.MessageType = 2
	manifest.env.Payload = []byte{0xa1, 0x01, 0x01}
	seed(manifest.raw(f))

	// The unsigned seed lets the fuzzer reach the checks before the signature.
	unsigned := newSignedEnvelope(f)
	unsigned.env.Signature = nil
	f.Add(unsigned.raw(f))

	f.Fuzz(func(t *testing.T, raw []byte) {
		// A new verifier per input has an empty freshness store, so a seed always
		// passes and the result depends only on the bytes.
		_, err := testVerifier(t).Verify(raw, testQName, testProtocolKey)
		if err == nil && !signed[string(raw)] {
			t.Fatalf("Verify accepted a modified envelope % x", raw)
		}
		if err != nil && signed[string(raw)] {
			t.Fatalf("Verify rejected a signed seed: %v", err)
		}
	})
}
