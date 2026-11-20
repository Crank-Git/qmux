package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/security"
)

// The exit codes of qmuxctl query, from FR-client-36 to FR-client-40.
const (
	exitResult = 0
	exitDNS    = 1
	exitApp    = 2
	exitVerify = 3
	exitUsage  = 64
)

// queryFlags holds the parsed flags of qmuxctl query.
type queryFlags struct {
	server, name     string
	trustAnchors     string
	class, typ, ver  uint16
	tcp, json        bool
	timeout          time.Duration
	setClass, setTyp bool
}

// queryOutput is the result as qmuxctl query prints it. The field order is the line
// order of the text output.
type queryOutput struct {
	Name          string  `json:"name"`
	Class         uint16  `json:"class"`
	Type          uint16  `json:"type"`
	RCode         string  `json:"rcode"`
	TTL           uint32  `json:"ttl"`
	Transport     string  `json:"transport"`
	FellBack      bool    `json:"fell_back"`
	RTTMillis     float64 `json:"rtt_ms"`
	QueryBytes    int     `json:"query_bytes"`
	ResponseBytes int     `json:"response_bytes"`
	Verified      bool    `json:"verified"`
	ProtocolID    string  `json:"protocol_id"`
	Version       uint16  `json:"version"`
	MessageType   string  `json:"message_type"`
	Status        string  `json:"status"`
	PayloadBytes  int     `json:"payload_bytes"`
	Payload       string  `json:"payload"`
}

// runQuery runs qmuxctl query with the flags in args and returns the exit code.
func runQuery(ctx context.Context, name string, args []string, stdout, stderr io.Writer) int {
	usage := name + " query"
	synopsis := usage + " --server <addr> --class <n> --type <n> --name <qname> [flags]"
	exits := "0 result, 1 DNS error, 2 application error, 3 verification error, 64 usage error."
	f, fs, err := parseQueryFlags(usage, args)
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

	network := "udp"
	if f.tcp {
		network = "tcp"
	}
	c := client.NewClient(client.ClientConfig{
		Resolver: f.server, Network: network, AttemptTimeout: f.timeout, TrustAnchors: verifier,
	})
	res, err := c.Query(ctx, client.Query{Name: f.name, Class: f.class, Type: f.typ, Version: f.ver})
	code := exitCode(err)
	// Query returns a result only with no error or with an AppError.
	if res == nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		if code == exitUsage {
			_ = printUsage(stderr, synopsis, exits, fs)
		}
		return code
	}

	out := queryOutput{
		Name: res.Name, Class: res.Key.Class, Type: res.Key.Type,
		// Query returns a KindRCode DNSError for every RCODE other than NOERROR, so a
		// result always has NOERROR.
		RCode: "NOERROR", TTL: res.TTL, Transport: res.Transport, FellBack: res.FellBack,
		RTTMillis:  float64(res.RTT) / float64(time.Millisecond),
		QueryBytes: res.QueryBytes, ResponseBytes: res.ResponseBytes,
		Verified: res.Verified, ProtocolID: res.ProtocolID, Version: res.Version,
		MessageType: messageTypeName(res.MessageType), Status: statusName(res.Status),
		PayloadBytes: len(res.Payload), Payload: hex.EncodeToString(res.Payload),
	}
	var werr error
	if f.json {
		werr = json.NewEncoder(stdout).Encode(out)
	} else {
		werr = printText(stdout, out)
	}
	if werr != nil {
		// A caller that reads stdout must not see exit 0 with missing output.
		return 1
	}
	if err != nil {
		// The result goes to stdout first, so a script reads the status and the error.
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
	}
	return code
}

// statusName returns the name of a framework application status, or the decimal
// number for a reserved or protocol-specific status.
func statusName(s protocol.Status) string {
	switch s {
	case protocol.StatusOK:
		return "ok"
	case protocol.StatusNotFound:
		return "not-found"
	case protocol.StatusBadRequest:
		return "bad-request"
	case protocol.StatusVersionUnsupported:
		return "version-unsupported"
	case protocol.StatusUnavailable:
		return "unavailable"
	}
	return strconv.FormatUint(uint64(s), 10)
}

// messageTypeName returns "record" or "manifest" for a framework message type, or the
// decimal number for a protocol-specific message type.
func messageTypeName(t protocol.MessageType) string {
	switch t {
	case protocol.MessageRecord:
		return "record"
	case protocol.MessageManifest:
		return "manifest"
	}
	return strconv.FormatUint(uint64(t), 10)
}

