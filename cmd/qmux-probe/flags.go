package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/transport"
)

// The defaults of FR-interop-13 to FR-interop-16 and FR-interop-19.
const (
	defaultClasses    = "1,3,4,65280,65281"
	defaultTypes      = "16,65280"
	defaultTransports = "udp,tcp"
	defaultZone       = "probe.fleet.example."
	defaultTimeout    = 3 * time.Second
)

// directPath is the path name of the probes to --server.
const directPath = "direct"

// errNoAnchors has the text of the error state "No trust anchor file" in the spec.
var errNoAnchors = errors.New("--trust-anchors is required")

// path is one destination of the probes: the direct path or one resolver.
type path struct {
	name, addr string
	// emulated is true when the image of the path has an architecture other than the
	// host architecture (FR-interop-39c).
	emulated bool
}

// probeFlags holds the parsed flags of qmux-probe.
type probeFlags struct {
	paths          []path
	classes, types []uint16
	transports     []string
	zone           string
	trustAnchors   string
	timeout        time.Duration
	observationLog string
	// hostArch is the architecture of the Docker host, in the GOARCH form.
	hostArch      string
	output        string
	markdown      bool
	requireDirect bool
}

// parseFlags returns the flags in args. It returns flag.ErrHelp for --help, and an
// error for a missing, invalid or extra argument. The direct path, when --server is
// set, is the first path. The resolver paths follow in the order of the flags.
func parseFlags(name string, args []string) (probeFlags, *flag.FlagSet, error) {
	var f probeFlags
	var server, classes, types, transports string
	var resolvers []path
	imageArch := archFlag{}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	// runProbe prints the error and the usage, so that --help goes to stdout and an
	// error goes to stderr once.
	fs.SetOutput(io.Discard)
	fs.StringVar(&server, "server", "", "IP address and port of qmuxd for the direct path")
	fs.Func("resolver", "name=addr of one resolver path; the flag repeats", func(s string) error {
		r, err := parseResolver(s)
		resolvers = append(resolvers, r)
		return err
	})
	fs.Var(imageArch, "image-arch", "name=arch of the image of one path, when it differs from --host-arch; the flag repeats")
	// The probe container runs without emulation, so its architecture is the host
	// architecture.
	fs.StringVar(&f.hostArch, "host-arch", runtime.GOARCH, "architecture of the Docker host")
	fs.StringVar(&classes, "classes", defaultClasses, "comma-separated QCLASS list")
	fs.StringVar(&types, "types", defaultTypes, "comma-separated QTYPE list")
	fs.StringVar(&transports, "transports", defaultTransports, "comma-separated list of udp and tcp")
	fs.StringVar(&f.zone, "zone", defaultZone, "probe zone; each QNAME is <nonce>.<zone>")
	fs.StringVar(&f.trustAnchors, "trust-anchors", "", "trust anchor file that verifies every answer (required)")
	fs.DurationVar(&f.timeout, "timeout", defaultTimeout, "deadline of one probe")
	fs.StringVar(&f.observationLog, "observation-log", "", "observation log of qmuxd")
	fs.StringVar(&f.output, "output", "", "file for the JSON array of probe records")
	fs.BoolVar(&f.markdown, "markdown", false, "print the interop matrix as a markdown table")
	fs.BoolVar(&f.requireDirect, "require-direct", false, "exit 1 when a direct probe has an outcome other than ok")

	if err := fs.Parse(args); err != nil {
		return f, fs, err
	}
	if fs.NArg() > 0 {
		return f, fs, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if f.trustAnchors == "" {
		return f, fs, errNoAnchors
	}
	if server != "" {
		if _, err := netip.ParseAddrPort(server); err != nil {
			return f, fs, fmt.Errorf("invalid value %q for flag --server: %w", server, err)
		}
		f.paths = append(f.paths, path{name: directPath, addr: server})
	}
	f.paths = append(f.paths, resolvers...)
	if err := checkPaths(f.paths); err != nil {
		return f, fs, err
	}
	if err := setEmulated(f.paths, imageArch, f.hostArch); err != nil {
		return f, fs, err
	}
	var err error
	if f.classes, err = uint16List("classes", classes); err != nil {
		return f, fs, err
	}
	if f.types, err = uint16List("types", types); err != nil {
		return f, fs, err
	}
	if f.transports, err = transportList(transports); err != nil {
		return f, fs, err
	}
	if f.zone, err = checkZone(f.zone); err != nil {
		return f, fs, err
	}
	if f.timeout <= 0 {
		return f, fs, fmt.Errorf("invalid value %v for flag --timeout: not a positive duration", f.timeout)
	}
	return f, fs, nil
}

// parseResolver returns the path of a --resolver value "name=addr". addr must be an
// IP address and port, because the client never resolves a name through DNS.
func parseResolver(s string) (path, error) {
	name, addr, ok := strings.Cut(s, "=")
	if !ok || name == "" {
		return path{}, fmt.Errorf("%q is not name=addr", s)
	}
	if name == directPath {
		return path{}, fmt.Errorf("the name %q is reserved for --server", directPath)
	}
	if _, err := netip.ParseAddrPort(addr); err != nil {
		return path{}, fmt.Errorf("%q: %w", s, err)
	}
	return path{name: name, addr: addr}, nil
}

// checkPaths returns an error when paths is empty or when two resolver paths share a
// name, because the records of the two paths would then be the same.
func checkPaths(paths []path) error {
	if len(paths) == 0 {
		return errors.New("missing flag --server or --resolver")
	}
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		if seen[p.name] {
			return fmt.Errorf("invalid value for flag --resolver: the name %q is used twice", p.name)
		}
		seen[p.name] = true
	}
	return nil
}

