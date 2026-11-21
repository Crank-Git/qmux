package discovery_test

import (
	"errors"
	"os"
	"slices"
	"sync"
	"testing"

	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/discovery"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/security"
	"github.com/Crank-Git/qmux/pkg/transport"
	"github.com/Crank-Git/qmux/pkg/transport/transporttest"
)

const firmwareID = "org.qmux.firmware"

// remappedKey is the protocol key of the firmware entry after the remap.
var remappedKey = protocol.ProtocolKey{Class: 65282, Type: 65280}

// changingServer is a signing DNS server whose registrations a test can change between
// queries. It answers the discovery key with the manifest of its registrations. It
// answers the key of a registration with an envelope of that protocol ID. It answers
// any other key with REFUSED, as the server does after a remap.
type changingServer struct {
	signer *security.Signer

	mu        sync.Mutex
	entries   []discovery.Entry
	status    protocol.Status
	protoID   string
	discovers int
	errs      []error
}

// newChangingServer returns a server with the registrations of testEntries.
func newChangingServer(clk *testClock) *changingServer {
	return &changingServer{signer: &security.Signer{Key: signingKey, Now: clk.now}, entries: testEntries()}
}

// setFirmware replaces the firmware registration with the versions at the key.
func (s *changingServer) setFirmware(versions []uint16, key protocol.ProtocolKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[1] = discovery.Entry{ID: firmwareID, Versions: versions, Class: key.Class, Type: key.Type}
}

// setAnswer makes each answer at a registration carry the status and the protocol ID.
// An empty protocol ID means the protocol ID of the registration.
func (s *changingServer) setAnswer(status protocol.Status, protoID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.protoID = status, protoID
}

// discoveries returns the number of queries at the discovery key. It fails the test
// when s could not answer a query.
func (s *changingServer) discoveries(t *testing.T) int {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, err := range s.errs {
		t.Errorf("changing server: %v", err)
	}
	return s.discovers
}

func (s *changingServer) respond(_ string, wire []byte) [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp, err := s.answer(wire)
	if err != nil {
		s.errs = append(s.errs, err)
		return nil
	}
	return [][]byte{resp}
}

// answer returns the response to the query wire. The caller holds s.mu.
func (s *changingServer) answer(wire []byte) ([]byte, error) {
	q, err := transporttest.Unpack(wire)
	if err != nil {
		return nil, err
	}
	question := q.Question[0]
	msg := transporttest.Message{ID: q.ID, Response: true, Authoritative: true, Question: q.Question}
	var e codec.Envelope
	switch i := slices.IndexFunc(s.entries, func(e discovery.Entry) bool {
		return protocol.ProtocolKey{Class: e.Class, Type: e.Type} == question.Key
	}); {
	case question.Key == discovery.DefaultKey:
		s.discovers++
		m := discovery.Manifest{FrameworkVersion: 1, Zone: testZone, Protocols: slices.Clone(s.entries)}
		payload, err := codec.Marshal(m)
		if err != nil {
			return nil, err
		}
		e = codec.Envelope{Format: 1, ProtocolID: discovery.ProtocolID, Version: 1,
			MessageType: uint64(protocol.MessageManifest), Payload: payload}
	case i >= 0:
		r := s.entries[i]
		id := r.ID
		if s.protoID != "" {
			id = s.protoID
		}
		// The server reads the version label, so the envelope echoes the version of the
		// query. A version-unsupported envelope carries the highest version instead.
		v := uint64(r.Versions[len(r.Versions)-1])
		if s.status == protocol.StatusOK {
			v = uint64(versionLabel(question.Name))
		}
		e = codec.Envelope{Format: 1, ProtocolID: id, Version: v,
			MessageType: uint64(protocol.MessageRecord), Status: uint64(s.status)}
	default:
		msg.RCode = transport.RCodeRefused
		return transporttest.Pack(msg)
	}
	if err := s.signer.Sign(&e, question.Name, question.Key); err != nil {
		return nil, err
	}
	rdata, err := codec.Encode(e, codec.Limits{})
	if err != nil {
		return nil, err
	}
	msg.Answer = []transporttest.RR{{Name: question.Name, Key: question.Key, TTL: 60, Data: rdata}}
	return transporttest.Pack(msg)
}

