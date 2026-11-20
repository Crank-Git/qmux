package codec

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
)

// Each pair is the core deterministic encoding of one key and its value in
// validEnvelope.
var (
	pairFormat      = []byte{0x00, 0x01}
	pairProtocolID  = append([]byte{0x01, 0x71}, "org.qmux.firmware"...)
	pairVersion     = []byte{0x02, 0x01}
	pairMessageType = []byte{0x03, 0x01}
	pairStatus      = []byte{0x04, 0x00}
	pairPayload     = append([]byte{0x05, 0x45}, "hello"...)
)

// rawMap returns a definite-length CBOR map with header byte 0xa0+n, where n is the
// number of pairs. Each pair is already encoded, in the order given.
func rawMap(pairs ...[]byte) []byte {
	b := []byte{0xa0 + byte(len(pairs))}
	for _, p := range pairs {
		b = append(b, p...)
	}
	return b
}

func validPairs() [][]byte {
	return [][]byte{pairFormat, pairProtocolID, pairVersion, pairMessageType, pairStatus, pairPayload}
}

func TestDecodeReturnsTheEnvelopeThatEncodeWrote(t *testing.T) {
	want := validEnvelope()
	want.Signature = &SignatureBlock{KeyID: make([]byte, 8), Alg: -8, IssuedAt: 1, ExpiresAt: 2, Signature: make([]byte, 64)}
	b, err := Encode(want, Limits{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := Decode(b, Limits{})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	again, err := Encode(got, Limits{})
	if err != nil || !bytes.Equal(again, b) {
		t.Errorf("decoded envelope encodes to % x (err %v), want % x", again, err, b)
	}
	if got.ProtocolID != want.ProtocolID || !bytes.Equal(got.Payload, want.Payload) || got.Signature == nil {
		t.Errorf("Decode = %+v, want %+v", got, want)
	}
}

func TestDecodeAcceptsTheHandBuiltValidVector(t *testing.T) {
	b := rawMap(validPairs()...)
	want, err := Encode(validEnvelope(), Limits{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !bytes.Equal(b, want) {
		t.Fatalf("hand-built vector % x differs from Encode output % x", b, want)
	}
	if _, err := Decode(b, Limits{}); err != nil {
		t.Errorf("Decode: %v", err)
	}
}

func TestDecodeReturnsErrTooLargeFor16385InputBytes(t *testing.T) {
	if _, err := Decode(make([]byte, 16385), Limits{}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("Decode error = %v, want ErrTooLarge", err)
	}
}

func TestDecodeChecksTheSizeBeforeItParsesAnyByte(t *testing.T) {
	// 0xff is not a valid start of a CBOR item, so a parse would fail with a
	// different error.
	b := bytes.Repeat([]byte{0xff}, 100)
	if _, err := Decode(b, Limits{MaxEnvelope: 64}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("Decode error = %v, want ErrTooLarge", err)
	}
}

func TestDecodeWithZeroLimitsAccepts16384BytesAndRejects16385Bytes(t *testing.T) {
	e := validEnvelope()
	e.Payload = payloadForSize(t, 16384)
	b, err := Encode(e, Limits{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if _, err := Decode(b, Limits{}); err != nil {
		t.Errorf("Decode of 16384 bytes: %v", err)
	}
	e.Payload = payloadForSize(t, 16385)
	b, err = Encode(e, Limits{MaxEnvelope: 65000})
	if err != nil || len(b) != 16385 {
		t.Fatalf("test envelope encodes to %d bytes (err %v), want 16385", len(b), err)
	}
	if _, err := Decode(b, Limits{}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("Decode error of 16385 bytes = %v, want ErrTooLarge", err)
	}
}

func TestDecodeRejectsMalformedInputWithAWrappedCBORError(t *testing.T) {
	sixtyFivePairs := []byte{0xb8, 65}
	for i := range 65 {
		sixtyFivePairs = append(sixtyFivePairs, 0x18, byte(i+24), 0x00)
	}
	// Each want is a part of the CBOR library error, so the test fails when the
	// input breaks a different rule from the one the case names.
	tests := []struct {
		name string
		in   []byte
		want string
	}{
		{"empty input", nil, "EOF"},
		{"duplicate key", rawMap(append(validPairs(), pairStatus)...), "duplicate map key"},
		{"indefinite-length map", append(append([]byte{0xbf}, bytes.Join(validPairs(), nil)...), 0xff), "indefinite-length"},
		{"tag", rawMap(pairFormat, append([]byte{0x01, 0xd8, 0x20, 0x71}, "org.qmux.firmware"...), pairVersion, pairMessageType, pairStatus, pairPayload), "tag isn't allowed"},
		{"invalid UTF-8", rawMap(pairFormat, []byte{0x01, 0x61, 0xff}, pairVersion, pairMessageType, pairStatus, pairPayload), "invalid UTF-8"},
		{"unknown key", rawMap(append(validPairs(), []byte{0x07, 0x00})...), "unknown field"},
		{"map with 65 pairs", sixtyFivePairs, "max number of key-value pairs"},
		{"float in a uint field", rawMap(pairFormat, pairProtocolID, []byte{0x02, 0xf9, 0x3c, 0x00}, pairMessageType, pairStatus, pairPayload), "cannot unmarshal"},
		{"one extra byte after a valid envelope", append(rawMap(validPairs()...), 0x00), "extraneous data"},
		{"top-level array", []byte{0x80}, "cannot unmarshal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Decode(tt.in, Limits{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Decode(% x) error = %v, want an error that contains %q", tt.in, err, tt.want)
			}
			if !strings.HasPrefix(err.Error(), "codec: decode envelope: ") || errors.Unwrap(err) == nil {
				t.Errorf("Decode error %q is not a wrapped CBOR error", err)
			}
		})
	}
}

func TestDecodeReturnsErrNotDeterministicForNonCanonicalInput(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
	}{
		{"key order 1 before 0", rawMap(pairProtocolID, pairFormat, pairVersion, pairMessageType, pairStatus, pairPayload)},
		{"non-shortest integer", rawMap([]byte{0x00, 0x18, 0x01}, pairProtocolID, pairVersion, pairMessageType, pairStatus, pairPayload)},
		{"null payload", rawMap(pairFormat, pairProtocolID, pairVersion, pairMessageType, pairStatus, []byte{0x05, 0xf6})},
		{"missing status key", rawMap(pairFormat, pairProtocolID, pairVersion, pairMessageType, pairPayload)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Decode(tt.in, Limits{}); !errors.Is(err, ErrNotDeterministic) {
				t.Errorf("Decode error = %v, want ErrNotDeterministic", err)
			}
		})
	}
}

func TestDecodeRejectsInvalidFields(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*Envelope)
		want   error
	}{
		{"format 2", func(e *Envelope) { e.Format = 2 }, ErrFormat},
		{"empty protocol ID", func(e *Envelope) { e.ProtocolID = "" }, ErrInvalidProtocolID},
		{"uppercase protocol ID", func(e *Envelope) { e.ProtocolID = "Org.Qmux" }, ErrInvalidProtocolID},
		{"129-byte protocol ID", func(e *Envelope) { e.ProtocolID = strings.Repeat("a", 129) }, ErrInvalidProtocolID},
		{"version 0", func(e *Envelope) { e.Version = 0 }, ErrInvalidVersion},
		{"version 65536", func(e *Envelope) { e.Version = 65536 }, ErrInvalidVersion},
		{"message type 0", func(e *Envelope) { e.MessageType = 0 }, ErrInvalidMessageType},
		{"message type 3", func(e *Envelope) { e.MessageType = 3 }, ErrInvalidMessageType},
		{"message type 65536", func(e *Envelope) { e.MessageType = 65536 }, ErrInvalidMessageType},
		{"status 65536", func(e *Envelope) { e.Status = 65536 }, ErrInvalidStatus},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := validEnvelope()
			tt.modify(&e)
			// Encode rejects these fields, so the test marshals the envelope
			// with the encode mode directly.
			b, err := encMode.Marshal(e)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			got, err := Decode(b, Limits{})
			if !errors.Is(err, tt.want) {
				t.Errorf("Decode error = %v, want %v", err, tt.want)
			}
			if got.ProtocolID != "" || got.Payload != nil {
				t.Errorf("Decode returned %+v with an error, want the zero Envelope", got)
			}
		})
	}
}

