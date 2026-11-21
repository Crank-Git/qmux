package interoperability

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDocker records each call and its QMUX_REQUIRE_DIRECT value in FAKE_LOG.
// FAKE_UP_STATUS and FAKE_WAIT_STATUS set the exit codes of "up" and "wait".
const fakeDocker = `#!/bin/sh
echo "$* QMUX_REQUIRE_DIRECT=${QMUX_REQUIRE_DIRECT-unset}" >> "$FAKE_LOG"
case " $* " in
  *" up "*)
    if [ -e results/results.json ]; then echo "stale results.json" >> "$FAKE_LOG"; fi
    exit "${FAKE_UP_STATUS:-0}" ;;
  *" wait "*) exit "${FAKE_WAIT_STATUS:-0}" ;;
esac
exit 0
`

type scriptRun struct {
	dir   string
	calls []string
	out   string
	code  int
}

// runInteropScript runs a copy of interop.sh in a temporary directory, so that the
// run cannot change the results directory of the checkout.
func runInteropScript(t *testing.T, env ...string) scriptRun {
	t.Helper()
	return runInteropScriptIn(t, t.TempDir(), env...)
}

func runInteropScriptIn(t *testing.T, dir string, env ...string) scriptRun {
	t.Helper()
	script, err := os.ReadFile("interop.sh")
	if err != nil {
		t.Fatalf("read interop.sh: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "interop.sh"), script, 0o755); err != nil {
		t.Fatalf("copy interop.sh: %v", err)
	}
	docker := filepath.Join(dir, "docker")
	if err := os.WriteFile(docker, []byte(fakeDocker), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	logPath := filepath.Join(dir, "docker.log")

	cmd := exec.Command("sh", filepath.Join(dir, "interop.sh"))
	cmd.Env = append(os.Environ(), "DOCKER="+docker, "FAKE_LOG="+logPath, "REQUIRE_DIRECT=")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	run := scriptRun{dir: dir, out: string(out)}
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		run.code = exitErr.ExitCode()
	case err != nil:
		t.Fatalf("run interop.sh: %v", err)
	}
	if data, err := os.ReadFile(logPath); err == nil {
		run.calls = strings.Split(strings.TrimSpace(string(data)), "\n")
	}
	return run
}

func (r scriptRun) indexOf(t *testing.T, sub string) int {
	t.Helper()
	for i, c := range r.calls {
		if strings.Contains(c, sub) {
			return i
		}
	}
	return -1
}

func (r scriptRun) assertStoppedLast(t *testing.T) {
	t.Helper()
	if len(r.calls) == 0 {
		t.Fatalf("interop.sh called docker zero times; output:\n%s", r.out)
	}
	last := r.calls[len(r.calls)-1]
	if !strings.Contains(last, "compose -p qmux-interop -f compose.yaml down -v") {
		t.Fatalf("last docker call = %q, want compose down -v; calls:\n%s", last, strings.Join(r.calls, "\n"))
	}
}

func TestInteropStopsTheEnvironmentAfterAProbeThatSucceeds(t *testing.T) {
	r := runInteropScript(t)
	if r.code != 0 {
		t.Fatalf("exit code = %d, want 0; output:\n%s", r.code, r.out)
	}
	if got := r.indexOf(t, "down -v"); got != 0 {
		t.Errorf("first down -v at call %d, want 0, so that keygen starts from an empty volume", got)
	}
	up, wait := r.indexOf(t, " up -d --build"), r.indexOf(t, " wait probe")
	if up < 0 || wait < up {
		t.Errorf("up at call %d and wait probe at call %d, want up before wait; calls:\n%s", up, wait, strings.Join(r.calls, "\n"))
	}
	r.assertStoppedLast(t)
}

func TestInteropStopsTheEnvironmentAndKeepsTheExitCodeWhenTheProbeFails(t *testing.T) {
	r := runInteropScript(t, "FAKE_WAIT_STATUS=1")
	if r.code != 1 {
		t.Fatalf("exit code = %d, want 1; output:\n%s", r.code, r.out)
	}
	r.assertStoppedLast(t)
}

func TestInteropPrintsTheServiceLogsAndStopsWhenAServiceIsNotReady(t *testing.T) {
	r := runInteropScript(t, "FAKE_UP_STATUS=1")
	if r.code != 1 {
		t.Fatalf("exit code = %d, want 1; output:\n%s", r.code, r.out)
	}
	if r.indexOf(t, " logs") < 0 {
		t.Errorf("interop.sh did not print the service logs; calls:\n%s", strings.Join(r.calls, "\n"))
	}
	if r.indexOf(t, " wait probe") >= 0 {
		t.Errorf("interop.sh waited for the probe after up failed")
	}
	r.assertStoppedLast(t)
}

func TestInteropPrintsDockerNotFoundAndExits1WhenDockerIsMissing(t *testing.T) {
	r := runInteropScript(t, "DOCKER=/nonexistent/docker")
	if r.code != 1 {
		t.Fatalf("exit code = %d, want 1", r.code)
	}
	if !strings.Contains(r.out, "docker not found") {
		t.Errorf("output = %q, want it to hold %q", r.out, "docker not found")
	}
}

func TestInteropPassesRequireDirectToTheProbeService(t *testing.T) {
	for _, tc := range []struct{ env, want string }{
		{"REQUIRE_DIRECT=", "QMUX_REQUIRE_DIRECT=false"},
		{"REQUIRE_DIRECT=true", "QMUX_REQUIRE_DIRECT=true"},
	} {
		r := runInteropScript(t, tc.env)
		up := r.indexOf(t, " up -d --build")
		if up < 0 || !strings.Contains(r.calls[up], tc.want) {
			t.Errorf("%s: up call does not carry %s; calls:\n%s", tc.env, tc.want, strings.Join(r.calls, "\n"))
		}
	}
}

func TestInteropMakesTheResultsDirectoryWritableAndRemovesAnOldResultsFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "results"), 0o755); err != nil {
		t.Fatalf("mkdir results: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "results", "results.json"), []byte("[]"), 0o644); err != nil {
		t.Fatalf("write old results.json: %v", err)
	}
	r := runInteropScriptIn(t, dir)
	// An old results.json would let a failed run look like a run that wrote its results.
	if r.indexOf(t, "stale results.json") >= 0 {
		t.Errorf("results.json from an earlier run exists when the environment starts")
	}
	info, err := os.Stat(filepath.Join(r.dir, "results"))
	if err != nil {
		t.Fatalf("stat results: %v", err)
	}
	// The probe runs as user 65532, which does not own the directory on a Linux host.
	if info.Mode().Perm() != 0o777 {
		t.Errorf("results mode = %v, want 0777", info.Mode().Perm())
	}
}
