package codec

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// testPayload declares key 10 before key 2, so a sorted encoding differs from the
// declaration order.
type testPayload struct {
	Name  string `cbor:"10,keyasint"`
	Count uint64 `cbor:"2,keyasint"`
}

func TestMarshalSortsIntegerKeysBytewise(t *testing.T) {
	got, err := Marshal(testPayload{Name: "a", Count: 1})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := []byte{0xa2, 0x02, 0x01, 0x0a, 0x61, 'a'}
	if !bytes.Equal(got, want) {
		t.Errorf("Marshal = % x, want % x", got, want)
	}
}

func TestUnmarshalReturnsTheValueThatMarshalWrote(t *testing.T) {
	want := testPayload{Name: "firmware", Count: 300}
	b, err := Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got testPayload
	if err := Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got != want {
		t.Errorf("Unmarshal = %+v, want %+v", got, want)
	}
}

func TestUnmarshalRejectsInputThatBreaksTheDecodeOptions(t *testing.T) {
	// Each want is a part of the CBOR library error, so the test fails when the
	// input breaks a different rule from the one the case names.
	tests := []struct {
		name string
		in   []byte
		want string
	}{
		{"undeclared key", []byte{0xa3, 0x02, 0x01, 0x07, 0x00, 0x0a, 0x61, 'a'}, "unknown field"},
		{"duplicate key", []byte{0xa3, 0x02, 0x01, 0x02, 0x01, 0x0a, 0x61, 'a'}, "duplicate map key"},
		{"indefinite-length map", []byte{0xbf, 0x02, 0x01, 0x0a, 0x61, 'a', 0xff}, "indefinite-length"},
		{"tag", []byte{0xa2, 0x02, 0x01, 0x0a, 0xd8, 0x20, 0x61, 'a'}, "tag isn't allowed"},
		{"invalid UTF-8", []byte{0xa2, 0x02, 0x01, 0x0a, 0x61, 0xff}, "invalid UTF-8"},
		{"one extra byte", []byte{0xa2, 0x02, 0x01, 0x0a, 0x61, 'a', 0x00}, "extraneous data"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v testPayload
			err := Unmarshal(tt.in, &v)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Unmarshal(% x) error = %v, want an error that contains %q", tt.in, err, tt.want)
			}
			if !strings.HasPrefix(err.Error(), "codec: unmarshal payload: ") || errors.Unwrap(err) == nil {
				t.Errorf("Unmarshal error %q is not a wrapped CBOR error", err)
			}
		})
	}
}

func TestUnmarshalReturnsErrNotDeterministicForNonCanonicalInput(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
	}{
		{"non-shortest integer", []byte{0xa2, 0x02, 0x18, 0x01, 0x0a, 0x61, 'a'}},
		{"key order 10 before 2", []byte{0xa2, 0x0a, 0x61, 'a', 0x02, 0x01}},
		{"missing key", []byte{0xa1, 0x0a, 0x61, 'a'}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v testPayload
			if err := Unmarshal(tt.in, &v); !errors.Is(err, ErrNotDeterministic) {
				t.Errorf("Unmarshal error = %v, want ErrNotDeterministic", err)
			}
		})
	}
}

func TestUnmarshalRejectsATargetThatIsNotAPointer(t *testing.T) {
	if err := Unmarshal([]byte{0xa2, 0x02, 0x01, 0x0a, 0x61, 'a'}, testPayload{}); err == nil {
		t.Error("Unmarshal into a struct value returned no error")
	}
}
