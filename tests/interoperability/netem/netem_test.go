package netem

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeDocker records each call in FAKE_LOG. When FAKE_TC_ERROR is set, the call writes it
// to stderr and exits 2, as tc does when the kernel refuses the change.
const fakeDocker = `#!/bin/sh
echo "$*" >> "$FAKE_LOG"
if [ -n "${FAKE_TC_ERROR:-}" ]; then
  echo "$FAKE_TC_ERROR" >&2
  exit 2
fi
exit 0
`

// newFakeCompose returns a Compose that calls a fake docker command and the path of its
// call log.
func newFakeCompose(t *testing.T, tcError string) (Compose, string) {
	t.Helper()
	dir := t.TempDir()
	docker := filepath.Join(dir, "docker")
	if err := os.WriteFile(docker, []byte(fakeDocker), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	logPath := filepath.Join(dir, "docker.log")
	t.Setenv("FAKE_LOG", logPath)
	t.Setenv("FAKE_TC_ERROR", tcError)
	return Compose{Docker: docker, Project: "qmux-bench", Files: []string{"bench-compose.yaml"}}, logPath
}

func readCalls(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read docker log: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestArgsAreTheTcCommandOfFRBench13(t *testing.T) {
	for _, tc := range []struct {
		loss float64
		want string
	}{
		{0, "tc qdisc replace dev eth0 root netem delay 10ms loss 0%"},
		{10, "tc qdisc replace dev eth0 root netem delay 10ms loss 10%"},
		{0.5, "tc qdisc replace dev eth0 root netem delay 10ms loss 0.5%"},
		{100, "tc qdisc replace dev eth0 root netem delay 10ms loss 100%"},
	} {
		if got := strings.Join(Args(tc.loss), " "); got != tc.want {
			t.Errorf("Args(%v) = %q, want %q", tc.loss, got, tc.want)
		}
	}
}

func TestApplyRunsTcInEachServiceThroughComposeExec(t *testing.T) {
	c, logPath := newFakeCompose(t, "")
	if err := c.Apply(t.Context(), 10, "bench-server", "bench-client"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := []string{
		"compose -p qmux-bench -f bench-compose.yaml exec -T bench-server tc qdisc replace dev eth0 root netem delay 10ms loss 10%",
		"compose -p qmux-bench -f bench-compose.yaml exec -T bench-client tc qdisc replace dev eth0 root netem delay 10ms loss 10%",
	}
	if got := readCalls(t, logPath); !slices.Equal(got, want) {
		t.Errorf("docker calls =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestApplyReportsNetemNeedsNetAdminWhenTcGetsOperationNotPermitted(t *testing.T) {
	c, logPath := newFakeCompose(t, "RTNETLINK answers: Operation not permitted")
	err := c.Apply(t.Context(), 10, "bench-server", "bench-client")
	if !errors.Is(err, ErrNeedsNetAdmin) {
		t.Fatalf("Apply error = %v, want ErrNeedsNetAdmin", err)
	}
	if want := "netem needs NET_ADMIN: RTNETLINK answers: Operation not permitted"; err.Error() != want {
		t.Errorf("Apply error = %q, want %q", err, want)
	}
	// The first refusal stops the step, so no container keeps a loss level of its own.
	if got := readCalls(t, logPath); len(got) != 1 {
		t.Errorf("docker calls = %d, want 1; calls:\n%s", len(got), strings.Join(got, "\n"))
	}
}

func TestApplyNamesTheServiceAndTheTcOutputForAnotherTcFailure(t *testing.T) {
	c, _ := newFakeCompose(t, "Cannot find device \"eth0\"")
	err := c.Apply(t.Context(), 1, "bench-client")
	if err == nil {
		t.Fatal("Apply error = nil, want an error")
	}
	if errors.Is(err, ErrNeedsNetAdmin) {
		t.Errorf("Apply error = %v, want an error that is not ErrNeedsNetAdmin", err)
	}
	for _, want := range []string{"bench-client", `Cannot find device "eth0"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Apply error = %q, want it to hold %q", err, want)
		}
	}
}

func TestApplyRejectsALossLevelOutsideZeroTo100WithoutACall(t *testing.T) {
	c, logPath := newFakeCompose(t, "")
	for _, loss := range []float64{-1, 100.5} {
		if err := c.Apply(t.Context(), loss, "bench-client"); err == nil {
			t.Errorf("Apply(%v) error = nil, want an error", loss)
		}
	}
	if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("docker was called for an invalid loss level")
	}
}
