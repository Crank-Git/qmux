package discovery_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/discovery"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/transport/transporttest"
)

const testZone = "fleet.example."

// firmwareKey is the protocol key of the firmware entry in testEntries.
var firmwareKey = protocol.ProtocolKey{Class: 65281, Type: 65280}

// testEntries returns a valid entry list for testZone. Each call returns a new slice,
// so that a test can change it.
func testEntries() []discovery.Entry {
	return []discovery.Entry{
		{ID: discovery.ProtocolID, Versions: []uint16{1}, Class: 65280, Type: 65534},
		{ID: "org.qmux.firmware", Versions: []uint16{1, 2}, Class: firmwareKey.Class, Type: firmwareKey.Type},
	}
}

// fakeServer is a DNS server that records each query and answers it with one envelope.
type fakeServer struct {
	envelope codec.Envelope

	mu      sync.Mutex
	queries []transporttest.RR
	errs    []error
}

func (s *fakeServer) respond(_ string, wire []byte) [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, err := transporttest.Unpack(wire)
	if err != nil {
		s.errs = append(s.errs, err)
		return nil
	}
	s.queries = append(s.queries, q.Question[0])
	rdata, err := codec.Encode(s.envelope, codec.Limits{})
	if err != nil {
		s.errs = append(s.errs, err)
		return nil
	}
	resp, err := transporttest.Pack(transporttest.Message{
		ID: q.ID, Response: true, Authoritative: true, Question: q.Question,
		Answer: []transporttest.RR{{Name: q.Question[0].Name, Key: q.Question[0].Key, TTL: 60, Data: rdata}},
	})
	if err != nil {
		s.errs = append(s.errs, err)
		return nil
	}
	return [][]byte{resp}
}

// received returns the question of each query that s received. It fails the test when
// s could not read or answer a query.
func (s *fakeServer) received(t *testing.T) []transporttest.RR {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, err := range s.errs {
		t.Errorf("fake server: %v", err)
	}
	return append([]transporttest.RR(nil), s.queries...)
}

// start starts s and returns a discovery client that sends every query to s.
func (s *fakeServer) start(t *testing.T, insecure bool) *discovery.Client {
	t.Helper()
	addr := transporttest.StartServer(t, s.respond)
	return &discovery.Client{
		Client:             client.NewClient(client.ClientConfig{Resolver: addr}),
		InsecureSkipVerify: insecure,
	}
}

// manifestServer returns a fake server that answers with the manifest m.
func manifestServer(t *testing.T, m discovery.Manifest) *fakeServer {
	t.Helper()
	payload, err := codec.Marshal(m)
	if err != nil {
		t.Fatalf("codec.Marshal(manifest) error = %v", err)
	}
	return &fakeServer{envelope: codec.Envelope{Format: 1, ProtocolID: discovery.ProtocolID, Version: 1,
		MessageType: uint64(protocol.MessageManifest), Payload: payload}}
}

