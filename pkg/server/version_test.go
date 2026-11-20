package server

import (
	"errors"
	"testing"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/transport"
)

// serveVersioned registers info for testKey with a recording handler, and serves one
// valid query for qname. It returns the answers and the requests that the handler
// received.
func serveVersioned(t *testing.T, info protocol.Info, qname string) ([]transport.Answer, []*protocol.Request) {
	t.Helper()
	var reqs []*protocol.Request
	s := NewServer(ServerConfig{Zones: []string{"fleet.example."}, Logger: quietLogger()})
	if err := s.RegisterProtocol(testKey, info, recordingHandler(&reqs)); err != nil {
		t.Fatalf("RegisterProtocol: %v", err)
	}
	q := validQuery()
	q.Name = qname
	return answer(s, q), reqs
}

// onlyRequest fails the test when the handler did not receive exactly one request,
// and returns that request.
func onlyRequest(t *testing.T, got []transport.Answer, reqs []*protocol.Request) *protocol.Request {
	t.Helper()
	checkRCode(t, got, transport.RCodeNoError)
	if len(reqs) != 1 {
		t.Fatalf("the handler received %d requests, want 1", len(reqs))
	}
	return reqs[0]
}

func TestServeQueryGivesTheHandlerTheVersionAndTheNameWithoutTheVersionLabel(t *testing.T) { // FR-discovery-17, FR-discovery-18
	got, reqs := serveVersioned(t, twoVersionInfo, "_v2.device42.fleet.example.")
	req := onlyRequest(t, got, reqs)
	if req.Version != 2 {
		t.Errorf("Version = %d, want 2", req.Version)
	}
	if req.Name != "device42.fleet.example." {
		t.Errorf("Name = %q, want %q", req.Name, "device42.fleet.example.")
	}
}

func TestServeQueryGivesTheHandlerTheQNameAsTheNameWhenNoVersionLabelIsPresent(t *testing.T) { // FR-discovery-18
	got, reqs := serveVersioned(t, twoVersionInfo, "Device42.Fleet.Example.")
	req := onlyRequest(t, got, reqs)
	if req.Name != "device42.fleet.example." {
		t.Fatalf("Name = %q, want %q", req.Name, "device42.fleet.example.")
	}
}

func TestServeQuerySetsTheHighestVersionWhenNoVersionLabelIsPresent(t *testing.T) { // FR-discovery-21
	got, reqs := serveVersioned(t, twoVersionInfo, "device42.fleet.example.")
	req := onlyRequest(t, got, reqs)
	if req.Version != 2 {
		t.Fatalf("Version = %d, want 2", req.Version)
	}
}

func TestServeQueryAnswersVersionUnsupportedForAVersionOutsideTheRegistration(t *testing.T) { // FR-discovery-19, FR-discovery-20
	got, reqs := serveVersioned(t, testInfo, "_v9.device42.fleet.example.")
	e := decodeAnswer(t, got)
	if e.Status != uint64(protocol.StatusVersionUnsupported) {
		t.Errorf("status = %d, want %d", e.Status, protocol.StatusVersionUnsupported)
	}
	if e.Version != 1 {
		t.Errorf("envelope version = %d, want 1", e.Version)
	}
	if e.ProtocolID != testInfo.ID {
		t.Errorf("protocol ID = %q, want %q", e.ProtocolID, testInfo.ID)
	}
	if len(reqs) != 0 {
		t.Errorf("the handler received %d requests, want 0", len(reqs))
	}
}

func TestServeQueryAnswersBadRequestForALabelThatStartsWithVButIsNoVersionLabel(t *testing.T) { // FR-discovery-22
	for _, qname := range []string{
		"_vx.device42.fleet.example.",
		"_v01.device42.fleet.example.",
		"_v0.device42.fleet.example.",
		"_v.device42.fleet.example.",
		"_v2x.device42.fleet.example.",
		"_v65536.device42.fleet.example.",
	} {
		t.Run(qname, func(t *testing.T) {
			got, reqs := serveVersioned(t, twoVersionInfo, qname)
			e := decodeAnswer(t, got)
			if e.Status != uint64(protocol.StatusBadRequest) {
				t.Errorf("status = %d, want %d", e.Status, protocol.StatusBadRequest)
			}
			if len(reqs) != 0 {
				t.Errorf("the handler received %d requests, want 0", len(reqs))
			}
		})
	}
}

func TestServeQueryDoesNotValidateAStatusEnvelopeThatTheServerBuilds(t *testing.T) { // FR-discovery-19
	// The schema checks handler payloads. A status envelope from the server has no
	// payload, so a schema that rejects every payload must not turn it into SERVFAIL.
	schema := &countingSchema{err: errors.New("bad payload")}
	info := protocol.Info{ID: "test.qmux", Versions: []uint16{1}, Schema: schema}
	got, _ := serveVersioned(t, info, "_v9.device42.fleet.example.")
	e := decodeAnswer(t, got)
	if e.Status != uint64(protocol.StatusVersionUnsupported) {
		t.Errorf("status = %d, want %d", e.Status, protocol.StatusVersionUnsupported)
	}
	if schema.calls() != 0 {
		t.Errorf("Validate calls = %d, want 0", schema.calls())
	}
}
