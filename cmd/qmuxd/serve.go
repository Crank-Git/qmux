package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Crank-Git/qmux/pkg/discovery"
	"github.com/Crank-Git/qmux/pkg/security"
	"github.com/Crank-Git/qmux/pkg/server"
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

// config is the configuration file of qmuxd serve.
//
// TODO(#55): Replace this minimal schema with the full schema of the spec.
type config struct {
	Listen     string   `json:"listen"`
	Zones      []string `json:"zones"`
	SigningKey string   `json:"signing_key"`
}

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

	logger := slog.New(slog.NewTextHandler(stderr, nil))
	d, err := newDaemon(path, listen, logger)
	if err != nil {
		// The exit code is 2 whether or not stderr accepts the write.
		_, _ = fmt.Fprintf(stderr, "qmuxd: config: %v\n", err)
		return exitConfig
	}
	return d.serve(ctx, logger, stderr)
}

// daemon is a loaded configuration and the server that it built.
type daemon struct {
	srv   *server.Server
	cfg   config
	keyID [8]byte
}

// newDaemon loads the configuration file at path and returns a signing server with its
// registrations. A non-empty listen replaces the listen field of the file.
func newDaemon(path, listen string, logger *slog.Logger) (*daemon, error) {
	cfg, err := loadConfig(path)
	if err != nil {
		return nil, err
	}
	if listen != "" {
		cfg.Listen = listen
	}
	key, warning, err := security.LoadPrivateKey(cfg.SigningKey)
	if err != nil {
		return nil, fmt.Errorf("signing_key: %w", err)
	}
	if warning != "" {
		logger.Warn(string(warning), "path", cfg.SigningKey)
	}
	srv := server.NewServer(server.ServerConfig{
		Address: cfg.Listen,
		Zones:   cfg.Zones,
		Logger:  logger,
		Signer:  &security.Signer{Key: key},
	})
	for _, z := range cfg.Zones {
		if err := discovery.Register(srv, z, discovery.Options{}); err != nil {
			return nil, fmt.Errorf("zones: %w", err)
		}
	}
	return &daemon{srv: srv, cfg: cfg, keyID: security.KeyID(key.Public().(ed25519.PublicKey))}, nil
}

// loadConfig decodes the configuration file at path. It rejects an unknown field, and it
// resolves a relative signing_key path from the directory of the file.
func loadConfig(path string) (config, error) {
	f, err := os.Open(path)
	if err != nil {
		return config{}, fmt.Errorf("open: %w", err)
	}
	defer f.Close() //nolint:errcheck // The file is open for reading only.
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	cfg := config{Listen: defaultListen}
	if err := dec.Decode(&cfg); err != nil {
		return config{}, fmt.Errorf("%s: %w", path, err)
	}
	if cfg.SigningKey != "" && !filepath.IsAbs(cfg.SigningKey) {
		cfg.SigningKey = filepath.Join(filepath.Dir(path), cfg.SigningKey)
	}
	return cfg, nil
}

// serve binds the UDP and TCP listeners, logs the ready record and serves until ctx ends
// or a signal arrives. It returns exitFailure when a listener does not bind.
func (d *daemon) serve(ctx context.Context, logger *slog.Logger, stderr io.Writer) int {
	pc, err := net.ListenPacket("udp", d.cfg.Listen)
	if err != nil {
		// The exit code is 1 whether or not stderr accepts the write.
		_, _ = fmt.Fprintf(stderr, "qmuxd: listen: %v\n", err)
		return exitFailure
	}
	// The TCP listener takes the port of the UDP listener, so that port 0 gives one
	// port for both transports.
	l, err := net.Listen("tcp", pc.LocalAddr().String())
	if err != nil {
		_ = pc.Close() //nolint:errcheck // The process exits with the listen error.
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
