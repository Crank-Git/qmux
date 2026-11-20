// Package schema defines the payloads of the three protocols of the embedded recovery
// demonstration: bootstrap, configuration and firmware.
//
// Each payload is protocol version 1, a CBOR map with uint keys. Marshal encodes a
// payload with the deterministic options of pkg/codec. The Unmarshal functions decode
// strictly: they reject an unknown key, a missing key, a wrong CBOR type and a field
// with the wrong length. Each payload type is also the protocol.Schema of its
// protocol, so the server validates a handler payload before it encodes the envelope.
//
// A protocol author can use this package as a model for a new payload schema.
package schema

import (
	"errors"
	"fmt"

	"github.com/Crank-Git/qmux/pkg/codec"
)

// The lengths that the payloads require.
const (
	// DigestSize is the length of a SHA-256 digest.
	DigestSize = 32
	// KeyIDSize is the length of a key ID.
	KeyIDSize = 8
)

var (
	// ErrDigestLength is the error for a digest that is not DigestSize bytes.
	ErrDigestLength = errors.New("schema: digest is not 32 bytes")
	// ErrKeyIDLength is the error for a firmware signing key ID that is not KeyIDSize
	// bytes.
	ErrKeyIDLength = errors.New("schema: firmware signing key ID is not 8 bytes")
	// ErrProtocolVersionUnsupported is the error that Validate returns for a protocol
	// version other than 1.
	ErrProtocolVersionUnsupported = errors.New("schema: protocol version is not 1")
)

// Bootstrap is the payload of org.qmux.bootstrap, protocol version 1 (FR-demo-1).
type Bootstrap struct {
	// ManagementURL is the URL of the management endpoint.
	ManagementURL string `cbor:"1,keyasint"`
	// ManagementTransport names the transport of the management endpoint, for
	// example "https".
	ManagementTransport string `cbor:"2,keyasint"`
}

// Marshal returns the deterministic CBOR encoding of p. Every failure is a wrapped
// codec error.
func (p Bootstrap) Marshal() ([]byte, error) {
	b, err := codec.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("schema: encode bootstrap: %w", err)
	}
	return b, nil
}

// UnmarshalBootstrap returns the bootstrap payload that b encodes. It returns an error
// for an unknown key, a missing key and a wrong CBOR type.
func UnmarshalBootstrap(b []byte) (Bootstrap, error) {
	var p Bootstrap
	if err := codec.Unmarshal(b, &p); err != nil {
		return Bootstrap{}, fmt.Errorf("schema: decode bootstrap: %w", err)
	}
	return p, nil
}

// Validate returns nil when payload is a valid bootstrap payload of version. It
// returns ErrProtocolVersionUnsupported for a version other than 1, and the error of
// UnmarshalBootstrap otherwise.
func (Bootstrap) Validate(version uint16, payload []byte) error {
	if version != 1 {
		return fmt.Errorf("%w: %d", ErrProtocolVersionUnsupported, version)
	}
	_, err := UnmarshalBootstrap(payload)
	return err
}

// Configuration is the payload of org.qmux.configuration, protocol version 1
// (FR-demo-2).
type Configuration struct {
	// Generation is the configuration generation.
	Generation uint64 `cbor:"1,keyasint"`
	// Digest is the SHA-256 digest of the configuration bundle. It holds DigestSize
	// bytes.
	Digest []byte `cbor:"2,keyasint"`
	// URL is the retrieval URL of the configuration bundle.
	URL string `cbor:"3,keyasint"`
}

// Marshal returns the deterministic CBOR encoding of p. It returns ErrDigestLength
// when p.Digest is not DigestSize bytes. Every other failure is a wrapped codec error.
func (p Configuration) Marshal() ([]byte, error) {
	if err := checkDigest(p.Digest); err != nil {
		return nil, fmt.Errorf("schema: encode configuration: %w", err)
	}
	b, err := codec.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("schema: encode configuration: %w", err)
	}
	return b, nil
}

