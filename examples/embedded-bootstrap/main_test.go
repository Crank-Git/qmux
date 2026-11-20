package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// staleState is the device state of the stale case in § Screens & states.
const staleState = `{"config_generation":3,"firmware_version":"2.3.0"}` + "\n"

// currentState is the device state that equals the default remote metadata.
const currentState = `{"config_generation":7,"firmware_version":"2.4.1"}` + "\n"

// files returns the --state and --data-dir arguments for a new temporary directory. A
// non-empty state is written to the state file first.
func files(t *testing.T, state string) (statePath string, args []string) {
	t.Helper()
	dir := t.TempDir()
	statePath = filepath.Join(dir, "device-state.json")
	if state != "" {
		if err := os.WriteFile(statePath, []byte(state), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	return statePath, []string{"--state", statePath, "--data-dir", filepath.Join(dir, "device-data")}
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// runDemo runs the program with args and returns standard output and the exit code.
func runDemo(t *testing.T, args ...string) (string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(ctxT(t), args, &stdout, &stderr)
	if stderr.Len() > 0 {
		t.Logf("stderr: %s", stderr.String())
	}
	return stdout.String(), code
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	return b
}

func TestStaleCaseOutputMatchesTheLinePrefixesOfTheScreen(t *testing.T) { // FR-demo-37
	_, args := files(t, staleState)
	out, code := runDemo(t, args...)
	want := []string{
		"[server]  zone fleet.example. on 127.0.0.1:",
		"[https]   mock service on https://127.0.0.1:",
		"[device]  state: config_generation=3 firmware_version=2.3.0",
		"[device]  discovery: verified manifest, 4 protocols",
		"[device]  negotiate: org.qmux.bootstrap v1, org.qmux.configuration v1, org.qmux.firmware v1",
		"[device]  bootstrap: management https://127.0.0.1:",
		"[device]  configuration: remote generation 7 > local 3, downloading",
		"[device]  configuration: digest ok, wrote config-7.bin",
		"[device]  firmware: remote 2.4.1 > local 2.3.0, key id ",
		"[device]  firmware: digest ok, stored firmware-2.4.1.img (not flashed)",
		"[device]  state written: config_generation=7 firmware_version=2.4.1",
		"[device]  result: reconciled (2 downloads)",
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(want), out)
	}
	for i, prefix := range want {
		if !strings.HasPrefix(lines[i], prefix) {
			t.Errorf("line %d = %q, want prefix %q", i+1, lines[i], prefix)
		}
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

func TestStaleCaseExitsWithCode0(t *testing.T) { // FR-demo-38
	_, args := files(t, "")
	if out, code := runDemo(t, args...); code != 0 {
		t.Fatalf("exit code = %d, want 0:\n%s", code, out)
	}
}

func TestCurrentCaseExitsWithCode0AndReportsUpToDate(t *testing.T) { // FR-demo-38
	_, args := files(t, currentState)
	out, code := runDemo(t, args...)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0:\n%s", code, out)
	}
	if !strings.HasSuffix(out, "[device]  result: up to date (0 downloads)\n") {
		t.Fatalf("output does not end with the up-to-date result:\n%s", out)
	}
}

func TestTamperDNSReportsAVerificationFailureAndExitsWithCode1(t *testing.T) { // FR-demo-39, FR-demo-41
	path, args := files(t, staleState)
	out, code := runDemo(t, append(args, "--tamper-dns")...)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	for _, line := range []string{
		"[device]  configuration: verification failed: bad signature\n",
		"[device]  result: failed\n",
	} {
		if !strings.Contains(out, line) {
			t.Errorf("output does not hold %q:\n%s", line, out)
		}
	}
	if b := readFile(t, path); string(b) != staleState {
		t.Errorf("state file = %q, want the bytes before the run %q", b, staleState)
	}
}

func TestTamperDNSSendsNoHTTPSRequest(t *testing.T) { // FR-demo-40, FR-demo-41
	_, args := files(t, staleState)
	cfg, err := parse(append(args, "--tamper-dns"), &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var out bytes.Buffer
	requests, err := demo(ctxT(t), cfg, &out)
	if err == nil {
		t.Fatalf("demo returned no error:\n%s", out.String())
	}
	if requests != 0 {
		t.Fatalf("mock HTTPS service counts %d requests, want 0", requests)
	}
}

func TestTamperHTTPSReportsADigestMismatchAndExitsWithCode1(t *testing.T) { // FR-demo-39, FR-demo-42, FR-demo-43
	path, args := files(t, staleState)
	out, code := runDemo(t, append(args, "--tamper-https")...)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	for _, line := range []string{
		"[device]  configuration: digest mismatch, resource discarded\n",
		"[device]  result: failed, state unchanged\n",
	} {
		if !strings.Contains(out, line) {
			t.Errorf("output does not hold %q:\n%s", line, out)
		}
	}
	if b := readFile(t, path); string(b) != staleState {
		t.Errorf("state file = %q, want the bytes before the run %q", b, staleState)
	}
}

func TestAFlagThatDoesNotParseExitsWithCode2(t *testing.T) {
	if _, code := runDemo(t, "--remote-generation", "seven"); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}
