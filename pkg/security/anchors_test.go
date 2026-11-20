package security

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func anchorFor(t *testing.T, seed byte, protocols ...string) TrustAnchor {
	t.Helper()
	pub := ed25519.NewKeyFromSeed(bytes32(seed)).Public().(ed25519.PublicKey)
	return TrustAnchor{KeyID: KeyID(pub), PublicKey: pub, Protocols: protocols}
}

func bytes32(b byte) []byte {
	s := make([]byte, ed25519.SeedSize)
	for i := range s {
		s[i] = b
	}
	return s
}

func writeAnchorFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "anchors.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func anchorJSON(keyID string, pub []byte) string {
	return fmt.Sprintf(`{"key_id":%q,"public_key":%q,"protocols":[]}`, keyID, base64.StdEncoding.EncodeToString(pub))
}

func TestWriteTrustAnchorsWritesAFileThatLoadsToAnEqualList(t *testing.T) {
	want := []TrustAnchor{
		anchorFor(t, 1, "org.qmux.firmware", "org.qmux.configuration"),
		anchorFor(t, 2),
	}
	path := filepath.Join(t.TempDir(), "anchors.json")
	if err := WriteTrustAnchors(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadTrustAnchors(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("loaded %d anchors, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].KeyID != want[i].KeyID || !got[i].PublicKey.Equal(want[i].PublicKey) ||
			!slices.Equal(got[i].Protocols, want[i].Protocols) {
			t.Fatalf("anchor %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestLoadTrustAnchorsReadsTheSpecSchema(t *testing.T) {
	a := anchorFor(t, 3, "org.qmux.firmware")
	body := fmt.Sprintf(`{"anchors":[{"key_id":%q,"public_key":%q,"protocols":["org.qmux.firmware"]}]}`,
		hex.EncodeToString(a.KeyID[:]), base64.StdEncoding.EncodeToString(a.PublicKey))
	got, err := LoadTrustAnchors(writeAnchorFile(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].KeyID != a.KeyID || !got[0].PublicKey.Equal(a.PublicKey) ||
		!slices.Equal(got[0].Protocols, a.Protocols) {
		t.Fatalf("got %+v, want [%+v]", got, a)
	}
}

func TestLoadTrustAnchorsRejectsAKeyIDThatDiffersFromThePublicKey(t *testing.T) {
	a := anchorFor(t, 4)
	other := anchorFor(t, 5)
	body := `{"anchors":[` + anchorJSON(hex.EncodeToString(other.KeyID[:]), a.PublicKey) + `]}`
	if _, err := LoadTrustAnchors(writeAnchorFile(t, body)); err == nil {
		t.Fatal("LoadTrustAnchors accepted a wrong key_id")
	}
}

func TestLoadTrustAnchorsRejectsAPublicKeyThatIsNot32Bytes(t *testing.T) {
	a := anchorFor(t, 6)
	short := a.PublicKey[:31]
	body := `{"anchors":[` + anchorJSON(hex.EncodeToString(a.KeyID[:]), short) + `]}`
	if _, err := LoadTrustAnchors(writeAnchorFile(t, body)); err == nil {
		t.Fatal("LoadTrustAnchors accepted a 31-byte public key")
	}
}

func TestLoadTrustAnchorsRejectsTwoAnchorsWithOneKeyID(t *testing.T) {
	a := anchorFor(t, 7)
	entry := anchorJSON(hex.EncodeToString(a.KeyID[:]), a.PublicKey)
	body := `{"anchors":[` + entry + `,` + entry + `]}`
	if _, err := LoadTrustAnchors(writeAnchorFile(t, body)); err == nil {
		t.Fatal("LoadTrustAnchors accepted a duplicate key ID")
	}
}

func TestLoadTrustAnchorsRejectsMalformedFields(t *testing.T) {
	a := anchorFor(t, 8)
	id := hex.EncodeToString(a.KeyID[:])
	pub := base64.StdEncoding.EncodeToString(a.PublicKey)
	cases := map[string]string{
		"not JSON":              `{"anchors":`,
		"key_id not hex":        fmt.Sprintf(`{"anchors":[{"key_id":"zz","public_key":%q}]}`, pub),
		"key_id of 4 bytes":     fmt.Sprintf(`{"anchors":[{"key_id":%q,"public_key":%q}]}`, id[:8], pub),
		"public_key not base64": fmt.Sprintf(`{"anchors":[{"key_id":%q,"public_key":"!!"}]}`, id),
		// A misspelt "protocols" would otherwise allow every protocol ID.
		"unknown field": fmt.Sprintf(`{"anchors":[{"key_id":%q,"public_key":%q,"protocol":["x"]}]}`, id, pub),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadTrustAnchors(writeAnchorFile(t, body)); err == nil {
				t.Fatal("LoadTrustAnchors returned nil")
			}
		})
	}
}

func TestLoadTrustAnchorsAcceptsAWorldWritableFile(t *testing.T) {
	a := anchorFor(t, 9)
	path := writeAnchorFile(t, `{"anchors":[`+anchorJSON(hex.EncodeToString(a.KeyID[:]), a.PublicKey)+`]}`)
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTrustAnchors(path); err != nil {
		t.Fatal(err)
	}
}
