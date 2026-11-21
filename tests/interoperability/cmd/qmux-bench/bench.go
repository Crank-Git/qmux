package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Crank-Git/qmux/tests/interoperability/baseline"
	"github.com/Crank-Git/qmux/tests/interoperability/footprint"
	"github.com/Crank-Git/qmux/tests/interoperability/netem"
)

// benchConfig holds the settings and the steps of one benchmark. The steps are
// functions, so a test can replace Docker, the toolchain and the clock.
type benchConfig struct {
	losses     []float64
	n          int
	resultsDir string

	// ready returns an error when bench-server does not answer.
	ready func(ctx context.Context) error
	// apply runs the netem step of FR-bench-13 in both containers.
	apply func(ctx context.Context, loss float64) error
	// lookups runs one benchmark run in the client container.
	lookups func(ctx context.Context, name string, n int) (Run, error)
	// footprint measures the client of each baseline (FR-bench-31 to FR-bench-34).
	footprint func(ctx context.Context) ([]footprint.Result, error)
	// dispatch runs the microbenchmarks of FR-bench-37.
	dispatch    func(ctx context.Context) ([]Dispatch, error)
	environment func(ctx context.Context) (Environment, error)
	now         func() time.Time
}

// hostFlags holds the flags of the host part that only the real steps use.
type hostFlags struct {
	docker, project, compose, root string
}

// parse returns the benchmark settings and the host flags from args. It returns an
// error for a flag error, a loss level outside 0 to 100 and a count below 1.
func parse(args []string, stderr io.Writer) (benchConfig, hostFlags, error) {
	fs := flag.NewFlagSet("qmux-bench", flag.ContinueOnError)
	fs.SetOutput(stderr)
	loss := fs.String("loss", "0,1,5,10", "comma-separated loss levels in percent")
	n := fs.Int("n", 500, "lookups per baseline per loss level")
	results := fs.String("results", "results", "directory of the results file")
	var h hostFlags
	fs.StringVar(&h.docker, "docker", "docker", "docker command")
	fs.StringVar(&h.project, "project", "qmux-bench", "compose project name")
	fs.StringVar(&h.compose, "compose", "bench-compose.yaml", "compose file")
	fs.StringVar(&h.root, "root", "../..", "directory of the root module")
	if err := fs.Parse(args); err != nil {
		return benchConfig{}, hostFlags{}, fmt.Errorf("flags: %w", err)
	}
	cfg := benchConfig{n: *n, resultsDir: *results}
	if cfg.n < 1 {
		return benchConfig{}, hostFlags{}, fmt.Errorf("--n %d is not a positive count", cfg.n)
	}
	for _, s := range strings.Split(*loss, ",") {
		v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil || v < 0 || v > 100 {
			return benchConfig{}, hostFlags{}, fmt.Errorf("--loss %q: %q is not a loss level from 0 to 100", *loss, s)
		}
		cfg.losses = append(cfg.losses, v)
	}
	return cfg, h, nil
}

// bench runs every baseline at every loss level, then the footprint and the
// microbenchmarks, and writes the results file and the markdown table.
//
// The order is fixed: each loss level, and in it each baseline in the order of
// baseline.Names. The netem step runs before each benchmark run. bench stops at the
// first error, and a netem error keeps the text of FR-bench-14.
func bench(ctx context.Context, cfg benchConfig, stdout io.Writer) error {
	start := cfg.now().UTC()
	if err := cfg.ready(ctx); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "qmux-bench: %d baselines x %d loss levels x %d lookups\n", len(baseline.Names), len(cfg.losses), cfg.n); err != nil {
		return fmt.Errorf("write stdout: %w", err)
	}
	var res Results
	for _, loss := range cfg.losses {
		for _, name := range baseline.Names {
			if err := cfg.apply(ctx, loss); err != nil {
				return err
			}
			r, err := cfg.lookups(ctx, name, cfg.n)
			if err != nil {
				return fmt.Errorf("run %s at loss %g%%: %w", name, loss, err)
			}
			r.Loss, r.Retry = loss, baseline.RetryPolicy(name)
			res.Runs = append(res.Runs, r)
			if _, err := fmt.Fprintln(stdout, line(r)); err != nil {
				return fmt.Errorf("write stdout: %w", err)
			}
		}
	}
	var err error
	if res.Clients, err = cfg.footprint(ctx); err != nil {
		return err
	}
	if res.Dispatch, err = cfg.dispatch(ctx); err != nil {
		return err
	}
	if res.Environment, err = cfg.environment(ctx); err != nil {
		return err
	}
	res.Environment.Date = start.Format(time.RFC3339)
	res.Assumptions = assumptions()
	jsonPath, mdPath, err := writeResults(cfg.resultsDir, "bench-"+start.Format("20060102T150405Z"), res)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "qmux-bench: wrote %s\nqmux-bench: wrote %s\n", jsonPath, mdPath); err != nil {
		return fmt.Errorf("write stdout: %w", err)
	}
	return nil
}

