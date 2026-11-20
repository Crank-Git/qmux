package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Crank-Git/qmux/pkg/discovery"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/security"
)

// The fixed seeds of the test keys. anchorSeed signs for the trust anchor file, and
// otherSeed is a key that no trust anchor file of these tests holds.
var (
	anchorSeed = bytes.Repeat([]byte{0x31}, ed25519.SeedSize)
	otherSeed  = bytes.Repeat([]byte{0x32}, ed25519.SeedSize)
)

// keygenCmd runs "qmuxctl keygen" with args and returns the exit code, stdout and
// stderr.
func keygenCmd(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(append([]string{"qmuxctl", "keygen"}, args...), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// writeAnchors writes a trust anchor file with the public key of seed and returns
// its path.
func writeAnchors(t *testing.T, seed []byte) string {
	t.Helper()
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	path := filepath.Join(t.TempDir(), "anchors.json")
	if err := security.WriteTrustAnchors(path, []security.TrustAnchor{{KeyID: security.KeyID(pub), PublicKey: pub}}); err != nil {
		t.Fatalf("WriteTrustAnchors: %v", err)
	}
	return path
}

func signerOf(seed []byte) *security.Signer {
	return &security.Signer{Key: ed25519.NewKeyFromSeed(seed)}
}

func TestKeygenWritesTheKeyAndTheAnchorFileAndPrintsTheKeyID(t *testing.T) { // FR-security-48
	prefix := filepath.Join(t.TempDir(), "k")
	code, stdout, stderr := keygenCmd("--out", prefix)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr)
	}

	key, warn, err := security.LoadPrivateKey(prefix + ".key")
	if err != nil || warn != "" {
		t.Fatalf("LoadPrivateKey = %v, warning %q; want a key and no warning", err, warn)
	}
	info, err := os.Stat(prefix + ".key")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %04o, want 0600", info.Mode().Perm())
	}
	anchors, err := security.LoadTrustAnchors(prefix + ".anchor.json")
	if err != nil {
		t.Fatalf("LoadTrustAnchors: %v", err)
	}
	id := security.KeyID(key.Public().(ed25519.PublicKey))
	if len(anchors) != 1 || anchors[0].KeyID != id || !anchors[0].PublicKey.Equal(key.Public()) || len(anchors[0].Protocols) != 0 {
		t.Errorf("anchors = %+v, want one anchor for the written key that allows every protocol ID", anchors)
	}

	want := "wrote " + prefix + ".key (mode 0600)\nwrote " + prefix + ".anchor.json\nkey_id " + hex.EncodeToString(id[:]) + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if !regexp.MustCompile(`key_id [0-9a-f]{16}\n$`).MatchString(stdout) {
		t.Errorf("stdout = %q, want a 16-character hex key ID on the last line", stdout)
	}
}

func TestKeygenExitsOneAndWritesNothingWhenAFileExists(t *testing.T) { // FR-security-49
	for _, existing := range []string{".key", ".anchor.json"} {
		t.Run(existing, func(t *testing.T) {
			prefix := filepath.Join(t.TempDir(), "k")
			old := []byte("existing file\n")
			if err := os.WriteFile(prefix+existing, old, 0o600); err != nil {
				t.Fatal(err)
			}

			code, stdout, stderr := keygenCmd("--out", prefix)

			if code != 1 {
				t.Fatalf("exit code = %d, want 1", code)
			}
			if stdout != "" || !strings.Contains(stderr, prefix+existing) {
				t.Errorf("stdout = %q, stderr = %q; want only an error that names %s", stdout, stderr, prefix+existing)
			}
			if b, err := os.ReadFile(prefix + existing); err != nil || !bytes.Equal(b, old) {
				t.Errorf("existing file = %q, %v; want it unchanged", b, err)
			}
			entries, err := os.ReadDir(filepath.Dir(prefix))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Errorf("directory holds %d files, want only the existing file", len(entries))
			}
		})
	}
}

func TestASecondKeygenWithTheSamePrefixExitsOne(t *testing.T) { // FR-security-49
	prefix := filepath.Join(t.TempDir(), "k")
	if code, _, stderr := keygenCmd("--out", prefix); code != 0 {
		t.Fatalf("first keygen: exit code = %d; stderr = %q", code, stderr)
	}
	before, err := os.ReadFile(prefix + ".key")
	if err != nil {
		t.Fatal(err)
	}

	if code, _, _ := keygenCmd("--out", prefix); code != 1 {
		t.Errorf("second keygen: exit code = %d, want 1", code)
	}
	if after, err := os.ReadFile(prefix + ".key"); err != nil || !bytes.Equal(after, before) {
		t.Errorf("key file changed after the second keygen")
	}
}

