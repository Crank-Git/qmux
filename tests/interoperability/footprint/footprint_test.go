//go:build linux || darwin

package footprint

import (
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Crank-Git/qmux/tests/interoperability/baseline"
)

// moduleDir is the directory of the harness module, relative to this package.
const moduleDir = ".."

// start starts the baseline servers on loopback ports that the operating system
// selects, and stops them at the end of the test.
func start(t *testing.T, tamper bool) baseline.Target {
	t.Helper()
	r, err := baseline.Start(t.Context(), baseline.Config{
		DNSAddr:   "127.0.0.1:0",
		CoAPAddr:  "127.0.0.1:0",
		HTTPSAddr: "127.0.0.1:0",
		Tamper:    tamper,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return r.Target()
}

func TestMaxRSSKiBReadsDarwinBytesAndLinuxKibibytes(t *testing.T) {
	tests := []struct {
		goos   string
		maxrss int64
		want   int64
	}{
		// getrusage(2) on macOS gives ru_maxrss in bytes.
		{"darwin", 8 * 1024 * 1024, 8 * 1024},
		// getrusage(2) on Linux gives ru_maxrss in KiB.
		{"linux", 8 * 1024, 8 * 1024},
	}
	for _, tt := range tests {
		if got := maxRSSKiB(tt.goos, tt.maxrss); got != tt.want {
			t.Errorf("maxRSSKiB(%q, %d) = %d, want %d", tt.goos, tt.maxrss, got, tt.want)
		}
	}
}

func TestMeasureGivesEveryBaselineANonzeroSizeRSSAndCPUTime(t *testing.T) {
	target := start(t, false)
	got, err := Measure(t.Context(), Config{ModuleDir: moduleDir, BinDir: t.TempDir(), Target: target})
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}
	names := make([]string, len(got))
	for i, r := range got {
		names[i] = r.Baseline
		t.Logf("%s: binary_bytes %d, max_rss_kib %d, cpu_ms %.1f", r.Baseline, r.BinaryBytes, r.MaxRSSKiB, r.CPUMs)
		if r.BinaryBytes <= 0 || r.MaxRSSKiB <= 0 || r.CPUMs <= 0 {
			t.Errorf("%s = %+v, want nonzero binary_bytes, max_rss_kib and cpu_ms", r.Baseline, r)
		}
	}
	if !slices.Equal(names, baseline.Names) {
		t.Fatalf("baselines = %v, want %v", names, baseline.Names)
	}
}

func TestMeasureFailsWhenAClientGetsABadSignature(t *testing.T) {
	target := start(t, true)
	_, err := Measure(t.Context(), Config{ModuleDir: moduleDir, BinDir: t.TempDir(), Target: target})
	if err == nil {
		t.Fatal("Measure against a tampering server returned nil, want the client error")
	}
	if !strings.Contains(err.Error(), "ErrBadSignature") {
		t.Fatalf("Measure error = %v, want the stderr of the client, which names the bad signature", err)
	}
}

func TestMeasureStopsAndReturnsTheBuildOutputWhenAClientFailsToBuild(t *testing.T) {
	_, err := Measure(t.Context(), Config{
		ModuleDir: filepath.Join("testdata", "broken"),
		BinDir:    t.TempDir(),
		Target:    baseline.Target{DNS: "127.0.0.1:1", CoAP: "127.0.0.1:1", HTTPS: "127.0.0.1:1"},
	})
	var be *BuildError
	if !errors.As(err, &be) {
		t.Fatalf("Measure error = %v, want a *BuildError", err)
	}
	if be.Baseline != baseline.QMuxPrivate {
		t.Errorf("BuildError.Baseline = %q, want %q, the first baseline", be.Baseline, baseline.QMuxPrivate)
	}
	if !strings.Contains(string(be.Output), "undefined: notDefined") {
		t.Errorf("BuildError.Output = %q, want the compiler error", be.Output)
	}
}

// The footprint of a client is only a fair cost of its protocol when the client links
// no code of another baseline.
func TestEachClientLinksOnlyThePackagesOfItsProtocol(t *testing.T) {
	const (
		coap = "github.com/plgd-dev/go-coap/v3"
		http = "net/http"
		dns  = "github.com/miekg/dns"
	)
	forbidden := map[string][]string{
		baseline.QMuxPrivate: {coap, http},
		baseline.DNSIn:       {coap, http},
		baseline.DNSInTXT:    {coap, http},
		baseline.CoAP:        {http, dns},
		baseline.HTTPSCold:   {coap, dns},
		baseline.HTTPSReuse:  {coap, dns},
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("find the go command: %v", err)
	}
	for _, name := range baseline.Names {
		cmd := exec.CommandContext(t.Context(), goTool, "list", "-deps", "./clients/"+name)
		cmd.Dir = moduleDir
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list -deps ./clients/%s: %v", name, err)
		}
		for _, dep := range strings.Fields(string(out)) {
			for _, f := range forbidden[name] {
				if dep == f || strings.HasPrefix(dep, f+"/") {
					t.Errorf("client %s links %s", name, dep)
				}
			}
		}
	}
}
