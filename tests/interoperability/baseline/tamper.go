package baseline

import (
	"encoding/binary"
	"net"
)

// tamperConn changes the last RDATA byte of the first answer RR in each UDP answer.
// The server signs before it writes, so the change comes after signing. The last
// envelope byte is a signature byte, also when TXT splits the envelope into
// character-strings.
type tamperConn struct {
	net.PacketConn
}

// WriteTo writes a changed copy of b when b holds an answer RR, and b unchanged
// otherwise.
func (c tamperConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	end, ok := answerRDATAEnd(b)
	if !ok {
		return c.PacketConn.WriteTo(b, addr)
	}
	out := append([]byte(nil), b...)
	out[end-1] ^= 0xff
	return c.PacketConn.WriteTo(out, addr)
}

// answerRDATAEnd returns the offset after the RDATA of the first answer RR in msg. It
// returns false when msg has not one question and at least one answer RR, or when the
// RDATA is empty or runs past msg.
func answerRDATAEnd(msg []byte) (int, bool) {
	const headerLen = 12
	if len(msg) < headerLen || binary.BigEndian.Uint16(msg[4:]) != 1 || binary.BigEndian.Uint16(msg[6:]) == 0 {
		return 0, false
	}
	off, ok := skipName(msg, headerLen)
	// The question: QNAME, then QTYPE and QCLASS in 4 bytes.
	if !ok || off+4 > len(msg) {
		return 0, false
	}
	// The answer RR: NAME, then TYPE, CLASS, TTL and RDLENGTH in 10 bytes.
	off, ok = skipName(msg, off+4)
	if !ok || off+10 > len(msg) {
		return 0, false
	}
	n := int(binary.BigEndian.Uint16(msg[off+8:]))
	end := off + 10 + n
	if n == 0 || end > len(msg) {
		return 0, false
	}
	return end, true
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
