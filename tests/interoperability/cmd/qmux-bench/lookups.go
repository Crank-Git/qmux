package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/Crank-Git/qmux/tests/interoperability/baseline"
)

// The commands lookups and ready run in the bench-client container. The host part of
// qmux-bench starts them with docker compose exec, so the lookups and the eth0
// counters belong to the client container.

// readyTimeout is the time that the ready command waits for bench-server.
const readyTimeout = 30 * time.Second

// targetFlags adds the server address flags to fs and returns a function that
// resolves them after fs.Parse.
func targetFlags(fs *flag.FlagSet) func(ctx context.Context) (baseline.Target, error) {
	dns := fs.String("dns", "bench-server:5353", "UDP and TCP address of the DNS baselines")
	coap := fs.String("coap", "bench-server:5683", "UDP address of the coap baseline")
	https := fs.String("https", "bench-server:8443", "TCP address of the HTTPS baselines")
	return func(ctx context.Context) (baseline.Target, error) {
		var t baseline.Target
		for _, a := range []struct {
			in  string
			out *string
		}{{*dns, &t.DNS}, {*coap, &t.CoAP}, {*https, &t.HTTPS}} {
			ip, err := resolve(ctx, a.in)
			if err != nil {
				return baseline.Target{}, err
			}
			*a.out = ip
		}
		return t, nil
	}
}

// resolve returns addr with its host name replaced by an IP address. pkg/client
// accepts only an IP address, and a lookup before the first counter read keeps the
// name resolution out of the counters.
func resolve(ctx context.Context, addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("address %q: %w", addr, err)
	}
	ips, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", host, err)
	}
	return net.JoinHostPort(ips[0], port), nil
}

// runLookups runs one benchmark run in the client container and prints the Run as
// JSON to stdout. It returns the exit code: 0 after the run, 2 for a flag error and 1
// for every other error.
//
// The lookups run one at a time (FR-bench-17), each with the deadline baseline.Deadline
// (FR-bench-18). A failed lookup counts its requests, and its latency is not in the
// percentiles.
func runLookups(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("qmux-bench lookups", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("baseline", "", "baseline of the run, one of "+fmt.Sprint(baseline.Names))
	n := fs.Int("n", 500, "lookups in the run")
	stats := fs.String("stats", "/sys/class/net/eth0/statistics", "statistics directory of eth0")
	target := targetFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	r, err := lookups(ctx, *name, *n, *stats, target)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "qmux-bench lookups: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(r); err != nil {
		_, _ = fmt.Fprintf(stderr, "qmux-bench lookups: write run: %v\n", err)
		return 1
	}
	return 0
}

// lookups does the n lookups of the baseline name and returns their Run.
func lookups(ctx context.Context, name string, n int, stats string, target func(context.Context) (baseline.Target, error)) (Run, error) {
	if n < 1 {
		return Run{}, fmt.Errorf("--n %d is not a positive count", n)
	}
	t, err := target(ctx)
	if err != nil {
		return Run{}, err
	}
	c, err := baseline.NewClient(t)
	if err != nil {
		return Run{}, fmt.Errorf("new client: %w", err)
	}
	defer c.Close()
	before, err := readCounters(stats)
	if err != nil {
		return Run{}, err
	}
	var latencies []float64
	requests := 0
	for range n {
		lctx, cancel := context.WithTimeout(ctx, baseline.Deadline)
		start := time.Now()
		_, req, err := c.LookupRequests(lctx, name)
		elapsed := time.Since(start)
		cancel()
		requests += req
		if err == nil {
			latencies = append(latencies, float64(elapsed)/float64(time.Millisecond))
		}
		if ctx.Err() != nil {
			return Run{}, fmt.Errorf("run of %s: %w", name, ctx.Err())
		}
	}
	after, err := readCounters(stats)
	if err != nil {
		return Run{}, err
	}
	delta, err := after.sub(before)
	if err != nil {
		return Run{}, err
	}
	return summarize(name, n, latencies, requests, delta), nil
}

// runReady waits until a qmux-private lookup succeeds. It returns 0 at the first
// success, and 1 with the message `bench-server not ready after <timeout>` when the
// timeout ends first.
func runReady(ctx context.Context, args []string, _, stderr io.Writer) int {
	fs := flag.NewFlagSet("qmux-bench ready", flag.ContinueOnError)
	fs.SetOutput(stderr)
	timeout := fs.Duration("timeout", readyTimeout, "time to wait for bench-server")
	target := targetFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	for {
		if ready(ctx, target) {
			return 0
		}
		select {
		case <-ctx.Done():
			_, _ = fmt.Fprintf(stderr, "bench-server not ready after %s s\n",
				strconv.FormatFloat(timeout.Seconds(), 'f', -1, 64))
			return 1
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// ready reports whether one qmux-private lookup succeeds. The name of bench-server
// can resolve only after its container starts, so each check resolves it again.
func ready(ctx context.Context, target func(context.Context) (baseline.Target, error)) bool {
	t, err := target(ctx)
	if err != nil {
		return false
	}
	c, err := baseline.NewClient(t)
	if err != nil {
		return false
	}
	defer c.Close()
	_, err = c.Lookup(ctx, baseline.QMuxPrivate)
	return err == nil
}
