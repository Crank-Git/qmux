package main

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
)

// Dispatch is the result of one dispatch microbenchmark (FR-bench-37). The JSON names
// are the names of the dispatch array in the results file.
type Dispatch struct {
	Name        string  `json:"name"`
	NsPerOp     float64 `json:"ns_per_op"`
	AllocsPerOp int64   `json:"allocs_per_op"`
}

// dispatchArgs is the go test command of FR-bench-37. It runs no test, only the two
// microbenchmarks of pkg/server.
var dispatchArgs = []string{"test", "-run=^$", "-bench=Dispatch", "-benchmem", "./pkg/server/"}

// benchLine matches one result line of go test -benchmem. The -N suffix is GOMAXPROCS.
var benchLine = regexp.MustCompile(`(?m)^(Benchmark\w+?)(?:-\d+)?\s+\d+\s+([0-9.]+) ns/op\s+[0-9.]+ B/op\s+(\d+) allocs/op`)

// runDispatch runs the microbenchmarks in the root module at root and returns their
// results. The error holds the go test output when go test fails.
func runDispatch(ctx context.Context, root string) ([]Dispatch, error) {
	cmd := exec.CommandContext(ctx, "go", dispatchArgs...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("go %v: %w\n%s", dispatchArgs, err, out)
	}
	return parseDispatch(out)
}

// parseDispatch returns one Dispatch for each result line in the go test output out.
// Output with fewer than the two microbenchmarks of FR-bench-35 and FR-bench-36 is an
// error.
func parseDispatch(out []byte) ([]Dispatch, error) {
	var ds []Dispatch
	for _, m := range benchLine.FindAllSubmatch(out, -1) {
		ns, err := strconv.ParseFloat(string(m[2]), 64)
		if err != nil {
			return nil, fmt.Errorf("parse ns/op of %s: %w", m[1], err)
		}
		allocs, err := strconv.ParseInt(string(m[3]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse allocs/op of %s: %w", m[1], err)
		}
		ds = append(ds, Dispatch{Name: string(m[1]), NsPerOp: ns, AllocsPerOp: allocs})
	}
	if len(ds) < 2 {
		return nil, fmt.Errorf("go test printed %d dispatch results, want 2:\n%s", len(ds), out)
	}
	return ds, nil
}
