package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Crank-Git/qmux/pkg/protocol"
)

// observation holds the fields of one observation log line of qmuxd that qmux-probe
// reads. pkg/server/observation.go writes the line.
type observation struct {
	QName  string `json:"qname"`
	QClass uint16 `json:"qclass"`
	QType  uint16 `json:"qtype"`
}

// correlate sets the observation log fields of each record from the log at path. It
// returns a warning when path is set and the log cannot be read. With no log, the
// fields stay null.
func correlate(ctx context.Context, path string, recs []record) string {
	if path == "" {
		return ""
	}
	select {
	case <-ctx.Done():
	case <-time.After(settle):
	}
	seen, err := observe(path)
	if err != nil {
		return fmt.Sprintf("warning: %v; reached_authoritative is null", err)
	}
	for i := range recs {
		o, ok := seen[protocol.FoldName(recs[i].QName)]
		recs[i].ReachedAuthoritative = ptr(ok)
		if ok {
			recs[i].ObservedQClass, recs[i].ObservedQType = ptr(o.QClass), ptr(o.QType)
		}
	}
	return ""
}

// observe returns the first line for each lowercase QNAME in the observation log at
// path. A resolver can repeat a query, so later lines for the same QNAME change no
// record. observe skips a line that is not JSON, because qmuxd can be in the middle of
// a write.
func observe(path string) (map[string]observation, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read observation log: %w", err)
	}
	defer f.Close() //nolint:errcheck // The file is open for reading only.
	seen := make(map[string]observation)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var o observation
		if json.Unmarshal(sc.Bytes(), &o) != nil {
			continue
		}
		name := protocol.FoldName(o.QName)
		if _, ok := seen[name]; !ok {
			seen[name] = o
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read observation log %s: %w", path, err)
	}
	return seen, nil
}

// report writes one line per record and the warning to progress, the JSON file of
// --output, and the table of --markdown to stdout.
func report(name string, f probeFlags, recs []record, warning string, stdout, progress io.Writer) error {
	if err := printLines(progress, recs); err != nil {
		return fmt.Errorf("write progress: %w", err)
	}
	if warning != "" {
		if _, err := fmt.Fprintf(progress, "%s: %s\n", name, warning); err != nil {
			return fmt.Errorf("write progress: %w", err)
		}
	}
	if f.output != "" {
		b, err := json.MarshalIndent(recs, "", "  ")
		if err != nil {
			return fmt.Errorf("encode records: %w", err)
		}
		if err := os.WriteFile(f.output, append(b, '\n'), 0o644); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
		if _, err := fmt.Fprintf(progress, "%s: wrote %s (%d records)\n", name, f.output, len(recs)); err != nil {
			return fmt.Errorf("write progress: %w", err)
		}
	}
	if f.markdown {
		if err := printMarkdown(stdout, recs); err != nil {
			return fmt.Errorf("write markdown: %w", err)
		}
	}
	return nil
}

// printLines writes one aligned line per record, in the form of § Screens & states.
func printLines(w io.Writer, recs []record) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, r := range recs {
		rcode, verified, reached := "-", "-", "-"
		if r.RCode != nil {
			rcode = *r.RCode
		}
		if r.Verified {
			verified = "verified"
		}
		if r.ReachedAuthoritative != nil && *r.ReachedAuthoritative {
			reached = "reached"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%d/%d\t%s\t%s\t%s\t%s\t%.1f ms\t%d/%d B\n", r.Path, r.Transport, r.QClass, r.QType,
			rcode, r.Outcome, verified, reached, r.RTTMillis, r.QueryBytes, r.ResponseBytes); err != nil {
			return fmt.Errorf("write line: %w", err)
		}
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("flush: %w", err)
	}
	return nil
}

// printMarkdown writes the interop matrix with one row per record. A dash means that
// the value is unknown: no observation log, or no envelope to verify.
func printMarkdown(w io.Writer, recs []record) error {
	var b strings.Builder
	b.WriteString("| path | transport | class | type | outcome | reached | verified |\n|---|---|---|---|---|---|---|\n")
	for _, r := range recs {
		outcome := r.Outcome
		if r.Outcome == outcomeRCode {
			outcome += " " + *r.RCode
		}
		reached := "—"
		if r.ReachedAuthoritative != nil {
			reached = yesNo(*r.ReachedAuthoritative)
		}
		verified := "—"
		if r.Verified || r.Outcome == outcomeVerify {
			verified = yesNo(r.Verified)
		}
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %s | %s | %s |\n", r.Path, r.Transport, r.QClass, r.QType, outcome, reached, verified)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
