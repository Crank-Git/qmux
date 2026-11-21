package integration

import (
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// readmePath is the README of the repository, relative to this package.
const readmePath = "../../README.md"

// repoRoot is the repository root, relative to this package. The quick start runs
// each command from the repository root.
const repoRoot = "../.."

// quickstartCommands returns the commands of the bash blocks in the "Quick start"
// section of the README, in order. Each command is a list of fields.
func quickstartCommands(t *testing.T) [][]string {
	t.Helper()
	b, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	_, section, ok := strings.Cut(string(b), "\n## Quick start\n")
	if !ok {
		t.Fatal("README.md has no Quick start section")
	}
	section, _, _ = strings.Cut(section, "\n## ")
	var cmds [][]string
	inBash := false
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "```bash":
			inBash = true
		case line == "```":
			inBash = false
		case inBash && line != "":
			// The test splits a command at spaces, so it cannot run shell syntax.
			if strings.ContainsAny(line, "\"'$|&;<>\\*") {
				t.Fatalf("quick-start command %q holds shell syntax, which the test cannot run", line)
			}
			cmds = append(cmds, strings.Fields(line))
		}
	}
	if len(cmds) == 0 {
		t.Fatal("README.md Quick start has no bash command")
	}
	return cmds
}

// flagValue returns the value that follows the flag name in args, or "" when args has no
// such flag.
func flagValue(args []string, name string) string {
	if i := slices.Index(args, name); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

// runQuickstartCommand runs name with args in dir and fails the test when the command
// exits with a code other than 0 (FR-docs-21).
func runQuickstartCommand(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

func TestQuickstartCommandsExitWithCodeZero(t *testing.T) { // FR-docs-20, FR-docs-21
	// dir is a copy of the files that the quick start reads, so the commands that
	// write a file do not change the checkout.
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "examples"), 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	cfg, err := os.ReadFile(exampleConfig)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "examples", "qmuxd.json"), cfg, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var bins tools
	var server *exec.Cmd
	var addr string
	for _, c := range quickstartCommands(t) {
		if strings.HasPrefix(c[0], "bin/") && bins.qmuxd == "" {
			t.Fatalf("quick-start command %q runs before make build", strings.Join(c, " "))
		}
		switch {
		case slices.Equal(c, []string{"make", "build"}):
			bins = buildTools(t)
		case c[0] == "make":
			runQuickstartCommand(t, repoRoot, "make", c[1:]...)
		case c[0] == "bin/qmuxd":
			// qmuxd runs until a signal, so the test starts it and stops it at the end.
			server, addr = startQmuxd(t, bins, dir, c[1:]...)
		case c[0] == "bin/qmuxctl":
			args := slices.Clone(c[1:])
			if i := slices.Index(args, "--server"); i >= 0 && i+1 < len(args) {
				if addr == "" {
					t.Fatalf("quick-start command %q runs before qmuxd starts", strings.Join(c, " "))
				}
				// The operating system selects the port of qmuxd, so a running server
				// on the README port does not change the result.
				args[i+1] = addr
			}
			runQuickstartCommand(t, dir, bins.qmuxctl, args...)
		default:
			runQuickstartCommand(t, dir, c[0], c[1:]...)
		}
	}
	if server == nil {
		t.Fatal("README.md Quick start does not start qmuxd")
	}
	stopQmuxd(t, server)
}

func TestQuickstartQueriesTheListenAddressOfTheExampleConfiguration(t *testing.T) {
	cfg, _ := readExample(t)
	listen, _ := cfg["listen"].(string)
	ap, err := netip.ParseAddrPort(listen)
	if err != nil {
		t.Fatalf("listen %q: %v", listen, err)
	}
	// mDNSResponder holds UDP port 5353 on macOS, so qmuxd cannot bind it there (#186).
	if ap.Port() == 5353 {
		t.Errorf("listen = %s, want a port other than 5353", listen)
	}
	for _, c := range quickstartCommands(t) {
		if c[0] == "bin/qmuxd" && flagValue(c, "--listen") != "" {
			t.Errorf("quick-start command %q has --listen, want the listen field of examples/qmuxd.json", strings.Join(c, " "))
		}
		if s := flagValue(c, "--server"); s != "" && s != listen {
			t.Errorf("quick-start command %q queries %s, want the listen address %s", strings.Join(c, " "), s, listen)
		}
	}
}
