package transport

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/miekg/dns"

	"github.com/Crank-Git/qmux/pkg/protocol"
)

// ErrMismatch means that a response does not answer the question of the query.
var ErrMismatch = errors.New("transport: response does not match the query")

// Packet is one query that NewQuery built. ExchangeUDP and ExchangeTCP match the
// response against ID, Name and Key.
type Packet struct {
	ID   uint16
	Name string
	Key  protocol.ProtocolKey
	// Wire holds the query bytes, without the TCP length prefix.
	Wire []byte
}

// Received is one response that matches a Packet.
type Received struct {
	RCode RCode
	// Truncated is true when the response has the TC bit. Only a UDP response can
	// have it.
	Truncated bool
	// TTL is the TTL of the answer RR, or 0 without an answer RR.
	TTL uint32
	// Payload holds the RDATA bytes of the answer RR, or nil without an answer RR.
	Payload []byte
	// WireSize is the number of bytes received, without the TCP length prefix.
	WireSize int
}

// NewQuery returns a query with one question for name and key, and a new random ID.
//
// The query holds an OPT RR that advertises udpSize. The RD bit is set when recursion
// is true. name must be fully qualified. NewQuery returns an error when the query does
// not pack.
func NewQuery(name string, key protocol.ProtocolKey, udpSize uint16, recursion bool) (Packet, error) {
	m := new(dns.Msg)
	// dns.Id draws the ID from crypto/rand, so an off-path sender cannot guess it.
	m.Id = dns.Id()
	m.RecursionDesired = recursion
	m.Question = []dns.Question{{Name: name, Qtype: key.Type, Qclass: key.Class}}
	m.SetEdns0(udpSize, false)
	wire, err := m.Pack()
	if err != nil {
		return Packet{}, fmt.Errorf("transport: pack query for %q: %w", name, err)
	}
	return Packet{ID: m.Id, Name: name, Key: key, Wire: wire}, nil
}

// ExchangeUDP sends p on c and returns the response that matches p.
//
// c is a connected UDP socket. ExchangeUDP ignores a response with a different ID, and
// waits for the next response until ctx ends. When ctx ends, ExchangeUDP sets the
// deadline of c to the past and returns an error that wraps ctx.Err().
//
// The error wraps ErrMalformed when the response does not parse. It wraps ErrMismatch
// when the response does not answer the question of p.
func ExchangeUDP(ctx context.Context, c net.Conn, p Packet) (Received, error) {
	stop := context.AfterFunc(ctx, func() {
		_ = c.SetDeadline(time.Now()) // The read that this unblocks returns the error.
	})
	defer stop()

	if _, err := c.Write(p.Wire); err != nil {
		return Received{}, exchangeErr(ctx, "write udp query", err)
	}
	buf := make([]byte, maxMessageSize)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return Received{}, exchangeErr(ctx, "read udp response", err)
		}
		if wrongID(buf[:n], p.ID) {
			continue
		}
		return match(buf[:n], p, UDP)
	}
}

// ExchangeTCP sends p on c and returns the response that matches p.
//
// c is a TCP connection. ExchangeTCP adds the 2-byte length prefix to the query and
// removes it from the response. It ignores a response with a different ID, because an
// earlier exchange on c can leave a late response. When ctx ends, ExchangeTCP sets the
// deadline of c to the past and returns an error that wraps ctx.Err().
//
// The error wraps ErrMalformed when the response does not parse or has the TC bit. It
// wraps ErrMismatch when the response does not answer the question of p.
func ExchangeTCP(ctx context.Context, c net.Conn, p Packet) (Received, error) {
	stop := context.AfterFunc(ctx, func() {
		_ = c.SetDeadline(time.Now()) // The read or write that this unblocks returns the error.
	})
	defer stop()

	framed := binary.BigEndian.AppendUint16(make([]byte, 0, 2+len(p.Wire)), uint16(len(p.Wire)))
	if _, err := c.Write(append(framed, p.Wire...)); err != nil {
		return Received{}, exchangeErr(ctx, "write tcp query", err)
	}
	for {
		var size [2]byte
		if _, err := io.ReadFull(c, size[:]); err != nil {
			return Received{}, exchangeErr(ctx, "read tcp response", err)
		}
		wire := make([]byte, binary.BigEndian.Uint16(size[:]))
		if _, err := io.ReadFull(c, wire); err != nil {
			return Received{}, exchangeErr(ctx, "read tcp response", err)
		}
		if wrongID(wire, p.ID) {
			continue
		}
		return match(wire, p, TCP)
	}
}

