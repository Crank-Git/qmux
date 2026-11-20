package codec

import (
	"bytes"
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

// decMode is immutable and safe for concurrent use. The options are constant, so
// an error from DecMode is a programming error and not an input error.
var decMode = func() cbor.DecMode {
	m, err := cbor.DecOptions{
		DupMapKey:         cbor.DupMapKeyEnforcedAPF,
		IndefLength:       cbor.IndefLengthForbidden,
		MaxNestedLevels:   8,
		MaxArrayElements:  1024,
		MaxMapPairs:       64,
		ExtraReturnErrors: cbor.ExtraDecErrorUnknownField,
		TagsMd:            cbor.TagsForbidden,
		UTF8:              cbor.UTF8RejectInvalid,
		NaN:               cbor.NaNDecodeForbidden,
		Inf:               cbor.InfDecodeForbidden,
	}.DecMode()
	if err != nil {
		panic(fmt.Sprintf("codec: build strict decode mode: %v", err))
	}
	return m
}()

// Decode returns the envelope that b encodes. It accepts only the core
// deterministic encoding of a valid envelope (RFC 8949 § 4.2.1).
//
// Decode returns ErrTooLarge when b is longer than l.MaxEnvelope, before it parses
// any byte. It returns ErrNotDeterministic when b is not the encoding that Encode
// gives for the decoded envelope. It returns the errors of Encode for a field that
// breaks a rule. Every other failure is a wrapped CBOR error. No error message
// contains payload bytes. Decode is safe for concurrent use.
func Decode(b []byte, l Limits) (Envelope, error) {
	if len(b) > l.maxEnvelope() {
		return Envelope{}, fmt.Errorf("%w: %d bytes, limit %d", ErrTooLarge, len(b), l.maxEnvelope())
	}
	var e Envelope
	// Unmarshal returns an ExtraneousDataError for bytes after the first CBOR item.
	if err := decMode.Unmarshal(b, &e); err != nil {
		return Envelope{}, fmt.Errorf("codec: decode envelope: %w", err)
	}
	if err := validate(e); err != nil {
		return Envelope{}, err
	}
	// The library has no decode option that rejects non-deterministic input, so
	// Decode encodes again and compares. The compare also rejects a missing key.
	again, err := marshal(e)
	if err != nil {
		return Envelope{}, err
	}
	if !bytes.Equal(again, b) {
		return Envelope{}, ErrNotDeterministic
	}
	return e, nil
}
