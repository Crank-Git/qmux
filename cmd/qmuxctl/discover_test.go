package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/discovery"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/security"
	"github.com/Crank-Git/qmux/pkg/server"
)

const testZone = "fleet.example."

// discoveryServer is a server with one discovery registration. It records the QNAME of
// each query that reaches the handler.
type discoveryServer struct {
	addr   string
	mu     sync.Mutex
	qnames []string
}

// received returns the QNAMEs that the handler received, in arrival order.
func (d *discoveryServer) received() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.qnames...)
}

// testManifest returns a manifest for testZone with three entries. The discovery entry
// is at key.
func testManifest(t *testing.T, key protocol.ProtocolKey) *discovery.Manifest {
	t.Helper()
	m, err := discovery.Static(testZone, []discovery.Entry{
		{ID: "org.qmux.bootstrap", Versions: []uint16{1}, Class: 65280, Type: 65280},
		{ID: discovery.ProtocolID, Versions: []uint16{1}, Class: key.Class, Type: key.Type},
		{ID: "org.qmux.firmware", Versions: []uint16{1, 2}, Class: 65282, Type: 65280},
	})
	if err != nil {
		t.Fatalf("Static: %v", err)
	}
	return m
}

// startDiscoveryServer runs a server that answers every query at key with resp until
// the test ends. A nil resp means the manifest envelope of testManifest.
func startDiscoveryServer(t *testing.T, key protocol.ProtocolKey, resp *protocol.Response) *discoveryServer {
	t.Helper()
	return startSignedDiscoveryServer(t, nil, key, resp)
}

// startSignedDiscoveryServer is startDiscoveryServer with a server that signs with
// signer. A nil signer means no signature.
func startSignedDiscoveryServer(t *testing.T, signer *security.Signer, key protocol.ProtocolKey, resp *protocol.Response) *discoveryServer {
	t.Helper()
	if resp == nil {
		payload, err := codec.Marshal(testManifest(t, key))
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		resp = &protocol.Response{Payload: payload, MessageType: protocol.MessageManifest}
	}
	d := &discoveryServer{}
	s := server.NewServer(server.ServerConfig{Address: "127.0.0.1:0", Logger: slog.New(slog.DiscardHandler), Signer: signer})
	h := protocol.HandlerFunc(func(_ context.Context, req *protocol.Request) (*protocol.Response, error) {
		d.mu.Lock()
		d.qnames = append(d.qnames, req.QName)
		d.mu.Unlock()
		return resp, nil
	})
	info := protocol.Info{ID: discovery.ProtocolID, Versions: []uint16{1}}
	if err := s.RegisterProtocol(key, info, h); err != nil {
		t.Fatalf("RegisterProtocol: %v", err)
	}
	d.addr = serve(t, s)
	return d
}

// discoverCmd runs "qmuxctl discover" with args and returns the exit code, stdout and
// stderr.
func discoverCmd(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(append([]string{"qmuxctl", "discover"}, args...), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestDiscoverHelpListsTheEightFlags(t *testing.T) { // FR-discovery-33, FR-security-50
	code, stdout, _ := discoverCmd("--help")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	for _, f := range []string{"--server", "--name", "--class", "--type", "--in", "--insecure", "--json", "--trust-anchors"} {
		if !strings.Contains(stdout, f+" ") {
			t.Errorf("help text has no %s:\n%s", f, stdout)
		}
	}
}

func TestDiscoverQueriesTheDiscoveryNameOfTheZoneAtTheDefaultKey(t *testing.T) { // FR-discovery-34
	d := startDiscoveryServer(t, discovery.DefaultKey, nil)
	code, _, stderr := discoverCmd("--server", d.addr, "--name", testZone, "--insecure")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr)
	}
	if got := d.received(); len(got) != 1 || got[0] != "_qmux."+testZone {
		t.Errorf("server received %q, want one query for %q", got, "_qmux."+testZone)
	}
}

func TestDiscoverInQueriesAtTheINKey(t *testing.T) { // FR-discovery-35
	d := startDiscoveryServer(t, discovery.INKey, nil)
	code, _, stderr := discoverCmd("--server", d.addr, "--name", testZone, "--insecure", "--in")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr)
	}
	if got := d.received(); len(got) != 1 {
		t.Errorf("server at %v received %q, want one query", discovery.INKey, got)
	}
}

func TestDiscoverClassAndTypeOverrideTheDiscoveryKey(t *testing.T) { // FR-discovery-35
	key := protocol.ProtocolKey{Class: 65290, Type: 65500}
	d := startDiscoveryServer(t, key, nil)
	code, _, stderr := discoverCmd("--server", d.addr, "--name", testZone, "--insecure",
		"--class", "65290", "--type", "65500")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr)
	}
	if got := d.received(); len(got) != 1 {
		t.Errorf("server at %v received %q, want one query", key, got)
	}
}

func TestDiscoverPrintsOneRowWithFourColumnsPerManifestEntry(t *testing.T) { // FR-discovery-36
	d := startDiscoveryServer(t, discovery.DefaultKey, nil)
	code, stdout, stderr := discoverCmd("--server", d.addr, "--name", testZone, "--insecure")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	want := [][]string{
		{"zone", testZone, "framework", "1", "verified", "no"},
		{"PROTOCOL", "VERSIONS", "CLASS", "TYPE"},
		{"org.qmux.bootstrap", "1", "65280", "65280"},
		{"org.qmux.discovery", "1", "65280", "65534"},
		{"org.qmux.firmware", "1,2", "65282", "65280"},
	}
	if len(lines) != len(want) {
		t.Fatalf("stdout has %d lines, want %d:\n%s", len(lines), len(want), stdout)
	}
	for i, w := range want {
		if got := strings.Fields(lines[i]); strings.Join(got, " ") != strings.Join(w, " ") {
			t.Errorf("line %d = %q, want the fields %q", i+1, lines[i], w)
		}
	}
}

