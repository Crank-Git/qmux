package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/security"
	"github.com/Crank-Git/qmux/pkg/server"
)

// The fixed seeds of the test keys. anchorSeed signs for the trust anchor file, and
// otherSeed is a key that no trust anchor file of these tests holds.
var (
	anchorSeed = bytes.Repeat([]byte{0x41}, ed25519.SeedSize)
	otherSeed  = bytes.Repeat([]byte{0x42}, ed25519.SeedSize)
)

// probeKey is the one protocol key that the test server answers. #60 adds QTYPE 16 to
// qmuxd, so the tests use the private type 65280 for an ok outcome.
var probeKey = protocol.ProtocolKey{Class: 65281, Type: 65280}

// testServer holds the address and the observation log path of a running server.
type testServer struct {
	addr, log string
}

// startProbeServer runs a server that answers probeKey like the probe protocol of
// qmuxd, signed with seed. payload returns the payload for a request. It writes its
// observation log to a file in a temporary directory.
func startProbeServer(t *testing.T, seed []byte, payload func(*protocol.Request) []byte) testServer {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "observations.jsonl")
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() }) //nolint:errcheck // The test reads the file by its path.
	s := server.NewServer(server.ServerConfig{
		Address:        "127.0.0.1:0",
		Zones:          []string{defaultZone},
		Logger:         slog.New(slog.DiscardHandler),
		Signer:         &security.Signer{Key: ed25519.NewKeyFromSeed(seed)},
		ObservationLog: f,
	})
	h := protocol.HandlerFunc(func(_ context.Context, r *protocol.Request) (*protocol.Response, error) {
		return &protocol.Response{Payload: payload(r)}, nil
	})
	if err := s.RegisterProtocol(probeKey, protocol.Info{ID: probeProtocolID, Versions: []uint16{1}}, h); err != nil {
		t.Fatalf("RegisterProtocol: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- s.ListenAndServe(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-errc; err != nil {
			t.Errorf("ListenAndServe: %v", err)
		}
	})
	// Addr is non-nil only after both listeners are bound, so it is the readiness signal.
	deadline := time.Now().Add(2 * time.Second)
	for s.Addr() == nil {
		if time.Now().After(deadline) {
			t.Fatal("Addr is nil 2s after the start")
		}
		time.Sleep(time.Millisecond)
	}
	return testServer{addr: s.Addr().String(), log: logPath}
}

// digestPayload is the probe payload of FR-interop-6.
func digestPayload(r *protocol.Request) []byte {
	sum := sha256.Sum256([]byte(r.QName))
	return sum[:]
}

// writeAnchors writes a trust anchor file with the public key of seed and returns its
// path.
func writeAnchors(t *testing.T, seed []byte) string {
	t.Helper()
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	path := filepath.Join(t.TempDir(), "anchors.json")
	if err := security.WriteTrustAnchors(path, []security.TrustAnchor{{KeyID: security.KeyID(pub), PublicKey: pub}}); err != nil {
		t.Fatalf("WriteTrustAnchors: %v", err)
	}
	return path
}

// silentUDP binds a UDP socket on 127.0.0.1:0 that reads every query and answers
// none. It returns the address and a channel that receives each query.
func silentUDP(t *testing.T) (string, <-chan []byte) {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan []byte, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 65535)
		for {
			n, _, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			got <- bytes.Clone(buf[:n])
		}
	}()
	t.Cleanup(func() {
		conn.Close() //nolint:errcheck // The close stops the read loop, and the test needs no result from it.
		<-done
	})
	return conn.LocalAddr().String(), got
}

