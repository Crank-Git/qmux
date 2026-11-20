package codec

import (
	"errors"
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

// Envelope is the RDATA of every answer RR. Each field has the integer key of the
// envelope table in docs/specs/spec.md § Data model.
type Envelope struct {
	// Format is the envelope format. Format 1 is the only valid value.
	Format uint64 `cbor:"0,keyasint"`
	// ProtocolID is the reverse-DNS name of the protocol, 1–128 bytes of [a-z0-9.-].
	ProtocolID string `cbor:"1,keyasint"`
	// Version is the protocol version, 1–65535.
	Version uint64 `cbor:"2,keyasint"`
	// MessageType is 1 (record), 2 (manifest), or a protocol-specific value 256–65535.
	MessageType uint64 `cbor:"3,keyasint"`
	// Status is the application status.
	Status uint64 `cbor:"4,keyasint"`
	// Payload holds the protocol payload bytes.
	Payload []byte `cbor:"5,keyasint"`
	// Signature is the optional signature block. A nil value encodes with no key 6.
	Signature *SignatureBlock `cbor:"6,keyasint,omitempty"`
}

// SignatureBlock is envelope key 6. Each field has the integer key of the signature
// block table in docs/specs/spec.md § Data model. Epic 6 fills and verifies it.
type SignatureBlock struct {
	// KeyID identifies the signing key. It is 8 bytes.
	KeyID []byte `cbor:"1,keyasint"`
	// Alg is the COSE algorithm number. -8 is EdDSA.
	Alg int64 `cbor:"2,keyasint"`
	// IssuedAt is the signing time in Unix seconds.
	IssuedAt uint64 `cbor:"3,keyasint"`
	// ExpiresAt is the expiry time in Unix seconds.
	ExpiresAt uint64 `cbor:"4,keyasint"`
	// Signature is the 64-byte signature.
	Signature []byte `cbor:"5,keyasint"`
}

// Limits holds the size limits of the codec. A zero field means its default value.
type Limits struct {
	// MaxEnvelope is the largest encoded envelope in bytes. 0 means 16384.
	MaxEnvelope int
}

const (
	defaultMaxEnvelope = 16384
	formatV1           = 1
	maxProtocolID      = 128
	maxVersion         = 65535
)

// The errors that Encode and Decode return. Use errors.Is to test for them.
var (
	// ErrTooLarge means the envelope is longer than Limits.MaxEnvelope.
	ErrTooLarge = errors.New("codec: envelope too large")
	// ErrFormat means the envelope format is not 1.
	ErrFormat = errors.New("codec: unsupported envelope format")
	// ErrInvalidProtocolID means the protocol ID is empty, too long, or has a byte outside [a-z0-9.-].
	ErrInvalidProtocolID = errors.New("codec: invalid protocol ID")
	// ErrInvalidVersion means the protocol version is 0 or above 65535.
	ErrInvalidVersion = errors.New("codec: invalid protocol version")
	// ErrInvalidMessageType means the message type is 0 or in 3–255.
	ErrInvalidMessageType = errors.New("codec: invalid message type")
)

// encMode is immutable and safe for concurrent use. The options are constant, so
// an error from EncMode is a programming error and not an input error.
var encMode = func() cbor.EncMode {
	m, err := cbor.CoreDetEncOptions().EncMode()
	if err != nil {
		panic(fmt.Sprintf("codec: build core deterministic encode mode: %v", err))
	}
	return m
}()

func (l Limits) maxEnvelope() int {
	if l.MaxEnvelope == 0 {
		return defaultMaxEnvelope
	}
	return l.MaxEnvelope
}

// Encode returns the core deterministic CBOR encoding of e (RFC 8949 § 4.2.1).
//
// Encode checks the fields of e with the same rules as Decode. It returns
// ErrFormat, ErrInvalidProtocolID, ErrInvalidVersion or ErrInvalidMessageType for
// a field that breaks a rule. It returns ErrTooLarge when the encoded envelope is
// longer than l.MaxEnvelope. A nil Payload encodes as an empty byte string.
// Encode is safe for concurrent use.
func Encode(e Envelope, l Limits) ([]byte, error) {
	if err := validate(e); err != nil {
		return nil, err
	}
	// Key 5 is a CBOR byte string. The core deterministic options encode a nil
	// slice as CBOR null, so Encode replaces a nil payload with an empty one.
	if e.Payload == nil {
		e.Payload = []byte{}
	}
	b, err := encMode.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("codec: encode envelope: %w", err)
	}
	if len(b) > l.maxEnvelope() {
		return nil, fmt.Errorf("%w: %d bytes, limit %d", ErrTooLarge, len(b), l.maxEnvelope())
	}
	return b, nil
}

// validate returns the first rule of the envelope table that e breaks, or nil.
// Encode and Decode use the same rules.
func validate(e Envelope) error {
	if e.Format != formatV1 {
		return fmt.Errorf("%w: %d", ErrFormat, e.Format)
	}
	if !validProtocolID(e.ProtocolID) {
		return fmt.Errorf("%w: %q", ErrInvalidProtocolID, e.ProtocolID)
	}
	if e.Version == 0 || e.Version > maxVersion {
		return fmt.Errorf("%w: %d", ErrInvalidVersion, e.Version)
	}
	if e.MessageType == 0 || (e.MessageType >= 3 && e.MessageType <= 255) {
		return fmt.Errorf("%w: %d", ErrInvalidMessageType, e.MessageType)
	}
	return nil
}

func validProtocolID(id string) bool {
	if len(id) == 0 || len(id) > maxProtocolID {
		return false
	}
	for i := range len(id) {
		c := id[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '.' && c != '-' {
			return false
		}
	}
	return true
}