func TestDecodeAcceptsMessageTypeAndStatus65535(t *testing.T) {
	e := validEnvelope()
	e.MessageType = 65535
	e.Status = 65535
	b, err := Encode(e, Limits{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := Decode(b, Limits{})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.MessageType != 65535 || got.Status != 65535 {
		t.Errorf("Decode = message type %d, status %d; want 65535, 65535", got.MessageType, got.Status)
	}
}

func TestDecodeErrorMessagesNeverContainPayloadBytes(t *testing.T) {
	payload := bytes.Repeat([]byte("QMUXPAYLOADBYTES"), 7)[:100]
	e := validEnvelope()
	e.Payload = payload
	valid, err := Encode(e, Limits{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	notShortest := append([]byte{}, valid...)
	notShortest[2] = 0x18 // format 1 as a one-byte unsigned integer.
	notShortest = append(notShortest[:3], append([]byte{0x01}, notShortest[3:]...)...)
	badFormat := append([]byte{}, valid...)
	badFormat[2] = 0x02
	tests := []struct {
		name string
		in   []byte
	}{
		{"extra byte", append(append([]byte{}, valid...), 0x00)},
		{"truncated", valid[:len(valid)-1]},
		{"non-shortest integer", notShortest},
		{"format 2", badFormat},
		{"too large", valid},
	}
	limits := map[string]Limits{"too large": {MaxEnvelope: 64}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Decode(tt.in, limits[tt.name])
			if err == nil {
				t.Fatal("Decode returned no error")
			}
			msg := err.Error()
			for _, form := range []string{string(payload), hex.EncodeToString(payload), strings.ToUpper(hex.EncodeToString(payload)), "QMUXPAYLOAD"} {
				if strings.Contains(msg, form) {
					t.Errorf("error message %q contains payload bytes", msg)
				}
			}
		})
	}
}

func TestDecodeIsSafeForConcurrentUse(t *testing.T) {
	b := rawMap(validPairs()...)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := Decode(b, Limits{}); err != nil {
				t.Errorf("concurrent Decode: %v", err)
			}
		})
	}
	wg.Wait()
}

func TestLimitsValidate(t *testing.T) {
	tests := []struct {
		name    string
		l       Limits
		wantErr bool
	}{
		{"zero value means the default", Limits{}, false},
		{"64 bytes", Limits{MaxEnvelope: 64}, false},
		{"65000 bytes", Limits{MaxEnvelope: 65000}, false},
		{"63 bytes", Limits{MaxEnvelope: 63}, true},
		{"65001 bytes", Limits{MaxEnvelope: 65001}, true},
		{"negative", Limits{MaxEnvelope: -1}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.l.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() = %v, want error %v", err, tt.wantErr)
			}
		})
	}
}