// stoppedAddr returns a loopback address with no UDP or TCP listener.
func stoppedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// probeCmd runs qmux-probe with args and returns the exit code, stdout and stderr.
func probeCmd(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(append([]string{"qmux-probe"}, args...), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// readRecords runs qmux-probe with args and --output, and returns the records of the
// output file as generic JSON objects.
func readRecords(t *testing.T, args ...string) []map[string]any {
	t.Helper()
	out := filepath.Join(t.TempDir(), "results.json")
	code, stdout, stderr := probeCmd(append(args, "--output", out)...)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout = %q; stderr = %q", code, stdout, stderr)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var recs []map[string]any
	if err := json.Unmarshal(b, &recs); err != nil {
		t.Fatalf("results.json is not a JSON array of objects: %v\n%s", err, b)
	}
	return recs
}

// oneKey returns the flags that restrict a run to probeKey over UDP.
func oneKey() []string {
	return []string{"--classes", "65281", "--types", "65280", "--transports", "udp"}
}

func TestDirectProbeHasOutcomeOkAndPayloadOkWhenThePayloadIsTheDigestOfTheQNAME(t *testing.T) { // FR-interop-6, FR-interop-27
	srv := startProbeServer(t, anchorSeed, digestPayload)
	recs := readRecords(t, append(oneKey(), "--server", srv.addr, "--trust-anchors", writeAnchors(t, anchorSeed))...)
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	r := recs[0]
	if r["outcome"] != "ok" || r["payload_ok"] != true || r["verified"] != true || r["error"] != "" {
		t.Errorf("record = %v, want outcome ok, payload_ok true, verified true, empty error", r)
	}
	if r["rcode"] != "NOERROR" || r["answer_class"] != 65281.0 || r["answer_type"] != 65280.0 {
		t.Errorf("record = %v, want rcode NOERROR and answer 65281/65280", r)
	}
	if r["query_bytes"].(float64) <= 0 || r["response_bytes"].(float64) <= 0 || r["rtt_ms"].(float64) <= 0 {
		t.Errorf("record = %v, want positive sizes and RTT", r)
	}
}

func TestProbeHasOutcomeVerifyWhenThePayloadIsNotTheDigestOfTheQNAME(t *testing.T) { // FR-interop-27
	srv := startProbeServer(t, anchorSeed, func(*protocol.Request) []byte { return []byte{1, 2, 3} })
	recs := readRecords(t, append(oneKey(), "--server", srv.addr, "--trust-anchors", writeAnchors(t, anchorSeed))...)
	if r := recs[0]; r["outcome"] != "verify" || r["payload_ok"] != false || r["verified"] != true || r["error"] == "" {
		t.Errorf("record = %v, want outcome verify, payload_ok false, verified true and an error", r)
	}
}

func TestPayloadCheckFoldsTheQNAMEToLowercase(t *testing.T) { // FR-interop-27
	sum := sha256.Sum256([]byte("abc.probe.fleet.example."))
	if !payloadOK("ABC.Probe.Fleet.Example.", sum[:]) {
		t.Error("payloadOK = false for the digest of the lowercase QNAME, want true")
	}
	if payloadOK("abc.probe.fleet.example.", sum[:31]) {
		t.Error("payloadOK = true for a short payload, want false")
	}
}

func TestRunWithDefaultsAndTwoPathsWritesFortyRecords(t *testing.T) { // FR-interop-10, FR-interop-13 to FR-interop-15
	srv := startProbeServer(t, anchorSeed, digestPayload)
	recs := readRecords(t, "--server", srv.addr, "--resolver", "bind="+srv.addr,
		"--trust-anchors", writeAnchors(t, anchorSeed), "--timeout", "200ms")
	if len(recs) != 40 {
		t.Fatalf("got %d records, want 40", len(recs))
	}
	seen := map[string]bool{}
	for _, r := range recs {
		key := r["path"].(string) + " " + r["transport"].(string) + " " +
			jsonNumber(r["qclass"]) + "/" + jsonNumber(r["qtype"])
		if seen[key] {
			t.Errorf("combination %s has two records", key)
		}
		seen[key] = true
	}
	for _, path := range []string{"direct", "bind"} {
		for _, tr := range []string{"udp", "tcp"} {
			for _, c := range []string{"1", "3", "4", "65280", "65281"} {
				for _, ty := range []string{"16", "65280"} {
					if k := path + " " + tr + " " + c + "/" + ty; !seen[k] {
						t.Errorf("no record for %s", k)
					}
				}
			}
		}
	}
}

func jsonNumber(v any) string {
	b, _ := json.Marshal(v) // A float64 from json.Unmarshal always marshals.
	return string(b)
}

func TestResolverFlagAddsARecordWithTheResolverNameAsPath(t *testing.T) { // FR-interop-11, FR-interop-12
	srv := startProbeServer(t, anchorSeed, digestPayload)
	recs := readRecords(t, append(oneKey(), "--server", srv.addr, "--resolver", "bind="+srv.addr,
		"--resolver", "unbound="+srv.addr, "--trust-anchors", writeAnchors(t, anchorSeed))...)
	var paths []string
	for _, r := range recs {
		paths = append(paths, r["path"].(string))
	}
	if strings.Join(paths, ",") != "direct,bind,unbound" {
		t.Errorf("paths = %v, want direct, bind, unbound", paths)
	}
	if recs[1]["outcome"] != "ok" {
		t.Errorf("bind record = %v, want outcome ok", recs[1])
	}
}

func TestEveryQNAMEIsUniqueAndIsASixteenHexNonceBelowTheZone(t *testing.T) { // FR-interop-16, FR-interop-17
	srv := startProbeServer(t, anchorSeed, digestPayload)
	recs := readRecords(t, "--server", srv.addr, "--trust-anchors", writeAnchors(t, anchorSeed), "--timeout", "200ms")
	re := regexp.MustCompile(`^[0-9a-f]{16}\.probe\.fleet\.example\.$`)
	seen := map[string]bool{}
	for _, r := range recs {
		name := r["qname"].(string)
		if !re.MatchString(name) {
			t.Errorf("qname %q is not <16 lowercase hex>.probe.fleet.example.", name)
		}
		if seen[name] {
			t.Errorf("qname %q is in two records", name)
		}
		seen[name] = true
	}
}

func TestZoneFlagSetsTheProbeZone(t *testing.T) { // FR-interop-16
	addr, _ := silentUDP(t)
	recs := readRecords(t, append(oneKey(), "--server", addr, "--zone", "other.example",
		"--trust-anchors", writeAnchors(t, anchorSeed), "--timeout", "20ms")...)
	if name := recs[0]["qname"].(string); !strings.HasSuffix(name, ".other.example.") {
		t.Errorf("qname = %q, want a name below other.example.", name)
	}
}

func TestWrongTrustAnchorFileGivesOutcomeVerifyOnTheDirectPath(t *testing.T) { // FR-interop-18
	srv := startProbeServer(t, anchorSeed, digestPayload)
	recs := readRecords(t, append(oneKey(), "--server", srv.addr, "--trust-anchors", writeAnchors(t, otherSeed))...)
	if r := recs[0]; r["outcome"] != "verify" || r["verified"] != false || !strings.Contains(r["error"].(string), "ErrUnknownKey") {
		t.Errorf("record = %v, want outcome verify, verified false and an ErrUnknownKey error", r)
	}
}

func TestProbeToAPortThatNeverAnswersHasOutcomeTimeoutAfterTheTimeout(t *testing.T) { // FR-interop-19
	addr, _ := silentUDP(t)
	start := time.Now()
	recs := readRecords(t, append(oneKey(), "--server", addr, "--trust-anchors", writeAnchors(t, anchorSeed), "--timeout", "150ms")...)
	elapsed := time.Since(start)
	r := recs[0]
	if r["outcome"] != "timeout" || r["rcode"] != nil || r["answer_class"] != nil {
		t.Errorf("record = %v, want outcome timeout, rcode null and answer_class null", r)
	}
	if elapsed < 150*time.Millisecond || r["rtt_ms"].(float64) < 150 {
		t.Errorf("elapsed = %v, rtt_ms = %v; want at least 150ms", elapsed, r["rtt_ms"])
	}
}

func TestTimeoutDefaultIsThreeSeconds(t *testing.T) { // FR-interop-19
	f, _, err := parseFlags("qmux-probe", []string{"--server", "127.0.0.1:53", "--trust-anchors", "a.json"})
	if err != nil {
		t.Fatal(err)
	}
	if f.timeout != 3*time.Second {
		t.Errorf("timeout = %v, want 3s", f.timeout)
	}
}

func TestDefaultsAreTheClassesTypesTransportsAndZoneOfTheSpec(t *testing.T) { // FR-interop-13 to FR-interop-16
	f, _, err := parseFlags("qmux-probe", []string{"--server", "127.0.0.1:53", "--trust-anchors", "a.json"})
	if err != nil {
		t.Fatal(err)
	}
	if !equal(f.classes, []uint16{1, 3, 4, 65280, 65281}) || !equal(f.types, []uint16{16, 65280}) ||
		strings.Join(f.transports, ",") != "udp,tcp" || f.zone != "probe.fleet.example." {
		t.Errorf("flags = %+v, want the defaults of FR-interop-13 to FR-interop-16", f)
	}
}

func equal(a, b []uint16) bool {
	return len(a) == len(b) && string(u16bytes(a)) == string(u16bytes(b))
}

func u16bytes(v []uint16) []byte {
	var b []byte
	for _, x := range v {
		b = append(b, byte(x>>8), byte(x))
	}
	return b
}

func TestResolverPathQueryHasRDSetAndDirectPathQueryHasRDClear(t *testing.T) { // FR-interop-12a
	direct, directQ := silentUDP(t)
	resolver, resolverQ := silentUDP(t)
	code, _, stderr := probeCmd(append(oneKey(), "--server", direct, "--resolver", "bind="+resolver,
		"--trust-anchors", writeAnchors(t, anchorSeed), "--timeout", "20ms")...)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr)
	}
	// The RD bit is the lowest bit of the third header byte (RFC 1035 § 4.1.1).
	if q := <-directQ; q[2]&0x01 != 0 {
		t.Errorf("direct query has RD=1, want RD=0")
	}
	if q := <-resolverQ; q[2]&0x01 != 1 {
		t.Errorf("resolver query has RD=0, want RD=1")
	}
}

