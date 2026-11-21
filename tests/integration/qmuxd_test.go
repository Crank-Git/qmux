package integration

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/examples/embedded-bootstrap/schema"
	"github.com/Crank-Git/qmux/pkg/discovery"
)

// exampleConfig is the example configuration file, relative to this package.
const exampleConfig = "../../examples/qmuxd.json"

// qmuxdTimeout limits each wait for a qmuxd event. It is a limit, not a sleep: a test
// continues as soon as the event occurs.
const qmuxdTimeout = 30 * time.Second

// observationFields are the fields of one observation log line (FR-qmuxd-33).
var observationFields = []string{
	"bytes_in", "bytes_out", "duration_us", "qclass", "qname", "qtype",
	"rcode", "source", "status", "transport", "ts",
}

// exampleDecoders decode the payload of each protocol of the example configuration
// file with the payload types of the embedded recovery demonstration.
var exampleDecoders = map[string]func([]byte) error{
	"org.qmux.bootstrap": func(b []byte) error {
		_, err := schema.UnmarshalBootstrap(b)
		return err
	},
	"org.qmux.configuration": func(b []byte) error {
		_, err := schema.UnmarshalConfiguration(b)
		return err
	},
	"org.qmux.firmware": func(b []byte) error {
		_, err := schema.UnmarshalFirmware(b)
		return err
	},
}

// tools holds the paths of the qmuxd and qmuxctl binaries that a test built.
type tools struct {
	qmuxd, qmuxctl string
}

// buildTools builds qmuxd and qmuxctl from this checkout into a temporary directory.
func buildTools(t *testing.T) tools {
	t.Helper()
	bin := t.TempDir()
	out, err := exec.Command("go", "build", "-o", bin, "../../cmd/qmuxd", "../../cmd/qmuxctl").CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return tools{qmuxd: filepath.Join(bin, "qmuxd"), qmuxctl: filepath.Join(bin, "qmuxctl")}
}

// exampleProtocol is one protocols entry of the example configuration file.
type exampleProtocol struct {
	ID      string                     `json:"id"`
	Class   uint16                     `json:"class"`
	Type    uint16                     `json:"type"`
	Records map[string]json.RawMessage `json:"records"`
}

