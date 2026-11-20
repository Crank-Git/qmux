package client

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/security"
	"github.com/Crank-Git/qmux/pkg/transport"
)

// ErrorKind is the DNS outcome that a DNSError reports.
type ErrorKind string

// The kinds of DNSError.
const (
	// KindRCode means that the response has an RCODE other than NOERROR.
	KindRCode ErrorKind = "rcode"
	// KindTimeout means that no response arrived before the attempts or ctx ended.
	KindTimeout ErrorKind = "timeout"
	// KindNetwork means that the dial, the write or the read failed for a reason other
	// than a timeout. A resolver that is not an IP address and port is also this kind.
	KindNetwork ErrorKind = "network"
	// KindMismatch means that the response question or answer RR differs from the
	// query, or that the envelope protocol ID or version differs from the query.
	KindMismatch ErrorKind = "mismatch"
	// KindMalformed means that the response does not parse, or that the RDATA of the
	// answer RR is not a valid envelope.
	KindMalformed ErrorKind = "malformed"
	// KindTruncation means that the UDP response has the TC bit and the client sends
	// no TCP query.
	KindTruncation ErrorKind = "truncation"
)

// DNSError is a DNS outcome that gives no result. It never holds an application
// status or a verification result.
type DNSError struct {
	Kind ErrorKind
	// RCode is the RCODE of the response for the kind KindRCode, and 0 for other kinds.
	RCode int
	// Name is the QNAME that the client sent.
	Name string
	Key  protocol.ProtocolKey
	// Attempts is the number of attempts that Query made for the kind KindTimeout, and 0
	// for other kinds. A TCP fallback attempt counts.
	Attempts int
	// Resolver is the IP address and port that Query sent to for the kind KindTimeout,
	// and empty for other kinds.
	Resolver string
	// Err is the cause. It is nil for the kind KindRCode, because the RCODE is the
	// complete outcome.
	Err error
}

// Error returns the text that qmuxctl prints after "error: ".
//
// For the kind KindRCode, the text is "dns: rcode <RCODE> for <QNAME> <class>/<type>",
// for example "dns: rcode REFUSED for device42.fleet.example. 65283/65280".
//
// For the kind KindTimeout with Attempts set, the text is
// "dns: timeout after <Attempts> attempts to <Resolver>". The text gives no cause,
// because the cause of a timeout is always the end of a deadline.
//
// For every other DNSError, the text is "dns: <kind> for <QNAME> <class>/<type>: <cause>".
func (e *DNSError) Error() string {
	switch {
	case e.Kind == KindRCode:
		return "dns: rcode " + transport.RCode(e.RCode).String() + " for " + e.Name + " " + e.Key.String()
	case e.Kind == KindTimeout && e.Attempts > 0:
		unit := " attempts"
		if e.Attempts == 1 {
			unit = " attempt"
		}
		return "dns: timeout after " + strconv.Itoa(e.Attempts) + unit + " to " + e.Resolver
	}
	s := "dns: " + string(e.Kind) + " for " + e.Name + " " + e.Key.String()
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap returns the cause, so that errors.Is finds transport.ErrMismatch,
// transport.ErrMalformed and context.DeadlineExceeded.
func (e *DNSError) Unwrap() error {
	return e.Err
}

// AppError is a response that the server answered with an application status other
// than StatusOK. Client.Query returns it as a *AppError together with the result.
type AppError struct {
	Status     protocol.Status
	ProtocolID string
	Version    uint16
}

// Error returns the application status, the protocol ID and the protocol version.
func (e *AppError) Error() string {
	return "client: application status " + strconv.FormatUint(uint64(e.Status), 10) +
		" from " + e.ProtocolID + " version " + strconv.FormatUint(uint64(e.Version), 10)
}

// VerifyError is a response that did not verify against the trust anchors.
// Client.Query returns it as a *VerifyError, and no result.
type VerifyError struct {
	// Reason is the security sentinel of the failed check, for example
	// security.ErrExpired. For RDATA that is not a valid envelope, Reason is the codec
	// error, because no sentinel exists for it.
	Reason error
	// Err is the error of security.Verifier.Verify. It names the protocol ID and the
	// QNAME.
	Err error
}

// Error returns the text of Err, which qmuxctl prints after "error: ". Without Err,
// the text names Reason.
func (e *VerifyError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "verify: " + fmt.Sprint(e.Reason)
}

// Unwrap returns Reason and Err, so that errors.Is finds the sentinel in each.
func (e *VerifyError) Unwrap() []error {
	return []error{e.Reason, e.Err}
}

// sentinels are the errors of security.Verifier.Verify, one for each check.
var sentinels = []error{
	security.ErrNotDeterministic, security.ErrUnsigned, security.ErrAlgorithm,
	security.ErrUnknownKey, security.ErrProtocolNotAllowed, security.ErrBadSignature,
	security.ErrNotYetValid, security.ErrExpired, security.ErrWindowTooLong, security.ErrReplay,
}

// reason returns the sentinel that err wraps. It returns the codec error that err wraps
// when err holds no sentinel, because Verify wraps it with the QNAME only.
func reason(err error) error {
	for _, s := range sentinels {
		if errors.Is(err, s) {
			return s
		}
	}
	if u := errors.Unwrap(err); u != nil {
		return u
	}
	return err
}
