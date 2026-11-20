package security

import (
	"crypto/ed25519"
	"fmt"
	"time"

	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/protocol"
)

// defaultValidity is the validity window that a Signer with a zero Validity uses.
const defaultValidity = time.Hour

// Signer signs envelopes with an Ed25519 signing key. A Signer is safe for
// concurrent use when the caller does not change its fields during a Sign call.
type Signer struct {
	// Key is the signing key.
	Key ed25519.PrivateKey
	// Validity is the length of the validity window. The default is 1 hour.
	Validity time.Duration
	// Now returns the signing time. The default is time.Now. A test sets a fixed
	// time here.
	Now func() time.Time
}

// Sign sets the signature block of e for the QNAME qname and the protocol key key.
//
// The signature block holds the key ID of s.Key, the algorithm -8, issued_at = now
// and expires_at = now + s.Validity, in Unix seconds. The signature is the Ed25519
// signature over the signing input of e, with the ASCII letters of qname in
// lowercase. qname is the full QNAME of the question, with the version label.
//
// Sign returns an error, and does not change e, when:
//   - s.Key is not an Ed25519 private key of 64 bytes
//   - the signing time is before 1970
//   - expires_at is not later than issued_at
//
// The error text never holds key material.
func (s *Signer) Sign(e *codec.Envelope, qname string, key protocol.ProtocolKey) error {
	// ed25519.Sign panics for a key of another length.
	if len(s.Key) != ed25519.PrivateKeySize {
		return fmt.Errorf("sign: key length %d, want %d", len(s.Key), ed25519.PrivateKeySize)
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	validity := s.Validity
	if validity == 0 {
		validity = defaultValidity
	}
	t := now()
	issued, expires := t.Unix(), t.Add(validity).Unix()
	if issued < 0 || expires <= issued {
		return fmt.Errorf("sign: validity window %d–%d is not a positive window after 1970", issued, expires)
	}
	id := KeyID(s.Key.Public().(ed25519.PublicKey))
	sb := &codec.SignatureBlock{
		KeyID:     id[:],
		Alg:       algEdDSA,
		IssuedAt:  uint64(issued),
		ExpiresAt: uint64(expires),
	}
	signed := *e
	signed.Signature = sb
	msg, err := signingInput(signed, qname, key)
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}
	sb.Signature = ed25519.Sign(s.Key, msg)
	e.Signature = sb
	return nil
}
