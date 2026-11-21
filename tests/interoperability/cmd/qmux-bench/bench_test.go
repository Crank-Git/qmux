package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/tests/interoperability/baseline"
	"github.com/Crank-Git/qmux/tests/interoperability/footprint"
	"github.com/Crank-Git/qmux/tests/interoperability/netem"
)

// fakeBench records the calls of bench and answers with fixed values.
type fakeBench struct {
	calls    []string
	applyErr error
	readyErr error
}

func (f *fakeBench) config(t *testing.T) benchConfig {
	return benchConfig{
		losses:     []float64{0, 10},
		n:          20,
		resultsDir: filepath.Join(t.TempDir(), "results"),
		apply: func(_ context.Context, loss float64) error {
			f.calls = append(f.calls, fmt.Sprintf("netem %v", loss))
			return f.applyErr
		},
		ready: func(context.Context) error {
			f.calls = append(f.calls, "ready")
			return f.readyErr
		},
		lookups: func(_ context.Context, name string, n int) (Run, error) {
			f.calls = append(f.calls, fmt.Sprintf("lookups %s %d", name, n))
			p := 10.0
			return Run{Baseline: name, N: n, SuccessRate: 1, P50MS: &p, P95MS: &p,
				PacketsPerLookup: 2, BytesPerLookup: 400, RequestsPerLookup: 1}, nil
		},
		footprint: func(context.Context) ([]footprint.Result, error) {
			f.calls = append(f.calls, "footprint")
			return []footprint.Result{{Baseline: baseline.QMuxPrivate, BinaryBytes: 1, MaxRSSKiB: 1, CPUMs: 1}}, nil
		},
		dispatch: func(context.Context) ([]Dispatch, error) {
			f.calls = append(f.calls, "dispatch")
			return []Dispatch{{Name: "BenchmarkDispatchByClass", NsPerOp: 1, AllocsPerOp: 1}}, nil
		},
		environment: func(context.Context) (Environment, error) {
			return Environment{CPU: "cpu", OS: "os", Kernel: "kernel", Docker: "docker",
				Go: "go", Commit: "commit", Date: "date", TCPSynRetries: "6"}, nil
		},
		now: func() time.Time { return time.Date(2026, 11, 20, 15, 4, 5, 0, time.UTC) },
	}
}

func TestBenchAppliesNetemBeforeEachBenchmarkRunInTheFixedOrder(t *testing.T) {
	f := &fakeBench{}
	var out bytes.Buffer
	if err := bench(t.Context(), f.config(t), &out); err != nil {
		t.Fatalf("bench: %v", err)
	}
	want := []string{"ready"}
	for _, loss := range []string{"0", "10"} {
		for _, name := range baseline.Names {
			want = append(want, "netem "+loss, "lookups "+name+" 20")
		}
	}
	want = append(want, "footprint", "dispatch")
	if !slices.Equal(f.calls, want) {
		t.Errorf("calls =\n%s\nwant\n%s", strings.Join(f.calls, "\n"), strings.Join(want, "\n"))
	}
}

