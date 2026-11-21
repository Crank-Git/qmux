//go:build dockerbench

// This test starts bench-compose.yaml in Docker, so it runs only with the build tag:
//
//	go test -tags dockerbench -run Docker -v ./netem/
//
// It uses the project name qmux-bench and removes the project with down -v at the end.
package netem

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const composeFile = "../bench-compose.yaml"

// noNetAdmin removes the capability NET_ADMIN from both services.
// Reference: https://docs.docker.com/reference/compose-file/merge/ (the !reset tag).
const noNetAdmin = `services:
  bench-server:
    cap_add: !reset []
  bench-client:
    cap_add: !reset []
`

func dockerCompose(t *testing.T, c Compose, args ...string) string {
	t.Helper()
	full := []string{"compose", "-p", c.Project}
	for _, f := range c.Files {
		full = append(full, "-f", f)
	}
	out, err := exec.CommandContext(t.Context(), c.Docker, append(full, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(append(full, args...), " "), err, out)
	}
	return string(out)
}

// startCompose starts the project and removes it, with its volumes, when the test ends.
func startCompose(t *testing.T, c Compose) {
	t.Helper()
	t.Cleanup(func() {
		// t.Context is done in a cleanup, so the cleanup needs a command of its own.
		args := []string{"compose", "-p", c.Project}
		for _, f := range c.Files {
			args = append(args, "-f", f)
		}
		if out, err := exec.Command(c.Docker, append(args, "down", "-v")...).CombinedOutput(); err != nil {
			t.Errorf("compose down -v: %v\n%s", err, out)
		}
	})
	dockerCompose(t, c, "up", "-d", "--build", "--wait")
}

func TestDockerTcQdiscShowPrintsTheNetemOfTheLossLevelInEachContainer(t *testing.T) {
	c := Compose{Docker: "docker", Project: "qmux-bench", Files: []string{composeFile}}
	startCompose(t, c)
	for _, tc := range []struct {
		loss float64
		want string
	}{
		{0, "delay 10ms"},
		{10, "delay 10ms loss 10%"},
	} {
		if err := c.Apply(t.Context(), tc.loss, "bench-server", "bench-client"); err != nil {
			t.Fatalf("Apply(%v): %v", tc.loss, err)
		}
		for _, service := range []string{"bench-server", "bench-client"} {
			out := dockerCompose(t, c, "exec", "-T", service, "tc", "qdisc", "show", "dev", "eth0")
			t.Logf("loss %v%%, %s: %s", tc.loss, service, strings.TrimSpace(out))
			if !strings.Contains(out, "netem") || !strings.Contains(out, tc.want) {
				t.Errorf("loss %v%%, %s: tc qdisc show = %q, want netem with %q", tc.loss, service, out, tc.want)
			}
		}
	}
}

func TestDockerApplyReportsNetemNeedsNetAdminInAContainerWithoutTheCapability(t *testing.T) {
	override := filepath.Join(t.TempDir(), "no-net-admin.yaml")
	if err := os.WriteFile(override, []byte(noNetAdmin), 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}
	c := Compose{Docker: "docker", Project: "qmux-bench", Files: []string{composeFile, override}}
	startCompose(t, c)
	err := c.Apply(t.Context(), 10, "bench-server", "bench-client")
	t.Logf("Apply error: %v", err)
	if !errors.Is(err, ErrNeedsNetAdmin) {
		t.Fatalf("Apply error = %v, want ErrNeedsNetAdmin", err)
	}
	if !strings.HasPrefix(err.Error(), "netem needs NET_ADMIN: ") {
		t.Errorf("Apply error = %q, want the prefix %q", err, "netem needs NET_ADMIN: ")
	}
}