// runBench runs the benchmark from the host and returns the exit code: 0 after the
// results file is written and 1 for every error. The compose environment must run.
func runBench(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	cfg, h, err := parse(args, stderr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "qmux-bench: %v\n", err)
		return 2
	}
	if _, err := exec.LookPath(h.docker); err != nil {
		_, _ = fmt.Fprintln(stderr, "docker not found")
		return 1
	}
	h.wire(&cfg)
	if err := bench(ctx, cfg, stdout); err != nil {
		// The netem and ready messages are the texts of the spec, so they stay bare.
		var ee *execError
		if errors.Is(err, netem.ErrNeedsNetAdmin) || errors.As(err, &ee) {
			_, _ = fmt.Fprintln(stderr, err)
		} else {
			_, _ = fmt.Fprintf(stderr, "qmux-bench: %v\n", err)
		}
		return 1
	}
	return 0
}

// wire sets the real steps of cfg: Docker Compose for the client container, the
// netem step, footprint.Measure and go test.
func (h hostFlags) wire(cfg *benchConfig) {
	compose := netem.Compose{Docker: h.docker, Project: h.project, Files: []string{h.compose}}
	cfg.apply = func(ctx context.Context, loss float64) error {
		return compose.Apply(ctx, loss, "bench-server", "bench-client")
	}
	cfg.ready = func(ctx context.Context) error {
		_, err := h.client(ctx, "qmux-bench", "ready")
		return err
	}
	cfg.lookups = func(ctx context.Context, name string, n int) (Run, error) {
		out, err := h.client(ctx, "qmux-bench", "lookups", "--baseline", name, "--n", strconv.Itoa(n))
		if err != nil {
			return Run{}, err
		}
		var r Run
		if err := json.Unmarshal(out, &r); err != nil {
			return Run{}, fmt.Errorf("decode run %q: %w", out, err)
		}
		return r, nil
	}
	cfg.footprint = measureFootprint
	cfg.dispatch = func(ctx context.Context) ([]Dispatch, error) { return runDispatch(ctx, h.root) }
	cfg.environment = h.environment
	cfg.now = time.Now
}

// execError is the error of a command in the client container. Its text is the
// standard error output of the command, for example the not-ready message.
type execError struct {
	err    error
	stderr string
}

func (e *execError) Error() string {
	if e.stderr != "" {
		return e.stderr
	}
	return e.err.Error()
}

func (e *execError) Unwrap() error { return e.err }

// client runs the command args in the bench-client container and returns its
// standard output.
func (h hostFlags) client(ctx context.Context, args ...string) ([]byte, error) {
	full := append([]string{"compose", "-p", h.project, "-f", h.compose, "exec", "-T", "bench-client"}, args...)
	cmd := exec.CommandContext(ctx, h.docker, full...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, &execError{err: fmt.Errorf("docker %s: %w", strings.Join(full, " "), err), stderr: strings.TrimSpace(stderr.String())}
	}
	return out, nil
}

// measureFootprint runs footprint.Measure against baseline servers on the loopback
// of the host. The host has the Go toolchain that the build of each client needs, and
// the client container has none.
func measureFootprint(ctx context.Context) ([]footprint.Result, error) {
	r, err := baseline.Start(ctx, baseline.Config{DNSAddr: "127.0.0.1:0", CoAPAddr: "127.0.0.1:0", HTTPSAddr: "127.0.0.1:0"})
	if err != nil {
		return nil, fmt.Errorf("start footprint servers: %w", err)
	}
	bin, err := os.MkdirTemp("", "qmux-bench-clients")
	if err != nil {
		return nil, errors.Join(fmt.Errorf("create client directory: %w", err), r.Close())
	}
	res, err := footprint.Measure(ctx, footprint.Config{ModuleDir: ".", BinDir: bin, Target: r.Target()})
	return res, errors.Join(err, r.Close(), os.RemoveAll(bin))
}