// versionLabel returns N of the leading "_v<N>." label of name, or 0 when the label is
// absent. The tests send only one-digit versions.
func versionLabel(name string) uint16 {
	if len(name) < 4 || name[:2] != "_v" || name[3] != '.' {
		return 0
	}
	return uint16(name[2] - '0')
}

// start returns a discovery client with CacheDir dir, whose client trusts signingKey.
func (s *changingServer) start(t *testing.T, clk *testClock, dir string) *discovery.Client {
	t.Helper()
	addr := transporttest.StartServer(t, s.respond)
	return &discovery.Client{Client: client.NewClient(client.ClientConfig{Resolver: addr, TrustAnchors: newTestVerifier(clk)}), CacheDir: dir}
}

// queryFirmware calls QueryProtocol for the firmware protocol with the versions.
func queryFirmware(t *testing.T, c *discovery.Client, m *discovery.Manifest, versions ...uint16) (*client.Result, error) {
	t.Helper()
	return c.QueryProtocol(testContext(t), m, firmwareID, "device42.fleet.example.", versions)
}

// firmwareEntry returns the firmware entry of m, and fails the test when m has none.
func firmwareEntry(t *testing.T, m *discovery.Manifest) discovery.Entry {
	t.Helper()
	e, ok := m.Lookup(firmwareID)
	if !ok {
		t.Fatalf("manifest = %+v, want the firmware entry", m)
	}
	return e
}

// Both a memory cache and a CacheDir must give the same result, so each test runs both.
var cacheDirs = map[string]bool{"memory only": false, "with CacheDir": true}

func cacheDir(t *testing.T, onDisk bool) string {
	t.Helper()
	if onDisk {
		return t.TempDir()
	}
	return ""
}

func TestDiscoverReturnsTheNewVersionsAfterAVersionUnsupportedAnswer(t *testing.T) { // FR-discovery-19, FR-security-42
	for name, onDisk := range cacheDirs {
		t.Run(name, func(t *testing.T) {
			clk := newTestClock()
			s := newChangingServer(clk)
			s.setFirmware([]uint16{1}, firmwareKey)
			c := s.start(t, clk, cacheDir(t, onDisk))
			m := discover(t, c)

			s.setFirmware([]uint16{2}, firmwareKey)
			s.setAnswer(protocol.StatusVersionUnsupported, "")
			_, err := queryFirmware(t, c, m, 1)
			var ae *client.AppError
			if !errors.As(err, &ae) || ae.Status != protocol.StatusVersionUnsupported {
				t.Fatalf("QueryProtocol() error = %v, want an AppError with status version-unsupported", err)
			}

			got := firmwareEntry(t, discover(t, c))
			if !slices.Equal(got.Versions, []uint16{2}) {
				t.Fatalf("firmware versions after rediscovery = %v, want [2]", got.Versions)
			}
			if n := s.discoveries(t); n != 2 {
				t.Fatalf("server received %d discovery queries, want 2", n)
			}
		})
	}
}

func TestDiscoverReturnsTheNewKeyAfterARefusedAnswer(t *testing.T) { // FR-discovery-19, FR-security-42
	for name, onDisk := range cacheDirs {
		t.Run(name, func(t *testing.T) {
			clk := newTestClock()
			s := newChangingServer(clk)
			c := s.start(t, clk, cacheDir(t, onDisk))
			m := discover(t, c)

			s.setFirmware([]uint16{1, 2}, remappedKey)
			_, err := queryFirmware(t, c, m, 2)
			var de *client.DNSError
			if !errors.As(err, &de) || de.Kind != client.KindRCode || de.RCode != int(transport.RCodeRefused) {
				t.Fatalf("QueryProtocol() error = %v, want a DNSError with the RCODE REFUSED", err)
			}

			got := firmwareEntry(t, discover(t, c))
			if key := (protocol.ProtocolKey{Class: got.Class, Type: got.Type}); key != remappedKey {
				t.Fatalf("firmware key after rediscovery = %s, want %s", key, remappedKey)
			}
		})
	}
}

