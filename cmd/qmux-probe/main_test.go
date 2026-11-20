package main

import (
	"bytes"
	"errors"
	"runtime/debug"
	"strings"
	"testing"
)

func TestVersionPrintsOneVersionLineAndExitsZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"qmux-probe", "version"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	// A test binary has no module version, so the fallback applies.
	if got, want := stdout.String(), "qmux-probe v0.0.0-dev\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestVersionNamesTheBinaryWithoutItsDirectory(t *testing.T) {
	var stdout, stderr bytes.Buffer
	run([]string{"bin/qmux-probe", "version"}, &stdout, &stderr)
	if got, want := stdout.String(), "qmux-probe v0.0.0-dev\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestVersionExitsOneWhenStdoutFails(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{"qmux-probe", "version"}, failingWriter{}, &stderr); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}

func TestHelpExitsOneWhenStdoutFails(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{"qmux-probe", "help"}, failingWriter{}, &stderr); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}

func TestVersionOfReturnsTheStampedModuleVersion(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}}
	if got := versionOf(info, true); got != "v1.2.3" {
		t.Errorf("versionOf = %q, want %q", got, "v1.2.3")
	}
}

func TestVersionOfReturnsTheFallbackWhenTheBuildHasNoVersion(t *testing.T) {
	cases := map[string]struct {
		info *debug.BuildInfo
		ok   bool
	}{
		"no build info": {nil, false},
		"empty version": {&debug.BuildInfo{Main: debug.Module{Version: ""}}, true},
		"devel version": {&debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := versionOf(tc.info, tc.ok); got != "v0.0.0-dev" {
				t.Errorf("versionOf = %q, want %q", got, "v0.0.0-dev")
			}
		})
	}
}

func TestHelpPrintsUsageAndExitsZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"qmux-probe", "help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.HasPrefix(stdout.String(), "Usage: qmux-probe") {
		t.Errorf("stdout = %q, want a usage text", stdout.String())
	}
}

func TestNoSubcommandPrintsUsageToStderrAndExitsOne(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"qmux-probe"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "Usage: qmux-probe") {
		t.Errorf("stdout = %q, stderr = %q, want usage on stderr only", stdout.String(), stderr.String())
	}
}

func TestUnknownSubcommandPrintsAnErrorToStderrAndExitsOne(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"qmux-probe", "unknown"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "Unknown command: unknown") {
		t.Errorf("stdout = %q, stderr = %q, want the error on stderr only", stdout.String(), stderr.String())
	}
}
