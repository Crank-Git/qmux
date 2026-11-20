package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/discovery"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/security"
)

// mainEnv makes the test binary run main in place of the tests, so a test can start
// qmuxd as a separate process and send it a signal.
const mainEnv = "QMUXD_TEST_RUN_MAIN"

// testZone is the zone of every test configuration file.
const testZone = "fleet.example."

// startTimeout limits the wait for the ready record and for the process exit. It is a
// limit, not a sleep: a test continues as soon as the event occurs.
const startTimeout = 10 * time.Second

// testSeed is the fixed seed of the test signing key.
var testSeed = bytes.Repeat([]byte{0x54}, ed25519.SeedSize)

func TestMain(m *testing.M) {
	if os.Getenv(mainEnv) == "1" {
		main()
	}
	os.Exit(m.Run())
}

// writeConfig writes the test signing key and a configuration file qmuxd.json with
// listen into a new directory, and returns the directory. The signing_key path is
// relative, so it resolves from the directory of the file.
func writeConfig(t *testing.T, listen string) string {
	t.Helper()
	dir := t.TempDir()
	if err := security.WritePrivateKey(filepath.Join(dir, "qmuxd.key"), ed25519.NewKeyFromSeed(testSeed)); err != nil {
		t.Fatalf("WritePrivateKey: %v", err)
	}
	cfg, err := json.Marshal(map[string]any{"listen": listen, "zones": []string{testZone}, "signing_key": "qmuxd.key"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "qmuxd.json"), cfg, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return dir
}

// qmuxdProcess is one qmuxd process that a test started.
type qmuxdProcess struct {
	cmd   *exec.Cmd
	ready chan string
	// done closes when the process has exited. waitErr is the error of cmd.Wait, and a
	// test reads it only after done closes.
	done    chan struct{}
	waitErr error
}

// startQmuxd starts "qmuxd serve" with args in the directory dir. The process ends
// when the test ends.
func startQmuxd(t *testing.T, dir string, args ...string) *qmuxdProcess {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"serve"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), mainEnv+"=1")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("StderrPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	p := &qmuxdProcess{cmd: cmd, ready: make(chan string, 1), done: make(chan struct{})}
	go func() {
		// The scan reads stderr to the end, so the per-query log records never fill
		// the pipe. Wait comes after the scan, as exec.Cmd.StderrPipe requires.
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			if strings.Contains(sc.Text(), `msg="qmuxd ready"`) {
				p.ready <- sc.Text()
			}
		}
		p.waitErr = cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		// Kill fails when the process has already exited, which is the expected case
		// for a test that waits for the exit.
		_ = cmd.Process.Kill()
		<-p.done
	})
	return p
}

// waitReady returns the ready record of p. It fails the test when p exits first or
// when no record arrives in startTimeout.
func (p *qmuxdProcess) waitReady(t *testing.T) string {
	t.Helper()
	select {
	case line := <-p.ready:
		return line
	case <-p.done:
		t.Fatalf("qmuxd exited before the ready record: %v", p.waitErr)
	case <-time.After(startTimeout):
		t.Fatalf("no ready record in %v", startTimeout)
	}
	return ""
}

// waitExit returns the exit code of p. It fails the test when p does not exit in limit.
func (p *qmuxdProcess) waitExit(t *testing.T, limit time.Duration) int {
	t.Helper()
	select {
	case <-p.done:
		var exitErr *exec.ExitError
		if p.waitErr != nil && !errors.As(p.waitErr, &exitErr) {
			t.Fatalf("Wait: %v", p.waitErr)
		}
		return p.cmd.ProcessState.ExitCode()
	case <-time.After(limit):
		t.Fatalf("qmuxd did not exit in %v", limit)
	}
	return -1
}

var listenField = regexp.MustCompile(`\blisten=(\S+)`)

// readyAddr returns the listen value of the ready record line.
func readyAddr(t *testing.T, line string) string {
	t.Helper()
	m := listenField.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("ready record has no listen field: %q", line)
	}
	return m[1]
}

