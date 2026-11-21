//go:build dockerbench

// This test starts bench-compose.yaml in Docker, so it runs only with the build tag:
//
//	go test -tags dockerbench -run Docker -v -timeout 15m ./cmd/qmux-bench/
//
// It uses the project name qmux-bench and removes the project with down -v at the end.
// The host needs Docker and the Go toolchain.
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Crank-Git/qmux/tests/interoperability/baseline"
)

// compose runs docker compose for the project qmux-bench from tests/interoperability.
func compose(t *testing.T, args ...string) {
	t.Helper()
	full := append([]string{"compose", "-p", "qmux-bench", "-f", "bench-compose.yaml"}, args...)
	if out, err := exec.Command("docker", full...).CombinedOutput(); err != nil {
		t.Fatalf("docker %v: %v\n%s", full, err, out)
	}
}

// benchDocker runs qmux-bench with args against the running compose project and
// returns the runs of the results file.
func benchDocker(t *testing.T, args ...string) map[float64]map[string]Run {
	t.Helper()
	results := t.TempDir()
	var out, errOut bytes.Buffer
	code := run(t.Context(), append(args, "--results", results), &out, &errOut)
	t.Logf("stdout:\n%s\nstderr:\n%s", out.String(), errOut.String())
	if code != 0 {
		t.Fatalf("qmux-bench exit %d", code)
	}
	files, err := filepath.Glob(filepath.Join(results, "bench-*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("results files %v, %v: want one", files, err)
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var res Results
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Clients) != len(baseline.Names) || len(res.Dispatch) != 2 {
		t.Errorf("%d clients and %d dispatch results, want %d and 2", len(res.Clients), len(res.Dispatch), len(baseline.Names))
	}
	runs := make(map[float64]map[string]Run)
	for _, r := range res.Runs {
		if runs[r.Loss] == nil {
			runs[r.Loss] = make(map[string]Run)
		}
		runs[r.Loss][r.Baseline] = r
	}
	return runs
}

func TestDockerBenchMeasuresEveryBaselineAtLossZeroAndLossHundred(t *testing.T) {
	t.Chdir("../..")
	t.Cleanup(func() {
		if out, err := exec.Command("docker", "compose", "-p", "qmux-bench", "-f", "bench-compose.yaml", "down", "-v").CombinedOutput(); err != nil {
			t.Errorf("compose down -v: %v\n%s", err, out)
		}
	})
	compose(t, "up", "-d", "--build", "--wait")

	zero := benchDocker(t, "--loss", "0", "--n", "10")[0]
	for _, name := range baseline.Names {
		r := zero[name]
		if r.N != 10 || r.SuccessRate != 1 || r.PacketsPerLookup == 0 || r.BytesPerLookup == 0 {
			t.Errorf("loss 0, %s: %+v, want n 10, success_rate 1 and nonzero packets and bytes", name, r)
		}
	}
	if r := zero[baseline.QMuxPrivate]; r.RequestsPerLookup != 1 {
		t.Errorf("loss 0, qmux-private: %v requests per lookup, want 1", r.RequestsPerLookup)
	}
	if cold, reuse := zero[baseline.HTTPSCold], zero[baseline.HTTPSReuse]; cold.PacketsPerLookup <= reuse.PacketsPerLookup {
		t.Errorf("https-cold %v packets per lookup, https-reuse %v: want more for https-cold", cold.PacketsPerLookup, reuse.PacketsPerLookup)
	}

	full := benchDocker(t, "--loss", "100", "--n", "2")[100]
	for _, name := range baseline.Names {
		r := full[name]
		if r.SuccessRate != 0 || r.P50MS != nil || r.P95MS != nil {
			t.Errorf("loss 100, %s: %+v, want success_rate 0 and null percentiles", name, r)
		}
	}
	for _, name := range []string{baseline.QMuxPrivate, baseline.DNSIn, baseline.DNSInTXT} {
		if r := full[name]; r.RequestsPerLookup != 5 {
			t.Errorf("loss 100, %s: %v requests per lookup, want 5", name, r.RequestsPerLookup)
		}
	}
	t.Logf("loss 100, coap: %v transmissions per lookup", full[baseline.CoAP].RequestsPerLookup)
}