// parseQueryFlags returns the flags in args. It returns flag.ErrHelp for --help, and an
// error for a missing flag, an invalid flag or an extra argument.
func parseQueryFlags(usage string, args []string) (queryFlags, *flag.FlagSet, error) {
	var f queryFlags
	fs := flag.NewFlagSet(usage, flag.ContinueOnError)
	// runQuery prints the error and the usage, so that --help goes to stdout and an
	// error goes to stderr once.
	fs.SetOutput(io.Discard)
	fs.StringVar(&f.server, "server", "", "IP address and port of the server, for example 127.0.0.1:5353 (required)")
	fs.Func("class", "QCLASS of the protocol key, 0 to 65535 (required)", uint16Flag(&f.class, &f.setClass))
	fs.Func("type", "QTYPE of the protocol key, 0 to 65535 (required)", uint16Flag(&f.typ, &f.setTyp))
	fs.StringVar(&f.name, "name", "", "QNAME of the resource (required)")
	fs.Func("version", "protocol version for the version label, 0 for no label (default 0)", uint16Flag(&f.ver, new(bool)))
	fs.BoolVar(&f.tcp, "tcp", false, "send the query over TCP")
	fs.DurationVar(&f.timeout, "timeout", 0, "time that one attempt waits for a response (default 1s)")
	fs.BoolVar(&f.json, "json", false, "print the result as one JSON object")
	fs.StringVar(&f.trustAnchors, "trust-anchors", "", "trust anchor file; the result must verify against it")

	if err := fs.Parse(args); err != nil {
		return f, fs, err
	}
	switch {
	case fs.NArg() > 0:
		return f, fs, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	case f.server == "":
		return f, fs, errors.New("missing flag --server")
	case !f.setClass:
		return f, fs, errors.New("missing flag --class")
	case !f.setTyp:
		return f, fs, errors.New("missing flag --type")
	case f.name == "":
		return f, fs, errors.New("missing flag --name")
	case f.timeout < 0:
		return f, fs, fmt.Errorf("invalid value %v for flag --timeout: negative duration", f.timeout)
	}
	return f, fs, nil
}

// loadVerifier returns a verifier with the trust anchors in the file at path, or nil
// when path is empty. The verifier keeps an in-memory freshness store, because one
// qmuxctl run sends one query.
func loadVerifier(path string) (*security.Verifier, error) {
	if path == "" {
		return nil, nil
	}
	anchors, err := security.LoadTrustAnchors(path)
	if err != nil {
		return nil, fmt.Errorf("invalid value for flag --trust-anchors: %w", err)
	}
	return &security.Verifier{Anchors: anchors}, nil
}

// uint16Flag returns a flag function that parses a decimal value into dst and sets
// *set to true.
func uint16Flag(dst *uint16, set *bool) func(string) error {
	return func(s string) error {
		v, err := strconv.ParseUint(s, 10, 16)
		if err != nil {
			return fmt.Errorf("not a number from 0 to 65535: %w", err)
		}
		*dst, *set = uint16(v), true
		return nil
	}
}

// printUsage writes the synopsis, the flags of fs and the exit codes to w, with each
// flag in the form "--name".
func printUsage(w io.Writer, synopsis, exits string, fs *flag.FlagSet) error {
	var b strings.Builder
	b.WriteString("Usage: " + synopsis + "\n\nFlags:\n")
	fs.VisitAll(func(fl *flag.Flag) {
		fmt.Fprintf(&b, "  --%-9s %s\n", fl.Name, fl.Usage)
	})
	b.WriteString("\nExit codes: " + exits + "\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// printText writes out as one "key: value" line per field. The envelope fields come
// after the DNS fields, and the payload hex is the last line because it can be long.
func printText(w io.Writer, out queryOutput) error {
	_, err := fmt.Fprintf(w, "name: %s\nclass: %d\ntype: %d\nrcode: %s\nttl: %d\ntransport: %s\n"+
		"fell_back: %t\nrtt_ms: %.2f\nquery_bytes: %d\nresponse_bytes: %d\nverified: %t\n"+
		"protocol_id: %s\nversion: %d\nmessage_type: %s\nstatus: %s\npayload_bytes: %d\npayload: %s\n",
		out.Name, out.Class, out.Type, out.RCode, out.TTL, out.Transport,
		out.FellBack, out.RTTMillis, out.QueryBytes, out.ResponseBytes, out.Verified,
		out.ProtocolID, out.Version, out.MessageType, out.Status, out.PayloadBytes, out.Payload)
	return err
}

// exitCode returns the exit code of qmuxctl query for the error of client.Query:
//   - 0 for nil.
//   - 1 for a *client.DNSError.
//   - 2 for a *client.AppError.
//   - 3 for a *client.VerifyError.
//   - 64 for every other error.
//
// Query returns an error of no QMux type only when it cannot build the query from the
// QNAME, or when ctx is cancelled. qmuxctl never cancels ctx, so the error comes from
// the --name flag, and an invalid flag exits 64.
func exitCode(err error) int {
	var dnsErr *client.DNSError
	var appErr *client.AppError
	var verifyErr *client.VerifyError
	switch {
	case err == nil:
		return exitResult
	case errors.As(err, &dnsErr):
		return exitDNS
	case errors.As(err, &appErr):
		return exitApp
	case errors.As(err, &verifyErr):
		return exitVerify
	default:
		return exitUsage
	}
}
