package interoperability

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	// The harness must build against the root module in this checkout. This import
	// keeps the require in go.mod, so that the replace directive has an effect.
	_ "github.com/Crank-Git/qmux"
)

// listedModule holds the fields of `go list -m -json` that this test reads.
type listedModule struct {
	Path    string
	Replace *struct {
		Path string
		Dir  string
	}
}

// The test asks the go command for the module graph. The build information in the
// test binary is not used, because its dependency list changes between Go versions.
func TestRootModuleResolvesToTheLocalCheckoutThroughReplace(t *testing.T) {
	// The go command puts its own GOROOT/bin first in PATH for a test, so this
	// finds the toolchain that runs the test.
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("find the go command: %v", err)
	}
	out, err := exec.CommandContext(t.Context(), goTool, "list", "-m", "-json", "github.com/Crank-Git/qmux").Output()
	if err != nil {
		t.Fatalf("go list -m github.com/Crank-Git/qmux: %v", err)
	}
	var mod listedModule
	if err := json.Unmarshal(out, &mod); err != nil {
		t.Fatalf("decode go list output: %v", err)
	}
	if mod.Replace == nil || mod.Replace.Path != "../../" {
		t.Fatalf("github.com/Crank-Git/qmux replace = %+v, want path ../../", mod.Replace)
	}

	wantDir, err := filepath.EvalSymlinks(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve the repository root: %v", err)
	}
	wantDir, err = filepath.Abs(wantDir)
	if err != nil {
		t.Fatalf("resolve the repository root: %v", err)
	}
	gotDir, err := filepath.EvalSymlinks(mod.Replace.Dir)
	if err != nil {
		t.Fatalf("resolve the replace directory %q: %v", mod.Replace.Dir, err)
	}
	if gotDir != wantDir {
		t.Fatalf("github.com/Crank-Git/qmux replace directory = %q, want the repository root %q", gotDir, wantDir)
	}
}

// goList returns the output of `go list -m` with args, run in dir.
func goList(t *testing.T, dir string, args ...string) string {
	t.Helper()
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("find the go command: %v", err)
	}
	cmd := exec.CommandContext(t.Context(), goTool, append([]string{"list", "-m"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -m %v in %s: %v", args, dir, err)
	}
	return string(out)
}

func TestHarnessModuleRequiresGoCoAPV382(t *testing.T) {
	got := strings.TrimSpace(goList(t, ".", "github.com/plgd-dev/go-coap/v3"))
	if want := "github.com/plgd-dev/go-coap/v3 v3.8.2"; got != want {
		t.Fatalf("go list -m github.com/plgd-dev/go-coap/v3 = %q, want %q", got, want)
	}
}

// The root module keeps go-coap out, so a user who imports QMux never downloads it
// (FR-bench-3, ADR 0002).
func TestRootModuleDoesNotRequireGoCoAP(t *testing.T) {
	for line := range strings.Lines(goList(t, filepath.Join("..", ".."), "all")) {
		if strings.HasPrefix(line, "github.com/plgd-dev/go-coap") {
			t.Fatalf("go list -m all in the root module shows %q", strings.TrimSpace(line))
		}
	}
}
