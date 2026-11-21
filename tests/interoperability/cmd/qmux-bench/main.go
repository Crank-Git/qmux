// Command qmux-bench measures every baseline at every loss level (FR-bench-15 to
// FR-bench-30, FR-bench-37).
//
// Usage:
//
//	qmux-bench [--loss 0,1,5,10] [--n 500] [--results DIR] [--docker CMD]
//	           [--project NAME] [--compose FILE] [--root DIR]
//	qmux-bench lookups --baseline NAME [--n N] [--stats DIR] [--dns ADDR] [--coap ADDR] [--https ADDR]
//	qmux-bench ready [--timeout D] [--dns ADDR] [--coap ADDR] [--https ADDR]
//
// Run the first form on the host, from tests/interoperability, while the compose
// project of bench-compose.yaml runs. It applies netem before each benchmark run. It
// starts the lookups command in the bench-client container with docker compose exec,
// so the lookups and the eth0 counters belong to that container. It then measures
// the client footprint and the dispatch microbenchmarks on the host, and writes
// results/bench-<UTC timestamp>.json and results/bench-<UTC timestamp>.md.
//
// The exit code is 0 after the results file is written, 2 for a flag error and 1 for
// every other error. A netem failure prints `netem needs NET_ADMIN: <tc error>`.
package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run starts the command that args names and returns its exit code.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "lookups":
			return runLookups(ctx, args[1:], stdout, stderr)
		case "ready":
			return runReady(ctx, args[1:], stdout, stderr)
		}
	}
	return runBench(ctx, args, stdout, stderr)
}