func TestDiscoverSendsAQueryAfterAProtocolIDMismatch(t *testing.T) { // FR-discovery-31, FR-security-42
	for name, onDisk := range cacheDirs {
		t.Run(name, func(t *testing.T) {
			clk := newTestClock()
			s := newChangingServer(clk)
			c := s.start(t, clk, cacheDir(t, onDisk))
			m := discover(t, c)

			s.setAnswer(protocol.StatusOK, "org.qmux.other")
			_, err := queryFirmware(t, c, m, 2)
			var de *client.DNSError
			if !errors.As(err, &de) || de.Kind != client.KindMismatch {
				t.Fatalf("QueryProtocol() error = %v, want a DNSError of kind %s", err, client.KindMismatch)
			}

			discover(t, c)
			if n := s.discoveries(t); n != 2 {
				t.Fatalf("server received %d discovery queries, want 2", n)
			}
		})
	}
}

func TestQueryProtocolKeepsTheCachedManifestForAnAnswerThatDoesNotShowAChange(t *testing.T) { // FR-security-42
	for name, status := range map[string]protocol.Status{
		"ok":        protocol.StatusOK,
		"not-found": protocol.StatusNotFound,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			clk := newTestClock()
			s := newChangingServer(clk)
			c := s.start(t, clk, dir)
			m := discover(t, c)

			s.setAnswer(status, "")
			res, err := queryFirmware(t, c, m, 2)
			if res == nil || res.Status != status {
				t.Fatalf("QueryProtocol() = %+v, %v, want a result with status %d", res, err, status)
			}

			discover(t, c)
			if n := s.discoveries(t); n != 1 {
				t.Fatalf("server received %d discovery queries, want 1", n)
			}
			if _, err := os.Stat(cachePath(dir)); err != nil {
				t.Fatalf("cache file: %v", err)
			}
		})
	}
}

func TestForgetMakesTheNextDiscoverSendAQuery(t *testing.T) { // FR-security-42, FR-security-44
	for name, onDisk := range cacheDirs {
		t.Run(name, func(t *testing.T) {
			dir := cacheDir(t, onDisk)
			clk := newTestClock()
			s := newChangingServer(clk)
			c := s.start(t, clk, dir)
			discover(t, c)

			// Forget folds the zone and adds the root label, as Discover does.
			c.Forget("FLEET.example")
			discover(t, c)

			if n := s.discoveries(t); n != 2 {
				t.Fatalf("server received %d discovery queries, want 2", n)
			}
		})
	}
}

func TestForgetRemovesTheCacheFile(t *testing.T) { // FR-security-43, FR-security-44
	dir := t.TempDir()
	clk := newTestClock()
	s := newChangingServer(clk)
	c := s.start(t, clk, dir)
	discover(t, c)

	c.Forget(testZone)

	if _, err := os.Stat(cachePath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cache file after Forget: Stat() error = %v, want os.ErrNotExist", err)
	}
	// A new client with the same CacheDir, as after a restart, must also send a query.
	next := &discovery.Client{Client: c.Client, CacheDir: dir}
	discover(t, next)
	if n := s.discoveries(t); n != 2 {
		t.Fatalf("server received %d discovery queries, want 2", n)
	}
}

func TestForgetMakesTheNextDiscoverSendAQueryWhenTheCacheFileStays(t *testing.T) { // FR-security-44
	if os.Geteuid() == 0 {
		t.Skip("root can remove a file from a read-only directory")
	}
	dir := t.TempDir()
	clk := newTestClock()
	s := newChangingServer(clk)
	c := s.start(t, clk, dir)
	discover(t, c)

	// A read-only directory makes the remove of the cache file fail.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod CacheDir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	c.Forget(testZone)
	if _, err := os.Stat(cachePath(dir)); err != nil {
		t.Fatalf("cache file after a failed remove: %v", err)
	}
	discover(t, c)

	if n := s.discoveries(t); n != 2 {
		t.Fatalf("server received %d discovery queries, want 2", n)
	}
}

func TestForgetOfAZoneWithNoCachedManifestDoesNothing(t *testing.T) { // FR-security-42
	dir := t.TempDir()
	clk := newTestClock()
	s := newChangingServer(clk)
	c := s.start(t, clk, dir)
	discover(t, c)

	c.Forget("other.example.")
	discover(t, c)

	if n := s.discoveries(t); n != 1 {
		t.Fatalf("server received %d discovery queries, want 1", n)
	}
}
