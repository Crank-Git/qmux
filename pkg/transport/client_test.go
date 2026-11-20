package transport_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/transport"
	"github.com/Crank-Git/qmux/pkg/transport/transporttest"
)

const testName = "device42.fleet.example."

var testPayload = []byte{0x01, 0x02, 0x03}

// respond returns a Responder that sends the responses that build makes from the
// query. build gets the unpacked query and a correct NOERROR response to edit.
func respond(t *testing.T, build func(q, ok transporttest.Message) []transporttest.Message) transporttest.Responder {
	t.Helper()
	return func(_ string, query []byte) [][]byte {
		q, err := transporttest.Unpack(query)
		if err != nil {
			t.Errorf("unpack query: %v", err)
			return nil
		}
		ok := transporttest.Message{
			ID:       q.ID,
			Response: true,
			Question: q.Question,
			Answer:   []transporttest.RR{{Name: q.Question[0].Name, Key: q.Question[0].Key, TTL: 300, Data: testPayload}},
		}
		var out [][]byte
		for _, m := range build(q, ok) {
			wire, err := transporttest.Pack(m)
			if err != nil {
				t.Errorf("pack response: %v", err)
				return nil
			}
			out = append(out, wire)
		}
		return out
	}
}

// respondRaw returns a Responder that sends the bytes that build makes from the query.
func respondRaw(build func(query []byte) []byte) transporttest.Responder {
	return func(_ string, query []byte) [][]byte {
		return [][]byte{build(query)}
	}
}

// clientExchange sends a query for testName and testKey to a fake server that uses r.
func clientExchange(t *testing.T, network string, r transporttest.Responder) (transport.Received, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return clientExchangeCtx(ctx, t, network, r)
}

func clientExchangeCtx(ctx context.Context, t *testing.T, network string, r transporttest.Responder) (transport.Received, error) {
	t.Helper()
	addr := transporttest.StartServer(t, r)
	c, err := net.Dial(network, addr)
	if err != nil {
		t.Fatalf("dial %s %s: %v", network, addr, err)
	}
	defer c.Close() //nolint:errcheck // The test reads nothing after the exchange.

	p, err := transport.NewQuery(testName, testKey, 1232, false)
	if err != nil {
		t.Fatalf("NewQuery: %v", err)
	}
	if network == transport.TCP {
		return transport.ExchangeTCP(ctx, c, p)
	}
	return transport.ExchangeUDP(ctx, c, p)
}

func TestNewQueryBuildsOneQuestionWithEDNSAndTheRDBit(t *testing.T) {
	for _, recursion := range []bool{true, false} {
		p, err := transport.NewQuery(testName, testKey, 1232, recursion)
		if err != nil {
			t.Fatalf("NewQuery: %v", err)
		}
		if p.Name != testName || p.Key != testKey {
			t.Errorf("Packet Name=%q Key=%v, want %q %v", p.Name, p.Key, testName, testKey)
		}
		m, err := transporttest.Unpack(p.Wire)
		if err != nil {
			t.Fatalf("unpack query: %v", err)
		}
		want := transporttest.Message{
			ID:               p.ID,
			RecursionDesired: recursion,
			Question:         []transporttest.RR{{Name: testName, Key: testKey}},
			EDNS:             true,
			UDPSize:          1232,
		}
		if !reflect.DeepEqual(m, want) {
			t.Errorf("query = %+v, want %+v", m, want)
		}
	}
}

func TestNewQueryUsesADifferentIDForEachQuery(t *testing.T) {
	seen := make(map[uint16]bool)
	for range 20 {
		p, err := transport.NewQuery(testName, testKey, 1232, false)
		if err != nil {
			t.Fatalf("NewQuery: %v", err)
		}
		seen[p.ID] = true
	}
	// Twenty random 16-bit IDs are all equal with a probability of 2^-304.
	if len(seen) < 2 {
		t.Errorf("20 queries have %d distinct IDs, want more than 1", len(seen))
	}
}

