package discovery_test

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/discovery"
	"github.com/Crank-Git/qmux/pkg/security"
	"github.com/Crank-Git/qmux/pkg/transport/transporttest"
)

// testClock is the clock of the test signer and of the test verifier. It is atomic,
// because the goroutine of the fake server reads it while the test changes it.
type testClock struct{ unix atomic.Int64 }

func newTestClock() *testClock {
	c := &testClock{}
	c.unix.Store(signTime.Unix())
	return c
}

func (c *testClock) now() time.Time { return time.Unix(c.unix.Load(), 0) }

func (c *testClock) add(d time.Duration) { c.unix.Add(int64(d / time.Second)) }

// startCached starts s with a signer and returns a discovery client with CacheDir dir,
// whose client trusts signingKey. The signer and the verifier use clk. The signer
// gives each envelope a validity window of one hour.
func startCached(t *testing.T, s *fakeServer, clk *testClock, dir string) *discovery.Client {
	t.Helper()
	s.signer = &security.Signer{Key: signingKey, Now: clk.now}
	addr := transporttest.StartServer(t, s.respond)
	return &discovery.Client{Client: client.NewClient(client.ClientConfig{Resolver: addr, TrustAnchors: newTestVerifier(clk)}), CacheDir: dir}
}

// newTestVerifier returns a verifier with its own freshness store that trusts
// signingKey.
func newTestVerifier(clk *testClock) *security.Verifier {
	pub := signingKey.Public().(ed25519.PublicKey)
	return &security.Verifier{
		Anchors: []security.TrustAnchor{{KeyID: security.KeyID(pub), PublicKey: pub}},
		Now:     clk.now,
	}
}

// cachePath is the cache file of testZone in dir.
func cachePath(dir string) string {
	return filepath.Join(dir, "fleet.example.qmuxcache")
}

// discover calls c.Discover for testZone and fails the test on an error, or when the
// manifest has no firmware entry.
func discover(t *testing.T, c *discovery.Client) *discovery.Manifest {
	t.Helper()
	m, err := c.Discover(testContext(t), testZone)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if _, ok := m.Lookup("org.qmux.firmware"); !ok {
		t.Fatalf("manifest = %+v, want the firmware entry", m)
	}
	return m
}

func wantQueries(t *testing.T, s *fakeServer, want int) {
	t.Helper()
	if got := s.received(t); len(got) != want {
		t.Fatalf("server received %d queries, want %d", len(got), want)
	}
}

func TestDiscoverReturnsTheCachedManifestWithNoQueryBeforeExpiresAt(t *testing.T) { // FR-security-41, FR-security-42
	clk := newTestClock()
	s := manifestServer(t, testManifest())
	c := startCached(t, s, clk, "")

	discover(t, c)
	clk.add(59 * time.Minute)
	discover(t, c)

	wantQueries(t, s, 1)
}

func TestDiscoverSendsAQueryAtExpiresAt(t *testing.T) { // FR-security-42, FR-security-46
	clk := newTestClock()
	s := manifestServer(t, testManifest())
	c := startCached(t, s, clk, "")

	discover(t, c)
	clk.add(time.Hour)
	discover(t, c)

	wantQueries(t, s, 2)
}

func TestDiscoverKeepsAManifestWithATTLOf1sCachedUntilExpiresAt(t *testing.T) { // FR-security-46
	clk := newTestClock()
	s := manifestServer(t, testManifest())
	s.ttl = 1
	c := startCached(t, s, clk, "")

	discover(t, c)
	clk.add(30 * time.Minute)
	discover(t, c)

	wantQueries(t, s, 1)
}

