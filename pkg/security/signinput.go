package security

import (
	"fmt"
	"strings"

	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/protocol"
)

// signingInputTag is the first item of the signing input. It separates a QMux
// signature from a signature over other data with the same key.
const signingInputTag = "qmux-sig/1"

// signingInput returns the signing input of e for qname and key, as defined in
// docs/specs/spec.md § Data model (FR-security-14). It encodes a CBOR array of 14
// items with cbor.CoreDetEncOptions, through codec.Marshal.
//
// The signing input covers the fields of e.Signature except the signature bytes,
// so e.Signature must not be nil. The QNAME is lowercase in the signing input.
func signingInput(e codec.Envelope, qname string, key protocol.ProtocolKey) ([]byte, error) {
	payload := e.Payload
	// The core deterministic options encode a nil slice as CBOR null, but the
	// envelope encodes a nil payload as an empty byte string.
	if payload == nil {
		payload = []byte{}
	}
	keyID := e.Signature.KeyID
	if keyID == nil {
		keyID = []byte{}
	}
	b, err := codec.Marshal([]any{
		signingInputTag,
		strings.ToLower(qname),
		key.Class,
		key.Type,
		e.Format,
		e.ProtocolID,
		e.Version,
		e.MessageType,
		e.Status,
		payload,
		keyID,
		e.Signature.Alg,
		e.Signature.IssuedAt,
		e.Signature.ExpiresAt,
	})
	if err != nil {
		return nil, fmt.Errorf("signing input: %w", err)
	}
	return b, nil
}