// readExample returns the example configuration file as a JSON object and its
// protocols.
func readExample(t *testing.T) (map[string]any, []exampleProtocol) {
	t.Helper()
	b, err := os.ReadFile(exampleConfig)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var cfg map[string]any
	var protocols struct {
		Protocols []exampleProtocol `json:"protocols"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("Unmarshal %s: %v", exampleConfig, err)
	}
	if err := json.Unmarshal(b, &protocols); err != nil {
		t.Fatalf("Unmarshal %s: %v", exampleConfig, err)
	}
	return cfg, protocols.Protocols
}

// keygen runs "qmuxctl keygen --out keys/qmuxd" in dir, as the README tells the
// operator to do, and returns the path of the trust anchor file.
func keygen(t *testing.T, bins tools, dir string) string {
	t.Helper()
	if err := os.Mkdir(filepath.Join(dir, "keys"), 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	cmd := exec.Command(bins.qmuxctl, "keygen", "--out", "keys/qmuxd")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("qmuxctl keygen: %v\n%s", err, out)
	}
	return filepath.Join(dir, "keys", "qmuxd.anchor.json")
}

// writeJSON writes v as JSON to path.
func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

var readyListen = regexp.MustCompile(`msg="qmuxd ready" listen=(\S+)`)

// startQmuxd runs qmuxd with args in dir on a free loopback port, and returns the
// process and the listen address of its ready record. The process ends when the test
// ends. The appended --listen flag replaces any earlier --listen flag in args.
func startQmuxd(t *testing.T, bins tools, dir string, args ...string) (*exec.Cmd, string) {
	t.Helper()
	cmd := exec.Command(bins.qmuxd, append(args, "--listen", "127.0.0.1:0")...)
	cmd.Dir = dir
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("StderrPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ready := make(chan string, 1)
	done := make(chan struct{})
	go func() {
		// The scan reads stderr to the end, so the log records never fill the pipe.
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			if m := readyListen.FindStringSubmatch(sc.Text()); m != nil {
				ready <- m[1]
			}
		}
		close(done)
	}()
	t.Cleanup(func() {
		// Kill fails when the test already stopped the process.
		_ = cmd.Process.Kill()
		<-done
		// Wait reports the kill signal, which is the expected result here.
		_ = cmd.Wait()
	})
	select {
	case addr := <-ready:
		return cmd, addr
	case <-done:
		t.Fatal("qmuxd exited before the ready record")
	case <-time.After(qmuxdTimeout):
		t.Fatalf("no ready record in %v", qmuxdTimeout)
	}
	return nil, ""
}

// stopQmuxd sends SIGTERM to cmd and fails the test when cmd does not exit with code 0.
// After stopQmuxd returns, the observation log holds every line.
func stopQmuxd(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("Signal: %v", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("qmuxd exit: %v", err)
		}
	case <-time.After(qmuxdTimeout):
		t.Fatalf("qmuxd did not exit in %v", qmuxdTimeout)
	}
}

// queryResult is the part of the "qmuxctl query --json" output that the tests read.
type queryResult struct {
	Verified   bool   `json:"verified"`
	ProtocolID string `json:"protocol_id"`
	Status     string `json:"status"`
	Payload    string `json:"payload"`
}

// qmuxctlQuery runs "qmuxctl query --json" for name at the protocol key of p, and
// returns the exit code, stdout and stderr.
func qmuxctlQuery(t *testing.T, bins tools, addr, anchors string, p exampleProtocol, name string) (int, []byte, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bins.qmuxctl, "query", "--json", "--server", addr,
		"--class", strconv.Itoa(int(p.Class)), "--type", strconv.Itoa(int(p.Type)),
		"--version", "1", "--name", name, "--trust-anchors", anchors)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Fatalf("qmuxctl query: %v", err)
	}
	return cmd.ProcessState.ExitCode(), stdout.Bytes(), stderr.String()
}

func TestQmuxdServesTheExampleConfigurationAfterKeygen(t *testing.T) { // FR-qmuxd-37, FR-qmuxd-38, FR-qmuxd-39, FR-qmuxd-42
	bins := buildTools(t)
	cfg, protocols := readExample(t)
	if got := cfg["signing_key"]; got != "keys/qmuxd.key" {
		t.Errorf("signing_key = %v, want keys/qmuxd.key", got)
	}
	if zones, _ := cfg["zones"].([]any); !slices.Equal(zones, []any{"fleet.example."}) {
		t.Errorf("zones = %v, want [fleet.example.]", cfg["zones"])
	}

	dir := t.TempDir()
	b, err := os.ReadFile(exampleConfig)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// The test serves an unchanged copy of the example configuration file.
	if err := os.WriteFile(filepath.Join(dir, "qmuxd.json"), b, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	anchors := keygen(t, bins, dir)
	_, addr := startQmuxd(t, bins, dir, "serve", "--config", "qmuxd.json")

	if len(protocols) != len(exampleDecoders) {
		t.Errorf("the example has %d protocols, want %d", len(protocols), len(exampleDecoders))
	}
	for _, p := range protocols {
		verifyExampleRecord(t, bins, addr, anchors, p)
	}

	// The discovery manifest must verify too, and list every configured protocol.
	ids := discoveredIDs(t, bins, addr, anchors)
	want := []string{discovery.ProtocolID}
	for _, p := range protocols {
		want = append(want, p.ID)
	}
	for _, want := range want {
		if !slices.Contains(ids, want) {
			t.Errorf("manifest protocols %v have no %s", ids, want)
		}
	}
}

// verifyExampleRecord queries the device42 record of p through qmuxctl. It fails the
// test when the answer does not verify or the payload does not decode with the
// schema type of p.
func verifyExampleRecord(t *testing.T, bins tools, addr, anchors string, p exampleProtocol) {
	t.Helper()
	decode, ok := exampleDecoders[p.ID]
	if !ok {
		t.Errorf("protocol %s is not a demonstration protocol", p.ID)
		return
	}
	code, out, stderr := qmuxctlQuery(t, bins, addr, anchors, p, "device42.fleet.example.")
	if code != 0 {
		t.Errorf("%s: exit code = %d, want 0: %s", p.ID, code, stderr)
		return
	}
	var res queryResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("%s: Unmarshal %s: %v", p.ID, out, err)
	}
	if !res.Verified || res.ProtocolID != p.ID || res.Status != "ok" {
		t.Errorf("%s: verified, protocol_id, status = %t, %s, %s; want true, %s, ok",
			p.ID, res.Verified, res.ProtocolID, res.Status, p.ID)
	}
	payload, err := hex.DecodeString(res.Payload)
	if err != nil {
		t.Fatalf("%s: payload %q: %v", p.ID, res.Payload, err)
	}
	if err := decode(payload); err != nil {
		t.Errorf("%s: the payload does not decode with the schema type: %v", p.ID, err)
	}
}

// discoveredIDs runs "qmuxctl discover" for fleet.example. with the trust anchor file,
// and returns the protocol IDs of the verified manifest.
func discoveredIDs(t *testing.T, bins tools, addr, anchors string) []string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bins.qmuxctl, "discover", "--json", "--server", addr,
		"--name", "fleet.example.", "--trust-anchors", anchors)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("qmuxctl discover: %v: %s", err, stderr.String())
	}
	var manifest struct {
		Protocols []struct {
			ID string `json:"id"`
		} `json:"protocols"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &manifest); err != nil {
		t.Fatalf("Unmarshal %s: %v", stdout.Bytes(), err)
	}
	var ids []string
	for _, e := range manifest.Protocols {
		ids = append(ids, e.ID)
	}
	return ids
}

