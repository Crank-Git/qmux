package protocol

import (
	"net/netip"
	"time"
)

// Request carries a protocol request parsed from a DNS query.
type Request struct {
	// QName is the QNAME in lowercase fully qualified form.
	QName string

	// Key is the protocol key derived from the QCLASS and QTYPE.
	Key ProtocolKey

	// Version is the leftmost _v<N> label value, or 0 when absent.
	Version uint16

	// Labels are the QNAME labels below the zone, with the version label removed.
	Labels []string

	// Zone is the longest matching configured zone, or "." when Zones is empty.
	Zone string

	// Source is the client address that sent the query.
	Source netip.AddrPort

	// Transport is "udp" or "tcp".
	Transport string

	// UDPSize is the advertised EDNS(0) UDP size, or 0 when no EDNS(0).
	UDPSize uint16

	// ID is the DNS message ID from the query.
	ID uint16

	// ReceivedAt is the time the query was received.
	ReceivedAt time.Time
}

// Response carries a protocol response produced by a handler.
type Response struct {
	// Payload is the opaque response data.
	Payload []byte

	// TTL is the answer RR TTL in seconds; 0 means the server default.
	TTL uint32

	// Status is the protocol status code.
	Status Status

	// Version is the response version; 0 means the highest registered version.
	Version uint16

	// MessageType is the message type; 0 means MessageRecord.
	MessageType MessageType
}