func TestDirectProbeReachedTheAuthoritativeServerWithTheObservedClassAndType(t *testing.T) { // FR-interop-20 to FR-interop-22
	srv := startProbeServer(t, anchorSeed, digestPayload)
	recs := readRecords(t, append(oneKey(), "--server", srv.addr, "--trust-anchors", writeAnchors(t, anchorSeed),
		"--observation-log", srv.log)...)
	r := recs[0]
	if r["reached_authoritative"] != true || r["observed_qclass"] != 65281.0 || r["observed_qtype"] != 65280.0 {
		t.Errorf("record = %v, want reached_authoritative true and observed 65281/65280", r)
	}
}

func TestProbeThatNeverReachedTheServerHasReachedAuthoritativeFalse(t *testing.T) { // FR-interop-21
	srv := startProbeServer(t, anchorSeed, digestPayload)
	addr, _ := silentUDP(t)
	recs := readRecords(t, append(oneKey(), "--server", addr, "--trust-anchors", writeAnchors(t, anchorSeed),
		"--observation-log", srv.log, "--timeout", "20ms")...)
	if r := recs[0]; r["reached_authoritative"] != false || r["observed_qclass"] != nil || r["observed_qtype"] != nil {
		t.Errorf("record = %v, want reached_authoritative false and observed class and type null", r)
	}
}

