package codec

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

func validEnvelope() Envelope {
	return Envelope{
		Format:      1,
		ProtocolID:  "org.qmux.firmware",
		Version:     1,
		MessageType: 1,
		Status:      0,
		Payload:     []byte("hello"),
	}
}

// mapKeys returns the encoded bytes of each key of the top-level CBOR map in b,
// in wire order. It fails the test when b is not one map of small size.
func mapKeys(t *testing.T, b []byte) [][]byte {
	t.Helper()
	if len(b) == 0 || b[0]>>5 != 5 || b[0]&0x1f >= 24 {
		t.Fatalf("encoded envelope does not start with a short map header: % x", b[:min(len(b), 4)])
	}
	pairs := int(b[0] & 0x1f)
	rest := b[1:]
	keys := make([][]byte, 0, pairs)
	for range pairs {
		_, afterKey, err := cbor.DiagnoseFirst(rest)
		if err != nil {
			t.Fatalf("read map key: %v", err)
		}
		keys = append(keys, rest[:len(rest)-len(afterKey)])
		_, afterValue, err := cbor.DiagnoseFirst(afterKey)
		if err != nil {
			t.Fatalf("read map value: %v", err)
		}
		rest = afterValue
	}
	if len(rest) != 0 {
		t.Fatalf("encoded envelope has %d bytes after the map", len(rest))
	}
	return keys
}

