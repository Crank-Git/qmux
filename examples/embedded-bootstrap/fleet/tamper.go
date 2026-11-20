package fleet

import (
	"encoding/binary"
	"net"

	"github.com/Crank-Git/qmux/pkg/codec"
)

// The protocol key of org.qmux.configuration (FR-demo-10).
const (
	configurationClass = 65281
	configurationType  = 65280
)

// tamperConn changes one byte of each configuration envelope that the server writes
// (FR-demo-40). The server signs before it writes, so the change comes after signing.
//
// Only UDP answers pass through tamperConn. The client asks over TCP only after a UDP
// answer with the TC bit, and the configuration answer fits in one UDP message.
type tamperConn struct {
	net.PacketConn
}

// WriteTo writes a changed copy of b when b is a configuration answer, and b unchanged
// otherwise.
func (c tamperConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	return c.PacketConn.WriteTo(tamper(b), addr)
}

// tamper returns a copy of the DNS message msg with one changed byte in the signature
// of the answer envelope. It returns msg unchanged when msg does not parse, is not an
// answer at the configuration protocol key, or holds no signed envelope.
//
// The changed byte is in the signature, so the envelope still decodes and the device
// reports a bad signature.
func tamper(msg []byte) []byte {
	start, end, ok := configurationRDATA(msg)
	if !ok {
		return msg
	}
	e, err := codec.Decode(msg[start:end], codec.Limits{})
	if err != nil || e.Signature == nil || len(e.Signature.Signature) == 0 {
		return msg
	}
	e.Signature.Signature[0] ^= 0xff
	b, err := codec.Encode(e, codec.Limits{})
	// A changed byte inside a byte string keeps the length of the encoding.
	if err != nil || len(b) != end-start {
		return msg
	}
	out := append([]byte(nil), msg...)
	copy(out[start:end], b)
	return out
}

// configurationRDATA returns the bounds of the RDATA of the first answer RR in msg. It
// returns false when the question is not at the configuration protocol key, or when
// msg ends before the RDATA ends.
func configurationRDATA(msg []byte) (start, end int, ok bool) {
	const headerLen = 12
	if len(msg) < headerLen || binary.BigEndian.Uint16(msg[4:]) != 1 || binary.BigEndian.Uint16(msg[6:]) == 0 {
		return 0, 0, false
	}
	off, ok := skipName(msg, headerLen)
	if !ok || off+4 > len(msg) {
		return 0, 0, false
	}
	if binary.BigEndian.Uint16(msg[off:]) != configurationType || binary.BigEndian.Uint16(msg[off+2:]) != configurationClass {
		return 0, 0, false
	}
	// The answer RR: NAME, then TYPE, CLASS, TTL and RDLENGTH in 10 bytes.
	off, ok = skipName(msg, off+4)
	if !ok || off+10 > len(msg) {
		return 0, 0, false
	}
	start = off + 10
	end = start + int(binary.BigEndian.Uint16(msg[off+8:]))
	if end > len(msg) {
		return 0, 0, false
	}
	return start, end, true
}

// skipName returns the offset after the domain name at off. A compression pointer ends
// the name in two bytes (RFC 1035 § 4.1.4).
func skipName(msg []byte, off int) (int, bool) {
	for off < len(msg) {
		n := int(msg[off])
		switch {
		case n == 0:
			return off + 1, true
		case n&0xc0 == 0xc0:
			return off + 2, off+2 <= len(msg)
		case n&0xc0 != 0:
			return 0, false
		}
		off += 1 + n
	}
	return 0, false
}
