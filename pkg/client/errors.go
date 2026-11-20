package client

import (
	"strconv"

	"github.com/Crank-Git/qmux/pkg/protocol"
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
	// query.
	KindMismatch ErrorKind = "mismatch"
	// KindMalformed means that the response does not parse.
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
	// Err is the cause. It is nil for the kind KindRCode, because the RCODE is the
	// complete outcome.
	Err error
}

// Error returns the kind, the QNAME and the protocol key. For the kind KindRCode, it
// also returns the RCODE text, for example "REFUSED".
func (e *DNSError) Error() string {
	s := "client: " + string(e.Kind) + " error for " + e.Name + " " + e.Key.String()
	if e.Kind == KindRCode {
		s += ": " + transport.RCode(e.RCode).String()
	}
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
// than StatusOK. The client returns it from Epic 4.
type AppError struct {
	Status     protocol.Status
	ProtocolID string
	Version    uint16
}

// Error returns the application status, the protocol ID and the protocol version.
func (e AppError) Error() string {
	return "client: application status " + strconv.FormatUint(uint64(e.Status), 10) +
		" from " + e.ProtocolID + " version " + strconv.FormatUint(uint64(e.Version), 10)
}

// VerifyError is a response that did not verify against the trust anchors. The client
// returns it from Epic 6.
type VerifyError struct {
	// Reason names the check that failed, for example "expired".
	Reason string
	Err    error
}

// Error returns the reason and the cause.
func (e VerifyError) Error() string {
	s := "client: verification failed: " + e.Reason
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap returns the cause.
func (e VerifyError) Unwrap() error {
	return e.Err
}
