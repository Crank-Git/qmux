package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/security"
	"github.com/Crank-Git/qmux/pkg/transport"
)

// The exit codes of qmux-probe.
const (
	exitRecords      = 0
	exitDirectFailed = 1
	exitUsage        = 64
)

// The outcomes of a probe. Each record holds exactly one.
const (
	outcomeOK       = "ok"
	outcomeRCode    = "rcode"
	outcomeTimeout  = "timeout"
	outcomeNetwork  = "network"
	outcomeMismatch = "mismatch"
	outcomeVerify   = "verify"
)

const (
	// probeProtocolID is the protocol ID of the probe protocol of qmuxd (FR-interop-2).
	probeProtocolID = "org.qmux.probe"
	// nonceBytes gives a nonce of 16 hex characters (FR-interop-17).
	nonceBytes = 8
	// udpSize is the client default. qmux-probe sets it, so that query_bytes of a probe
	// with no result comes from the same query that the client sends.
	udpSize = 1232
	// settle is the wait after the last probe before qmux-probe reads the observation
	// log, because qmuxd can write the line after it sends the response.
	settle = 500 * time.Millisecond
)

// record is one probe record, with the fields of § Data touched in
// docs/specs/features/09-interop-harness.md. A nil pointer is JSON null.
type record struct {
	Path                 string  `json:"path"`
	Transport            string  `json:"transport"`
	QClass               uint16  `json:"qclass"`
	QType                uint16  `json:"qtype"`
	QName                string  `json:"qname"`
	RCode                *string `json:"rcode"`
	AnswerClass          *uint16 `json:"answer_class"`
	AnswerType           *uint16 `json:"answer_type"`
	PayloadOK            bool    `json:"payload_ok"`
	Verified             bool    `json:"verified"`
	RTTMillis            float64 `json:"rtt_ms"`
	QueryBytes           int     `json:"query_bytes"`
	ResponseBytes        int     `json:"response_bytes"`
	Outcome              string  `json:"outcome"`
	Error                string  `json:"error"`
	ReachedAuthoritative *bool   `json:"reached_authoritative"`
	ObservedQClass       *uint16 `json:"observed_qclass"`
	ObservedQType        *uint16 `json:"observed_qtype"`
}

