package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Crank-Git/qmux/pkg/discovery"
	"github.com/Crank-Git/qmux/pkg/security"
	"github.com/Crank-Git/qmux/pkg/server"
	"github.com/Crank-Git/qmux/pkg/transport"
)

// The exit codes of qmuxd serve.
const (
	exitResult  = 0
	exitFailure = 1
	exitConfig  = 2
	exitUsage   = 64
)

// defaultListen is the listen address when the configuration file has no listen field.
const defaultListen = "127.0.0.1:5353"

// runServe runs qmuxd serve with the flags in args and returns the exit code. It serves
// until ctx ends or the process receives SIGINT or SIGTERM.
func runServe(ctx context.Context, name string, args []string, stdout, stderr io.Writer) int {
	synopsis := name + " serve [--config <file>] [--listen <addr>]"
	var path, listen string
	fset := flag.NewFlagSet(name+" serve", flag.ContinueOnError)
	// runServe prints the error and the usage, so that --help goes to stdout and an
	// error goes to stderr once.
	fset.SetOutput(io.Discard)
	fset.StringVar(&path, "config", "./qmuxd.json", "path of the configuration file")
	fset.StringVar(&listen, "listen", "", "host:port that replaces the listen field of the configuration file")
	err := fset.Parse(args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		if printUsage(stdout, synopsis, fset) != nil {
			return exitFailure
		}
		return exitResult
	case err == nil && fset.NArg() > 0:
		err = fmt.Errorf("unexpected argument %q", fset.Arg(0))
	}
	if err != nil {
		// The exit code is 64 whether or not stderr accepts the write.
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		_ = printUsage(stderr, synopsis, fset)
		return exitUsage
	}

	d, err := newDaemon(path, listen, stderr)
	if err != nil {
		// The exit code is 2 whether or not stderr accepts the write.
		_, _ = fmt.Fprintf(stderr, "qmuxd: config: %v\n", err)
		return exitConfig
	}
	return d.serve(ctx, stderr)
}

// observationLogMode is the file mode of a new observation log (FR-qmuxd-36).
const observationLogMode = 0o644

// daemon is a loaded configuration and the server that it built.
type daemon struct {
	srv    *server.Server
	cfg    config
	logger *slog.Logger
	keyID  [8]byte
	// obsLog is the open observation log, or nil when the configuration has none.
	obsLog *os.File
}

