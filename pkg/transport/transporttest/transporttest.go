// Package transporttest builds and reads DNS messages for tests, and runs a fake DNS
// server. Tests outside pkg/transport use it, so that they do not import miekg/dns.
//
// Only test code imports this package.
package transporttest

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/transport"
)

// paddingType is the RR type of the additional RR that pads a query without EDNS(0).
// It is in the private-use range, so no DNS software gives it a meaning.
const paddingType = 65534

// RR is one resource record. In the question section, only Name and Key apply.
type RR struct {
	Name string
	// Key holds the class and the type of the RR.
	Key protocol.ProtocolKey
	TTL uint32
	// Data holds the RDATA bytes as they are on the wire.
	Data []byte
}

// Message is a DNS message that a test builds with Pack or reads with Unpack.
type Message struct {
	ID                 uint16
	Opcode             uint16
	Response           bool // the QR bit
	Authoritative      bool // the AA bit
	Truncated          bool // the TC bit
	RecursionDesired   bool // the RD bit
	RecursionAvailable bool // the RA bit
	// RCode is the full RCODE. Unpack adds the extended RCODE bits of the OPT RR.
	RCode    transport.RCode
	Question []RR
	Answer   []RR
	// EDNS is true when the message holds an OPT RR.
	EDNS        bool
	EDNSVersion uint8
	UDPSize     uint16

	padTo int
}

// PadTo makes Pack return exactly size bytes for m. The padded message stays
// parseable.
//
// With EDNS(0), Pack adds an EDNS(0) padding option of RFC 7830. Without EDNS(0), Pack
// adds one additional RR of type 65534 with the root name. Pack returns an error when
// size is too small for the padding.
func (m *Message) PadTo(size int) {
	m.padTo = size
}

// Pack returns the wire bytes of m. It does not compress names.
func Pack(m Message) ([]byte, error) {
	msg := new(dns.Msg)
	msg.Id = m.ID
	msg.Opcode = int(m.Opcode)
	msg.Response = m.Response
	msg.Authoritative = m.Authoritative
	msg.Truncated = m.Truncated
	msg.RecursionDesired = m.RecursionDesired
	msg.RecursionAvailable = m.RecursionAvailable
	msg.Rcode = int(m.RCode)
	for _, q := range m.Question {
		msg.Question = append(msg.Question, dns.Question{Name: q.Name, Qtype: q.Key.Type, Qclass: q.Key.Class})
	}
	for _, rr := range m.Answer {
		msg.Answer = append(msg.Answer, opaqueRR(rr.Name, rr.Key, rr.TTL, rr.Data))
	}
	if m.EDNS {
		msg.SetEdns0(m.UDPSize, false)
		msg.IsEdns0().SetVersion(m.EDNSVersion)
	}

	wire, err := msg.Pack()
	if err != nil {
		return nil, fmt.Errorf("transporttest: pack message: %w", err)
	}
	if m.padTo == 0 {
		return wire, nil
	}
	return pad(msg, len(wire), m.padTo)
}

func opaqueRR(name string, key protocol.ProtocolKey, ttl uint32, data []byte) *dns.RFC3597 {
	return &dns.RFC3597{
		Hdr:   dns.RR_Header{Name: name, Rrtype: key.Type, Class: key.Class, Ttl: ttl},
		Rdata: hex.EncodeToString(data),
	}
}

// pad adds padding to msg so that it packs to size bytes. current is the packed size
// without padding.
func pad(msg *dns.Msg, current, size int) ([]byte, error) {
	extra := size - current
	if opt := msg.IsEdns0(); opt != nil {
		// The padding option has a 4-byte option header.
		if extra < 4 {
			return nil, fmt.Errorf("transporttest: pad to %d bytes: the message has %d bytes and EDNS(0) padding needs 4", size, current)
		}
		opt.Option = append(opt.Option, &dns.EDNS0_PADDING{Padding: make([]byte, extra-4)})
	} else {
		// The padding RR has a 1-byte root name and a 10-byte fixed header.
		if extra < 11 {
			return nil, fmt.Errorf("transporttest: pad to %d bytes: the message has %d bytes and an RR needs 11", size, current)
		}
		msg.Extra = append(msg.Extra, opaqueRR(".", protocol.ProtocolKey{Class: dns.ClassINET, Type: paddingType}, 0, make([]byte, extra-11)))
	}

	wire, err := msg.Pack()
	if err != nil {
		return nil, fmt.Errorf("transporttest: pack padded message: %w", err)
	}
	return wire, nil
}

// Unpack returns the message that wire holds. Additional RRs other than the OPT RR
// are not in the result.
//
// The Data of an answer RR is the RDATA as it is on the wire, also for a type that
// miekg/dns knows, for example TXT.
func Unpack(wire []byte) (Message, error) {
	msg := new(dns.Msg)
	if err := msg.Unpack(wire); err != nil {
		return Message{}, fmt.Errorf("transporttest: unpack message: %w", err)
	}

	m := Message{
		ID:                 msg.Id,
		Opcode:             uint16(msg.Opcode),
		Response:           msg.Response,
		Authoritative:      msg.Authoritative,
		Truncated:          msg.Truncated,
		RecursionDesired:   msg.RecursionDesired,
		RecursionAvailable: msg.RecursionAvailable,
		RCode:              transport.RCode(msg.Rcode),
	}
	for _, q := range msg.Question {
		m.Question = append(m.Question, RR{Name: q.Name, Key: protocol.ProtocolKey{Class: q.Qclass, Type: q.Qtype}})
	}
	for _, rr := range msg.Answer {
		data, err := rdata(rr)
		if err != nil {
			return Message{}, err
		}
		h := rr.Header()
		m.Answer = append(m.Answer, RR{Name: h.Name, Key: protocol.ProtocolKey{Class: h.Class, Type: h.Rrtype}, TTL: h.Ttl, Data: data})
	}
	if opt := msg.IsEdns0(); opt != nil {
		m.EDNS = true
		m.EDNSVersion = opt.Version()
		m.UDPSize = opt.UDPSize()
	}
	return m, nil
}

