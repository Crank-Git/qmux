package security

import (
	"cmp"
	"crypto/ed25519"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/protocol"
)

// The errors that Verify returns, one for each check. Verify wraps each one in an
// error that names the protocol ID and the QNAME. Use errors.Is to test for them.
var (
	// ErrUnsigned means the envelope has no signature block.
	ErrUnsigned = errors.New("security: envelope has no signature block")
	// ErrAlgorithm means the signature algorithm is not -8 (EdDSA).
	ErrAlgorithm = errors.New("security: signature algorithm is not -8")
	// ErrUnknownKey means no trust anchor has the key ID of the signature block.
	ErrUnknownKey = errors.New("security: key ID is not a trust anchor")
	// ErrProtocolNotAllowed means the trust anchor does not allow the protocol ID.
	ErrProtocolNotAllowed = errors.New("security: trust anchor does not allow the protocol ID")
	// ErrBadSignature means the Ed25519 check over the signing input failed.
	ErrBadSignature = errors.New("security: bad signature")
	// ErrNotYetValid means issued_at is later than now + Skew.
	ErrNotYetValid = errors.New("security: envelope is not yet valid")
	// ErrExpired means expires_at is earlier than now - Skew.
	ErrExpired = errors.New("security: envelope expired")
	// ErrWindowTooLong means the validity window is longer than MaxWindow or is not positive.
	ErrWindowTooLong = errors.New("security: validity window too long")
	// ErrReplay means issued_at is older than the freshness store holds for the
	// protocol ID and the QNAME.
	ErrReplay = errors.New("security: envelope is older than an accepted envelope")
	// ErrNotDeterministic means the envelope bytes do not encode back to the same bytes.
	ErrNotDeterministic = errors.New("security: envelope is not deterministic CBOR")
)

const (
	defaultSkew      = 60 * time.Second
	defaultMaxWindow = 24 * time.Hour
	// algEdDSA is the COSE algorithm number of EdDSA (RFC 9053 § 2.2).
	algEdDSA = -8
	// maxEnvelope is the largest envelope that the codec accepts. The client
	// applies its own limit before Verify, so Verify accepts every valid size.
	maxEnvelope = 65000
	// maxUnixSeconds keeps a Unix time from a signature block inside the range of
	// time.Time. It is far later than any clock, so a comparison keeps its result.
	maxUnixSeconds = 1 << 40
)

// Verifier checks the signature and the freshness of envelopes against trust
// anchors. A zero field means its default value. Use a Verifier through a
// pointer, because it keeps the default freshness store. A Verifier is safe for
// concurrent use when no goroutine changes its fields.
type Verifier struct {
	// Anchors are the trust anchors that can sign an envelope.
	Anchors []TrustAnchor
	// Skew is the clock difference that Verify allows at both ends of the
	// validity window. 0 means 60 s.
	Skew time.Duration
	// MaxWindow is the longest validity window that Verify accepts. 0 means 24 h.
	MaxWindow time.Duration
	// Store holds the newest accepted issued_at per protocol ID and QNAME. nil
	// means an in-memory store that the Verifier keeps for its lifetime.
	Store *FreshnessStore
	// Now returns the current time. nil means time.Now.
	Now func() time.Time

	local FreshnessStore
}

