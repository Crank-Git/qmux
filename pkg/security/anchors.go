package security

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

// TrustAnchor is one public key that a client trusts to sign envelopes.
type TrustAnchor struct {
	// KeyID is KeyID(PublicKey).
	KeyID [8]byte
	// PublicKey is the 32-byte Ed25519 public key.
	PublicKey ed25519.PublicKey
	// Protocols lists the protocol IDs that the key can sign. An empty list
	// allows every protocol ID.
	Protocols []string
}

// anchorFile is the JSON schema of the trust anchor file in docs/specs/spec.md § Files.
type anchorFile struct {
	Anchors []anchorEntry `json:"anchors"`
}

type anchorEntry struct {
	KeyID     string   `json:"key_id"`
	PublicKey string   `json:"public_key"`
	Protocols []string `json:"protocols"`
}

// LoadTrustAnchors returns the trust anchors in the JSON file at path.
//
// It returns an error when:
//   - the file is not the trust anchor file schema, or holds an unknown field
//   - a key_id is not 8 bytes of hex, or differs from KeyID(public_key)
//   - a public_key is not 32 bytes after base64 decoding
//   - two anchors share a key ID
func LoadTrustAnchors(path string) ([]TrustAnchor, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load trust anchors: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	// A misspelt "protocols" field would otherwise allow every protocol ID.
	dec.DisallowUnknownFields()
	var f anchorFile
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("load trust anchors %s: %w", path, err)
	}
	anchors := make([]TrustAnchor, 0, len(f.Anchors))
	seen := make(map[[8]byte]bool, len(f.Anchors))
	for i, e := range f.Anchors {
		a, err := parseAnchor(e)
		if err != nil {
			return nil, fmt.Errorf("load trust anchors %s: anchor %d: %w", path, i, err)
		}
		if seen[a.KeyID] {
			return nil, fmt.Errorf("load trust anchors %s: anchor %d: duplicate key_id %x", path, i, a.KeyID)
		}
		seen[a.KeyID] = true
		anchors = append(anchors, a)
	}
	return anchors, nil
}

func parseAnchor(e anchorEntry) (TrustAnchor, error) {
	id, err := hex.DecodeString(e.KeyID)
	if err != nil {
		return TrustAnchor{}, fmt.Errorf("key_id: %w", err)
	}
	if len(id) != 8 {
		return TrustAnchor{}, fmt.Errorf("key_id is %d bytes, want 8", len(id))
	}
	pub, err := base64.StdEncoding.DecodeString(e.PublicKey)
	if err != nil {
		return TrustAnchor{}, fmt.Errorf("public_key: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return TrustAnchor{}, fmt.Errorf("public_key is %d bytes, want %d", len(pub), ed25519.PublicKeySize)
	}
	a := TrustAnchor{KeyID: [8]byte(id), PublicKey: pub, Protocols: e.Protocols}
	if want := KeyID(a.PublicKey); a.KeyID != want {
		return TrustAnchor{}, fmt.Errorf("key_id %x differs from KeyID(public_key) %x", a.KeyID, want)
	}
	return a, nil
}

// WriteTrustAnchors writes a to the file at path in the trust anchor file schema
// that LoadTrustAnchors reads. It replaces an existing file.
func WriteTrustAnchors(path string, a []TrustAnchor) error {
	f := anchorFile{Anchors: make([]anchorEntry, 0, len(a))}
	for _, t := range a {
		protocols := t.Protocols
		// The schema holds a list, so write [] and not null for no protocols.
		if protocols == nil {
			protocols = []string{}
		}
		f.Anchors = append(f.Anchors, anchorEntry{
			KeyID:     hex.EncodeToString(t.KeyID[:]),
			PublicKey: base64.StdEncoding.EncodeToString(t.PublicKey),
			Protocols: protocols,
		})
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("write trust anchors %s: %w", path, err)
	}
	// The file holds only public keys, so other users can read it.
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("write trust anchors: %w", err)
	}
	return nil
}