// archFlag maps a path name to the image architecture of the path. Its Set method
// reads one --image-arch value name=arch.
type archFlag map[string]string

// String returns the empty default of --image-arch.
func (a archFlag) String() string { return "" }

// Set adds one value name=arch. It returns an error for a value with no name or no
// arch, and for a second value of the same path.
func (a archFlag) Set(s string) error {
	name, arch, ok := strings.Cut(s, "=")
	switch {
	case !ok || name == "" || arch == "":
		return fmt.Errorf("%q is not name=arch", s)
	case a[name] != "":
		return fmt.Errorf("the path %q has two values", name)
	}
	a[name] = arch
	return nil
}

// setEmulated marks each path whose image architecture in imageArch differs from
// hostArch. A path that imageArch does not name has an image of the host architecture.
// It returns an error when hostArch is empty, and when imageArch names no path,
// because the value then labels no record.
func setEmulated(paths []path, imageArch archFlag, hostArch string) error {
	if hostArch == "" {
		return errors.New("invalid value for flag --host-arch: the value is empty")
	}
	for name, arch := range imageArch {
		i := slices.IndexFunc(paths, func(p path) bool { return p.name == name })
		if i < 0 {
			return fmt.Errorf("invalid value for flag --image-arch: no path has the name %q", name)
		}
		paths[i].emulated = arch != hostArch
	}
	return nil
}

// uint16List returns the values of a comma-separated list for the flag --<flagName>.
func uint16List(flagName, s string) ([]uint16, error) {
	var out []uint16
	for _, v := range strings.Split(s, ",") {
		n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 16)
		if err != nil {
			return nil, fmt.Errorf("invalid value %q for flag --%s: not a list of numbers from 0 to 65535", s, flagName)
		}
		out = append(out, uint16(n))
	}
	return out, nil
}

// transportList returns the transports of a comma-separated list of "udp" and "tcp".
func transportList(s string) ([]string, error) {
	var out []string
	for _, v := range strings.Split(s, ",") {
		v = strings.TrimSpace(v)
		if v != transport.UDP && v != transport.TCP {
			return nil, fmt.Errorf("invalid value %q for flag --transports: %q is not udp or tcp", s, v)
		}
		out = append(out, v)
	}
	return out, nil
}

// checkZone returns zone as a fully qualified name. It returns an error when a QNAME
// below zone cannot be a DNS name, because every probe would then fail the same way.
func checkZone(zone string) (string, error) {
	if !strings.HasSuffix(zone, ".") {
		zone += "."
	}
	if _, err := transport.NewQuery(strings.Repeat("0", nonceBytes*2)+"."+zone, protocol.ProtocolKey{}, udpSize, false); err != nil {
		return "", fmt.Errorf("invalid value %q for flag --zone: %w", zone, err)
	}
	return zone, nil
}

// printUsage writes the synopsis, the flags of fs and the exit codes to w, with each
// flag in the form "--name".
func printUsage(w io.Writer, name string, fs *flag.FlagSet) error {
	var b strings.Builder
	b.WriteString("Usage: " + name + " --trust-anchors <file> [--server <addr>] [--resolver <name=addr>]... [flags]\n")
	b.WriteString("       " + name + " <version|help>\n\nFlags:\n")
	fs.VisitAll(func(fl *flag.Flag) {
		fmt.Fprintf(&b, "  --%-16s %s", fl.Name, fl.Usage)
		if fl.DefValue != "" && fl.DefValue != "false" {
			fmt.Fprintf(&b, " (default %s)", fl.DefValue)
		}
		b.WriteString("\n")
	})
	b.WriteString("\nExit codes: 0 every probe has a record, 1 a direct probe failed with --require-direct" +
		" or an output write failed, 64 usage error.\n")
	_, err := io.WriteString(w, b.String())
	return err
}
