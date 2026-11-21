package transport

import (
	"context"
	"errors"
	"net/netip"
	"strconv"
	"time"

	"github.com/Crank-Git/qmux/pkg/protocol"
)

// RCode is a DNS RCODE. Values above 15 are extended RCODEs, which the OPT RR carries.
type RCode uint16

// The RCODEs that QMux sends.
const (
	RCodeNoError  RCode = 0
	RCodeFormErr  RCode = 1
	RCodeServFail RCode = 2
	RCodeNotImp   RCode = 4
	RCodeRefused  RCode = 5
	RCodeBadVers  RCode = 16
)

// String returns the RCODE mnemonic, for example "NOERROR" or "BADVERS". For an RCODE
// that QMux does not send, it returns "RCODE" and the decimal value.
func (r RCode) String() string {
	switch r {
	case RCodeNoError:
		return "NOERROR"
	case RCodeFormErr:
		return "FORMERR"
	case RCodeServFail:
		return "SERVFAIL"
	case RCodeNotImp:
		return "NOTIMP"
	case RCodeRefused:
		return "REFUSED"
	case RCodeBadVers:
		return "BADVERS"
	}
	return "RCODE" + strconv.FormatUint(uint64(r), 10)
}

// The transport names. Query.Transport holds one of them.
const (
	UDP = "udp"
	TCP = "tcp"
)

var (
	// ErrMalformed means that a message does not parse.
	ErrMalformed = errors.New("transport: malformed message")
	// ErrAlreadyWritten means that the Writer already sent the response to the query.
	ErrAlreadyWritten = errors.New("transport: response already written")
)

// Query is one received DNS query.
type Query struct {
	ID uint16
	// Opcode stays as received. The server answers NOTIMP to an opcode other than QUERY.
	Opcode uint16
	QR     bool
	// QuestionCount is the QDCOUNT field of the header.
	QuestionCount int
	// Name is the QNAME of question 1, with the case as received.
	Name string
	// Key holds the QCLASS and the QTYPE of question 1.
	Key         protocol.ProtocolKey
	EDNS        bool
	EDNSVersion uint8
	// UDPSize is the UDP size that the OPT RR advertises, or 0 without EDNS(0).
	UDPSize uint16
	// WireSize is the exact number of bytes received.
	WireSize int
	Source   netip.AddrPort
	// Transport is UDP or TCP.
	Transport  string
	ReceivedAt time.Time
	// Err wraps ErrMalformed when the header parsed and the body did not. Only the
	// header fields are set then.
	Err error
}

// Answer is the response that a handler gives to Writer.Write.
type Answer struct {
	RCode RCode
	// TTL is the TTL of the answer RR.
	TTL uint32
	// Payload is the RDATA of the answer RR. Write sends an answer RR only for
	// RCodeNoError. For QTYPE 16, Write splits Payload into TXT character-strings of
	// at most 255 bytes.
	Payload []byte
}

// Sent describes the response that Writer.Write sent.
type Sent struct {
	// RCode is the RCODE sent. It is RCodeServFail when a TCP response exceeds 65535
	// bytes.
	RCode RCode
	// Truncated is true when the UDP response has the TC bit and no answer RR.
	Truncated bool
	// WireSize is the number of bytes sent, without the TCP length prefix.
	WireSize int
}

// Writer sends the response to one query.
type Writer interface {
	// Write sends the response that a describes. It returns every pack error and
	// every write error, wrapped. A second call returns ErrAlreadyWritten.
	Write(a Answer) (Sent, error)
}

// Handler answers queries.
type Handler interface {
	// ServeQuery answers q through w. A handler that does not call w.Write sends no
	// response.
	ServeQuery(ctx context.Context, q *Query, w Writer)
}

// HandlerFunc is a function that satisfies Handler.
type HandlerFunc func(ctx context.Context, q *Query, w Writer)

// ServeQuery calls f(ctx, q, w).
func (f HandlerFunc) ServeQuery(ctx context.Context, q *Query, w Writer) {
	f(ctx, q, w)
}
