package discovery

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/security"
)

// cacheFile is the CBOR content of a cache file. It holds the raw envelope with the
// QNAME and the protocol key that its signature binds.
type cacheFile struct {
	Envelope []byte `cbor:"1,keyasint"`
	QName    string `cbor:"2,keyasint"`
	Class    uint16 `cbor:"3,keyasint"`
	Type     uint16 `cbor:"4,keyasint"`
}

// cached is one verified manifest in the memory cache of a Client.
type cached struct {
	manifest  *Manifest
	issuedAt  uint64
	expiresAt time.Time
}

// newCached returns the memory cache value for the manifest m of the verified envelope e.
func newCached(m *Manifest, e codec.Envelope) cached {
	return cached{manifest: m, issuedAt: e.Signature.IssuedAt,
		expiresAt: time.Unix(int64(min(e.Signature.ExpiresAt, maxUnixSeconds)), 0)}
}

// maxUnixSeconds keeps an expires_at inside the range of time.Time. It is far later
// than any clock, so the expiry comparison keeps its result.
const maxUnixSeconds = 1 << 40

// lookup returns a copy of the manifest of zone from the memory cache, or from the
// cache file when the memory cache has no manifest. The bool is false when neither
// holds a manifest that verifies and has not expired at the time of v.
func (c *Client) lookup(v *security.Verifier, zone string, f cacheFile) (*Manifest, bool) {
	now := clock(v)
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.cache[zone]; ok && now.Before(e.expiresAt) {
		return e.manifest.clone(), true
	}
	e, ok := load(v, c.cachePath(zone), f, zone)
	if !ok || !now.Before(e.expiresAt) {
		return nil, false
	}
	c.set(zone, e)
	return e.manifest.clone(), true
}

// load returns the verified manifest of the cache file at path. The bool is false
// when the file is absent, does not decode, names another QNAME or protocol key, or
// fails verification. Discover then sends a query, so load drops the cause.
func load(v *security.Verifier, path string, want cacheFile, zone string) (cached, bool) {
	if path == "" {
		return cached{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return cached{}, false
	}
	var f cacheFile
	if err := codec.Unmarshal(b, &f); err != nil || f.QName != want.QName || f.Class != want.Class || f.Type != want.Type {
		return cached{}, false
	}
	e, err := v.Verify(f.Envelope, f.QName, protocol.ProtocolKey{Class: f.Class, Type: f.Type})
	if err != nil || e.ProtocolID != ProtocolID || e.Status != uint64(protocol.StatusOK) {
		return cached{}, false
	}
	m, err := decodeManifest(protocol.MessageType(e.MessageType), e.Payload, zone)
	if err != nil {
		return cached{}, false
	}
	return newCached(m, e), true
}

// store keeps the manifest m of the verified file f in the memory cache and in the
// cache file of zone. It keeps the cached manifest when m is not newer. store returns
// an error when the cache file write fails.
func (c *Client) store(v *security.Verifier, zone string, m *Manifest, f cacheFile) error {
	// The client verified f.Envelope, so the envelope decodes and has a signature block.
	e, err := codec.Decode(f.Envelope, codec.Limits{})
	if err != nil {
		return fmt.Errorf("discovery: cache %s: %w", zone, err)
	}
	n := newCached(m.clone(), e)
	c.mu.Lock()
	defer c.mu.Unlock()
	// An expired manifest is no longer a cached manifest, so any manifest replaces it.
	if old, ok := c.cache[zone]; ok && clock(v).Before(old.expiresAt) && n.issuedAt <= old.issuedAt {
		return nil
	}
	c.set(zone, n)
	path := c.cachePath(zone)
	if path == "" {
		return nil
	}
	b, err := codec.Marshal(f)
	if err != nil {
		return fmt.Errorf("discovery: cache %s: %w", zone, err)
	}
	// A partial file fails to decode or to verify at the next load, so a plain write is
	// enough.
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("discovery: cache %s: %w", zone, err)
	}
	return nil
}

// set puts e in the memory cache. The caller holds c.mu.
func (c *Client) set(zone string, e cached) {
	if c.cache == nil {
		c.cache = make(map[string]cached)
	}
	c.cache[zone] = e
}

// cachePath returns "<CacheDir>/<zone>.qmuxcache" for the fully qualified zone. It
// returns "" when CacheDir is empty, or when the zone holds a path separator and so
// names no single file in CacheDir.
func (c *Client) cachePath(zone string) string {
	if c.CacheDir == "" || strings.ContainsAny(zone, `/\`) {
		return ""
	}
	return filepath.Join(c.CacheDir, strings.TrimSuffix(zone, ".")+".qmuxcache")
}

// clock returns the current time of v, so the cache and Verify use one clock.
func clock(v *security.Verifier) time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

// clone returns a deep copy of m, so a caller cannot change a cached manifest.
func (m *Manifest) clone() *Manifest {
	n := *m
	n.Protocols = slices.Clone(m.Protocols)
	for i := range n.Protocols {
		n.Protocols[i].Versions = slices.Clone(m.Protocols[i].Versions)
	}
	return &n
}
