package security

import (
	"errors"
	"fmt"
	"strings"

	"github.com/fxamacker/cbor/v2"

	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/protocol"
)

// signingInputLabel is the first item of the signing input. It separates a QMux
// signature from an Ed25519 signature over other data.
const signingInputLabel = "qmux-sig/1"

// algEdDSA is the COSE algorithm number of EdDSA (RFC 9053 § 2.2).
const algEdDSA = -8

// errNoSignatureBlock means that the envelope has no signature block, so the
// signing input has no key ID and no validity window.
var errNoSignatureBlock = errors.New("envelope has no signature block")

// sigEncMode is immutable and safe for concurrent use. The options are constant, so
// an error from EncMode is a programming error and not an input error.
var sigEncMode = func() cbor.EncMode {
	m, err := cbor.CoreDetEncOptions().EncMode()
	if err != nil {
		panic(fmt.Sprintf("security: build core deterministic encode mode: %v", err))
	}
	return m
}()

// signingInput returns the signing input of e for qname and key. It is the core
// deterministic CBOR encoding of the 14-item array in docs/specs/spec.md § Data
// model (FR-security-14).
//
// qname is the fully qualified QNAME of the question, with the version label.
// signingInput uses it in lowercase. The signature field of e.Signature is not part
// of the signing input. signingInput returns an error when e has no signature block.
// The signer and the verifier both call it, so both use the same bytes.
func signingInput(e codec.Envelope, qname string, key protocol.ProtocolKey) ([]byte, error) {
	sb := e.Signature
	if sb == nil {
		return nil, errNoSignatureBlock
	}
	// The core deterministic options encode a nil slice as CBOR null. The spec
	// gives both fields the CBOR type bytes, so a nil slice becomes an empty one.
	payload, keyID := e.Payload, sb.KeyID
	if payload == nil {
		payload = []byte{}
	}
	if keyID == nil {
		keyID = []byte{}
	}
	b, err := sigEncMode.Marshal([]any{
		signingInputLabel,
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
		sb.Alg,
		sb.IssuedAt,
		sb.ExpiresAt,
	})
	if err != nil {
		return nil, fmt.Errorf("encode signing input: %w", err)
	}
	return b, nil
}