func TestDiscoverJSONPrintsTheManifestAsOneJSONObject(t *testing.T) { // FR-discovery-37
	d := startDiscoveryServer(t, discovery.DefaultKey, nil)
	code, stdout, stderr := discoverCmd("--server", d.addr, "--name", testZone, "--insecure", "--json")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr)
	}
	dec := json.NewDecoder(strings.NewReader(stdout))
	var got struct {
		FrameworkVersion *uint64 `json:"framework_version"`
		Zone             string  `json:"zone"`
		Protocols        []struct {
			ID       string   `json:"id"`
			Versions []uint16 `json:"versions"`
			Class    uint16   `json:"class"`
			Type     uint16   `json:"type"`
		} `json:"protocols"`
	}
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		t.Errorf("stdout holds more than one JSON value: %q", stdout)
	}
	if got.FrameworkVersion == nil || *got.FrameworkVersion != 1 || got.Zone != testZone || len(got.Protocols) != 3 {
		t.Fatalf("JSON = %s, want framework_version 1, zone %s and 3 protocols", stdout, testZone)
	}
	fw := got.Protocols[2]
	if fw.ID != "org.qmux.firmware" || len(fw.Versions) != 2 || fw.Versions[1] != 2 || fw.Class != 65282 || fw.Type != 65280 {
		t.Errorf("protocols[2] = %+v, want org.qmux.firmware [1 2] 65282/65280", fw)
	}
}

func TestDiscoverWithoutInsecureExitsThreeAndSendsNoQuery(t *testing.T) { // FR-discovery-38
	d := startDiscoveryServer(t, discovery.DefaultKey, nil)
	code, stdout, stderr := discoverCmd("--server", d.addr, "--name", testZone)
	if code != 3 {
		t.Fatalf("exit code = %d, want 3; stderr = %q", code, stderr)
	}
	if want := "discovery requires --trust-anchors or --insecure\n"; stdout != "" || stderr != want {
		t.Errorf("stdout = %q, stderr = %q; want only %q on stderr", stdout, stderr, want)
	}
	if got := d.received(); len(got) != 0 {
		t.Errorf("server received %q, want no query", got)
	}
}

func TestDiscoverWithAnInvalidFlagExitsSixtyFourAndPrintsTheUsage(t *testing.T) { // FR-discovery-35
	base := []string{"--server", "127.0.0.1:5353", "--name", testZone, "--insecure"}
	cases := map[string][]string{
		"in with class":     append(base, "--in", "--class", "65280"),
		"no server":         {"--name", testZone, "--insecure"},
		"no name":           {"--server", "127.0.0.1:5353", "--insecure"},
		"class above 65535": append(base, "--class", "65536"),
		"type not a number": append(base, "--type", "txt"),
		"unknown flag":      append(base, "--bogus"),
		"extra argument":    append(base, "extra"),
		// A label of 64 bytes is longer than DNS allows, so the client cannot build the query.
		"zone label too long": {"--server", "127.0.0.1:5353", "--insecure", "--name", strings.Repeat("a", 64) + ".example."},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr := discoverCmd(args...)
			if code != 64 {
				t.Fatalf("exit code = %d, want 64; stderr = %q", code, stderr)
			}
			if stdout != "" || !strings.Contains(stderr, "Usage: qmuxctl discover") {
				t.Errorf("stdout = %q, stderr = %q; want the usage on stderr only", stdout, stderr)
			}
		})
	}
}

func TestDiscoverExitsOneWhenTheServerHasNoDiscoveryRegistration(t *testing.T) {
	addr := startServer(t)
	code, stdout, stderr := discoverCmd("--server", addr, "--name", testZone, "--insecure")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr = %q", code, stderr)
	}
	if stdout != "" || !strings.HasPrefix(stderr, "error: ") || !strings.Contains(stderr, "REFUSED") {
		t.Errorf("stdout = %q, stderr = %q; want only an error line with REFUSED on stderr", stdout, stderr)
	}
}

func TestDiscoverPrintsTheStatusNameAndExitsTwoForANotFoundStatus(t *testing.T) {
	d := startDiscoveryServer(t, discovery.DefaultKey, &protocol.Response{Status: protocol.StatusNotFound})
	code, stdout, stderr := discoverCmd("--server", d.addr, "--name", testZone, "--insecure")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr = %q", code, stderr)
	}
	if want := "error: not-found\n"; stdout != "" || stderr != want {
		t.Errorf("stdout = %q, stderr = %q; want only %q on stderr", stdout, stderr, want)
	}
}

func TestDiscoverExitsOneForARecordEnvelopeAtTheDiscoveryKey(t *testing.T) {
	d := startDiscoveryServer(t, discovery.DefaultKey, &protocol.Response{Payload: []byte{0xa0}})
	code, stdout, stderr := discoverCmd("--server", d.addr, "--name", testZone, "--insecure")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr = %q", code, stderr)
	}
	if stdout != "" || !strings.Contains(stderr, "invalid manifest") {
		t.Errorf("stdout = %q, stderr = %q; want only an invalid manifest error on stderr", stdout, stderr)
	}
}

func TestDiscoverExitsOneWhenStdoutFails(t *testing.T) {
	d := startDiscoveryServer(t, discovery.DefaultKey, nil)
	var stderr bytes.Buffer
	args := []string{"qmuxctl", "discover", "--server", d.addr, "--name", testZone, "--insecure"}
	if code := run(args, failingWriter{}, &stderr); code != 1 || stderr.Len() != 0 {
		t.Errorf("exit code = %d, stderr = %q; want 1 and no error, because the discovery succeeded", code, stderr.String())
	}
}