func TestEncodeWritesKeysZeroToFiveAsUnsignedIntegers(t *testing.T) {
	b, err := Encode(validEnvelope(), Limits{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	keys := mapKeys(t, b)
	if len(keys) != 6 {
		t.Fatalf("got %d keys, want 6", len(keys))
	}
	for i, k := range keys {
		// A CBOR unsigned integer below 24 is one byte of major type 0.
		if len(k) != 1 || k[0]>>5 != 0 || int(k[0]) != i {
			t.Errorf("key %d encodes as % x, want unsigned integer %d", i, k, i)
		}
	}
}

func TestEncodeOmitsKeySixWhenSignatureBlockIsNil(t *testing.T) {
	b, err := Encode(validEnvelope(), Limits{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var m map[uint64]cbor.RawMessage
	if err := cbor.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode as generic map: %v", err)
	}
	if _, ok := m[6]; ok {
		t.Errorf("encoded envelope has key 6 without a signature block")
	}
}

func TestEncodeWritesKeySixWhenSignatureBlockIsSet(t *testing.T) {
	e := validEnvelope()
	e.Signature = &SignatureBlock{
		KeyID:     make([]byte, 8),
		Alg:       -8,
		IssuedAt:  1,
		ExpiresAt: 2,
		Signature: make([]byte, 64),
	}
	b, err := Encode(e, Limits{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var top map[uint64]cbor.RawMessage
	if err := cbor.Unmarshal(b, &top); err != nil {
		t.Fatalf("decode as generic map: %v", err)
	}
	var sig map[uint64]cbor.RawMessage
	if err := cbor.Unmarshal(top[6], &sig); err != nil {
		t.Fatalf("decode key 6 as a map: %v", err)
	}
	for k := uint64(1); k <= 5; k++ {
		if _, ok := sig[k]; !ok {
			t.Errorf("signature block has no key %d", k)
		}
	}
}

func TestEncodeWritesMapKeysInBytewiseOrder(t *testing.T) {
	e := validEnvelope()
	e.Signature = &SignatureBlock{KeyID: make([]byte, 8), Alg: -8, IssuedAt: 1, ExpiresAt: 2, Signature: make([]byte, 64)}
	b, err := Encode(e, Limits{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	keys := mapKeys(t, b)
	for i := 1; i < len(keys); i++ {
		if bytes.Compare(keys[i-1], keys[i]) >= 0 {
			t.Errorf("key % x comes before key % x", keys[i-1], keys[i])
		}
	}
}

func TestEncodeWritesANilPayloadAsAnEmptyByteString(t *testing.T) {
	e := validEnvelope()
	e.Payload = nil
	b, err := Encode(e, Limits{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var m map[uint64]cbor.RawMessage
	if err := cbor.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode as generic map: %v", err)
	}
	if got := []byte(m[5]); !bytes.Equal(got, []byte{0x40}) {
		t.Errorf("key 5 encodes as % x, want 40 (empty byte string)", got)
	}
}

// payloadForSize returns a payload that makes validEnvelope encode to exactly size bytes.
func payloadForSize(t *testing.T, size int) []byte {
	t.Helper()
	e := validEnvelope()
	e.Payload = make([]byte, 16000)
	b, err := Encode(e, Limits{MaxEnvelope: 65000})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	// The payload length header stays 3 bytes for every length from 256 to 65535.
	return make([]byte, 16000+size-len(b))
}

func TestEncodeReturnsErrTooLargeForA16385ByteEnvelope(t *testing.T) {
	e := validEnvelope()
	e.Payload = payloadForSize(t, 16385)
	if b, err := Encode(e, Limits{MaxEnvelope: 65000}); err != nil || len(b) != 16385 {
		t.Fatalf("test envelope encodes to %d bytes (err %v), want 16385", len(b), err)
	}
	if _, err := Encode(e, Limits{}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("Encode error = %v, want ErrTooLarge", err)
	}
}

func TestEncodeAcceptsA16384ByteEnvelope(t *testing.T) {
	e := validEnvelope()
	e.Payload = payloadForSize(t, 16384)
	b, err := Encode(e, Limits{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if len(b) != 16384 {
		t.Errorf("envelope is %d bytes, want 16384", len(b))
	}
}

func TestEncodeUsesTheConfiguredMaxEnvelope(t *testing.T) {
	e := validEnvelope()
	e.Payload = make([]byte, 100)
	if _, err := Encode(e, Limits{MaxEnvelope: 64}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("Encode error = %v, want ErrTooLarge", err)
	}
}

func TestEncodeRejectsInvalidFields(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*Envelope)
		want   error
	}{
		{"format 2", func(e *Envelope) { e.Format = 2 }, ErrFormat},
		{"format 0", func(e *Envelope) { e.Format = 0 }, ErrFormat},
		{"empty protocol ID", func(e *Envelope) { e.ProtocolID = "" }, ErrInvalidProtocolID},
		{"uppercase protocol ID", func(e *Envelope) { e.ProtocolID = "Org.Qmux" }, ErrInvalidProtocolID},
		{"129-byte protocol ID", func(e *Envelope) { e.ProtocolID = strings.Repeat("a", 129) }, ErrInvalidProtocolID},
		{"protocol ID with underscore", func(e *Envelope) { e.ProtocolID = "org.q_mux" }, ErrInvalidProtocolID},
		{"version 0", func(e *Envelope) { e.Version = 0 }, ErrInvalidVersion},
		{"version 65536", func(e *Envelope) { e.Version = 65536 }, ErrInvalidVersion},
		{"message type 0", func(e *Envelope) { e.MessageType = 0 }, ErrInvalidMessageType},
		{"message type 3", func(e *Envelope) { e.MessageType = 3 }, ErrInvalidMessageType},
		{"message type 255", func(e *Envelope) { e.MessageType = 255 }, ErrInvalidMessageType},
		{"message type 65536", func(e *Envelope) { e.MessageType = 65536 }, ErrInvalidMessageType},
		{"status 65536", func(e *Envelope) { e.Status = 65536 }, ErrInvalidStatus},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := validEnvelope()
			tt.modify(&e)
			b, err := Encode(e, Limits{})
			if !errors.Is(err, tt.want) {
				t.Errorf("Encode error = %v, want %v", err, tt.want)
			}
			if b != nil {
				t.Errorf("Encode returned %d bytes with an error", len(b))
			}
		})
	}
}

func TestEncodeAcceptsBoundaryFieldValues(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*Envelope)
	}{
		{"128-byte protocol ID", func(e *Envelope) { e.ProtocolID = strings.Repeat("a", 128) }},
		{"protocol ID with digits, dots and hyphens", func(e *Envelope) { e.ProtocolID = "org.q-mux.v2" }},
		{"version 65535", func(e *Envelope) { e.Version = 65535 }},
		{"message type 2", func(e *Envelope) { e.MessageType = 2 }},
		{"message type 256", func(e *Envelope) { e.MessageType = 256 }},
		{"message type 65535", func(e *Envelope) { e.MessageType = 65535 }},
		{"status 256", func(e *Envelope) { e.Status = 256 }},
		{"status 65535", func(e *Envelope) { e.Status = 65535 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := validEnvelope()
			tt.modify(&e)
			if _, err := Encode(e, Limits{}); err != nil {
				t.Errorf("Encode: %v", err)
			}
		})
	}
}

func TestEncodeIsSafeForConcurrentUse(t *testing.T) {
	want, err := Encode(validEnvelope(), Limits{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			got, err := Encode(validEnvelope(), Limits{})
			if err != nil || !bytes.Equal(got, want) {
				t.Errorf("concurrent Encode = % x, %v; want % x", got, err, want)
			}
		})
	}
	wg.Wait()
}
