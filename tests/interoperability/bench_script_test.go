package interoperability

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBenchDocker records each docker call in FAKE_LOG. FAKE_UP_STATUS sets the exit
// code of "up".
const fakeBenchDocker = `#!/bin/sh
echo "docker $*" >> "$FAKE_LOG"
case " $* " in
  *" up "*) exit "${FAKE_UP_STATUS:-0}" ;;
esac
exit 0
`

// fakeGo records the build in FAKE_LOG and writes FAKE_BENCH to the -o path, so the
// script runs the fake qmux-bench.
const fakeGo = `#!/bin/sh
echo "go $*" >> "$FAKE_LOG"
while [ $# -gt 0 ]; do
  if [ "$1" = "-o" ]; then cp "$FAKE_BENCH" "$2" && chmod +x "$2"; fi
  shift
done
exit "${FAKE_BUILD_STATUS:-0}"
`

// fakeQMuxBench records its arguments in FAKE_LOG, prints FAKE_BENCH_STDERR to
// standard error and exits with FAKE_BENCH_STATUS.
const fakeQMuxBench = `#!/bin/sh
echo "run qmux-bench $*" >> "$FAKE_LOG"
if [ -n "${FAKE_BENCH_STDERR:-}" ]; then echo "$FAKE_BENCH_STDERR" >&2; fi
exit "${FAKE_BENCH_STATUS:-0}"
`

// runBenchScript runs a copy of bench.sh in a temporary directory with a fake docker,
// a fake go and a fake qmux-bench, so the run starts no container.
func runBenchScript(t *testing.T, env ...string) scriptRun {
	t.Helper()
	dir := t.TempDir()
	script, err := os.ReadFile("bench.sh")
	if err != nil {
		t.Fatalf("read bench.sh: %v", err)
	}
	files := map[string]string{"bench.sh": string(script), "docker": fakeBenchDocker, "go": fakeGo, "fake-qmux-bench": fakeQMuxBench}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	logPath := filepath.Join(dir, "calls.log")

	cmd := exec.Command("sh", filepath.Join(dir, "bench.sh"))
	cmd.Env = append(os.Environ(),
		"DOCKER="+filepath.Join(dir, "docker"), "GO="+filepath.Join(dir, "go"),
		"FAKE_BENCH="+filepath.Join(dir, "fake-qmux-bench"), "FAKE_LOG="+logPath, "LOSS=", "N=")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	run := scriptRun{dir: dir, out: string(out)}
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		run.code = exitErr.ExitCode()
	case err != nil:
		t.Fatalf("run bench.sh: %v", err)
	}
	if data, err := os.ReadFile(logPath); err == nil {
		run.calls = strings.Split(strings.TrimSpace(string(data)), "\n")
	}
	return run
}

func (r scriptRun) assertBenchStoppedLast(t *testing.T) {
	t.Helper()
	if len(r.calls) == 0 {
		t.Fatalf("bench.sh made no calls; output:\n%s", r.out)
	}
	last := r.calls[len(r.calls)-1]
	if !strings.Contains(last, "compose -p qmux-bench -f bench-compose.yaml down -v") {
		t.Fatalf("last call = %q, want compose down -v; calls:\n%s", last, strings.Join(r.calls, "\n"))
	}
}

func TestBenchRunsQMuxBenchWithLossAndNBetweenUpAndDown(t *testing.T) {
	r := runBenchScript(t, "LOSS=0", "N=10")
	if r.code != 0 {
		t.Fatalf("exit code = %d, want 0; output:\n%s", r.code, r.out)
	}
	up, bench := r.indexOf(t, " up -d --build --wait"), r.indexOf(t, "run qmux-bench --docker "+filepath.Join(r.dir, "docker")+" --loss 0 --n 10")
	if up < 0 || bench < up {
		t.Errorf("up at call %d and qmux-bench at call %d, want up before qmux-bench; calls:\n%s", up, bench, strings.Join(r.calls, "\n"))
	}
	r.assertBenchStoppedLast(t)
}

func TestBenchKeepsTheQMuxBenchDefaultsWhenLossAndNAreEmpty(t *testing.T) {
	r := runBenchScript(t)
	bench := r.indexOf(t, "run qmux-bench ")
	if bench < 0 {
		t.Fatalf("qmux-bench did not run; calls:\n%s", strings.Join(r.calls, "\n"))
	}
	if c := r.calls[bench]; strings.Contains(c, "--loss") || strings.Contains(c, "--n ") {
		t.Errorf("qmux-bench call %q, want no --loss and no --n flag", c)
	}
}

func TestBenchExits1AndPrintsTheNetemErrorAndStopsTheEnvironmentWhenNetemFails(t *testing.T) {
	msg := "netem needs NET_ADMIN: RTNETLINK answers: Operation not permitted"
	r := runBenchScript(t, "FAKE_BENCH_STATUS=1", "FAKE_BENCH_STDERR="+msg)
	if r.code != 1 {
		t.Fatalf("exit code = %d, want 1; output:\n%s", r.code, r.out)
	}
	if !strings.Contains(r.out, msg) {
		t.Errorf("output = %q, want it to hold %q", r.out, msg)
	}
	r.assertBenchStoppedLast(t)
}

func TestBenchPrintsTheServiceLogsAndStopsWhenAServiceDoesNotStart(t *testing.T) {
	r := runBenchScript(t, "FAKE_UP_STATUS=1")
	if r.code != 1 {
		t.Fatalf("exit code = %d, want 1; output:\n%s", r.code, r.out)
	}
	if r.indexOf(t, " logs") < 0 {
		t.Errorf("bench.sh did not print the service logs; calls:\n%s", strings.Join(r.calls, "\n"))
	}
	if r.indexOf(t, "run qmux-bench ") >= 0 {
		t.Errorf("bench.sh ran qmux-bench after up failed")
	}
	r.assertBenchStoppedLast(t)
}

func TestBenchExits1AndStopsTheEnvironmentWhenQMuxBenchDoesNotBuild(t *testing.T) {
	r := runBenchScript(t, "FAKE_BUILD_STATUS=1")
	if r.code != 1 {
		t.Fatalf("exit code = %d, want 1; output:\n%s", r.code, r.out)
	}
	if r.indexOf(t, "run qmux-bench ") >= 0 {
		t.Errorf("bench.sh ran qmux-bench after the build failed")
	}
	r.assertBenchStoppedLast(t)
}

func TestBenchPrintsDockerNotFoundAndExits1WhenDockerIsMissing(t *testing.T) {
	r := runBenchScript(t, "DOCKER=/nonexistent/docker")
	if r.code != 1 {
		t.Fatalf("exit code = %d, want 1", r.code)
	}
	if !strings.Contains(r.out, "docker not found") {
		t.Errorf("output = %q, want it to hold %q", r.out, "docker not found")
	}
}
