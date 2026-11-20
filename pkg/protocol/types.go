package protocol

import (
	"context"
	"fmt"
)

// ProtocolKey identifies a protocol by its DNS class and type.
type ProtocolKey struct {
	Class uint16
	Type  uint16
}

// String returns the key in "<class>/<type>" decimal form.
func (k ProtocolKey) String() string {
	return fmt.Sprintf("%d/%d", k.Class, k.Type)
}

// Info describes a protocol registered in the registry.
type Info struct {
	ID       string
	Versions []uint16
	Schema   Schema
}

// Schema validates a payload for a specific protocol version.
type Schema interface {
	Validate(version uint16, payload []byte) error
}

// Handler processes a protocol request.
type Handler interface {
	Handle(ctx context.Context, req *Request) (*Response, error)
}

// HandlerFunc is a function that implements the Handler interface.
type HandlerFunc func(ctx context.Context, req *Request) (*Response, error)

// Handle implements the Handler interface by calling the wrapped function.
func (f HandlerFunc) Handle(ctx context.Context, req *Request) (*Response, error) {
	return f(ctx, req)
}

// Describer returns protocol information.
type Describer interface {
	Info() Info
}

// Status codes for protocol responses.
type Status uint16

const (
	StatusOK                 Status = 0
	StatusNotFound           Status = 1
	StatusBadRequest         Status = 2
	StatusVersionUnsupported Status = 3
	StatusUnavailable        Status = 4
)

// Message types for protocol messages.
type MessageType uint16

const (
	MessageRecord   MessageType = 1
	MessageManifest MessageType = 2
)

// IsPrivateClass returns true for DNS class values in the private range (65280-65534).
func IsPrivateClass(c uint16) bool {
	return c >= 65280 && c <= 65534
}

// IsPrivateType returns true for DNS type values in the private range (65280-65534).
func IsPrivateType(t uint16) bool {
	return t >= 65280 && t <= 65534
}