// queryDiscovery queries the discovery name of testZone at addr over network, and
// verifies the envelope with the test key. It returns the error of client.Query.
func queryDiscovery(addr, network string) error {
	pub := ed25519.NewKeyFromSeed(testSeed).Public().(ed25519.PublicKey)
	c := client.NewClient(client.ClientConfig{
		Resolver:       addr,
		Network:        network,
		AttemptTimeout: time.Second,
		Attempts:       1,
		TrustAnchors:   &security.Verifier{Anchors: []security.TrustAnchor{{KeyID: security.KeyID(pub), PublicKey: pub}}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	res, err := c.Query(ctx, client.Query{
		Name: "_qmux." + testZone, Class: discovery.DefaultKey.Class, Type: discovery.DefaultKey.Type,
		Version: 1, ExpectedProtocolID: discovery.ProtocolID,
	})
	if err != nil {
		return fmt.Errorf("query over %s: %w", network, err)
	}
	if !res.Verified || res.Status != protocol.StatusOK {
		return fmt.Errorf("query over %s: verified = %t, status = %v, want true and ok", network, res.Verified, res.Status)
	}
	return nil
}

func TestServeReadsQmuxdJSONInTheWorkingDirectoryByDefault(t *testing.T) { // FR-qmuxd-1
	p := startQmuxd(t, writeConfig(t, "127.0.0.1:0"))
	addr := readyAddr(t, p.waitReady(t))
	if err := queryDiscovery(addr, "udp"); err != nil {
		t.Error(err)
	}
}

func TestServeResolvesTheSigningKeyFromTheDirectoryOfTheConfigFile(t *testing.T) {
	dir := writeConfig(t, "127.0.0.1:0")
	p := startQmuxd(t, t.TempDir(), "--config", filepath.Join(dir, "qmuxd.json"))
	addr := readyAddr(t, p.waitReady(t))
	if err := queryDiscovery(addr, "udp"); err != nil {
		t.Error(err)
	}
}

func TestServeListenFlagOverridesTheListenFieldOfTheFile(t *testing.T) { // FR-qmuxd-2
	// The test holds the address of the listen field, so qmuxd exits 1 if it binds
	// that address and not the --listen address.
	held, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	t.Cleanup(func() { _ = held.Close() })
	fileAddr := held.LocalAddr().String()

	p := startQmuxd(t, writeConfig(t, fileAddr), "--listen", "127.0.0.1:0")
	addr := readyAddr(t, p.waitReady(t))
	if addr == fileAddr {
		t.Fatalf("qmuxd listens on %s, the listen value of the file", addr)
	}
	if err := queryDiscovery(addr, "udp"); err != nil {
		t.Error(err)
	}
}

func TestServeAnswersOverUDPAndTCPFromOneProcess(t *testing.T) { // FR-qmuxd-3
	p := startQmuxd(t, writeConfig(t, "127.0.0.1:0"))
	addr := readyAddr(t, p.waitReady(t))
	for _, network := range []string{"udp", "tcp"} {
		if err := queryDiscovery(addr, network); err != nil {
			t.Error(err)
		}
	}
}

func TestServeReadyRecordHoldsListenZonesKeyIDAndProtocols(t *testing.T) { // FR-qmuxd-4
	p := startQmuxd(t, writeConfig(t, "127.0.0.1:0"))
	line := p.waitReady(t)
	id := security.KeyID(ed25519.NewKeyFromSeed(testSeed).Public().(ed25519.PublicKey))
	for _, want := range []string{
		"level=INFO",
		"listen=127.0.0.1:",
		"zones=[" + testZone + "]",
		fmt.Sprintf("key_id=%x", id),
		"protocols=1",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("ready record %q has no %q", line, want)
		}
	}
}

func TestServeExitsZeroOnASignalWithinTheGracePeriod(t *testing.T) { // FR-qmuxd-5
	// The limit is the default grace period of server.ServerConfig.ShutdownGrace.
	const grace = 5 * time.Second
	for name, sig := range map[string]os.Signal{"SIGTERM": syscall.SIGTERM, "SIGINT": os.Interrupt} {
		t.Run(name, func(t *testing.T) {
			p := startQmuxd(t, writeConfig(t, "127.0.0.1:0"))
			p.waitReady(t)
			if err := p.cmd.Process.Signal(sig); err != nil {
				t.Fatalf("Signal: %v", err)
			}
			if code := p.waitExit(t, grace); code != 0 {
				t.Errorf("exit code = %d, want 0", code)
			}
		})
	}
}

func TestServeExitsOneWhenASecondQmuxdUsesTheSameAddress(t *testing.T) { // FR-qmuxd-7
	first := startQmuxd(t, writeConfig(t, "127.0.0.1:0"))
	addr := readyAddr(t, first.waitReady(t))
	second := startQmuxd(t, writeConfig(t, addr))
	if code := second.waitExit(t, startTimeout); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}

func TestServeExitsTwoWhenTheConfigFileDoesNotLoad(t *testing.T) {
	cases := map[string]string{
		"missing file":  "",
		"unknown field": `{"listn": "127.0.0.1:0"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "qmuxd.json")
			if body != "" {
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
			}
			var stdout, stderr bytes.Buffer
			if code := run([]string{"qmuxd", "serve", "--config", path}, &stdout, &stderr); code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
			if !strings.HasPrefix(stderr.String(), "qmuxd: config: ") {
				t.Errorf("stderr = %q, want a config error", stderr.String())
			}
		})
	}
}

func TestServeExitsTwoWhenTheSigningKeyDoesNotLoad(t *testing.T) {
	dir := writeConfig(t, "127.0.0.1:0")
	if err := os.Remove(filepath.Join(dir, "qmuxd.key")); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"qmuxd", "serve", "--config", filepath.Join(dir, "qmuxd.json")}, &stdout, &stderr); code != 2 {
		t.Errorf("exit code = %d, want 2; stderr = %q", code, stderr.String())
	}
}

func TestServeExitsSixtyFourOnAUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"qmuxd", "serve", "extra"}, &stdout, &stderr); code != 64 {
		t.Errorf("exit code = %d, want 64", code)
	}
	if !strings.Contains(stderr.String(), "Usage: qmuxd serve") {
		t.Errorf("stderr = %q, want the usage", stderr.String())
	}
}

func TestServeHelpListsTheConfigAndListenFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"qmuxd", "serve", "--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	for _, f := range []string{"--config ", "--listen "} {
		if !strings.Contains(stdout.String(), f) {
			t.Errorf("help text has no %s:\n%s", f, stdout.String())
		}
	}
}
