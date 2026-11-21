// Command bench-server serves the six baselines of the QMux benchmark: qmux-private,
// dns-in and dns-in-txt over DNS, coap over CoAP, and https-cold and https-reuse over
// HTTPS (FR-bench-4 to FR-bench-8).
//
// Usage:
//
//	bench-server [--dns ADDR] [--coap ADDR] [--https ADDR]
//
// When every listener is bound, bench-server prints one ready line to stdout. It logs
// each assumption of package baseline to stderr. It stops at SIGINT or SIGTERM.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Crank-Git/qmux/tests/interoperability/baseline"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run starts the baseline servers with the flags in args and serves until ctx ends.
// It returns the process exit code: 0 after a clean stop, 2 for a flag error and 1
// for every other error.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bench-server", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cfg baseline.Config
	fs.StringVar(&cfg.DNSAddr, "dns", ":5353", "UDP and TCP address of the DNS baselines")
	fs.StringVar(&cfg.CoAPAddr, "coap", ":5683", "UDP address of the coap baseline")
	fs.StringVar(&cfg.HTTPSAddr, "https", ":8443", "TCP address of the HTTPS baselines")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	cfg.Logger = logger
	for _, a := range baseline.Assumptions() {
		logger.Info("assumption", "name", a.Name, "value", a.Value)
	}

	r, err := baseline.Start(ctx, cfg)
	if err != nil {
		logger.Error("bench-server: start", "err", err)
		return 1
	}
	t := r.Target()
	if _, err := fmt.Fprintf(stdout, "bench-server: dns %s (udp, tcp), coap %s (udp), https %s (tcp, TLS 1.3)\n", t.DNS, t.CoAP, t.HTTPS); err != nil {
		// A caller that waits for the ready line must not wait for ever.
		logger.Error("bench-server: write ready line", "err", errors.Join(err, r.Close()))
		return 1
	}
	<-ctx.Done()
	if err := r.Close(); err != nil {
		logger.Error("bench-server: stop", "err", err)
		return 1
	}
	return 0
}
