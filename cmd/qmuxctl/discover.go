package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/discovery"
)

// errNoTrustAnchors is the message of FR-discovery-38.
const errNoTrustAnchors = "discovery requires --trust-anchors or --insecure"

// discoverFlags holds the parsed flags of qmuxctl discover.
type discoverFlags struct {
	server, name       string
	trustAnchors       string
	class, typ         uint16
	setClass, setTyp   bool
	in, insecure, json bool
}

// manifestOutput is the manifest as qmuxctl discover prints it with --json.
type manifestOutput struct {
	FrameworkVersion uint64        `json:"framework_version"`
	Zone             string        `json:"zone"`
	Protocols        []entryOutput `json:"protocols"`
}

// entryOutput is one manifest entry in manifestOutput.
type entryOutput struct {
	ID       string   `json:"id"`
	Versions []uint16 `json:"versions"`
	Class    uint16   `json:"class"`
	Type     uint16   `json:"type"`
}

// runDiscover runs qmuxctl discover with the flags in args and returns the exit code.
func runDiscover(ctx context.Context, name string, args []string, stdout, stderr io.Writer) int {
	usage := name + " discover"
	synopsis := usage + " --server <addr> --name <zone> (--trust-anchors <path> | --insecure) [flags]"
	exits := "0 manifest, 1 DNS error or invalid manifest, 2 application error, " +
		"3 verification error or neither --trust-anchors nor --insecure, 64 usage error."
	f, fs, err := parseDiscoverFlags(usage, args)
	if errors.Is(err, flag.ErrHelp) {
		if printUsage(stdout, synopsis, exits, fs) != nil {
			return 1
		}
		return exitResult
	}
	if err != nil {
		// The exit code is 64 whether or not stderr accepts the write.
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		_ = printUsage(stderr, synopsis, exits, fs)
		return exitUsage
	}
	verifier, err := loadVerifier(f.trustAnchors)
	if err != nil {
		// The exit code is 64 whether or not stderr accepts the write.
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		_ = printUsage(stderr, synopsis, exits, fs)
		return exitUsage
	}
	if verifier == nil && !f.insecure {
		// The exit code is 3 whether or not stderr accepts the write.
		_, _ = fmt.Fprintln(stderr, errNoTrustAnchors)
		return exitVerify
	}

	key := discovery.DefaultKey
	if f.in {
		key = discovery.INKey
	}
	if f.setClass {
		key.Class = f.class
	}
	if f.setTyp {
		key.Type = f.typ
	}
	dc := discovery.Client{
		Client: client.NewClient(client.ClientConfig{Resolver: f.server, TrustAnchors: verifier}),
		Key:    key, InsecureSkipVerify: f.insecure,
	}
	m, err := dc.Discover(ctx, f.name)
	if err != nil {
		code := discoverExitCode(err)
		printDiscoverError(stderr, err)
		if code == exitUsage {
			_ = printUsage(stderr, synopsis, exits, fs)
		}
		return code
	}

	out := manifestOutput{FrameworkVersion: m.FrameworkVersion, Zone: m.Zone, Protocols: []entryOutput{}}
	for _, e := range m.Protocols {
		out.Protocols = append(out.Protocols, entryOutput(e))
	}
	if f.json {
		err = json.NewEncoder(stdout).Encode(out)
	} else {
		err = printManifest(stdout, out, verifier != nil)
	}
	if err != nil {
		// A caller that reads stdout must not see exit 0 with missing output.
		return 1
	}
	return exitResult
}

// printDiscoverError writes the error line of err to stderr. The states table of the
// spec names only the status for an application error. A verification error prints
// as in qmuxctl query, with no discovery prefix. The exit code does not depend on the
// write, so printDiscoverError ignores a write error.
func printDiscoverError(stderr io.Writer, err error) {
	var appErr *client.AppError
	var verifyErr *client.VerifyError
	switch {
	case errors.As(err, &appErr):
		_, _ = fmt.Fprintf(stderr, "error: %s\n", statusName(appErr.Status))
	case errors.As(err, &verifyErr):
		_, _ = fmt.Fprintf(stderr, "error: %v\n", verifyErr)
	default:
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
	}
}

// discoverExitCode returns the exit code of qmuxctl discover for the error of
// discovery.Client.Discover. An invalid manifest is a bad response from the server, so
// it exits 1 like a DNS mismatch. Every other error maps as in exitCode.
func discoverExitCode(err error) int {
	if errors.Is(err, discovery.ErrInvalidManifest) {
		return exitDNS
	}
	return exitCode(err)
}

// parseDiscoverFlags returns the flags in args. It returns flag.ErrHelp for --help, and
// an error for a missing flag, an invalid flag, an extra argument, or --in with --class.
func parseDiscoverFlags(usage string, args []string) (discoverFlags, *flag.FlagSet, error) {
	var f discoverFlags
	fs := flag.NewFlagSet(usage, flag.ContinueOnError)
	// runDiscover prints the error and the usage, so that --help goes to stdout and an
	// error goes to stderr once.
	fs.SetOutput(io.Discard)
	fs.StringVar(&f.server, "server", "", "IP address and port of the server, for example 127.0.0.1:5353 (required)")
	fs.StringVar(&f.name, "name", "", "zone of the server; the query goes to _qmux.<zone> (required)")
	fs.Func("class", "QCLASS of the discovery key, 0 to 65535 (default 65280)", uint16Flag(&f.class, &f.setClass))
	fs.Func("type", "QTYPE of the discovery key, 0 to 65535 (default 65534)", uint16Flag(&f.typ, &f.setTyp))
	fs.BoolVar(&f.in, "in", false, "use the IN discovery key 1/65534; not allowed with --class")
	fs.BoolVar(&f.insecure, "insecure", false, "accept a manifest with no verification when no --trust-anchors is set")
	fs.BoolVar(&f.json, "json", false, "print the manifest as one JSON object")
	fs.StringVar(&f.trustAnchors, "trust-anchors", "", "trust anchor file; the manifest must verify against it")

	if err := fs.Parse(args); err != nil {
		return f, fs, err
	}
	switch {
	case fs.NArg() > 0:
		return f, fs, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	case f.server == "":
		return f, fs, errors.New("missing flag --server")
	case f.name == "":
		return f, fs, errors.New("missing flag --name")
	case f.in && f.setClass:
		return f, fs, errors.New("flag --in sets the class, so it is not allowed with --class")
	}
	return f, fs, nil
}

// printManifest writes out as a header line and one table row per manifest entry. The
// header states whether the manifest was verified. The versions have no spaces, so
// each row has exactly four columns.
func printManifest(w io.Writer, out manifestOutput, verified bool) error {
	state := "no"
	if verified {
		state = "yes"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "zone %s  framework %d  verified %s\n", out.Zone, out.FrameworkVersion, state)
	b.WriteString("PROTOCOL\tVERSIONS\tCLASS\tTYPE\n")
	for _, e := range out.Protocols {
		vs := make([]string, len(e.Versions))
		for i, v := range e.Versions {
			vs[i] = strconv.FormatUint(uint64(v), 10)
		}
		fmt.Fprintf(&b, "%s\t%s\t%d\t%d\n", e.ID, strings.Join(vs, ","), e.Class, e.Type)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := io.WriteString(tw, b.String()); err != nil {
		return err
	}
	// The tabwriter buffers every row, so Flush returns the first write error.
	return tw.Flush()
}
