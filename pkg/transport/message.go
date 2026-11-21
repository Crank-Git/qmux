package transport

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/miekg/dns"
)

const (
	// headerSize is the size of the DNS header.
	headerSize = 12
	// minUDPLimit is the UDP size limit without EDNS(0). RFC 6891 also treats an
	// advertised size below 512 as 512.
	minUDPLimit = 512
	// maxMessageSize is the largest message that the TCP length prefix can describe.
	maxMessageSize = 65535
)

// parseQuery returns the query in wire and the unpacked message. The message is nil
// when the body does not parse. wire must hold at least headerSize bytes.
func parseQuery(wire []byte, src netip.AddrPort, network string, receivedAt time.Time) (*Query, *dns.Msg) {
	flags := binary.BigEndian.Uint16(wire[2:4])
	q := &Query{
		ID:            binary.BigEndian.Uint16(wire[0:2]),
		Opcode:        (flags >> 11) & 0xf,
		QR:            flags&0x8000 != 0,
		QuestionCount: int(binary.BigEndian.Uint16(wire[4:6])),
		WireSize:      len(wire),
		Source:        src,
		Transport:     network,
		ReceivedAt:    receivedAt,
	}

	msg := new(dns.Msg)
	if err := msg.Unpack(wire); err != nil {
		// %v keeps miekg/dns error types out of the error chain that callers see.
		q.Err = fmt.Errorf("%w: %v", ErrMalformed, err)
		return q, nil
	}
	if len(msg.Question) > 0 {
		q.Name = msg.Question[0].Name
		q.Key.Class = msg.Question[0].Qclass
		q.Key.Type = msg.Question[0].Qtype
	}
	if opt := msg.IsEdns0(); opt != nil {
		q.EDNS = true
		q.EDNSVersion = opt.Version()
		q.UDPSize = opt.UDPSize()
	}
	return q, msg
}

// writer is the Writer for one query.
type writer struct {
	q *Query
	// req is nil when the query body does not parse.
	req     *dns.Msg
	udpSize uint16
	send    func(wire []byte) error

	mu      sync.Mutex
	written bool
}

func (w *writer) Write(a Answer) (Sent, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.written {
		return Sent{}, ErrAlreadyWritten
	}
	w.written = true

	wire, sent, err := w.pack(a)
	if err != nil {
		return Sent{}, err
	}
	if err := w.send(wire); err != nil {
		return Sent{}, fmt.Errorf("transport: write %s response: %w", w.q.Transport, err)
	}
	return sent, nil
}

// pack returns the response wire bytes for a, after the size rules.
func (w *writer) pack(a Answer) ([]byte, Sent, error) {
	m := w.reply(a.RCode)
	rdata := a.Payload
	if w.q.Key.Type == dns.TypeTXT {
		rdata = splitTXT(a.Payload)
	}
	if a.RCode == RCodeNoError {
		m.Authoritative = true
		m.Answer = []dns.RR{&dns.RFC3597{
			Hdr:   dns.RR_Header{Name: w.q.Name, Rrtype: w.q.Key.Type, Class: w.q.Key.Class, Ttl: a.TTL},
			Rdata: hex.EncodeToString(rdata),
		}}
	}

	limit := w.limit()
	// RDLENGTH is 16 bits, so a larger RDATA cannot fit in any message.
	if len(rdata) <= maxMessageSize {
		wire, err := packMsg(m)
		if err != nil || len(wire) <= limit {
			return wire, Sent{RCode: a.RCode, WireSize: len(wire)}, err
		}
	}

	if w.q.Transport == TCP {
		wire, err := packMsg(w.reply(RCodeServFail))
		return wire, Sent{RCode: RCodeServFail, WireSize: len(wire)}, err
	}
	// Truncate sets TC, removes the answer RR and keeps the OPT RR.
	m.Truncate(limit)
	wire, err := packMsg(m)
	return wire, Sent{RCode: a.RCode, Truncated: m.Truncated, WireSize: len(wire)}, err
}

// reply returns a response with no answer RR. It copies the ID and the question, and
// holds an OPT RR when the query has EDNS(0).
func (w *writer) reply(rcode RCode) *dns.Msg {
	m := new(dns.Msg)
	if w.req != nil {
		m.SetReply(w.req)
	} else {
		m.Id = w.q.ID
		m.Response = true
		m.Opcode = int(w.q.Opcode)
	}
	m.RecursionAvailable = false
	m.Compress = true
	m.Rcode = int(rcode)
	if w.q.EDNS {
		m.SetEdns0(w.udpSize, false)
	}
	return m
}

// limit returns the largest response size for the query.
func (w *writer) limit() int {
	if w.q.Transport == TCP {
		return maxMessageSize
	}
	if !w.q.EDNS {
		return minUDPLimit
	}
	return max(int(min(w.q.UDPSize, w.udpSize)), minUDPLimit)
}

// maxCharacterString is the largest TXT character-string, because one length byte
// starts it.
const maxCharacterString = 255

// splitTXT returns the TXT RDATA that holds payload as character-strings of at most
// 255 bytes (FR-interop-7). An empty payload gives one empty character-string, because
// TXT RDATA holds at least one.
func splitTXT(payload []byte) []byte {
	out := make([]byte, 0, len(payload)+len(payload)/maxCharacterString+1)
	for {
		n := min(len(payload), maxCharacterString)
		out = append(out, byte(n))
		out = append(out, payload[:n]...)
		payload = payload[n:]
		if len(payload) == 0 {
			return out
		}
	}
}

// joinTXT returns the bytes of the character-strings in the TXT RDATA rdata
// (FR-interop-8). It returns an error that wraps ErrMalformed when a length byte runs
// past the end of rdata.
func joinTXT(rdata []byte) ([]byte, error) {
	out := make([]byte, 0, len(rdata))
	for len(rdata) > 0 {
		n := int(rdata[0])
		if 1+n > len(rdata) {
			return nil, fmt.Errorf("%w: TXT character-string of %d bytes runs past the RDATA", ErrMalformed, n)
		}
		out = append(out, rdata[1:1+n]...)
		rdata = rdata[1+n:]
	}
	return out, nil
}

func packMsg(m *dns.Msg) ([]byte, error) {
	wire, err := m.Pack()
	if err != nil {
		return nil, fmt.Errorf("transport: pack response: %w", err)
	}
	return wire, nil
}
