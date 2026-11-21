package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// rollbackState is the device state of the rollback case: its local generation 9 is
// greater than the default remote generation 7.
const rollbackState = `{"config_generation":9,"firmware_version":"2.4.1"}` + "\n"

// reconciledState is the device state that the stale case writes.
const reconciledState = `{"config_generation":7,"firmware_version":"2.4.1"}` + "\n"

// demoCase is one case of FR-demo-45. The program runs from its entry point against
// the server and the mock HTTPS service on loopback.
type demoCase struct {
	name  string
	state string
	flags []string
	// code is the exit code of run.
	code int
	// requests is the HTTPS request count of the mock HTTPS service.
	requests int64
	// lines are step log lines that the output must hold.
	lines []string
	// result is the last line of the output.
	result string
	// wantState is the content of the state file after the run.
	wantState string
	// files are the names in the data directory after the run.
	files []string
}

var demoCases = []demoCase{
	{
		name:     "stale",
		state:    staleState,
		code:     0,
		requests: 2,
		lines: []string{
			"[device]  configuration: digest ok, wrote config-7.bin",
			"[device]  firmware: digest ok, stored firmware-2.4.1.img (not flashed)",
			"[device]  state written: config_generation=7 firmware_version=2.4.1",
		},
		result:    "[device]  result: reconciled (2 downloads)",
		wantState: reconciledState,
		files:     []string{"config-7.bin", "firmware-2.4.1.img"},
	},
	{
		name:     "current",
		state:    currentState,
		code:     0,
		requests: 0,
		lines: []string{
			"[device]  configuration: remote generation 7 = local 7, up to date",
			"[device]  firmware: remote 2.4.1 = local 2.4.1, up to date",
		},
		result:    "[device]  result: up to date (0 downloads)",
		wantState: currentState,
	},
	{
		name:     "rollback",
		state:    rollbackState,
		code:     0,
		requests: 0,
		lines: []string{
			"[device]  configuration: remote generation 7 < local 9, rollback refused",
		},
		result:    "[device]  result: up to date (0 downloads)",
		wantState: rollbackState,
	},
	{
		name:     "rollback to the generation of the screen",
		state:    staleState,
		flags:    []string{"--remote-generation", "2", "--remote-firmware", "2.3.0"},
		code:     0,
		requests: 0,
		lines: []string{
			"[device]  configuration: remote generation 2 < local 3, rollback refused",
		},
		result:    "[device]  result: up to date (0 downloads)",
		wantState: staleState,
	},
	{
		name:     "tamper dns",
		state:    staleState,
		flags:    []string{"--tamper-dns"},
		code:     1,
		requests: 0,
		lines: []string{
			"[device]  configuration: verification failed: bad signature",
		},
		result:    "[device]  result: failed",
		wantState: staleState,
	},
	{
		name:  "tamper https",
		state: staleState,
		flags: []string{"--tamper-https"},
		code:  1,
		// The device downloads the configuration bundle and stops at the digest
		// mismatch, so it never asks for the firmware image.
		requests: 1,
		lines: []string{
			"[device]  configuration: digest mismatch, resource discarded",
		},
		result:    "[device]  result: failed, state unchanged",
		wantState: staleState,
	},
	{
		name:     "tamper https on a current device",
		state:    currentState,
		flags:    []string{"--tamper-https"},
		code:     0,
		requests: 0,
		lines:    []string{noEffectLine},
		result:   "[device]  result: up to date (0 downloads)",
		// The device downloads no configuration bundle, so the tampered bundle is
		// never served and the device state stays the same.
		wantState: currentState,
	},
	{
		// The README runs this step after the stale case, so the device state holds
		// generation 7 and a higher remote generation makes the device download.
		name:     "tamper https after the stale case with a higher remote generation",
		state:    reconciledState,
		flags:    []string{"--remote-generation", "8", "--tamper-https"},
		code:     1,
		requests: 1,
		lines: []string{
			"[device]  configuration: digest mismatch, resource discarded",
		},
		result:    "[device]  result: failed, state unchanged",
		wantState: reconciledState,
	},
}

// noEffectLine is the line that the program prints when --tamper-https is set and
// the device downloads no configuration bundle.
const noEffectLine = "[device]  tamper: --tamper-https has no effect, the device downloads no configuration bundle; " +
	"use a new --state path or a higher --remote-generation"

func TestAnUntamperedCurrentDevicePrintsNoTamperLine(t *testing.T) {
	_, args := files(t, currentState)
	out, code := runDemo(t, args...)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if strings.Contains(out, "tamper:") {
		t.Errorf("output holds a tamper line:\n%s", out)
	}
}

func TestEachDemonstrationCaseGivesItsExitCodeOutputFilesAndHTTPSRequestCount(t *testing.T) { // FR-demo-45
	for _, tc := range demoCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("output, exit code and files", func(t *testing.T) {
				statePath, args := files(t, tc.state)
				out, code := runDemo(t, append(args, tc.flags...)...)
				if code != tc.code {
					t.Errorf("exit code = %d, want %d", code, tc.code)
				}
				lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
				for _, line := range tc.lines {
					if !slices.Contains(lines, line) {
						t.Errorf("output does not hold the line %q", line)
					}
				}
				if last := lines[len(lines)-1]; last != tc.result {
					t.Errorf("last line = %q, want %q", last, tc.result)
				}
				if t.Failed() {
					t.Logf("output:\n%s", out)
				}
				if b := readFile(t, statePath); string(b) != tc.wantState {
					t.Errorf("state file = %q, want %q", b, tc.wantState)
				}
				dir := filepath.Dir(statePath)
				if got := names(t, filepath.Join(dir, "device-data")); !slices.Equal(got, tc.files) {
					t.Errorf("data directory holds %q, want %q", got, tc.files)
				}
				// FR-demo-35: the atomic write leaves no temporary file next to the state file.
				for _, name := range names(t, dir) {
					if name != "device-state.json" && name != "device-data" {
						t.Errorf("state directory holds the unexpected file %q", name)
					}
				}
			})
			t.Run("https requests", func(t *testing.T) {
				_, args := files(t, tc.state)
				cfg, err := parse(append(args, tc.flags...), &bytes.Buffer{})
				if err != nil {
					t.Fatalf("parse: %v", err)
				}
				var out bytes.Buffer
				requests, err := demo(ctxT(t), cfg, &out)
				if (err == nil) != (tc.code == 0) {
					t.Errorf("demo error = %v, want an error only for exit code 1", err)
				}
				if requests != tc.requests {
					t.Errorf("mock HTTPS service counts %d requests, want %d:\n%s", requests, tc.requests, out.String())
				}
			})
		})
	}
}

// names returns the sorted file names in dir. A directory that does not exist holds
// no names.
func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}
