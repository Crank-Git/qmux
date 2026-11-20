package protocol

import (
	"net/netip"
	"time"
)

// Request is one query that the authoritative server gives to a handler.
type Request struct {
	// QName is the lowercase FQDN of the query.
	QName string
	Key   ProtocolKey
	// Version is the protocol version from the version label, or 0 when the
	// QNAME has no version label.
	Version uint16
	// Labels holds the QNAME labels below the zone, without the version label.
	Labels []string
	// Zone is the longest configured zone that matches the QNAME.
	Zone   string
	Source netip.AddrPort
	// Transport is "udp" or "tcp".
	Transport  string
	UDPSize    uint16
	ID         uint16
	ReceivedAt time.Time
}

// Response is what a handler returns: the payload, the TTL, the application
// status and the protocol version.
type Response struct {
	Payload []byte
	// TTL is the answer TTL in seconds. 0 selects the server default. The TTL
	// is never a security expiry.
	TTL    uint32
	Status Status
	// Version is the protocol version of the payload. 0 selects the highest
	// registered version.
	Version uint16
	// MessageType is the kind of envelope. 0 selects MessageRecord.
	MessageType MessageType
}