func TestNewQueryReturnsAnErrorForANameThatIsNotFullyQualified(t *testing.T) {
	if _, err := transport.NewQuery("device42.fleet.example", testKey, 1232, false); err == nil {
		t.Error("NewQuery returned no error for a name without the final dot")
	}
}

func TestExchangeUDPIgnoresAResponseWithADifferentID(t *testing.T) {
	r := respond(t, func(_, ok transporttest.Message) []transporttest.Message {
		wrong := ok
		wrong.ID = ok.ID + 1
		wrong.Answer = []transporttest.RR{{Name: ok.Question[0].Name, Key: ok.Question[0].Key, Data: []byte("wrong")}}
		return []transporttest.Message{wrong, ok}
	})
	got, err := clientExchange(t, transport.UDP, r)
	if err != nil {
		t.Fatalf("ExchangeUDP: %v", err)
	}
	if !bytes.Equal(got.Payload, testPayload) {
		t.Errorf("Payload = %x, want %x from the matching response", got.Payload, testPayload)
	}
}

func TestExchangeTCPIgnoresAResponseWithADifferentID(t *testing.T) {
	r := respond(t, func(_, ok transporttest.Message) []transporttest.Message {
		wrong := ok
		wrong.ID = ok.ID + 1
		wrong.Answer = []transporttest.RR{{Name: ok.Question[0].Name, Key: ok.Question[0].Key, Data: []byte("wrong")}}
		return []transporttest.Message{wrong, ok}
	})
	got, err := clientExchange(t, transport.TCP, r)
	if err != nil {
		t.Fatalf("ExchangeTCP: %v", err)
	}
	if !bytes.Equal(got.Payload, testPayload) {
		t.Errorf("Payload = %x, want %x from the matching response", got.Payload, testPayload)
	}
}

func TestExchangeReturnsErrMalformedForAResponseThatDoesNotParse(t *testing.T) {
	cases := map[string]func(query []byte) []byte{
		// The header holds the query ID and one question, and the question name stops
		// after its length byte.
		"a cut question": func(query []byte) []byte {
			resp := append([]byte(nil), query[:12]...)
			resp[2] |= 0x80
			return append(resp, 5, 'a')
		},
		"a message shorter than the header": func([]byte) []byte { return []byte{0, 1, 2} },
	}
	for _, network := range []string{transport.UDP, transport.TCP} {
		for name, build := range cases {
			t.Run(network+"/"+name, func(t *testing.T) {
				_, err := clientExchange(t, network, respondRaw(build))
				if !errors.Is(err, transport.ErrMalformed) {
					t.Errorf("err = %v, want ErrMalformed", err)
				}
			})
		}
	}
}

func TestExchangeTCPReturnsErrMalformedForTheTCBit(t *testing.T) {
	r := respond(t, func(_, ok transporttest.Message) []transporttest.Message {
		ok.Truncated = true
		ok.Answer = nil
		return []transporttest.Message{ok}
	})
	_, err := clientExchange(t, transport.TCP, r)
	if !errors.Is(err, transport.ErrMalformed) {
		t.Errorf("err = %v, want ErrMalformed", err)
	}
}

