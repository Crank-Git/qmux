package transport

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/miekg/dns"
)

// TestPrivateClassQueryRoundTripsUnchanged proves FR-foundation-28.
// A query with QCLASS 65281 and QTYPE 65280 unpacks to the question that was packed.
func TestPrivateClassQueryRoundTripsUnchanged(t *testing.T) {
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

	if len(unpacked.Question) != 1 {
		t.Fatalf("question count = %d, want 1", len(unpacked.Question))
	}
	if got, want := unpacked.Question[0], m.Question[0]; got != want {
		t.Errorf("question = %+v, want %+v", got, want)
	}
}

// opaqueRoundTrip packs rdata as an RR of type 65280 in the given class.
// It returns the RDATA bytes that unpack from the wire.
// miekg/dns holds opaque RDATA as a hex string, so the helper decodes it to bytes.
func opaqueRoundTrip(t *testing.T, class uint16, rdata []byte) []byte {
	t.Helper()

	rr := &dns.RFC3597{
		Hdr: dns.RR_Header{
			Name:   "example.test.",
			Class:  class,
			Ttl:    300,
			Rrtype: 65280,
		},
		Rdata: hex.EncodeToString(rdata),
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

	opaque, ok := unpacked.(*dns.RFC3597)
	if !ok {
		t.Fatalf("unpacked RR is %T, want *dns.RFC3597", unpacked)
	}
	if opaque.Hdr.Class != class || opaque.Hdr.Rrtype != 65280 {
		t.Fatalf("unpacked header class %d type %d, want class %d type 65280",
			opaque.Hdr.Class, opaque.Hdr.Rrtype, class)
	}

	got, err := hex.DecodeString(opaque.Rdata)
	if err != nil {
		t.Fatalf("decode unpacked RDATA %q: %v", opaque.Rdata, err)
	}
	return got
}

// TestOpaqueRDATARoundTripsByteExact proves FR-foundation-29.
// An RR of type 65280 and class 65281 keeps its RDATA bytes through pack and unpack.
func TestOpaqueRDATARoundTripsByteExact(t *testing.T) {
	want := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}

	got := opaqueRoundTrip(t, 65281, want)
	if !bytes.Equal(got, want) {
		t.Errorf("RDATA = %x, want %x", got, want)
	}
}

// TestOpaqueRDATARoundTripsByteExactInStandardClasses proves FR-foundation-30.
// Opaque RDATA keeps its bytes in the classes IN, CH and HS.
func TestOpaqueRDATARoundTripsByteExactInStandardClasses(t *testing.T) {
	want := []byte{0xDE, 0xAD, 0xBE, 0xEF}

	classes := []struct {
		val  uint16
		name string
	}{
		{dns.ClassINET, "IN"},
		{dns.ClassCHAOS, "CH"},
		{dns.ClassHESIOD, "HS"},
	}

	for _, c := range classes {
		t.Run(c.name, func(t *testing.T) {
			got := opaqueRoundTrip(t, c.val, want)
			if !bytes.Equal(got, want) {
				t.Errorf("RDATA = %x, want %x", got, want)
			}
		})
	}
}

// TestEDNS0UDPSizeRoundTrips proves FR-foundation-31.
// A message with an EDNS(0) UDP size of 1232 unpacks with a UDP size of 1232.
func TestEDNS0UDPSizeRoundTrips(t *testing.T) {
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
		t.Fatal("unpacked message has no OPT RR")
	}

	if opt.UDPSize() != 1232 {
		t.Errorf("UDPSize = %d, want 1232", opt.UDPSize())
	}
}

// TestMsgTruncateDropsLargeOpaqueAnswerAndSetsTC proves FR-foundation-32.
// It records that Msg.Truncate removes an answer that does not fit, sets the TC bit,
// and keeps the OPT RR.
func TestMsgTruncateDropsLargeOpaqueAnswerAndSetsTC(t *testing.T) {
	m := new(dns.Msg)
	m.SetQuestion("example.test.", 65280)
	m.Question[0].Qclass = 65281
	m.SetEdns0(512, false)

	// A 500-byte RDATA makes the message larger than 512 bytes.
	largePayload := bytes.Repeat([]byte{0xAA}, 500)
	m.Answer = append(m.Answer, &dns.RFC3597{
		Hdr: dns.RR_Header{
			Name:   "example.test.",
			Class:  65281,
			Ttl:    300,
			Rrtype: 65280,
		},
		Rdata: hex.EncodeToString(largePayload),
	})

	// Truncate reads Rdlength to find the wire size of each RR, and Pack sets Rdlength.
	if _, err := m.Pack(); err != nil {
		t.Fatalf("pack: %v", err)
	}

	// miekg/dns treats a size below 512 as 512.
	m.Truncate(512)

	if !m.Truncated {
		t.Error("TC bit is clear after Truncate, want set")
	}

	if len(m.Answer) != 0 {
		t.Errorf("answer count = %d, want 0 after Truncate", len(m.Answer))
	}

	if m.IsEdns0() == nil {
		t.Error("Truncate removed the OPT RR, want it kept")
	}
}