func TestMissingObservationLogGivesNullReachedAndAWarning(t *testing.T) { // FR-interop-20
	srv := startProbeServer(t, anchorSeed, digestPayload)
	out := filepath.Join(t.TempDir(), "results.json")
	missing := filepath.Join(t.TempDir(), "absent.jsonl")
	code, stdout, stderr := probeCmd(append(oneKey(), "--server", srv.addr, "--trust-anchors", writeAnchors(t, anchorSeed),
		"--observation-log", missing, "--output", out)...)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "qmux-probe: warning: ") || !strings.Contains(stdout, missing) {
		t.Errorf("stdout = %q, want a warning that names %s", stdout, missing)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var recs []map[string]any
	if err := json.Unmarshal(b, &recs); err != nil {
		t.Fatal(err)
	}
	if v, ok := recs[0]["reached_authoritative"]; !ok || v != nil {
		t.Errorf("record = %v, want reached_authoritative null", recs[0])
	}
}

func TestObservedClassAndTypeComeFromTheLogLineAlsoWhenTheyDifferFromTheProbe(t *testing.T) { // FR-interop-22
	path := filepath.Join(t.TempDir(), "obs.jsonl")
	log := `{"qname":"other.probe.fleet.example.","qclass":1,"qtype":1}` + "\n" +
		"not json\n" +
		`{"qname":"ABC.probe.fleet.example.","qclass":1,"qtype":16}` + "\n" +
		`{"qname":"abc.probe.fleet.example.","qclass":3,"qtype":3}` + "\n"
	if err := os.WriteFile(path, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := observe(path)
	if err != nil {
		t.Fatal(err)
	}
	if o := got["abc.probe.fleet.example."]; o.QClass != 1 || o.QType != 16 {
		t.Errorf("observation = %+v, want the first line for the QNAME: 1/16", o)
	}
}

func TestOutputIsAJSONArrayWithEveryFieldOfDataTouched(t *testing.T) { // FR-interop-23
	srv := startProbeServer(t, anchorSeed, digestPayload)
	addr, _ := silentUDP(t)
	recs := readRecords(t, append(oneKey(), "--server", srv.addr, "--resolver", "bind="+addr,
		"--trust-anchors", writeAnchors(t, anchorSeed), "--observation-log", srv.log, "--timeout", "20ms")...)
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}
	fields := []string{
		"path", "transport", "qclass", "qtype", "qname", "rcode", "answer_class", "answer_type",
		"payload_ok", "verified", "rtt_ms", "query_bytes", "response_bytes", "outcome", "error",
		"reached_authoritative", "observed_qclass", "observed_qtype",
	}
	for i, r := range recs {
		for _, f := range fields {
			if _, ok := r[f]; !ok {
				t.Errorf("record %d has no field %q: %v", i, f, r)
			}
		}
		if len(r) != len(fields) {
			t.Errorf("record %d has %d fields, want %d: %v", i, len(r), len(fields), r)
		}
	}
}

