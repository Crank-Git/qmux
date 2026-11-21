// Package netem applies the delay and the packet loss of a loss level to the containers
// of bench-compose.yaml (FR-bench-13 and FR-bench-14).
//
// netem shapes the egress of an interface. The step runs tc in both containers, so each
// direction gets the loss level. Reference: https://man7.org/linux/man-pages/man8/tc-netem.8.html
package netem

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// ErrNeedsNetAdmin reports that a container refused the tc change, because it does not
// have the capability NET_ADMIN. Its message is the prefix of FR-bench-14.
var ErrNeedsNetAdmin = errors.New("netem needs NET_ADMIN")

// tc prints this text when the kernel refuses a qdisc change with EPERM.
const operationNotPermitted = "Operation not permitted"

// Args returns the tc command of FR-bench-13 for a loss level in percent.
func Args(loss float64) []string {
	return []string{
		"tc", "qdisc", "replace", "dev", "eth0", "root", "netem",
		"delay", "10ms", "loss", strconv.FormatFloat(loss, 'f', -1, 64) + "%",
	}
}

// Compose runs tc in the services of one Docker Compose project with
// `docker compose exec`. Docker is the docker command, Project is the project name
// and Files are the compose files.
type Compose struct {
	Docker  string
	Project string
	Files   []string
}

// Apply runs the tc command of FR-bench-13 in each service, in order, and stops at the
// first failure.
//
// The loss level is a percentage from 0 to 100. Apply returns an error that wraps
// ErrNeedsNetAdmin when tc reports "Operation not permitted". The message is then
// `netem needs NET_ADMIN: <tc error>`. Another tc failure returns an error that names
// the service and holds the tc output.
func (c Compose) Apply(ctx context.Context, loss float64, services ...string) error {
	if !(loss >= 0 && loss <= 100) {
		return fmt.Errorf("netem: loss level %v%% is not in the range 0 to 100", loss)
	}
	for _, service := range services {
		args := []string{"compose", "-p", c.Project}
		for _, f := range c.Files {
			args = append(args, "-f", f)
		}
		args = append(args, "exec", "-T", service)
		args = append(args, Args(loss)...)
		out, err := exec.CommandContext(ctx, c.Docker, args...).CombinedOutput()
		if err == nil {
			continue
		}
		tcErr := strings.TrimSpace(string(out))
		if bytes.Contains(out, []byte(operationNotPermitted)) {
			return fmt.Errorf("%w: %s", ErrNeedsNetAdmin, tcErr)
		}
		return fmt.Errorf("netem: %s: %w: %s", service, err, tcErr)
	}
	return nil
}