func TestExchangeReturnsErrMismatchForADifferentQuestion(t *testing.T) {
	other := protocol.ProtocolKey{Class: testKey.Class + 1, Type: testKey.Type}
	cases := map[string]func(q []transporttest.RR) []transporttest.RR{
		"no question":   func([]transporttest.RR) []transporttest.RR { return nil },
		"two questions": func(q []transporttest.RR) []transporttest.RR { return []transporttest.RR{q[0], q[0]} },
		"another name": func(q []transporttest.RR) []transporttest.RR {
			return []transporttest.RR{{Name: "device43.fleet.example.", Key: q[0].Key}}
		},
		"another class": func(q []transporttest.RR) []transporttest.RR {
			return []transporttest.RR{{Name: q[0].Name, Key: other}}
		},
		"another type": func(q []transporttest.RR) []transporttest.RR {
			return []transporttest.RR{{Name: q[0].Name, Key: protocol.ProtocolKey{Class: q[0].Key.Class, Type: q[0].Key.Type + 1}}}
		},
	}
	for _, network := range []string{transport.UDP, transport.TCP} {
		for name, question := range cases {
			t.Run(network+"/"+name, func(t *testing.T) {
				r := respond(t, func(_, ok transporttest.Message) []transporttest.Message {
					ok.Question = question(ok.Question)
					return []transporttest.Message{ok}
				})
				_, err := clientExchange(t, network, r)
				if !errors.Is(err, transport.ErrMismatch) {
					t.Errorf("err = %v, want ErrMismatch", err)
				}
			})
		}
	}
}

func TestExchangeMatchesANameThatDiffersOnlyInCase(t *testing.T) {
	r := respond(t, func(_, ok transporttest.Message) []transporttest.Message {
		ok.Question = []transporttest.RR{{Name: "DEVICE42.Fleet.Example.", Key: testKey}}
		ok.Answer[0].Name = "Device42.FLEET.example."
		return []transporttest.Message{ok}
	})
	got, err := clientExchange(t, transport.UDP, r)
	if err != nil {
		t.Fatalf("ExchangeUDP: %v", err)
	}
	if !bytes.Equal(got.Payload, testPayload) {
		t.Errorf("Payload = %x, want %x", got.Payload, testPayload)
	}
}

func TestExchangeReturnsErrMismatchForANoErrorResponseWithoutOneMatchingAnswer(t *testing.T) {
	cases := map[string]func(a transporttest.RR) []transporttest.RR{
		"no answer":   func(transporttest.RR) []transporttest.RR { return nil },
		"two answers": func(a transporttest.RR) []transporttest.RR { return []transporttest.RR{a, a} },
		"another name": func(a transporttest.RR) []transporttest.RR {
			a.Name = "device43.fleet.example."
			return []transporttest.RR{a}
		},
		"another class": func(a transporttest.RR) []transporttest.RR {
			a.Key.Class++
			return []transporttest.RR{a}
		},
		"another type": func(a transporttest.RR) []transporttest.RR {
			a.Key.Type++
			return []transporttest.RR{a}
		},
	}
	for _, network := range []string{transport.UDP, transport.TCP} {
		for name, answer := range cases {
			t.Run(network+"/"+name, func(t *testing.T) {
				r := respond(t, func(_, ok transporttest.Message) []transporttest.Message {
					ok.Answer = answer(ok.Answer[0])
					return []transporttest.Message{ok}
				})
				_, err := clientExchange(t, network, r)
				if !errors.Is(err, transport.ErrMismatch) {
					t.Errorf("err = %v, want ErrMismatch", err)
				}
			})
		}
	}
}

func TestExchangeReturnsEveryByteValueOfTheBinaryPayload(t *testing.T) {
	payload := make([]byte, 256)
	for i := range payload {
		payload[i] = byte(i)
	}
	for _, network := range []string{transport.UDP, transport.TCP} {
		t.Run(network, func(t *testing.T) {
			r := respond(t, func(_, ok transporttest.Message) []transporttest.Message {
				ok.Answer[0].Data = payload
				return []transporttest.Message{ok}
			})
			got, err := clientExchange(t, network, r)
			if err != nil {
				t.Fatalf("exchange: %v", err)
			}
			if !bytes.Equal(got.Payload, payload) {
				t.Errorf("Payload = %x, want %x", got.Payload, payload)
			}
		})
	}
}