// Verify returns the envelope that raw encodes, after it verifies the envelope
// for qname and key. raw is the RDATA of the answer.
//
// Verify returns the first failure of these checks, in this order:
//   - ErrNotDeterministic: raw does not encode back to the same bytes
//   - ErrUnsigned: the envelope has no signature block
//   - ErrAlgorithm: the algorithm is not -8
//   - ErrUnknownKey: no trust anchor has the key ID
//   - ErrProtocolNotAllowed: the anchor has a protocols list without the protocol ID
//   - ErrBadSignature: the Ed25519 check over the signing input fails
//   - ErrNotYetValid: issued_at is later than now + Skew
//   - ErrExpired: expires_at is earlier than now - Skew
//   - ErrWindowTooLong: the validity window is longer than MaxWindow or not positive
//   - ErrReplay: issued_at is older than the freshness store holds
//
// Each error names the protocol ID and the QNAME. Raw bytes that are not a valid
// envelope give the wrapped codec error. Verify updates the freshness store only
// when every check passes.
func (v *Verifier) Verify(raw []byte, qname string, key protocol.ProtocolKey) (codec.Envelope, error) {
	e, err := codec.Decode(raw, codec.Limits{MaxEnvelope: maxEnvelope})
	if errors.Is(err, codec.ErrNotDeterministic) {
		// The codec returns no fields for this error, so the protocol ID is unknown.
		return codec.Envelope{}, fmt.Errorf("verify: %w: unknown protocol ID %s", ErrNotDeterministic, qname)
	}
	if err != nil {
		return codec.Envelope{}, fmt.Errorf("verify %s: %w", qname, err)
	}
	if err := v.check(e, qname, key); err != nil {
		return codec.Envelope{}, fmt.Errorf("verify: %w: %s %s", err, e.ProtocolID, qname)
	}
	return e, nil
}

// check returns the sentinel of the first check after the deterministic check
// that e fails, or nil. It updates the freshness store only when it returns nil.
func (v *Verifier) check(e codec.Envelope, qname string, key protocol.ProtocolKey) error {
	sig := e.Signature
	if sig == nil {
		return ErrUnsigned
	}
	if sig.Alg != algEdDSA {
		return ErrAlgorithm
	}
	i := slices.IndexFunc(v.Anchors, func(a TrustAnchor) bool { return string(a.KeyID[:]) == string(sig.KeyID) })
	if i < 0 {
		return ErrUnknownKey
	}
	anchor := v.Anchors[i]
	if len(anchor.Protocols) > 0 && !slices.Contains(anchor.Protocols, e.ProtocolID) {
		return ErrProtocolNotAllowed
	}
	in, err := signingInput(e, qname, key)
	if err != nil {
		return err
	}
	// ed25519.Verify panics for a public key of the wrong size. An anchor built
	// without LoadTrustAnchors can have one.
	if len(anchor.PublicKey) != ed25519.PublicKeySize || !ed25519.Verify(anchor.PublicKey, in, sig.Signature) {
		return ErrBadSignature
	}
	if err := v.checkWindow(sig); err != nil {
		return err
	}
	if !v.store().admit(newFreshnessKey(e.ProtocolID, qname), sig.IssuedAt) {
		return ErrReplay
	}
	return nil
}

// checkWindow returns ErrNotYetValid, ErrExpired or ErrWindowTooLong when the
// validity window of sig fails that check at the current time, or nil.
func (v *Verifier) checkWindow(sig *codec.SignatureBlock) error {
	now := time.Now
	if v.Now != nil {
		now = v.Now
	}
	skew := cmp.Or(v.Skew, defaultSkew)
	t := now()
	issued, expires := unixTime(sig.IssuedAt), unixTime(sig.ExpiresAt)
	if issued.After(t.Add(skew)) {
		return ErrNotYetValid
	}
	if expires.Before(t.Add(-skew)) {
		return ErrExpired
	}
	if !expires.After(issued) || expires.Sub(issued) > cmp.Or(v.MaxWindow, defaultMaxWindow) {
		return ErrWindowTooLong
	}
	return nil
}

func (v *Verifier) store() *FreshnessStore {
	if v.Store != nil {
		return v.Store
	}
	return &v.local
}

// unixTime returns the time of s Unix seconds. A value above maxUnixSeconds
// returns the time of maxUnixSeconds, because time.Unix overflows for a value
// near the maximum int64.
func unixTime(s uint64) time.Time {
	return time.Unix(int64(min(s, maxUnixSeconds)), 0)
}