// newDaemon loads the configuration file at path and returns a signing server with its
// registrations. A non-empty listen replaces the listen field of the file. The logger of
// the daemon writes to stderr with the log_level and log_format of the file.
//
// The error names the JSON path of the first configuration error. It also covers a
// signing key that does not load, an observation log that does not open, and a
// registration that the registry rejects. On an error, newDaemon closes the
// observation log.
func newDaemon(path, listen string, stderr io.Writer) (d *daemon, err error) {
	cfg, err := loadConfig(path, listen)
	if err != nil {
		return nil, err
	}
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	var h slog.Handler = slog.NewTextHandler(stderr, opts)
	if cfg.LogFormat == "json" {
		h = slog.NewJSONHandler(stderr, opts)
	}
	logger := slog.New(h)

	key, warning, err := security.LoadPrivateKey(cfg.SigningKey)
	if err != nil {
		return nil, fmt.Errorf("signing_key: %w", err)
	}
	if warning != "" {
		logger.Warn(string(warning), "path", cfg.SigningKey)
	}
	signer := &security.Signer{Key: key, Validity: cfg.SignatureValidity}

	var obsLog *os.File
	// A nil *os.File in an io.Writer is not a nil io.Writer, so obsWriter stays nil
	// when the configuration has no observation log.
	var obsWriter io.Writer
	if cfg.ObservationLog != "" {
		// O_APPEND keeps the lines of earlier runs (FR-qmuxd-36).
		obsLog, err = os.OpenFile(cfg.ObservationLog, os.O_WRONLY|os.O_CREATE|os.O_APPEND, observationLogMode)
		if err != nil {
			return nil, fmt.Errorf("observation_log: %w", err)
		}
		obsWriter = obsLog
		defer func() {
			if err != nil {
				err = errors.Join(err, obsLog.Close())
			}
		}()
	}
	srv := server.NewServer(server.ServerConfig{
		Address:        cfg.Listen,
		Zones:          cfg.Zones,
		TTL:            cfg.TTL,
		UDPSize:        cfg.UDPSize,
		HandlerTimeout: cfg.HandlerTimeout,
		Logger:         logger,
		Registry:       cfg.Registry,
		Signer:         signer,
		RateLimit:      cfg.RateLimit,
		ObservationLog: obsWriter,
	})
	for i, p := range cfg.Protocols {
		path := fmt.Sprintf("protocols[%d]", i)
		if err := srv.RegisterProtocol(p.Key, p.Info, recordHandler(p.Records)); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		// The registration comes first, so a bad protocol ID gives the registry error.
		if err := checkEnvelopeSizes(p, path, signer); err != nil {
			return nil, err
		}
	}
	if cfg.Discovery.Enabled {
		// One registration serves every zone, because the registry holds one protocol
		// key for the discovery protocol ID.
		if err := discovery.RegisterZones(srv, cfg.Zones, discovery.Options{InClass: cfg.Discovery.InClass}); err != nil {
			return nil, fmt.Errorf("discovery: %w", err)
		}
	}
	return &daemon{
		srv: srv, cfg: cfg, logger: logger, obsLog: obsLog,
		keyID: security.KeyID(key.Public().(ed25519.PublicKey)),
	}, nil
}

// serve binds the UDP and TCP listeners, logs the ready record and serves until ctx ends
// or a signal arrives. It returns exitFailure when a listener does not bind or when the
// observation log does not close. serve closes the observation log before it returns.
func (d *daemon) serve(ctx context.Context, stderr io.Writer) (code int) {
	if d.obsLog != nil {
		defer func() {
			// Close reports a write that the file system did not complete.
			if err := d.obsLog.Close(); err != nil {
				_, _ = fmt.Fprintf(stderr, "qmuxd: observation_log: %v\n", err)
				if code == exitResult {
					code = exitFailure
				}
			}
		}()
	}
	logger := d.logger
	// Listen gives one port for both transports, and retries a port 0 bind that
	// another process holds for TCP.
	pc, l, err := transport.Listen(d.cfg.Listen)
	if err != nil {
		// The exit code is 1 whether or not stderr accepts the write.
		_, _ = fmt.Fprintf(stderr, "qmuxd: listen: %v\n", err)
		return exitFailure
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Info("qmuxd ready",
		"listen", pc.LocalAddr().String(),
		"zones", d.cfg.Zones,
		"key_id", fmt.Sprintf("%x", d.keyID),
		"protocols", len(d.srv.Registry().List()))
	if err := d.srv.Serve(ctx, pc, l); err != nil {
		// The exit code is 1 whether or not stderr accepts the write.
		_, _ = fmt.Fprintf(stderr, "qmuxd: %v\n", err)
		return exitFailure
	}
	return exitResult
}

// printUsage writes the usage text of qmuxd serve with the flags of fs to w.
func printUsage(w io.Writer, synopsis string, fs *flag.FlagSet) error {
	var b strings.Builder
	b.WriteString("Usage: " + synopsis + "\n\nFlags:\n")
	fs.VisitAll(func(fl *flag.Flag) {
		fmt.Fprintf(&b, "  --%-9s %s\n", fl.Name, fl.Usage)
	})
	b.WriteString("\nExit codes: 0 shut down after SIGINT or SIGTERM, 1 a listener does not bind, " +
		"2 the configuration file or the signing key does not load, 64 usage error.\n")
	_, err := io.WriteString(w, b.String())
	return err
}
