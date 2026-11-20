package protocol

import (
	"context"
	"strconv"
)

// ProtocolKey is the (QCLASS, QTYPE) pair that one protocol owns on one server.
type ProtocolKey struct {
	Class uint16
	Type  uint16
}

// String returns the protocol key as "<class>/<type>" in decimal, for example
// "65280/65280".
func (k ProtocolKey) String() string {
	return strconv.FormatUint(uint64(k.Class), 10) + "/" + strconv.FormatUint(uint64(k.Type), 10)
}

// Info is the protocol ID, the supported protocol versions and the optional
// payload schema of one protocol.
type Info struct {
	ID       string
	Versions []uint16
	// Schema is optional and can be nil.
	Schema Schema
}

// Schema reports whether a payload is valid for one protocol version.
type Schema interface {
	// Validate returns nil when payload is valid for version, and an error otherwise.
	Validate(version uint16, payload []byte) error
}

// Handler answers the requests for one protocol key.
type Handler interface {
	// Handle returns the response to req. A non-nil error means the handler
	// cannot produce a response.
	Handle(ctx context.Context, req *Request) (*Response, error)
}

// HandlerFunc is a function that satisfies Handler.
type HandlerFunc func(ctx context.Context, req *Request) (*Response, error)

// Handle returns the result of f(ctx, req).
func (f HandlerFunc) Handle(ctx context.Context, req *Request) (*Response, error) {
	return f(ctx, req)
}

// Describer is a type that states its own Info.
type Describer interface {
	// Info returns the protocol ID, the versions and the schema of the protocol.
	Info() Info
}

// Status is the application status of a response. It reports the application
// outcome and is separate from the RCODE. The values 256–65535 are
// protocol-specific, and the protocol defines their meaning.
type Status uint16

// The application status values that QMux defines.
const (
	// StatusOK means the payload holds the requested resource.
	StatusOK Status = 0
	// StatusNotFound means the protocol has no resource for the QNAME.
	StatusNotFound Status = 1
	// StatusBadRequest means the QNAME labels do not parse for this protocol.
	StatusBadRequest Status = 2
	// StatusVersionUnsupported means the version label names a version that the
	// registration does not support.
	StatusVersionUnsupported Status = 3
	// StatusUnavailable means the handler cannot answer now.
	StatusUnavailable Status = 4
)

// MessageType is the envelope field that names the kind of envelope.
type MessageType uint16

// The message types that QMux defines.
const (
	// MessageRecord is an envelope that carries one resource of a protocol.
	MessageRecord MessageType = 1
	// MessageManifest is an envelope that carries the manifest.
	MessageManifest MessageType = 2
)

// The private-use range for classes and for types is 65280–65534. 65535 is
// reserved, so it is outside the range.
const (
	privateMin = 65280
	privateMax = 65534
)

// IsPrivateClass returns true only when c is in the private-use range
// 65280–65534.
func IsPrivateClass(c uint16) bool {
	return c >= privateMin && c <= privateMax
}

// IsPrivateType returns true only when t is in the private-use range
// 65280–65534.
func IsPrivateType(t uint16) bool {
	return t >= privateMin && t <= privateMax
}