// exchangeErr wraps ctx.Err() when ctx ended, because the I/O error then only reports
// the deadline that the AfterFunc set.
func exchangeErr(ctx context.Context, op string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("transport: %s: %w", op, ctxErr)
	}
	return fmt.Errorf("transport: %s: %w", op, err)
}

// wrongID reports whether wire holds a full header with an ID other than id. A message
// shorter than the header has no ID, so match reports it as malformed.
func wrongID(wire []byte, id uint16) bool {
	return len(wire) >= headerSize && binary.BigEndian.Uint16(wire[0:2]) != id
}

// match returns the response in wire after the rules for Exchange in ADR 0005.
func match(wire []byte, p Packet, network string) (Received, error) {
	m := new(dns.Msg)
	if err := m.Unpack(wire); err != nil {
		// %v keeps miekg/dns error types out of the error chain that callers see.
		return Received{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if m.Truncated && network == TCP {
		return Received{}, fmt.Errorf("%w: tcp response has the TC bit", ErrMalformed)
	}
	r := Received{RCode: RCode(m.Rcode), Truncated: m.Truncated, WireSize: len(wire)}
	// A server can send an error RCODE without the question, for example NOTIMP from
	// BIND. An absent question does not differ from the query, so the RCODE applies.
	if len(m.Question) == 0 && r.RCode != RCodeNoError {
		return r, nil
	}
	if len(m.Question) != 1 {
		return Received{}, fmt.Errorf("%w: response has %d questions", ErrMismatch, len(m.Question))
	}
	q := m.Question[0]
	if !sameRR(q.Name, q.Qclass, q.Qtype, p) {
		return Received{}, fmt.Errorf("%w: response question %q %d/%d", ErrMismatch, q.Name, q.Qclass, q.Qtype)
	}

	// A truncated response holds no answer RR, and the caller retries over TCP.
	if r.RCode != RCodeNoError || r.Truncated {
		return r, nil
	}
	if len(m.Answer) != 1 {
		return Received{}, fmt.Errorf("%w: NOERROR response has %d answer RRs", ErrMismatch, len(m.Answer))
	}
	h := m.Answer[0].Header()
	if !sameRR(h.Name, h.Class, h.Rrtype, p) {
		return Received{}, fmt.Errorf("%w: answer RR %q %d/%d", ErrMismatch, h.Name, h.Class, h.Rrtype)
	}
	payload, err := rdata(m.Answer[0])
	if err != nil {
		return Received{}, err
	}
	r.TTL = h.Ttl
	r.Payload = payload
	return r, nil
}

// sameRR reports whether name, class and rrtype match the question of p. DNS names
// compare without regard to ASCII case.
func sameRR(name string, class, rrtype uint16, p Packet) bool {
	return class == p.Key.Class && rrtype == p.Key.Type && dns.CanonicalName(name) == dns.CanonicalName(p.Name)
}

// rdata returns the RDATA bytes of rr. miekg/dns unpacks a type that it knows, for
// example TXT, into a typed RR, so rdata packs that RR again to get the bytes.
func rdata(rr dns.RR) ([]byte, error) {
	if opaque, ok := rr.(*dns.RFC3597); ok {
		data, err := hex.DecodeString(opaque.Rdata)
		if err != nil {
			return nil, fmt.Errorf("%w: answer RDATA: %v", ErrMalformed, err)
		}
		return data, nil
	}
	buf := make([]byte, maxMessageSize)
	end, err := dns.PackRR(rr, buf, 0, nil, false)
	if err != nil {
		return nil, fmt.Errorf("%w: pack answer RR: %v", ErrMalformed, err)
	}
	// PackRR sets Rdlength, and the RDATA is the last part of the packed RR.
	return bytes.Clone(buf[end-int(rr.Header().Rdlength) : end]), nil
}