func TestKeygenWithoutOutExitsSixtyFourAndPrintsTheUsage(t *testing.T) {
	code, stdout, stderr := keygenCmd()
	if code != 64 {
		t.Fatalf("exit code = %d, want 64", code)
	}
	if stdout != "" || !strings.Contains(stderr, "missing flag --out") || !strings.Contains(stderr, "Usage:") {
		t.Errorf("stdout = %q, stderr = %q; want the error and the usage on stderr", stdout, stderr)
	}
}

func TestQueryWithTrustAnchorsPrintsVerifiedTrueForASignedAnswer(t *testing.T) { // FR-security-50
	addr := startSignedServer(t, signerOf(anchorSeed), protocol.Response{Payload: testPayload})
	code, stdout, stderr := queryCmd(append(keyArgs(addr), "--trust-anchors", writeAnchors(t, anchorSeed))...)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "\nverified: true\n") {
		t.Errorf("stdout = %q, want the line verified: true", stdout)
	}
}

func TestQueryWithAWrongTrustAnchorExitsThreeAndPrintsErrUnknownKey(t *testing.T) { // FR-security-50, FR-security-51
	addr := startSignedServer(t, signerOf(otherSeed), protocol.Response{Payload: testPayload})
	code, stdout, stderr := queryCmd(append(keyArgs(addr), "--trust-anchors", writeAnchors(t, anchorSeed))...)
	if code != 3 {
		t.Fatalf("exit code = %d, want 3; stderr = %q", code, stderr)
	}
	if want := "error: verify: ErrUnknownKey: " + testInfo.ID + " " + testName + "\n"; stdout != "" || stderr != want {
		t.Errorf("stdout = %q, stderr = %q; want only %q on stderr", stdout, stderr, want)
	}
}

func TestQueryWithTrustAnchorsExitsThreeAndPrintsErrUnsignedForAnUnsignedAnswer(t *testing.T) { // FR-security-51
	addr := startServer(t)
	code, _, stderr := queryCmd(append(keyArgs(addr), "--trust-anchors", writeAnchors(t, anchorSeed))...)
	if code != 3 || !strings.Contains(stderr, "ErrUnsigned") {
		t.Errorf("exit code = %d, stderr = %q; want 3 and ErrUnsigned", code, stderr)
	}
}

func TestQueryWithAnUnreadableTrustAnchorFileExitsSixtyFourAndPrintsTheUsage(t *testing.T) { // FR-security-50
	missing := filepath.Join(t.TempDir(), "missing.json")
	code, stdout, stderr := queryCmd(append(keyArgs("127.0.0.1:5353"), "--trust-anchors", missing)...)
	if code != 64 {
		t.Fatalf("exit code = %d, want 64", code)
	}
	if stdout != "" || !strings.Contains(stderr, "--trust-anchors") || !strings.Contains(stderr, "Usage:") {
		t.Errorf("stdout = %q, stderr = %q; want an error that names --trust-anchors and the usage", stdout, stderr)
	}
}

func TestDiscoverWithTrustAnchorsNeedsNoInsecureAndPrintsVerifiedYes(t *testing.T) { // FR-security-50
	d := startSignedDiscoveryServer(t, signerOf(anchorSeed), discovery.DefaultKey, nil)
	code, stdout, stderr := discoverCmd("--server", d.addr, "--name", testZone, "--trust-anchors", writeAnchors(t, anchorSeed))
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr)
	}
	if !strings.HasPrefix(stdout, "zone "+testZone+"  framework 1  verified yes\n") {
		t.Errorf("stdout = %q, want the header with verified yes", stdout)
	}
}

func TestDiscoverWithAWrongTrustAnchorExitsThreeAndPrintsErrUnknownKey(t *testing.T) { // FR-security-51
	d := startSignedDiscoveryServer(t, signerOf(otherSeed), discovery.DefaultKey, nil)
	code, stdout, stderr := discoverCmd("--server", d.addr, "--name", testZone, "--trust-anchors", writeAnchors(t, anchorSeed))
	if code != 3 {
		t.Fatalf("exit code = %d, want 3; stderr = %q", code, stderr)
	}
	if want := "error: verify: ErrUnknownKey: " + discovery.ProtocolID + " _qmux." + testZone + "\n"; stdout != "" || stderr != want {
		t.Errorf("stdout = %q, stderr = %q; want only %q on stderr", stdout, stderr, want)
	}
}

func TestDiscoverWithAnUnreadableTrustAnchorFileExitsSixtyFour(t *testing.T) { // FR-security-50
	missing := filepath.Join(t.TempDir(), "missing.json")
	code, _, stderr := discoverCmd("--server", "127.0.0.1:5353", "--name", testZone, "--trust-anchors", missing)
	if code != 64 || !strings.Contains(stderr, "--trust-anchors") {
		t.Errorf("exit code = %d, stderr = %q; want 64 and an error that names --trust-anchors", code, stderr)
	}
}
