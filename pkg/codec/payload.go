package codec

import (
	"bytes"
	"fmt"
)

// Marshal returns the core deterministic CBOR encoding of the protocol payload v
// (RFC 8949 § 4.2.1). It uses the encode options of Encode.
//
// A nil slice or a nil map in v encodes as CBOR null. Every failure is a wrapped
// CBOR error. Marshal is safe for concurrent use.
func Marshal(v any) ([]byte, error) {
	b, err := encMode.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("codec: marshal payload: %w", err)
	}
	return b, nil
}

// Unmarshal decodes the protocol payload b into the value that v points to. It uses
// the decode options of Decode, so a map key that the target struct does not
// declare is an error.
//
// Unmarshal returns ErrNotDeterministic when b is not the encoding that Marshal
// gives for the decoded value. A key that is absent from b but declared without
// omitempty gives ErrNotDeterministic, because Marshal writes that key. Every other
// failure is a wrapped CBOR error. When Unmarshal returns an error, the content of
// v is not defined. Unmarshal is safe for concurrent use.
func Unmarshal(b []byte, v any) error {
	// Unmarshal returns an ExtraneousDataError for bytes after the first CBOR item.
	if err := decMode.Unmarshal(b, v); err != nil {
		return fmt.Errorf("codec: unmarshal payload: %w", err)
	}
	// The library has no decode option that rejects non-deterministic input, so
	// Unmarshal encodes again and compares.
	again, err := Marshal(v)
	if err != nil {
		return err
	}
	if !bytes.Equal(again, b) {
		return ErrNotDeterministic
	}
	return nil
}