// runProbe runs every probe of the flags in args and returns the exit code.
func runProbe(ctx context.Context, name string, args []string, stdout, stderr io.Writer) int {
	f, fs, err := parseFlags(name, args)
	if errors.Is(err, flag.ErrHelp) {
		if printUsage(stdout, name, fs) != nil {
			return 1
		}
		return exitRecords
	}
	if err != nil {
		// The exit code is 64 whether or not stderr accepts the write.
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", name, err)
		_ = printUsage(stderr, name, fs)
		return exitUsage
	}
	anchors, err := security.LoadTrustAnchors(f.trustAnchors)
	if err != nil {
		// The exit code is 64 whether or not stderr accepts the write.
		_, _ = fmt.Fprintf(stderr, "%s: invalid value for flag --trust-anchors: %v\n", name, err)
		_ = printUsage(stderr, name, fs)
		return exitUsage
	}

	// The table goes to stdout alone, so that a caller can redirect it to a file.
	progress := stdout
	if f.markdown {
		progress = stderr
	}
	n := len(f.paths) * len(f.transports) * len(f.classes) * len(f.types)
	if _, err := fmt.Fprintf(progress, "%s: %d paths x %d transports x %d classes x %d types = %d probes\n",
		name, len(f.paths), len(f.transports), len(f.classes), len(f.types), n); err != nil {
		return 1
	}

	recs := probeAll(ctx, f, &security.Verifier{Anchors: anchors})
	warning := correlate(ctx, f.observationLog, recs)
	if err := report(name, f, recs, warning, stdout, progress); err != nil {
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	if f.requireDirect {
		for _, r := range recs {
			if r.Path == directPath && r.Outcome != outcomeOK {
				return exitDirectFailed
			}
		}
	}
	return exitRecords
}

// probeAll sends one probe for each combination of path, transport, class and type, in
// that order. It sends the probes one at a time, so that each observation log line
// maps to one probe.
func probeAll(ctx context.Context, f probeFlags, v *security.Verifier) []record {
	var recs []record
	for _, p := range f.paths {
		for _, network := range f.transports {
			c := client.NewClient(client.ClientConfig{
				Resolver: p.addr, Network: network, AttemptTimeout: f.timeout,
				// One attempt per probe: a retry or a TCP fallback would hide the outcome.
				Attempts: 1, DisableTCPFallback: true, UDPSize: udpSize,
				Recursion: p.name != directPath, TrustAnchors: v,
			})
			for _, class := range f.classes {
				for _, typ := range f.types {
					key := protocol.ProtocolKey{Class: class, Type: typ}
					recs = append(recs, probe(ctx, c, p, network, key, nonce()+"."+f.zone))
				}
			}
		}
	}
	return recs
}

// probe sends one query for qname and key with c and returns its record without the
// observation log fields.
func probe(ctx context.Context, c *client.Client, p path, network string, key protocol.ProtocolKey, qname string) record {
	r := record{Path: p.name, Transport: network, QClass: key.Class, QType: key.Type, QName: qname}
	start := time.Now()
	res, err := c.Query(ctx, client.Query{Name: qname, Class: key.Class, Type: key.Type, ExpectedProtocolID: probeProtocolID})
	r.RTTMillis = millis(time.Since(start))
	// A query with no result gives no sizes, so the size of the query comes from the
	// same packet that the client builds. The zone check of parseFlags excludes an error.
	if q, qerr := transport.NewQuery(qname, key, udpSize, p.name != directPath); res == nil && qerr == nil {
		r.QueryBytes = len(q.Wire)
	}
	r.Outcome = outcomeOf(err)
	if err != nil {
		r.Error = err.Error()
	}

	var de *client.DNSError
	var ve *client.VerifyError
	switch {
	case res != nil:
		r.RCode, r.AnswerClass, r.AnswerType = ptr("NOERROR"), ptr(key.Class), ptr(key.Type)
		r.RTTMillis = millis(res.RTT)
		r.QueryBytes, r.ResponseBytes = res.QueryBytes, res.ResponseBytes
		r.Verified = res.Verified
		r.PayloadOK = payloadOK(qname, res.Payload)
		if err == nil && !r.PayloadOK {
			r.Outcome, r.Error = outcomeVerify, "payload is not the SHA-256 digest of the lowercase QNAME"
		}
	case errors.As(err, &ve):
		// The transport accepts only an answer RR with the class and type of the
		// question, and the client verifies after that check.
		r.RCode, r.AnswerClass, r.AnswerType = ptr("NOERROR"), ptr(key.Class), ptr(key.Type)
	case errors.As(err, &de) && de.Kind == client.KindRCode:
		r.RCode = ptr(transport.RCode(de.RCode).String())
	}
	return r
}

// outcomeOf returns the outcome for the error of client.Query. An error with a result
// that passes every check gives outcomeOK, and probe checks the payload after it.
//
// The client has more error kinds than the spec has outcomes:
//   - A malformed or truncated response gives outcomeNetwork, because no usable DNS
//     answer arrived.
//   - An application status other than ok gives outcomeVerify, because the envelope
//     holds no probe payload.
//   - Any other error gives outcomeNetwork. The flag checks exclude a QNAME that the
//     client cannot send.
func outcomeOf(err error) string {
	var de *client.DNSError
	var ve *client.VerifyError
	var ae *client.AppError
	switch {
	case err == nil:
		return outcomeOK
	case errors.As(err, &ve), errors.As(err, &ae):
		return outcomeVerify
	case errors.As(err, &de):
		switch de.Kind {
		case client.KindRCode:
			return outcomeRCode
		case client.KindTimeout:
			return outcomeTimeout
		case client.KindMismatch:
			return outcomeMismatch
		}
	}
	return outcomeNetwork
}

// payloadOK reports whether payload is the SHA-256 digest of the lowercase qname
// (FR-interop-6, FR-interop-27).
func payloadOK(qname string, payload []byte) bool {
	sum := sha256.Sum256([]byte(protocol.FoldName(qname)))
	return bytes.Equal(payload, sum[:])
}

// nonce returns 16 random lowercase hex characters.
func nonce() string {
	b := make([]byte, nonceBytes)
	// crypto/rand.Read never returns an error. It ends the program when the operating
	// system has no random source.
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func millis(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

func ptr[T any](v T) *T {
	return &v
}
