package transport

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/miekg/dns"
)

// TestPrivateClassQueryRoundTrip verifies that a DNS query with a private
// QCLASS (65281) and private QTYPE (65280) packs and unpacks without loss.
func TestPrivateClassQueryRoundTrip(t *testing.T) {
	m := new(dns.Msg)
	m.SetQuestion("example.test.", 65280)
	m.Question[0].Qclass = 65281

	original, err := m.Pack()
	if err != nil {
		t.Fatalf("pack query: %v", err)
	}

	unpacked := new(dns.Msg)
	if err := unpacked.Unpack(original); err != nil {
		t.Fatalf("unpack query: %v", err)
	}

	repacked, err := unpacked.Pack()
	if err != nil {
		t.Fatalf("repack query: %v", err)
	}

	if !bytes.Equal(original, repacked) {
		t.Errorf("round-trip changed wire bytes")
	}

	q := unpacked.Question[0]
	if q.Qclass != 65281 {
		t.Errorf("Qclass = %d, want 65281", q.Qclass)
	}
	if q.Qtype != 65280 {
		t.Errorf("Qtype = %d, want 65280", q.Qtype)
	}
}

// TestOpaqueRDARoundTrip verifies that an RR with a private type and class
// round-trips through RFC3597 opaque encoding without data loss.
func TestOpaqueRDARoundTrip(t *testing.T) {
	originalBytes := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	hexStr := hex.EncodeToString(originalBytes)

	rr := &dns.RFC3597{
		Hdr: dns.RR_Header{
			Name:   "example.test.",
			Class:  65281,
			Ttl:    300,
			Rrtype: 65280,
		},
		Rdata: hexStr,
	}

	wire := make([]byte, 512)
	n, err := dns.PackRR(rr, wire, 0, nil, false)
	if err != nil {
		t.Fatalf("pack RR: %v", err)
	}

	unpacked, _, err := dns.UnpackRR(wire[:n], 0)
	if err != nil {
		t.Fatalf("unpack RR: %v", err)
	}

	rfc3597, ok := unpacked.(*dns.RFC3597)
	if !ok {
		t.Fatalf("expected *dns.RFC3597, got %T", unpacked)
	}

	if rfc3597.Rdata != hexStr {
		t.Errorf("Rdata = %q, want %q", rfc3597.Rdata, hexStr)
	}
}

// TestMultiClassRoundTrip verifies opaque RDATA round-trips for IN, CH, HS,
// and private classes.
func TestMultiClassRoundTrip(t *testing.T) {
	payload := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	hexStr := hex.EncodeToString(payload)

	classes := []struct {
		val uint16
		name string
	}{
		{dns.ClassINET, "IN"},
		{dns.ClassCHAOS, "CH"},
		{dns.ClassHESIOD, "HS"},
		{65281, "private"},
	}

	for _, c := range classes {
		t.Run(c.name, func(t *testing.T) {
			rr := &dns.RFC3597{
				Hdr: dns.RR_Header{
					Name:   "example.test.",
					Class:  c.val,
					Ttl:    300,
					Rrtype: 65280,
				},
				Rdata: hexStr,
			}

			wire := make([]byte, 512)
			n, err := dns.PackRR(rr, wire, 0, nil, false)
			if err != nil {
				t.Fatalf("pack: %v", err)
			}

			unpacked, _, err := dns.UnpackRR(wire[:n], 0)
			if err != nil {
				t.Fatalf("unpack: %v", err)
			}

			rfc3597, ok := unpacked.(*dns.RFC3597)
			if !ok {
				t.Fatalf("expected *dns.RFC3597, got %T", unpacked)
			}

			if rfc3597.Rdata != hexStr {
				t.Errorf("Rdata = %q, want %q", rfc3597.Rdata, hexStr)
			}
		})
	}
}

// TestEDNS0UDPSIZE verifies that EDNS(0) sets and preserves the UDP size.
func TestEDNS0UDPSIZE(t *testing.T) {
	m := new(dns.Msg)
	m.SetQuestion("example.test.", dns.TypeA)
	m.SetEdns0(1232, false)

	wire, err := m.Pack()
	if err != nil {
		t.Fatalf("pack: %v", err)
	}

	unpacked := new(dns.Msg)
	if err := unpacked.Unpack(wire); err != nil {
		t.Fatalf("unpack: %v", err)
	}

	opt := unpacked.IsEdns0()
	if opt == nil {
		t.Fatal("expected EDNS(0) OPT RR, got nil")
	}

	if opt.UDPSize() != 1232 {
		t.Errorf("UDPSize = %d, want 1232", opt.UDPSize())
	}
}

// TestMsgTruncate verifies the truncation behaviour of dns.Msg.Truncate:
// it keeps the OPT RR, sets the TC bit when it removes answers, and the
// answer count reflects only the kept RRs.
func TestMsgTruncate(t *testing.T) {
	m := new(dns.Msg)
	m.SetQuestion("example.test.", 65280)
	m.Question[0].Qclass = 65281

	// Add an OPT RR so Truncate knows the UDP size limit.
	m.SetEdns0(512, false)

	// Add a large opaque RR so truncation is triggered at 512 bytes.
	largePayload := bytes.Repeat([]byte{0xAA}, 500)
	rr := &dns.RFC3597{
		Hdr: dns.RR_Header{
			Name:   "example.test.",
			Class:  65281,
			Ttl:    300,
			Rrtype: 65280,
		},
		Rdata: hex.EncodeToString(largePayload),
	}
	m.Answer = append(m.Answer, rr)

	// Pack first so Rdlength is set on the RR header. Truncate reads Rdlength
	// to determine the wire size of each answer.
	_, err := m.Pack()
	if err != nil {
		t.Fatalf("pack: %v", err)
	}

	// Truncate to 512 bytes (the minimum per miekg/dns).
	m.Truncate(512)

	// The TC bit must be set when the message was cut.
	if !m.Truncated {
		t.Error("expected TC bit set after truncation")
	}

	// The answer count should be zero because the large RR was removed.
	if len(m.Answer) != 0 {
		t.Errorf("answer count = %d, want 0 after truncation", len(m.Answer))
	}

	// The OPT RR must be kept.
	if m.IsEdns0() == nil {
		t.Error("expected OPT RR to be kept after truncation")
	}
}