func TestMarkdownPrintsATableWithOneRowPerProbe(t *testing.T) { // FR-interop-24
	srv := startProbeServer(t, anchorSeed, digestPayload)
	code, stdout, stderr := probeCmd("--server", srv.addr, "--trust-anchors", writeAnchors(t, anchorSeed),
		"--types", "65280", "--markdown")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 2+10 {
		t.Fatalf("stdout has %d lines, want a header, a separator and 10 rows:\n%s", len(lines), stdout)
	}
	if lines[0] != "| path | transport | class | type | outcome | reached | verified |" || lines[1] != "|---|---|---|---|---|---|---|" {
		t.Errorf("header = %q / %q, want the table header of the spec", lines[0], lines[1])
	}
	if !strings.Contains(stdout, "| direct | udp | 65281 | 65280 | ok | — | yes |\n") {
		t.Errorf("stdout has no ok row for 65281/65280:\n%s", stdout)
	}
	if !strings.Contains(stderr, "qmux-probe: 1 paths x 2 transports x 5 classes x 1 types = 10 probes") {
		t.Errorf("stderr = %q, want the progress lines when stdout holds the table", stderr)
	}
}

func TestRunInWhichEveryResolverProbeFailsExitsZero(t *testing.T) { // FR-interop-25
	srv := startProbeServer(t, anchorSeed, digestPayload)
	addr, _ := silentUDP(t)
	code, _, stderr := probeCmd(append(oneKey(), "--server", srv.addr, "--resolver", "bind="+addr,
		"--trust-anchors", writeAnchors(t, anchorSeed), "--timeout", "20ms", "--require-direct")...)
	if code != 0 {
		t.Errorf("exit code = %d, want 0; stderr = %q", code, stderr)
	}
}

func TestRequireDirectExitsOneWhenTheServerIsStopped(t *testing.T) { // FR-interop-26
	out := filepath.Join(t.TempDir(), "results.json")
	code, _, stderr := probeCmd(append(oneKey(), "--server", stoppedAddr(t), "--trust-anchors", writeAnchors(t, anchorSeed),
		"--timeout", "50ms", "--require-direct", "--output", out)...)
	if code != 1 {
		t.Errorf("exit code = %d, want 1; stderr = %q", code, stderr)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("results.json: %v, want the records also when a direct probe fails", err)
	}
}