func TestExchangeReportsTheReceivedFields(t *testing.T) {
	cases := map[string]struct {
		network string
		build   func(ok transporttest.Message) transporttest.Message
		want    transport.Received
	}{
		"a NOERROR answer": {
			network: transport.TCP,
			build:   func(ok transporttest.Message) transporttest.Message { return ok },
			want:    transport.Received{RCode: transport.RCodeNoError, TTL: 300, Payload: testPayload},
		},
		"a REFUSED response": {
			network: transport.UDP,
			build: func(ok transporttest.Message) transporttest.Message {
				ok.RCode = transport.RCodeRefused
				ok.Answer = nil
				return ok
			},
			want: transport.Received{RCode: transport.RCodeRefused},
		},
		"a BADVERS response in the OPT RR": {
			network: transport.UDP,
			build: func(ok transporttest.Message) transporttest.Message {
				ok.RCode = transport.RCodeBadVers
				ok.Answer = nil
				ok.EDNS = true
				ok.UDPSize = 1232
				return ok
			},
			want: transport.Received{RCode: transport.RCodeBadVers},
		},
		"a truncated UDP response": {
			network: transport.UDP,
			build: func(ok transporttest.Message) transporttest.Message {
				ok.Truncated = true
				ok.Answer = nil
				return ok
			},
			want: transport.Received{RCode: transport.RCodeNoError, Truncated: true},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// The channel carries the size from the server goroutine to the test goroutine.
			sent := make(chan int, 1)
			r := func(_ string, query []byte) [][]byte {
				resps := respond(t, func(_, ok transporttest.Message) []transporttest.Message {
					return []transporttest.Message{tc.build(ok)}
				})(tc.network, query)
				if len(resps) == 1 {
					sent <- len(resps[0])
				}
				return resps
			}
			got, err := clientExchange(t, tc.network, r)
			if err != nil {
				t.Fatalf("exchange: %v", err)
			}
			tc.want.WireSize = <-sent
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Received = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestExchangeReturnsThePackedRDATAOfAKnownType(t *testing.T) {
	txtKey := protocol.ProtocolKey{Class: 1, Type: 16}
	// One TXT character-string of length 2.
	rdata := []byte{2, 'h', 'i'}
	addr := transporttest.StartServer(t, func(_ string, query []byte) [][]byte {
		q, err := transporttest.Unpack(query)
		if err != nil {
			t.Errorf("unpack query: %v", err)
			return nil
		}
		resp, err := transporttest.Pack(transporttest.Message{
			ID: q.ID, Response: true, Question: q.Question,
			Answer: []transporttest.RR{{Name: q.Question[0].Name, Key: txtKey, TTL: 60, Data: rdata}},
		})
		if err != nil {
			t.Errorf("pack response: %v", err)
			return nil
		}
		return [][]byte{resp}
	})
	c, err := net.Dial(transport.UDP, addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close() //nolint:errcheck // The test reads nothing after the exchange.
	p, err := transport.NewQuery(testName, txtKey, 1232, false)
	if err != nil {
		t.Fatalf("NewQuery: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := transport.ExchangeUDP(ctx, c, p)
	if err != nil {
		t.Fatalf("ExchangeUDP: %v", err)
	}
	if !bytes.Equal(got.Payload, rdata) {
		t.Errorf("Payload = %x, want %x", got.Payload, rdata)
	}
}

func TestExchangeReturnsWhenTheContextExpires(t *testing.T) {
	drop := func(string, []byte) [][]byte { return nil }
	for _, network := range []string{transport.UDP, transport.TCP} {
		t.Run(network, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			_, err := clientExchangeCtx(ctx, t, network, drop)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("err = %v, want context.DeadlineExceeded", err)
			}
		})
	}
}

func TestExchangeUDPReturnsWhenTheContextExpiresDuringWrongIDs(t *testing.T) {
	// Each query gets a wrong ID only, so the exchange waits until ctx expires.
	r := func(_ string, query []byte) [][]byte {
		resp := append([]byte(nil), query...)
		binary.BigEndian.PutUint16(resp, binary.BigEndian.Uint16(query)+1)
		return [][]byte{resp}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := clientExchangeCtx(ctx, t, transport.UDP, r)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
}