func testManifest() discovery.Manifest {
	return discovery.Manifest{FrameworkVersion: 1, Zone: testZone, Protocols: testEntries()}
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestDiscoverWithAZeroKeySendsOneQueryAtTheDefaultKey(t *testing.T) { // FR-discovery-23, FR-discovery-24
	s := manifestServer(t, testManifest())
	c := s.start(t, true)

	m, err := c.Discover(testContext(t), testZone)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	got := s.received(t)
	if len(got) != 1 {
		t.Fatalf("server received %d queries, want 1", len(got))
	}
	want := protocol.ProtocolKey{Class: 65280, Type: 65534}
	if got[0].Key != want || discovery.DefaultKey != want {
		t.Fatalf("query key = %s, DefaultKey = %s, want %s", got[0].Key, discovery.DefaultKey, want)
	}
	if got[0].Name != "_qmux.fleet.example." {
		t.Fatalf("query name = %q, want %q", got[0].Name, "_qmux.fleet.example.")
	}
	if e, ok := m.Lookup("org.qmux.firmware"); !ok || e.Class != firmwareKey.Class {
		t.Fatalf("manifest Lookup(org.qmux.firmware) = %+v, %t, want the firmware entry", e, ok)
	}
}

func TestDiscoverSendsTheQueryAtTheKeyOfTheClient(t *testing.T) { // FR-discovery-23
	s := manifestServer(t, testManifest())
	c := s.start(t, true)
	c.Key = discovery.INKey

	if _, err := c.Discover(testContext(t), testZone); err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if got := s.received(t); len(got) != 1 || got[0].Key != discovery.INKey {
		t.Fatalf("server received %+v, want one query at %s", got, discovery.INKey)
	}
}

func TestDiscoverSendsTheDiscoveryNameInLowercase(t *testing.T) { // FR-discovery-24
	s := manifestServer(t, testManifest())
	c := s.start(t, true)

	if _, err := c.Discover(testContext(t), "FLEET.Example"); err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if got := s.received(t); len(got) != 1 || got[0].Name != "_qmux.fleet.example." {
		t.Fatalf("server received %+v, want one query for _qmux.fleet.example.", got)
	}
}

func TestDiscoverReturnsAnErrorForARecordEnvelope(t *testing.T) { // FR-discovery-25
	s := manifestServer(t, testManifest())
	s.envelope.MessageType = uint64(protocol.MessageRecord)
	c := s.start(t, true)

	m, err := c.Discover(testContext(t), testZone)
	if err == nil {
		t.Fatalf("Discover() = %+v, want an error", m)
	}
}

func TestDiscoverReturnsAnErrorForAnotherProtocolID(t *testing.T) { // FR-discovery-25
	s := manifestServer(t, testManifest())
	s.envelope.ProtocolID = "org.qmux.firmware"
	c := s.start(t, true)

	m, err := c.Discover(testContext(t), testZone)
	if err == nil {
		t.Fatalf("Discover() = %+v, want an error", m)
	}
}

func TestDiscoverReturnsErrInvalidManifestForAnotherZone(t *testing.T) { // FR-discovery-24
	m := testManifest()
	m.Zone = "other.example."
	c := manifestServer(t, m).start(t, true)

	if _, err := c.Discover(testContext(t), testZone); !errors.Is(err, discovery.ErrInvalidManifest) {
		t.Fatalf("Discover() error = %v, want ErrInvalidManifest", err)
	}
}

func TestDiscoverReturnsErrInvalidManifestForAPayloadThatIsNotAManifest(t *testing.T) { // FR-discovery-24
	s := manifestServer(t, testManifest())
	s.envelope.Payload = []byte{0xff}
	c := s.start(t, true)

	if _, err := c.Discover(testContext(t), testZone); !errors.Is(err, discovery.ErrInvalidManifest) {
		t.Fatalf("Discover() error = %v, want ErrInvalidManifest", err)
	}
}

func TestDiscoverReturnsTheAppErrorForAStatusOtherThanOK(t *testing.T) { // FR-discovery-24
	s := manifestServer(t, testManifest())
	s.envelope.Status = uint64(protocol.StatusNotFound)
	c := s.start(t, true)

	m, err := c.Discover(testContext(t), testZone)
	var ae *client.AppError
	if !errors.As(err, &ae) || m != nil {
		t.Fatalf("Discover() = %+v, %v, want nil and a *client.AppError", m, err)
	}
}

func TestDiscoverWithoutInsecureSkipVerifyReturnsErrInsecureAndSendsNoQuery(t *testing.T) { // FR-discovery-26
	s := manifestServer(t, testManifest())
	c := s.start(t, false)

	if _, err := c.Discover(testContext(t), testZone); !errors.Is(err, discovery.ErrInsecure) {
		t.Fatalf("Discover() error = %v, want ErrInsecure", err)
	}
	if got := s.received(t); len(got) != 0 {
		t.Fatalf("server received %d queries, want 0", len(got))
	}
}

func TestNegotiatePicksTheHighestCommonVersion(t *testing.T) { // FR-discovery-27
	m, err := discovery.Static(testZone, testEntries())
	if err != nil {
		t.Fatalf("Static() error = %v", err)
	}
	key, v, err := discovery.Negotiate(m, "org.qmux.firmware", []uint16{2, 3})
	if err != nil || key != firmwareKey || v != 2 {
		t.Fatalf("Negotiate() = %s, %d, %v, want %s, 2, nil", key, v, err, firmwareKey)
	}
}

func TestNegotiateReturnsErrProtocolNotFoundForAnUnknownProtocolID(t *testing.T) { // FR-discovery-28
	m, err := discovery.Static(testZone, testEntries())
	if err != nil {
		t.Fatalf("Static() error = %v", err)
	}
	if _, _, err := discovery.Negotiate(m, "x.y", []uint16{1}); !errors.Is(err, discovery.ErrProtocolNotFound) {
		t.Fatalf("Negotiate() error = %v, want ErrProtocolNotFound", err)
	}
}

func TestNegotiateReturnsErrVersionUnsupportedWhenNoCommonVersionExists(t *testing.T) { // FR-discovery-29
	m, err := discovery.Static(testZone, testEntries())
	if err != nil {
		t.Fatalf("Static() error = %v", err)
	}
	if _, _, err := discovery.Negotiate(m, "org.qmux.firmware", []uint16{3}); !errors.Is(err, discovery.ErrVersionUnsupported) {
		t.Fatalf("Negotiate() error = %v, want ErrVersionUnsupported", err)
	}
}

func TestQueryProtocolSendsNoQueryWhenTheNegotiationFails(t *testing.T) { // FR-discovery-30
	m, err := discovery.Static(testZone, testEntries())
	if err != nil {
		t.Fatalf("Static() error = %v", err)
	}
	s := &fakeServer{envelope: codec.Envelope{Format: 1, ProtocolID: "org.qmux.firmware", Version: 1,
		MessageType: uint64(protocol.MessageRecord)}}
	c := s.start(t, true)

	for name, tc := range map[string]struct {
		id        string
		supported []uint16
		want      error
	}{
		"unknown protocol ID": {"x.y", []uint16{1}, discovery.ErrProtocolNotFound},
		"no common version":   {"org.qmux.firmware", []uint16{3}, discovery.ErrVersionUnsupported},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := c.QueryProtocol(testContext(t), m, tc.id, "device42.fleet.example.", tc.supported)
			if !errors.Is(err, tc.want) || res != nil {
				t.Fatalf("QueryProtocol() = %+v, %v, want nil and %v", res, err, tc.want)
			}
		})
	}
	if got := s.received(t); len(got) != 0 {
		t.Fatalf("server received %d queries, want 0", len(got))
	}
}

