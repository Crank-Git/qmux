// Command embedded-bootstrap is the embedded recovery demonstration. It starts the
// authoritative server for fleet.example., the mock HTTPS service and the device in one
// process. The device reconciles its device state once, and the program prints one log
// line per step to standard output.
//
// The program exits with code 0 when the device reconciles or is already current, with
// code 1 when a step fails, and with code 2 when a flag does not parse.
//
// Usage:
//
//	go run ./examples/embedded-bootstrap --state ./device-state.json --data-dir ./device-data
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/Crank-Git/qmux/examples/embedded-bootstrap/device"
	"github.com/Crank-Git/qmux/examples/embedded-bootstrap/fleet"
	"github.com/Crank-Git/qmux/pkg/security"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// config holds the flag values of the program.
type config struct {
	statePath string
	dataDir   string
	fleet     fleet.Options
}

// run runs the program with the arguments args and returns the exit code. The step log
// goes to stdout. A failure that the step log does not show goes to stderr.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	cfg, err := parse(args, stderr)
	if err != nil {
		return 2
	}
	_, err = demo(ctx, cfg, stdout)
	var step *device.StepError
	switch {
	case err == nil:
		return 0
	case !errors.As(err, &step):
		_, _ = fmt.Fprintf(stderr, "embedded-bootstrap: %v\n", err)
	}
	return 1
}

// parse returns the flag values in args. The flag package writes the parse error and
// the usage to stderr.
func parse(args []string, stderr io.Writer) (config, error) {
	var cfg config
	fs := flag.NewFlagSet("embedded-bootstrap", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.statePath, "state", "./device-state.json", "path of the device state file")
	fs.StringVar(&cfg.dataDir, "data-dir", "./device-data", "directory for downloaded resources")
	cfg.fleet.BindFlags(fs)
	if err := fs.Parse(args); err != nil {
		return config{}, fmt.Errorf("parse flags: %w", err)
	}
	return cfg, nil
}

// demo starts the server and the mock HTTPS service, runs one reconcile and writes the
// step log to stdout. Each [device] line comes from device.Reconcile, and demo adds the
// result line. It returns the number of HTTPS requests that the mock HTTPS service
// received.
//
// demo returns a *device.StepError when a step fails. The step log already holds that
// failure. It returns another error when the server does not start or stop, or when a
// write to stdout fails.
func demo(ctx context.Context, cfg config, stdout io.Writer) (int64, error) {
	cfg.fleet.Out = stdout
	r, err := fleet.Start(ctx, cfg.fleet)
	if err != nil {
		return 0, fmt.Errorf("start: %w", err)
	}
	var writeErr error
	logLine := func(line string) {
		if writeErr == nil {
			_, writeErr = fmt.Fprintf(stdout, "[device]  %s\n", line)
		}
	}
	report, err := device.Reconcile(ctx, device.Config{
		Resolver:     r.Addr,
		TrustAnchors: &security.Verifier{Anchors: []security.TrustAnchor{r.TrustAnchor}},
		HTTPClient:   r.HTTPClient,
		StatePath:    cfg.statePath,
		DataDir:      cfg.dataDir,
		// The spec keeps the allowed firmware key IDs as a list in the program.
		AllowedFirmwareKeyIDs: [][8]byte{r.FirmwareKeyID},
		Log:                   logLine,
	})
	switch {
	case errors.Is(err, device.ErrDigestMismatch):
		logLine(err.Error())
		logLine("result: failed, state unchanged")
	case err != nil:
		logLine(err.Error())
		logLine("result: failed")
	case report.StateWritten:
		logLine(fmt.Sprintf("result: reconciled (%d downloads)", report.Downloads))
	default:
		logLine(fmt.Sprintf("result: up to date (%d downloads)", report.Downloads))
	}
	if writeErr != nil {
		err = errors.Join(err, fmt.Errorf("write step log: %w", writeErr))
	}
	return r.Requests(), errors.Join(err, r.Close())
}