// readResults runs bench with the fake steps and returns the decoded results file,
// the markdown table and the stdout output.
func readResults(t *testing.T) (map[string]any, string, string) {
	t.Helper()
	f := &fakeBench{}
	cfg := f.config(t)
	var out bytes.Buffer
	if err := bench(t.Context(), cfg, &out); err != nil {
		t.Fatalf("bench: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(cfg.resultsDir, "bench-20261120T150405Z.json"))
	if err != nil {
		t.Fatalf("read results file: %v", err)
	}
	var res map[string]any
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatalf("results file: %v", err)
	}
	md, err := os.ReadFile(filepath.Join(cfg.resultsDir, "bench-20261120T150405Z.md"))
	if err != nil {
		t.Fatalf("read markdown table: %v", err)
	}
	if !strings.Contains(out.String(), "qmux-bench: wrote "+filepath.Join(cfg.resultsDir, "bench-20261120T150405Z.json")) {
		t.Errorf("stdout names no results file:\n%s", out.String())
	}
	return res, string(md), out.String()
}

// missing returns the names in fields that m does not hold, or holds as "".
func missing(m map[string]any, fields ...string) []string {
	var out []string
	for _, f := range fields {
		if v, ok := m[f]; !ok || v == "" {
			out = append(out, f)
		}
	}
	return out
}

func TestTheResultsFileHoldsTheFourArraysAndTheEnvironmentBlock(t *testing.T) {
	res, _, _ := readResults(t)
	if m := missing(res, "environment", "runs", "clients", "dispatch", "assumptions"); m != nil {
		t.Errorf("the results file has no %v", m)
	}
	env := res["environment"].(map[string]any)
	if m := missing(env, "cpu", "os", "kernel", "docker", "go", "commit", "date", "tcp_syn_retries"); m != nil {
		t.Errorf("the environment block has no %v", m)
	}
}

func TestEachRunObjectHoldsTheMetricsAndTheRetryObject(t *testing.T) {
	res, _, _ := readResults(t)
	runs := res["runs"].([]any)
	if len(runs) != 2*len(baseline.Names) {
		t.Fatalf("%d runs, want %d", len(runs), 2*len(baseline.Names))
	}
	for _, r := range runs {
		run := r.(map[string]any)
		if m := missing(run, "baseline", "loss", "n", "success_rate", "p50_ms", "p95_ms",
			"packets_per_lookup", "bytes_per_lookup", "requests_per_lookup", "retry"); m != nil {
			t.Errorf("run %v has no %v", run["baseline"], m)
		}
		if run["n"] != 20.0 {
			t.Errorf("run %v: n = %v, want 20", run["baseline"], run["n"])
		}
	}
}

func TestTheMarkdownTableLabelsTheReferenceAndTheConnectionReuseRows(t *testing.T) {
	_, md, out := readResults(t)
	if !strings.Contains(out, "qmux-bench: 6 baselines x 2 loss levels x 20 lookups") {
		t.Errorf("stdout has no header line:\n%s", out)
	}
	for _, label := range []string{"dns-in-txt (reference)", "https-reuse (connection reuse)"} {
		if !strings.Contains(md, label) || !strings.Contains(out, label) {
			t.Errorf("the markdown table or stdout has no row label %q", label)
		}
	}
}

func TestBenchStopsWithTheNetemErrorWhenAContainerRefusesNetAdmin(t *testing.T) {
	f := &fakeBench{applyErr: fmt.Errorf("%w: RTNETLINK answers: Operation not permitted", netem.ErrNeedsNetAdmin)}
	err := bench(t.Context(), f.config(t), &bytes.Buffer{})
	if !errors.Is(err, netem.ErrNeedsNetAdmin) {
		t.Fatalf("err = %v, want netem.ErrNeedsNetAdmin", err)
	}
	if err.Error() != "netem needs NET_ADMIN: RTNETLINK answers: Operation not permitted" {
		t.Errorf("message = %q", err.Error())
	}
	if slices.Contains(f.calls, "lookups "+baseline.QMuxPrivate+" 20") {
		t.Error("bench ran lookups after the netem step failed")
	}
}

func TestBenchStopsBeforeTheFirstRunWhenTheServerIsNotReady(t *testing.T) {
	f := &fakeBench{readyErr: errors.New("bench-server not ready after 30 s")}
	err := bench(t.Context(), f.config(t), &bytes.Buffer{})
	if err == nil || err.Error() != "bench-server not ready after 30 s" {
		t.Fatalf("err = %v, want the not-ready message", err)
	}
	if len(f.calls) != 1 {
		t.Errorf("calls = %v, want only the ready check", f.calls)
	}
}

func TestRunParsesTheLossLevelsAndTheLookupCount(t *testing.T) {
	cfg, _, err := parse([]string{"--loss", "0,1.5,10", "--n", "7"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !slices.Equal(cfg.losses, []float64{0, 1.5, 10}) || cfg.n != 7 {
		t.Errorf("losses %v, n %d", cfg.losses, cfg.n)
	}
	def, _, err := parse(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !slices.Equal(def.losses, []float64{0, 1, 5, 10}) || def.n != 500 {
		t.Errorf("defaults: losses %v, n %d, want 0,1,5,10 and 500", def.losses, def.n)
	}
	for _, bad := range [][]string{{"--loss", "x"}, {"--loss", "101"}, {"--n", "0"}} {
		if _, _, err := parse(bad, &bytes.Buffer{}); err == nil {
			t.Errorf("parse(%v) returned no error", bad)
		}
	}
}
