package transporttest_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/transport"
	"github.com/Crank-Git/qmux/pkg/transport/transporttest"
)

var key = protocol.ProtocolKey{Class: 65281, Type: 65280}

func query(id uint16) transporttest.Message {
	return transporttest.Message{ID: id, Question: []transporttest.RR{{Name: "a.example.", Key: key}}}
}

func TestPackAndUnpackKeepEveryField(t *testing.T) {
	want := transporttest.Message{
		ID:                 7,
		Opcode:             2,
		Response:           true,
		Authoritative:      true,
		Truncated:          true,
		RecursionDesired:   true,
		RecursionAvailable: true,
		RCode:              transport.RCodeBadVers,
		Question:           []transporttest.RR{{Name: "a.example.", Key: key}},
		Answer:             []transporttest.RR{{Name: "a.example.", Key: key, TTL: 60, Data: []byte{0, 1, 0xff}}},
		EDNS:               true,
		EDNSVersion:        1,
		UDPSize:            1232,
	}
	wire, err := transporttest.Pack(want)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	got, err := transporttest.Unpack(wire)
	if err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Unpack(Pack(m)) = %+v, want %+v", got, want)
	}
}

func TestUnpackReturnsThePackedRDATAOfAKnownType(t *testing.T) {
	m := new(dns.Msg)
	m.SetQuestion("a.example.", dns.TypeTXT)
	m.Response = true
	m.Answer = []dns.RR{&dns.TXT{Hdr: dns.RR_Header{Name: "a.example.", Rrtype: dns.TypeTXT, Class: dns.ClassINET}, Txt: []string{"hi"}}}
	wire, err := m.Pack()
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	got, err := transporttest.Unpack(wire)
	if err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if want := []byte{2, 'h', 'i'}; len(got.Answer) != 1 || !bytes.Equal(got.Answer[0].Data, want) {
		t.Errorf("answer = %+v, want one RR with RDATA %x", got.Answer, want)
	}
}

func TestUnpackReturnsAnErrorForBytesThatDoNotParse(t *testing.T) {
	if _, err := transporttest.Unpack([]byte{1, 2, 3}); err == nil {
		t.Error("Unpack of 3 bytes returned nil, want an error")
	}
}

func TestPadToMakesAParseableQueryOfExactlyTheSize(t *testing.T) {
	for _, edns := range []bool{false, true} {
		m := query(3)
		m.EDNS, m.UDPSize = edns, 1232
		m.PadTo(1500)
		wire, err := transporttest.Pack(m)
		if err != nil {
			t.Fatalf("EDNS %v: Pack: %v", edns, err)
		}
		if len(wire) != 1500 {
			t.Errorf("EDNS %v: %d bytes, want 1500", edns, len(wire))
		}
		got, err := transporttest.Unpack(wire)
		if err != nil {
			t.Fatalf("EDNS %v: Unpack: %v", edns, err)
		}
		if got.ID != 3 || len(got.Question) != 1 || got.EDNS != edns {
			t.Errorf("EDNS %v: unpacked %+v, want the query back", edns, got)
		}
	}
}

func TestPadToReturnsAnErrorWhenTheSizeIsTooSmall(t *testing.T) {
	m := query(3)
	m.PadTo(20)
	if _, err := transporttest.Pack(m); err == nil {
		t.Error("Pack with PadTo(20) returned nil, want an error")
	}
}

func exchange(t *testing.T, network, addr string, q []byte) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return transporttest.Exchange(ctx, network, addr, q)
}

func TestStartServerSendsTheResponderBytesOverUDPAndTCP(t *testing.T) {
	raw := []byte{0xde, 0xad}
	addr := transporttest.StartServer(t, func(network string, q []byte) [][]byte {
		return [][]byte{append(append([]byte(nil), raw...), network...)}
	})
	for _, network := range []string{transport.UDP, transport.TCP} {
		got, err := exchange(t, network, addr, []byte("query"))
		if err != nil {
			t.Fatalf("%s: Exchange: %v", network, err)
		}
		if want := append(append([]byte(nil), raw...), network...); !bytes.Equal(got, want) {
			t.Errorf("%s: response = %x, want %x", network, got, want)
		}
	}
}

func TestStartServerSendsEveryResponseInOrder(t *testing.T) {
	addr := transporttest.StartServer(t, func(_ string, q []byte) [][]byte {
		wrong, err := transporttest.Pack(query(1))
		if err != nil {
			return nil
		}
		return [][]byte{wrong, q}
	})
	want, err := transporttest.Pack(query(2))
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	// Exchange returns the first response, so the wrong ID arrives first.
	got, err := exchange(t, transport.UDP, addr, want)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if m, err := transporttest.Unpack(got); err != nil || m.ID != 1 {
		t.Errorf("first response ID = %d (%v), want 1", m.ID, err)
	}
}

func TestStartServerDropsAQueryWhenTheResponderReturnsNothing(t *testing.T) {
	addr := transporttest.StartServer(t, func(string, []byte) [][]byte { return nil })
	// No response can prove a drop, so the test waits a short time for one.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := transporttest.Exchange(ctx, transport.UDP, addr, []byte("query"))
	if !errors.Is(err, context.DeadlineExceeded) && !isTimeout(err) {
		t.Errorf("Exchange = %v, want a timeout", err)
	}
}

func isTimeout(err error) bool {
	var te interface{ Timeout() bool }
	return errors.As(err, &te) && te.Timeout()
}
