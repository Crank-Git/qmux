package security

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

// pemTypePrivateKey is the PEM block type of a PKCS#8 private key.
const pemTypePrivateKey = "PRIVATE KEY"

// Warning is a condition that LoadPrivateKey reports without an error. An empty
// Warning means no condition. The caller logs a non-empty Warning.
type Warning string

// KeyID returns the first 8 bytes of the SHA-256 digest of the 32-byte Ed25519
// public key pub.
func KeyID(pub ed25519.PublicKey) [8]byte {
	sum := sha256.Sum256(pub)
	var id [8]byte
	copy(id[:], sum[:8])
	return id
}

// GenerateKey returns a new Ed25519 private key from crypto/rand.
func GenerateKey() (ed25519.PrivateKey, error) {
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	return k, nil
}

// WritePrivateKey writes k to a new file at path as a PKCS#8 PEM block of type
// "PRIVATE KEY", with file mode 0600.
//
// It returns an error when the file exists, and then it does not change the file.
func WritePrivateKey(path string, k ed25519.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return fmt.Errorf("write private key %s: %w", path, err)
	}
	// O_EXCL makes the existence check and the create one step, so no other
	// process can put a file at path between them.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("write private key: %w", err)
	}
	werr := pem.Encode(f, &pem.Block{Type: pemTypePrivateKey, Bytes: der})
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		// A partial file blocks the next attempt, because of O_EXCL.
		rerr := os.Remove(path)
		return fmt.Errorf("write private key %s: %w", path, errors.Join(err, rerr))
	}
	return nil
}

// LoadPrivateKey returns the Ed25519 private key in the PEM file at path.
//
// It returns a non-empty Warning when the file mode lets the group or other
// users read the file. It returns an error when:
//   - the file holds no PEM block
//   - the block type is not "PRIVATE KEY", or the block is not PKCS#8
//   - the key is not an Ed25519 key
//
// The error text never holds key material.
func LoadPrivateKey(path string) (ed25519.PrivateKey, Warning, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("load private key: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, "", fmt.Errorf("load private key: %w", err)
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, "", fmt.Errorf("load private key %s: no PEM block", path)
	}
	if block.Type != pemTypePrivateKey {
		return nil, "", fmt.Errorf("load private key %s: PEM block type %q, want %q", path, block.Type, pemTypePrivateKey)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, "", fmt.Errorf("load private key %s: %w", path, err)
	}
	k, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, "", fmt.Errorf("load private key %s: key type %T, want ed25519.PrivateKey", path, parsed)
	}
	var warn Warning
	if mode := info.Mode().Perm(); mode&0o044 != 0 {
		warn = Warning(fmt.Sprintf("private key file %s has mode %04o: the group or other users can read it", path, mode))
	}
	return k, warn, nil
}
