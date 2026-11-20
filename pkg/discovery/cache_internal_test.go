package discovery

import (
	"bytes"
	"os"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/security"
)

const internalZone = "fleet.example."

// signedFile returns a cache file for internalZone whose envelope holds a manifest
// with one entry of the protocol ID id, issued at issuedAt for one hour. store does
// not verify, so the signature bytes are not a real signature.
func signedFile(t *testing.T, id string, issuedAt time.Time) (*Manifest, cacheFile) {
	t.Helper()
	m := &Manifest{FrameworkVersion: frameworkVersion, Zone: internalZone,
		Protocols: []Entry{{ID: id, Versions: []uint16{1}, Class: 65281, Type: 65280}}}
	payload, err := codec.Marshal(m)
	if err != nil {
		t.Fatalf("codec.Marshal(manifest) error = %v", err)
	}
	raw, err := codec.Encode(codec.Envelope{Format: 1, ProtocolID: ProtocolID, Version: 1,
		MessageType: uint64(protocol.MessageManifest), Payload: payload,
		Signature: &codec.SignatureBlock{KeyID: make([]byte, 8), Alg: -8,
			IssuedAt: uint64(issuedAt.Unix()), ExpiresAt: uint64(issuedAt.Add(time.Hour).Unix()),
			Signature: make([]byte, 64)}}, codec.Limits{})
	if err != nil {
		t.Fatalf("codec.Encode() error = %v", err)
	}
	return m, cacheFile{Envelope: raw, QName: "_qmux." + internalZone, Class: DefaultKey.Class, Type: DefaultKey.Type}
}

func TestAManifestReplacesTheCachedManifestOnlyWhenItsIssuedAtIsNewer(t *testing.T) { // FR-security-45
	now := time.Unix(1_800_000_000, 0)
	v := &security.Verifier{Now: func() time.Time { return now }}
	for _, tc := range []struct {
		name    string
		second  time.Time
		replace bool
	}{
		{name: "older issued_at", second: now.Add(-time.Minute), replace: false},
		{name: "equal issued_at", second: now, replace: false},
		{name: "newer issued_at", second: now.Add(time.Minute), replace: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Client{CacheDir: t.TempDir()}
			firstManifest, firstFile := signedFile(t, "org.qmux.first", now)
			secondManifest, secondFile := signedFile(t, "org.qmux.second", tc.second)
			if err := c.store(v, internalZone, firstManifest, firstFile); err != nil {
				t.Fatalf("store(first) error = %v", err)
			}
			if err := c.store(v, internalZone, secondManifest, secondFile); err != nil {
				t.Fatalf("store(second) error = %v", err)
			}

			wantID, wantFile := "org.qmux.first", firstFile
			if tc.replace {
				wantID, wantFile = "org.qmux.second", secondFile
			}
			if got := c.cache[internalZone].manifest.Protocols[0].ID; got != wantID {
				t.Errorf("memory cache holds %q, want %q", got, wantID)
			}
			b, err := os.ReadFile(c.cachePath(internalZone))
			if err != nil {
				t.Fatalf("read cache file: %v", err)
			}
			want, err := codec.Marshal(wantFile)
			if err != nil {
				t.Fatalf("codec.Marshal(cache file) error = %v", err)
			}
			if !bytes.Equal(b, want) {
				t.Errorf("cache file does not hold the manifest %q", wantID)
			}
		})
	}
}

func TestAnOlderManifestReplacesAnExpiredCachedManifest(t *testing.T) { // FR-security-46
	issued := time.Unix(1_800_000_000, 0)
	now := issued.Add(2 * time.Hour)
	v := &security.Verifier{Now: func() time.Time { return now }}
	c := &Client{}
	firstManifest, firstFile := signedFile(t, "org.qmux.first", issued)
	secondManifest, secondFile := signedFile(t, "org.qmux.second", issued.Add(-time.Minute))
	if err := c.store(v, internalZone, firstManifest, firstFile); err != nil {
		t.Fatalf("store(first) error = %v", err)
	}
	if err := c.store(v, internalZone, secondManifest, secondFile); err != nil {
		t.Fatalf("store(second) error = %v", err)
	}

	if got := c.cache[internalZone].manifest.Protocols[0].ID; got != "org.qmux.second" {
		t.Errorf("memory cache holds %q, want org.qmux.second", got)
	}
}

func TestCachePathIsEmptyForAZoneWithAPathSeparator(t *testing.T) { // FR-security-43
	c := &Client{CacheDir: t.TempDir()}
	for _, zone := range []string{"a/b.example.", `a\b.example.`} {
		if got := c.cachePath(zone); got != "" {
			t.Errorf("cachePath(%q) = %q, want \"\"", zone, got)
		}
	}
}