func TestStoppedServerWithoutRequireDirectExitsZero(t *testing.T) { // FR-interop-25
	code, _, stderr := probeCmd(append(oneKey(), "--server", stoppedAddr(t), "--trust-anchors", writeAnchors(t, anchorSeed),
		"--timeout", "50ms")...)
	if code != 0 {
		t.Errorf("exit code = %d, want 0; stderr = %q", code, stderr)
	}
}

func TestMissingTrustAnchorsIsAUsageErrorWithTheSpecText(t *testing.T) {
	code, _, stderr := probeCmd("--server", "127.0.0.1:53")
	if code != 64 || !strings.HasPrefix(stderr, "qmux-probe: --trust-anchors is required\n") {
		t.Errorf("exit code = %d, stderr = %q; want 64 and the spec text", code, stderr)
	}
}

func TestInvalidFlagsAreUsageErrors(t *testing.T) {
	anchors := writeAnchors(t, anchorSeed)
	cases := map[string][]string{
		"no path":           {},
		"host name":         {"--server", "localhost:53"},
		"resolver no name":  {"--resolver", "=127.0.0.1:53"},
		"resolver direct":   {"--resolver", "direct=127.0.0.1:53"},
		"resolver twice":    {"--resolver", "a=127.0.0.1:53", "--resolver", "a=127.0.0.1:54"},
		"resolver no addr":  {"--resolver", "bind"},
		"bad class":         {"--server", "127.0.0.1:53", "--classes", "1,70000"},
		"empty types":       {"--server", "127.0.0.1:53", "--types", ""},
		"bad transport":     {"--server", "127.0.0.1:53", "--transports", "udp,quic"},
		"negative timeout":  {"--server", "127.0.0.1:53", "--timeout", "-1s"},
		"bad zone":          {"--server", "127.0.0.1:53", "--zone", strings.Repeat("a", 64) + ".example."},
		"extra argument":    {"--server", "127.0.0.1:53", "extra"},
		"unknown flag":      {"--server", "127.0.0.1:53", "--nope"},
		"bad anchor file":   {"--server", "127.0.0.1:53", "--trust-anchors", filepath.Join(t.TempDir(), "absent.json")},
		"anchors and blank": {"--server", "127.0.0.1:53", "--trust-anchors", ""},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if !strings.HasPrefix(name, "bad anchor") && !strings.HasPrefix(name, "anchors and") {
				args = append(args, "--trust-anchors", anchors)
			}
			code, stdout, stderr := probeCmd(args...)
			if code != 64 || stdout != "" || !strings.Contains(stderr, "Usage: qmux-probe") {
				t.Errorf("exit code = %d, stdout = %q, stderr = %q; want 64 and the usage on stderr", code, stdout, stderr)
			}
		})
	}
}

func TestHelpListsEveryProbeFlag(t *testing.T) {
	code, stdout, _ := probeCmd("--help")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	for _, f := range []string{"--server", "--resolver", "--classes", "--types", "--transports", "--zone",
		"--trust-anchors", "--timeout", "--observation-log", "--output", "--markdown", "--require-direct"} {
		if !strings.Contains(stdout, f) {
			t.Errorf("help text has no %s:\n%s", f, stdout)
		}
	}
}

func TestOutcomeMapsEachClientErrorToOneOfTheSixOutcomes(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&client.DNSError{Kind: client.KindRCode, RCode: 5}, outcomeRCode},
		{&client.DNSError{Kind: client.KindTimeout}, outcomeTimeout},
		{&client.DNSError{Kind: client.KindNetwork}, outcomeNetwork},
		{&client.DNSError{Kind: client.KindMalformed}, outcomeNetwork},
		{&client.DNSError{Kind: client.KindTruncation}, outcomeNetwork},
		{&client.DNSError{Kind: client.KindMismatch}, outcomeMismatch},
		{&client.VerifyError{Reason: security.ErrBadSignature}, outcomeVerify},
		{&client.AppError{Status: protocol.StatusNotFound}, outcomeVerify},
		{errors.New("other"), outcomeNetwork},
	}
	for _, tc := range cases {
		if got := outcomeOf(tc.err); got != tc.want {
			t.Errorf("outcomeOf(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}