func TestQmuxdRefusesQueriesAboveTheRateLimitAndLogsEveryQuery(t *testing.T) { // FR-qmuxd-43, FR-qmuxd-44
	bins := buildTools(t)
	cfg, protocols := readExample(t)
	// One token, and the next token after 100 s, so the second query finds an empty
	// bucket on any machine.
	cfg["rate_limit"] = map[string]any{"qps": 0.01, "burst": 1}
	cfg["observation_log"] = "observations.jsonl"
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "qmuxd.json"), cfg)
	anchors := keygen(t, bins, dir)
	cmd, addr := startQmuxd(t, bins, dir, "serve", "--config", "qmuxd.json")

	p := protocols[0]
	if code, _, stderr := qmuxctlQuery(t, bins, addr, anchors, p, "device42.fleet.example."); code != 0 {
		t.Fatalf("first query: exit code = %d, want 0: %s", code, stderr)
	}
	code, _, stderr := qmuxctlQuery(t, bins, addr, anchors, p, "device42.fleet.example.")
	if code != 1 || !strings.Contains(stderr, "rcode REFUSED") {
		t.Errorf("second query: exit code = %d, stderr = %q; want 1 and rcode REFUSED", code, stderr)
	}
	stopQmuxd(t, cmd)

	b, err := os.ReadFile(filepath.Join(dir, "observations.jsonl"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d observation log lines, want 2:\n%s", len(lines), b)
	}
	var rcodes []string
	for i, line := range lines {
		var obs map[string]any
		if err := json.Unmarshal([]byte(line), &obs); err != nil {
			t.Fatalf("line %d %q: %v", i+1, line, err)
		}
		if keys := slices.Sorted(maps.Keys(obs)); !slices.Equal(keys, observationFields) {
			t.Errorf("line %d fields = %v, want %v", i+1, keys, observationFields)
		}
		rcode, _ := obs["rcode"].(string)
		rcodes = append(rcodes, rcode)
		// A refused query gets no envelope, so its status is null (FR-qmuxd-34).
		if rcode == "REFUSED" && obs["status"] != nil {
			t.Errorf("line %d: status = %v for REFUSED, want null", i+1, obs["status"])
		}
	}
	if !slices.Equal(rcodes, []string{"NOERROR", "REFUSED"}) {
		t.Errorf("rcodes = %v, want [NOERROR REFUSED]", rcodes)
	}
}
