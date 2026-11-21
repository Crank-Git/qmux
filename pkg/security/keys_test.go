package security

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

// rfc8032Seed is the secret key of RFC 8032 § 7.1 TEST 1. Its public key is
// d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a.
const rfc8032Seed = "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"

func testKey(t testing.TB) ed25519.PrivateKey {
	t.Helper()
	seed, err := hex.DecodeString(rfc8032Seed)
	if err != nil {
		t.Fatal(err)
	}
	return ed25519.NewKeyFromSeed(seed)
}

func TestKeyIDIsTheFirst8BytesOfTheSHA256OfThePublicKey(t *testing.T) {
	// The vector is the first 8 bytes of sha256(d75a98…511a), computed with shasum.
	const want = "21fe31dfa154a261"
	pub := testKey(t).Public().(ed25519.PublicKey)
	id := KeyID(pub)
	if got := hex.EncodeToString(id[:]); got != want {
		t.Fatalf("KeyID = %s, want %s", got, want)
	}
}

func TestGenerateKeyReturnsADifferentKeyEachCall(t *testing.T) {
	a, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != ed25519.PrivateKeySize {
		t.Fatalf("key length = %d, want %d", len(a), ed25519.PrivateKeySize)
	}
	if a.Equal(b) {
		t.Fatal("two GenerateKey calls returned the same key")
	}
}

func TestWritePrivateKeyWritesAPKCS8PEMBlockWithMode0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k.key")
	key := testKey(t)
	if err := WritePrivateKey(path, key); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode = %o, want 600", mode)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "PRIVATE KEY" {
		t.Fatalf("file has no PRIVATE KEY PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !key.Equal(parsed) {
		t.Fatal("the PEM block holds a different key")
	}
}

func TestWritePrivateKeyRefusesAnExistingFileAndLeavesItUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k.key")
	want := []byte("existing content")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivateKey(path, testKey(t)); err == nil {
		t.Fatal("WritePrivateKey to an existing path returned nil")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("file content changed to %q", got)
	}
}

func TestLoadPrivateKeyReturnsTheKeyThatWritePrivateKeyWrote(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k.key")
	key := testKey(t)
	if err := WritePrivateKey(path, key); err != nil {
		t.Fatal(err)
	}
	got, warn, err := LoadPrivateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if warn != "" {
		t.Fatalf("warning = %q for a 0600 file, want none", warn)
	}
	if !key.Equal(got) {
		t.Fatal("LoadPrivateKey returned a different key")
	}
}

func writePEM(t *testing.T, path, typ string, der []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), mode); err != nil {
		t.Fatal(err)
	}
	// os.WriteFile applies the umask, so set the mode the test needs explicitly.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPrivateKeyRejectsAnECDSAPKCS8File(t *testing.T) {
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(ec)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ec.key")
	writePEM(t, path, "PRIVATE KEY", der, 0o600)
	if _, _, err := LoadPrivateKey(path); err == nil {
		t.Fatal("LoadPrivateKey of an ECDSA key returned nil")
	}
}

func TestLoadPrivateKeyRejectsAPEMBlockThatIsNotPKCS8(t *testing.T) {
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(ec)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cases := map[string]struct {
		typ string
		der []byte
	}{
		"SEC 1 key":                          {"EC PRIVATE KEY", der},
		"wrong PEM type":                     {"PUBLIC KEY", der},
		"not DER at all":                     {"PRIVATE KEY", []byte("not der")},
		"SEC 1 bytes in a PRIVATE KEY block": {"PRIVATE KEY", der},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name)
			writePEM(t, path, c.typ, c.der, 0o600)
			if _, _, err := LoadPrivateKey(path); err == nil {
				t.Fatal("LoadPrivateKey returned nil")
			}
		})
	}
}

func TestLoadPrivateKeyRejectsAFileWithNoPEMBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k.key")
	if err := os.WriteFile(path, []byte("no pem here"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadPrivateKey(path); err == nil {
		t.Fatal("LoadPrivateKey returned nil")
	}
}

func TestLoadPrivateKeyWarnsWhenGroupOrWorldCanReadTheFile(t *testing.T) {
	der, err := x509.MarshalPKCS8PrivateKey(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604} {
		path := filepath.Join(t.TempDir(), "k.key")
		writePEM(t, path, "PRIVATE KEY", der, mode)
		key, warn, err := LoadPrivateKey(path)
		if err != nil {
			t.Fatal(err)
		}
		if warn == "" {
			t.Fatalf("mode %o: warning is empty", mode)
		}
		if key == nil {
			t.Fatalf("mode %o: key is nil", mode)
		}
	}
}

func TestLoadPrivateKeyErrorsNeverHoldKeyMaterial(t *testing.T) {
	key := testKey(t)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "k.key")
	// A PKCS#8 block with the wrong PEM type holds a valid key that must not leak.
	writePEM(t, path, "RSA PRIVATE KEY", der, 0o600)
	_, _, err = LoadPrivateKey(path)
	if err == nil {
		t.Fatal("LoadPrivateKey returned nil")
	}
	for _, secret := range []string{hex.EncodeToString(key.Seed()), hex.EncodeToString(der)} {
		if bytes.Contains([]byte(err.Error()), []byte(secret)) {
			t.Fatalf("error text holds key material: %v", err)
		}
	}
}
