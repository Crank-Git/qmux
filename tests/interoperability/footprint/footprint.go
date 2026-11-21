//go:build linux || darwin

// Package footprint measures the client footprint of each baseline (FR-bench-31 to
// FR-bench-34): the binary size, the maximum resident set size and the CPU time.
//
// Measure builds each program in tests/interoperability/clients with
// -trimpath -ldflags="-s -w" and runs it as a child process against the baseline
// servers. The memory and the CPU time come from the resource usage of the child
// process that the operating system records, not from the Go runtime.
//
// The package builds only on Linux and macOS, because the resource usage fields of
// syscall.Rusage differ on other systems.
package footprint

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/Crank-Git/qmux/tests/interoperability/baseline"
)

// Result is the footprint of the client of one baseline. The JSON names are the names
// of the clients array in the results file.
type Result struct {
	// Baseline is the name of the baseline, one of baseline.Names.
	Baseline string `json:"baseline"`
	// BinaryBytes is the size of the stripped client binary in bytes (FR-bench-32).
	BinaryBytes int64 `json:"binary_bytes"`
	// MaxRSSKiB is the maximum resident set size of the client process in KiB
	// (FR-bench-33).
	MaxRSSKiB int64 `json:"max_rss_kib"`
	// CPUMs is the user plus system CPU time of the client process in milliseconds
	// (FR-bench-34).
	CPUMs float64 `json:"cpu_ms"`
}

// Config holds the settings of Measure.
type Config struct {
	// ModuleDir is the directory of the harness module, which holds clients/<baseline>.
	ModuleDir string
	// BinDir receives the client binaries.
	BinDir string
	// Target holds the addresses of the baseline servers that the clients query.
	Target baseline.Target
}

// BuildError is the error of Measure when a client program fails to build. Output
// holds the output of go build, so the caller can print it.
type BuildError struct {
	// Baseline is the name of the baseline whose client failed to build.
	Baseline string
	// Output is the combined output of go build.
	Output []byte
	// Err is the error of the go build process.
	Err error
}

// Error returns the baseline, the build error and the build output.
func (e *BuildError) Error() string {
	return fmt.Sprintf("footprint: build client %s: %v\n%s", e.Baseline, e.Err, e.Output)
}

// Unwrap returns the error of the go build process.
func (e *BuildError) Unwrap() error {
	return e.Err
}

// Measure builds and runs the client of each baseline, in the order of
// baseline.Names, and returns one Result for each.
//
// Measure stops at the first failure. A client that fails to build gives a
// *BuildError. A client that exits with a nonzero code gives an error that holds its
// standard error output.
func Measure(ctx context.Context, cfg Config) ([]Result, error) {
	goTool, err := exec.LookPath("go")
	if err != nil {
		return nil, fmt.Errorf("footprint: find the go command: %w", err)
	}
	results := make([]Result, 0, len(baseline.Names))
	for _, name := range baseline.Names {
		bin, err := filepath.Abs(filepath.Join(cfg.BinDir, name))
		if err != nil {
			return nil, fmt.Errorf("footprint: resolve the binary path of %s: %w", name, err)
		}
		build := exec.CommandContext(ctx, goTool, "build", "-trimpath", "-ldflags=-s -w", "-o", bin, "./clients/"+name)
		build.Dir = cfg.ModuleDir
		if out, err := build.CombinedOutput(); err != nil {
			return nil, &BuildError{Baseline: name, Output: out, Err: err}
		}
		info, err := os.Stat(bin)
		if err != nil {
			return nil, fmt.Errorf("footprint: stat client %s: %w", name, err)
		}
		r, err := run(ctx, bin, clientAddr(name, cfg.Target))
		if err != nil {
			return nil, fmt.Errorf("footprint: run client %s: %w", name, err)
		}
		r.Baseline = name
		r.BinaryBytes = info.Size()
		results = append(results, r)
	}
	return results, nil
}

// clientAddr returns the server address that the client of the baseline name queries.
func clientAddr(name string, t baseline.Target) string {
	switch name {
	case baseline.CoAP:
		return t.CoAP
	case baseline.HTTPSCold, baseline.HTTPSReuse:
		return t.HTTPS
	}
	return t.DNS
}

// run runs the client binary bin against addr and returns the maximum resident set
// size and the CPU time of the child process.
func run(ctx context.Context, bin, addr string) (Result, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "--addr", addr)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return Result{}, fmt.Errorf("%w: %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
	// On Linux and macOS, SysUsage is the rusage of wait4(2) for this child only.
	usage := cmd.ProcessState.SysUsage().(*syscall.Rusage)
	cpu := usage.Utime.Nano() + usage.Stime.Nano()
	return Result{
		MaxRSSKiB: maxRSSKiB(runtime.GOOS, usage.Maxrss),
		CPUMs:     float64(cpu) / 1e6,
	}, nil
}

// maxRSSKiB returns ru_maxrss in KiB. The getrusage(2) manual page of macOS gives
// ru_maxrss in bytes, and the manual page of Linux gives it in KiB.
func maxRSSKiB(goos string, maxrss int64) int64 {
	if goos == "darwin" {
		return maxrss / 1024
	}
	return maxrss
}