// UnmarshalConfiguration returns the configuration payload that b encodes. It returns
// ErrDigestLength for a digest that is not DigestSize bytes. It returns an error for an
// unknown key, a missing key and a wrong CBOR type.
func UnmarshalConfiguration(b []byte) (Configuration, error) {
	var p Configuration
	if err := codec.Unmarshal(b, &p); err != nil {
		return Configuration{}, fmt.Errorf("schema: decode configuration: %w", err)
	}
	if err := checkDigest(p.Digest); err != nil {
		return Configuration{}, fmt.Errorf("schema: decode configuration: %w", err)
	}
	return p, nil
}

// Validate returns nil when payload is a valid configuration payload of version. It
// returns ErrProtocolVersionUnsupported for a version other than 1, and the error of
// UnmarshalConfiguration otherwise.
func (Configuration) Validate(version uint16, payload []byte) error {
	if version != 1 {
		return fmt.Errorf("%w: %d", ErrProtocolVersionUnsupported, version)
	}
	_, err := UnmarshalConfiguration(payload)
	return err
}

// Firmware is the payload of org.qmux.firmware, protocol version 1 (FR-demo-3).
type Firmware struct {
	// Version is the firmware version as MAJOR.MINOR.PATCH. ParseFirmwareVersion
	// gives the form that compares.
	Version string `cbor:"1,keyasint"`
	// Digest is the SHA-256 digest of the firmware image. It holds DigestSize bytes.
	Digest []byte `cbor:"2,keyasint"`
	// URL is the download URL of the firmware image.
	URL string `cbor:"3,keyasint"`
	// KeyID is the firmware signing key ID. It holds KeyIDSize bytes.
	KeyID []byte `cbor:"4,keyasint"`
}

// Marshal returns the deterministic CBOR encoding of p. It returns the error of
// p.check for a field that breaks the schema. Every other failure is a wrapped codec
// error.
func (p Firmware) Marshal() ([]byte, error) {
	if err := p.check(); err != nil {
		return nil, fmt.Errorf("schema: encode firmware: %w", err)
	}
	b, err := codec.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("schema: encode firmware: %w", err)
	}
	return b, nil
}

// UnmarshalFirmware returns the firmware payload that b encodes. It returns an error
// for an unknown key, a missing key and a wrong CBOR type. It also returns:
//   - ErrFirmwareVersionSyntax for a firmware version that does not parse.
//   - ErrDigestLength for a digest that is not DigestSize bytes.
//   - ErrKeyIDLength for a firmware signing key ID that is not KeyIDSize bytes.
func UnmarshalFirmware(b []byte) (Firmware, error) {
	var p Firmware
	if err := codec.Unmarshal(b, &p); err != nil {
		return Firmware{}, fmt.Errorf("schema: decode firmware: %w", err)
	}
	if err := p.check(); err != nil {
		return Firmware{}, fmt.Errorf("schema: decode firmware: %w", err)
	}
	return p, nil
}

// Validate returns nil when payload is a valid firmware payload of version. It
// returns ErrProtocolVersionUnsupported for a version other than 1, and the error of
// UnmarshalFirmware otherwise.
func (Firmware) Validate(version uint16, payload []byte) error {
	if version != 1 {
		return fmt.Errorf("%w: %d", ErrProtocolVersionUnsupported, version)
	}
	_, err := UnmarshalFirmware(payload)
	return err
}

// check returns the error of ParseFirmwareVersion, ErrDigestLength or ErrKeyIDLength
// for the first field of p that breaks the schema.
func (p Firmware) check() error {
	if _, err := ParseFirmwareVersion(p.Version); err != nil {
		return err
	}
	if err := checkDigest(p.Digest); err != nil {
		return err
	}
	if len(p.KeyID) != KeyIDSize {
		return fmt.Errorf("%w: %d bytes", ErrKeyIDLength, len(p.KeyID))
	}
	return nil
}

// checkDigest returns ErrDigestLength when d is not DigestSize bytes.
func checkDigest(d []byte) error {
	if len(d) != DigestSize {
		return fmt.Errorf("%w: %d bytes", ErrDigestLength, len(d))
	}
	return nil
}