func TestQueryProtocolSendsTheVersionLabelAtTheNegotiatedKey(t *testing.T) { // FR-discovery-31
	entries := testEntries()
	entries[1].Versions = []uint16{1}
	m, err := discovery.Static(testZone, entries)
	if err != nil {
		t.Fatalf("Static() error = %v", err)
	}
	s := &fakeServer{envelope: codec.Envelope{Format: 1, ProtocolID: "org.qmux.firmware", Version: 1,
		MessageType: uint64(protocol.MessageRecord)}}
	c := s.start(t, true)

	res, err := c.QueryProtocol(testContext(t), m, "org.qmux.firmware", "device42.fleet.example.", []uint16{1})
	if err != nil {
		t.Fatalf("QueryProtocol() error = %v", err)
	}
	got := s.received(t)
	if len(got) != 1 || got[0].Name != "_v1.device42.fleet.example." || got[0].Key != firmwareKey {
		t.Fatalf("server received %+v, want one query for _v1.device42.fleet.example. at %s", got, firmwareKey)
	}
	if res.ProtocolID != "org.qmux.firmware" || res.Version != 1 {
		t.Fatalf("result protocol = %s version %d, want org.qmux.firmware version 1", res.ProtocolID, res.Version)
	}
}

func TestQueryProtocolRejectsAnEnvelopeOfAnotherProtocolID(t *testing.T) { // FR-discovery-31
	m, err := discovery.Static(testZone, testEntries())
	if err != nil {
		t.Fatalf("Static() error = %v", err)
	}
	s := &fakeServer{envelope: codec.Envelope{Format: 1, ProtocolID: "org.qmux.other", Version: 2,
		MessageType: uint64(protocol.MessageRecord)}}
	c := s.start(t, true)

	_, err = c.QueryProtocol(testContext(t), m, "org.qmux.firmware", "device42.fleet.example.", []uint16{2})
	var de *client.DNSError
	if !errors.As(err, &de) || de.Kind != client.KindMismatch {
		t.Fatalf("QueryProtocol() error = %v, want a DNSError of kind %s", err, client.KindMismatch)
	}
}

// A stale manifest lists version 2, but the server now supports only version 1.
func TestQueryProtocolReturnsTheAppErrorForAVersionUnsupportedEnvelope(t *testing.T) { // FR-discovery-19, FR-discovery-20
	m, err := discovery.Static(testZone, testEntries())
	if err != nil {
		t.Fatalf("Static() error = %v", err)
	}
	s := &fakeServer{envelope: codec.Envelope{Format: 1, ProtocolID: "org.qmux.firmware", Version: 1,
		MessageType: uint64(protocol.MessageRecord), Status: uint64(protocol.StatusVersionUnsupported)}}
	c := s.start(t, true)

	res, err := c.QueryProtocol(testContext(t), m, "org.qmux.firmware", "device42.fleet.example.", []uint16{2})
	var ae *client.AppError
	if !errors.As(err, &ae) || ae.Status != protocol.StatusVersionUnsupported {
		t.Fatalf("QueryProtocol() error = %v, want an AppError with status version-unsupported", err)
	}
	if res == nil || res.Version != 1 {
		t.Fatalf("QueryProtocol() result = %+v, want a result with version 1", res)
	}
	if got := s.received(t); len(got) != 1 || got[0].Name != "_v2.device42.fleet.example." {
		t.Fatalf("server received %+v, want one query for _v2.device42.fleet.example.", got)
	}
}

func TestStaticReturnsAnErrorForADuplicateProtocolID(t *testing.T) { // FR-discovery-32
	entries := append(testEntries(), discovery.Entry{ID: "org.qmux.firmware", Versions: []uint16{1}, Class: 65282, Type: 65280})
	m, err := discovery.Static(testZone, entries)
	if !errors.Is(err, discovery.ErrInvalidManifest) || m != nil {
		t.Fatalf("Static() = %+v, %v, want nil and ErrInvalidManifest", m, err)
	}
}

func TestStaticReturnsAManifestForAValidList(t *testing.T) { // FR-discovery-32
	m, err := discovery.Static(testZone, testEntries())
	if err != nil {
		t.Fatalf("Static() error = %v", err)
	}
	if m.FrameworkVersion != 1 || m.Zone != testZone || len(m.Protocols) != 2 {
		t.Fatalf("Static() = %+v, want framework 1, zone %s and 2 entries", m, testZone)
	}
}