// rdata returns the RDATA bytes of rr.
func rdata(rr dns.RR) ([]byte, error) {
	if opaque, ok := rr.(*dns.RFC3597); ok {
		data, err := hex.DecodeString(opaque.Rdata)
		if err != nil {
			return nil, fmt.Errorf("transporttest: decode RDATA: %w", err)
		}
		return data, nil
	}

	buf := make([]byte, dns.MaxMsgSize)
	end, err := dns.PackRR(rr, buf, 0, nil, false)
	if err != nil {
		return nil, fmt.Errorf("transporttest: pack %s RR: %w", dns.TypeToString[rr.Header().Rrtype], err)
	}
	// PackRR sets Rdlength, and the RDATA is the last part of the packed RR.
	return buf[end-int(rr.Header().Rdlength) : end], nil
}

// Exchange sends query to addr and returns the first response.
//
// network is transport.UDP or transport.TCP. Over TCP, Exchange adds the 2-byte length
// prefix to query and removes it from the response. Exchange returns an error when ctx
// ends before a response arrives.
func Exchange(ctx context.Context, network, addr string, query []byte) ([]byte, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, fmt.Errorf("transporttest: dial %s %s: %w", network, addr, err)
	}
	defer c.Close() //nolint:errcheck // The response is already read, so a close error changes nothing.
	stop := context.AfterFunc(ctx, func() {
		_ = c.SetDeadline(time.Now()) // The read that this unblocks returns the error.
	})
	defer stop()

	if network == transport.TCP {
		return exchangeTCP(c, query)
	}
	if _, err := c.Write(query); err != nil {
		return nil, fmt.Errorf("transporttest: write udp query: %w", err)
	}
	buf := make([]byte, dns.MaxMsgSize)
	n, err := c.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("transporttest: read udp response: %w", err)
	}
	return buf[:n], nil
}

func exchangeTCP(c net.Conn, query []byte) ([]byte, error) {
	if err := writeFrame(c, query); err != nil {
		return nil, fmt.Errorf("transporttest: write tcp query: %w", err)
	}
	resp, err := readFrame(c)
	if err != nil {
		return nil, fmt.Errorf("transporttest: read tcp response: %w", err)
	}
	return resp, nil
}

func writeFrame(w io.Writer, msg []byte) error {
	framed := binary.BigEndian.AppendUint16(make([]byte, 0, 2+len(msg)), uint16(len(msg)))
	_, err := w.Write(append(framed, msg...))
	return err
}

func readFrame(r io.Reader) ([]byte, error) {
	var size [2]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return nil, err
	}
	msg := make([]byte, binary.BigEndian.Uint16(size[:]))
	if _, err := io.ReadFull(r, msg); err != nil {
		return nil, err
	}
	return msg, nil
}

// Responder returns the raw responses that the fake server sends for one query, in
// order. An empty result drops the query. A responder can send a wrong ID or bytes that
// do not parse.
//
// network is transport.UDP or transport.TCP. The server calls Responder from more than
// one goroutine. Responder must not call t.Fatal, because it does not run in the test
// goroutine.
type Responder func(network string, query []byte) [][]byte

// StartServer starts a fake DNS server on 127.0.0.1 with UDP and TCP on the same port,
// and returns its address. Over TCP, the server reads and writes 2-byte length
// prefixes around each message.
//
// The server is ready when StartServer returns. It stops when the test ends.
func StartServer(t testing.TB, r Responder) string {
	t.Helper()
	pc, l, err := transport.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("transporttest: start server: %v", err)
	}

	s := &fakeServer{respond: r, conns: make(map[net.Conn]struct{})}
	s.wg.Go(func() { s.serveUDP(pc) })
	s.wg.Go(func() { s.serveTCP(l) })
	t.Cleanup(func() {
		s.mu.Lock()
		s.closed = true
		for c := range s.conns {
			_ = c.Close() // Each connection goroutine sees the close and returns.
		}
		s.mu.Unlock()
		if err := errors.Join(pc.Close(), l.Close()); err != nil {
			t.Errorf("transporttest: stop server: %v", err)
		}
		s.wg.Wait()
	})
	return pc.LocalAddr().String()
}

type fakeServer struct {
	respond Responder
	wg      sync.WaitGroup

	mu     sync.Mutex
	closed bool
	conns  map[net.Conn]struct{}
}

func (s *fakeServer) serveUDP(pc net.PacketConn) {
	buf := make([]byte, dns.MaxMsgSize)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		for _, resp := range s.respond(transport.UDP, append([]byte(nil), buf[:n]...)) {
			if _, err := pc.WriteTo(resp, addr); err != nil {
				return
			}
		}
	}
}

func (s *fakeServer) serveTCP(l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = c.Close() // The server stops, so it refuses the connection.
			return
		}
		s.conns[c] = struct{}{}
		s.mu.Unlock()
		s.wg.Go(func() { s.serveConn(c) })
	}
}

func (s *fakeServer) serveConn(c net.Conn) {
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		_ = c.Close() // The fake server has no caller to report a close error to.
	}()
	for {
		query, err := readFrame(c)
		if err != nil {
			return
		}
		for _, resp := range s.respond(transport.TCP, query) {
			if err := writeFrame(c, resp); err != nil {
				return
			}
		}
	}
}
