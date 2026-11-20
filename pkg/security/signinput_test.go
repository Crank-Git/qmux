package security

import (
	"bytes"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/protocol"
)

// vectorKey is the protocol key of the signing input vector.
var vectorKey = protocol.ProtocolKey{Class: 65282, Type: 65280}

// vectorEnvelope returns the envelope of the signing input vector. The signature
// field is empty, because the signing input does not include it.
func vectorEnvelope() codec.Envelope {
	return codec.Envelope{
		Format:      1,
		ProtocolID:  "org.qmux.firmware",
		Version:     1,
		MessageType: 1,
		Status:      0,
		Payload:     []byte("hello"),
		Signature: &codec.SignatureBlock{
			KeyID:     []byte{1, 2, 3, 4, 5, 6, 7, 8},
			Alg:       algEdDSA,
			IssuedAt:  1700000000,
			ExpiresAt: 1700003600,
		},
	}
}

// readHexVector returns the bytes of the hex file at path. It ignores the text after
// a # and all white space.
func readHexVector(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vector: %v", err)
	}
	var digits strings.Builder
	for line := range strings.Lines(string(b)) {
		line, _, _ = strings.Cut(line, "#")
		digits.WriteString(strings.Join(strings.Fields(line), ""))
	}
	v, err := hex.DecodeString(digits.String())
	if err != nil {
		t.Fatalf("decode vector hex: %v", err)
	}
	return v
}

func TestSigningInputEqualsTheCommittedVector(t *testing.T) { // FR-security-13, FR-security-14
	want := readHexVector(t, "testdata/signing-input-v1.hex")
	// The mixed case shows that the signing input uses the lowercase QNAME.
	got, err := signingInput(vectorEnvelope(), "_v1.Device42.FLEET.example.", vectorKey)
	if err != nil {
		t.Fatalf("signingInput: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("signing input = %x, want %x", got, want)
	}
}

func TestSigningInputEncodesANilPayloadAsAnEmptyByteString(t *testing.T) {
	withEmpty := vectorEnvelope()
	withEmpty.Payload = []byte{}
	withNil := vectorEnvelope()
	withNil.Payload = nil
	want, err := signingInput(withEmpty, "a.example.", vectorKey)
	if err != nil {
		t.Fatalf("signingInput(empty payload): %v", err)
	}
	got, err := signingInput(withNil, "a.example.", vectorKey)
	if err != nil {
		t.Fatalf("signingInput(nil payload): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("signing input with nil payload = %x, want %x", got, want)
	}
}

func TestSigningInputDiffersForEachBoundField(t *testing.T) { // spec § Data model: the signature binds the name and the key
	base, err := signingInput(vectorEnvelope(), "a.example.", vectorKey)
	if err != nil {
		t.Fatalf("signingInput: %v", err)
	}
	other := []struct {
		name  string
		qname string
		key   protocol.ProtocolKey
	}{
		{"another QNAME", "b.example.", vectorKey},
		{"another class", "a.example.", protocol.ProtocolKey{Class: 65283, Type: 65280}},
		{"another type", "a.example.", protocol.ProtocolKey{Class: 65282, Type: 65281}},
	}
	for _, tc := range other {
		t.Run(tc.name, func(t *testing.T) {
			got, err := signingInput(vectorEnvelope(), tc.qname, tc.key)
			if err != nil {
				t.Fatalf("signingInput: %v", err)
			}
			if bytes.Equal(got, base) {
				t.Fatal("the signing input did not change")
			}
		})
	}
}