func TestDiscoverReturnsACopyOfTheCachedManifest(t *testing.T) { // FR-security-42
	s := manifestServer(t, testManifest())
	c := startCached(t, s, newTestClock(), "")

	m := discover(t, c)
	m.Protocols[1].ID = "org.qmux.changed"
	m.Protocols[1].Versions[0] = 9
	got := discover(t, c)

	if e, ok := got.Lookup("org.qmux.firmware"); !ok || e.Versions[0] != 1 {
		t.Fatalf("second Discover() = %+v, want the unchanged firmware entry", got)
	}
}

func TestDiscoverWritesTheCacheFileInCacheDir(t *testing.T) { // FR-security-43
	dir := t.TempDir()
	c := startCached(t, manifestServer(t, testManifest()), newTestClock(), dir)

	discover(t, c)

	if _, err := os.Stat(cachePath(dir)); err != nil {
		t.Fatalf("cache file: %v", err)
	}
}

func TestANewClientWithTheSameCacheDirReturnsTheManifestWithNoQuery(t *testing.T) { // FR-security-44
	dir := t.TempDir()
	clk := newTestClock()
	s := manifestServer(t, testManifest())
	first := startCached(t, s, clk, dir)
	discover(t, first)

	// The same verifier already holds the issued_at of the cache file in its freshness
	// store. A new verifier has an empty freshness store, as after a restart.
	for name, v := range map[string]*security.Verifier{
		"same verifier": first.Client.TrustAnchors(),
		"new verifier":  newTestVerifier(clk),
	} {
		c := &discovery.Client{Client: client.NewClient(client.ClientConfig{Resolver: "127.0.0.1:9", TrustAnchors: v}), CacheDir: dir}
		if _, err := c.Discover(testContext(t), testZone); err != nil {
			t.Fatalf("%s: Discover() error = %v", name, err)
		}
	}

	wantQueries(t, s, 1)
}

func TestDiscoverSendsAQueryForATamperedCacheFile(t *testing.T) { // FR-security-44
	dir := t.TempDir()
	s := manifestServer(t, testManifest())
	first := startCached(t, s, newTestClock(), dir)
	discover(t, first)

	b, err := os.ReadFile(cachePath(dir))
	if err != nil {
		t.Fatalf("read cache file: %v", err)
	}
	tampered := bytes.Replace(b, []byte("org.qmux.firmware"), []byte("org.qmux.firmwarf"), 1)
	if bytes.Equal(tampered, b) {
		t.Fatal("the cache file does not hold the firmware protocol ID")
	}
	if err := os.WriteFile(cachePath(dir), tampered, 0o600); err != nil {
		t.Fatalf("write cache file: %v", err)
	}
	second := &discovery.Client{Client: first.Client, CacheDir: dir}
	discover(t, second)

	wantQueries(t, s, 2)
}

func TestDiscoverSendsAQueryForAnOlderCacheFile(t *testing.T) { // FR-security-44
	oldDir, dir := t.TempDir(), t.TempDir()
	clk := newTestClock()
	s := manifestServer(t, testManifest())
	first := startCached(t, s, clk, oldDir)
	discover(t, first)
	old, err := os.ReadFile(cachePath(oldDir))
	if err != nil {
		t.Fatalf("read cache file: %v", err)
	}
	clk.add(10 * time.Minute)
	discover(t, &discovery.Client{Client: first.Client, CacheDir: dir})

	// The freshness store holds the newer issued_at, so the older file gives ErrReplay.
	if err := os.WriteFile(cachePath(dir), old, 0o600); err != nil {
		t.Fatalf("write cache file: %v", err)
	}
	discover(t, &discovery.Client{Client: first.Client, CacheDir: dir})

	wantQueries(t, s, 3)
}

func TestAnInsecureDiscoverWritesNoCacheFile(t *testing.T) { // FR-security-47
	dir := t.TempDir()
	s := manifestServer(t, testManifest())
	c := s.start(t, true)
	c.CacheDir = dir

	discover(t, c)
	discover(t, c)

	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read CacheDir: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("CacheDir holds %d files, want 0", len(files))
	}
	wantQueries(t, s, 2)
}
