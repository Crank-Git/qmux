package client_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/transport"
	"github.com/Crank-Git/qmux/pkg/transport/transporttest"
)

var testQuery = client.Query{Name: testName, Class: testKey.Class, Type: testKey.Type}

// reply returns a responder that answers each query with the messages that build
// returns. build gets the parsed query and a correct response to it. A responder error
// drops the query, so the test then fails with a timeout.
func reply(build func(q, ok transporttest.Message) []transporttest.Message) transporttest.Responder {
	return func(_ string, wire []byte) [][]byte {
		q, err := transporttest.Unpack(wire)
		if err != nil {
			return nil
		}
		ok := transporttest.Message{
			ID:       q.ID,
			Response: true,
			Question: q.Question,
			Answer:   []transporttest.RR{{Name: q.Question[0].Name, Key: q.Question[0].Key, TTL: 60, Data: testPayload}},
		}
		var out [][]byte
		for _, m := range build(q, ok) {
			b, err := transporttest.Pack(m)
			if err != nil {
				return nil
			}
			out = append(out, b)
		}
		return out
	}
}

// queryErr sends testQuery to a fake server that uses r and returns the error.
func queryErr(t *testing.T, r transporttest.Responder) error {
	t.Helper()
	c := client.NewClient(client.ClientConfig{Resolver: transporttest.StartServer(t, r), AttemptTimeout: 50 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := c.Query(ctx, testQuery)
	if err == nil {
		t.Fatalf("Query returned %+v and no error", res)
	}
	return err
}

// wantKind fails the test when err is not a DNSError of kind want, and returns a copy
// of the DNSError. The copy is not an error value, so errcheck accepts an unused result.
func wantKind(t *testing.T, err error, want client.ErrorKind) client.DNSError {
	t.Helper()
	var de *client.DNSError
	if !errors.As(err, &de) {
		t.Fatalf("Query error %v is not a DNSError", err)
	}
	if de.Kind != want {
		t.Fatalf("DNSError kind = %q, want %q (%v)", de.Kind, want, err)
	}
	return *de
}

func TestQueryIgnoresAUDPResponseWithADifferentID(t *testing.T) { // FR-client-16
	r := reply(func(_, ok transporttest.Message) []transporttest.Message {
		wrong := ok
		wrong.ID = ok.ID + 1
		wrong.Answer = []transporttest.RR{{Name: ok.Answer[0].Name, Key: ok.Answer[0].Key, TTL: 60, Data: []byte{0xff}}}
		return []transporttest.Message{wrong, ok}
	})
	c := client.NewClient(client.ClientConfig{Resolver: transporttest.StartServer(t, r)})

	res := query(t, c, testQuery)

	if !bytes.Equal(res.Payload, testPayload) {
		t.Errorf("Payload = %x, want %x from the response with the query ID", res.Payload, testPayload)
	}
}

func TestQueryReturnsAMismatchErrorForADifferentQuestionType(t *testing.T) { // FR-client-17
	err := queryErr(t, reply(func(_, ok transporttest.Message) []transporttest.Message {
		ok.Question = []transporttest.RR{{Name: testName, Key: protocol.ProtocolKey{Class: testKey.Class, Type: 65281}}}
		return []transporttest.Message{ok}
	}))

	wantKind(t, err, client.KindMismatch)
	if !errors.Is(err, transport.ErrMismatch) {
		t.Errorf("errors.Is(err, transport.ErrMismatch) = false for %v", err)
	}
}

func TestQueryMatchesAResponseNameWithADifferentCase(t *testing.T) { // FR-client-18
	r := reply(func(_, ok transporttest.Message) []transporttest.Message {
		ok.Question = []transporttest.RR{{Name: "DEVICE42.fleet.example.", Key: testKey}}
		ok.Answer[0].Name = "DEVICE42.fleet.example."
		return []transporttest.Message{ok}
	})
	c := client.NewClient(client.ClientConfig{Resolver: transporttest.StartServer(t, r)})

	res := query(t, c, testQuery)

	if !bytes.Equal(res.Payload, testPayload) {
		t.Errorf("Payload = %x, want %x", res.Payload, testPayload)
	}
}

func TestQueryReturnsAMismatchErrorWhenANoErrorResponseDoesNotHoldOneAnswer(t *testing.T) { // FR-client-19
	tests := []struct {
		name    string
		answers int
	}{
		{"zero answer RRs", 0},
		{"two answer RRs", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := queryErr(t, reply(func(_, ok transporttest.Message) []transporttest.Message {
				rr := ok.Answer[0]
				ok.Answer = nil
				for range tt.answers {
					ok.Answer = append(ok.Answer, rr)
				}
				return []transporttest.Message{ok}
			}))

			wantKind(t, err, client.KindMismatch)
		})
	}
}

func TestQueryReturnsAMismatchErrorWhenTheAnswerRRDiffersFromTheQuestion(t *testing.T) { // FR-client-20
	tests := []struct {
		name string
		rr   transporttest.RR
	}{
		{"class 65281", transporttest.RR{Name: testName, Key: protocol.ProtocolKey{Class: 65281, Type: testKey.Type}}},
		{"type 65281", transporttest.RR{Name: testName, Key: protocol.ProtocolKey{Class: testKey.Class, Type: 65281}}},
		{"other name", transporttest.RR{Name: "device43.fleet.example.", Key: testKey}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := queryErr(t, reply(func(_, ok transporttest.Message) []transporttest.Message {
				tt.rr.TTL = 60
				tt.rr.Data = testPayload
				ok.Answer = []transporttest.RR{tt.rr}
				return []transporttest.Message{ok}
			}))

			wantKind(t, err, client.KindMismatch)
		})
	}
}

func TestQueryReturnsAnRCodeErrorThatHoldsTheRCode(t *testing.T) { // FR-client-21
	tests := []struct {
		name      string
		questions bool
	}{
		{"with the question", true},
		// ADR 0005 Exchange rule 4 accepts an error RCODE without the question.
		{"without the question", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := queryErr(t, reply(func(_, ok transporttest.Message) []transporttest.Message {
				ok.RCode = transport.RCodeRefused
				ok.Answer = nil
				if !tt.questions {
					ok.Question = nil
				}
				return []transporttest.Message{ok}
			}))

			de := wantKind(t, err, client.KindRCode)
			if de.RCode != 5 {
				t.Errorf("DNSError RCode = %d, want 5", de.RCode)
			}
			if !strings.Contains(err.Error(), "REFUSED") {
				t.Errorf("Error() = %q, want the RCODE text REFUSED", err.Error())
			}
		})
	}
}

func TestQueryReturnsAMalformedErrorForAUDPResponseThatDoesNotParse(t *testing.T) { // FR-client-22
	err := queryErr(t, func(_ string, wire []byte) [][]byte {
		// The ID of the query keeps the packet from the wrong-ID rule, and the
		// cut makes the question section incomplete.
		resp := append([]byte(nil), wire[:14]...)
		resp[2] |= 0x80
		return [][]byte{resp}
	})

	wantKind(t, err, client.KindMalformed)
	if !errors.Is(err, transport.ErrMalformed) {
		t.Errorf("errors.Is(err, transport.ErrMalformed) = false for %v", err)
	}
}

func TestQueryReturnsATruncationErrorForTheTCBitWithDisableTCPFallback(t *testing.T) { // FR-client-15
	r := recorder{edit: func(_ string, m *transporttest.Message) {
		m.Truncated = true
		m.Answer = nil
	}}
	c := client.NewClient(client.ClientConfig{Resolver: r.start(t), DisableTCPFallback: true})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.Query(ctx, testQuery)

	wantKind(t, err, client.KindTruncation)
	if got := r.receivedOver(); !slices.Equal(got, []string{transport.UDP}) {
		t.Errorf("server received queries over %v, want one over udp", got)
	}
}

func TestQueryReturnsAMalformedErrorWhenTheTCPFallbackResponseHasTheTCBit(t *testing.T) {
	r := recorder{edit: func(_ string, m *transporttest.Message) {
		m.Truncated = true
		m.Answer = nil
	}}
	c := client.NewClient(client.ClientConfig{Resolver: r.start(t)})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.Query(ctx, testQuery)

	wantKind(t, err, client.KindMalformed)
	if got := r.receivedOver(); !slices.Equal(got, []string{transport.UDP, transport.TCP}) {
		t.Errorf("server received queries over %v, want udp then tcp", got)
	}
}

func TestQueryReturnsANetworkErrorWhenNothingListensOnTheTCPPortDuringFallback(t *testing.T) {
	// The server listens only on UDP, so the TCP dial of the fallback fails.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go answerTruncated(pc)
	c := client.NewClient(client.ClientConfig{Resolver: pc.LocalAddr().String()})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = c.Query(ctx, testQuery)

	wantKind(t, err, client.KindNetwork)
}

// answerTruncated answers each UDP query on pc with the TC bit until pc closes.
func answerTruncated(pc net.PacketConn) {
	buf := make([]byte, 65535)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		q, err := transporttest.Unpack(buf[:n])
		if err != nil {
			continue
		}
		resp, err := transporttest.Pack(transporttest.Message{ID: q.ID, Response: true, Truncated: true, Question: q.Question})
		if err != nil {
			continue
		}
		_, _ = pc.WriteTo(resp, addr) // A lost response makes the test fail with a timeout.
	}
}

func TestQueryMakesExactlyThreeAttemptsWhenTheServerDropsEveryPacket(t *testing.T) { // FR-client-10
	r := recorder{drop: func(int) bool { return true }}
	c := client.NewClient(client.ClientConfig{Resolver: r.start(t), AttemptTimeout: 50 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.Query(ctx, testQuery)

	wantKind(t, err, client.KindTimeout)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("errors.Is(err, context.DeadlineExceeded) = false for %v", err)
	}
	if !strings.Contains(err.Error(), "3 attempts") {
		t.Errorf("Error() = %q, want the attempt count", err.Error())
	}
	if got := r.receivedOver(); !slices.Equal(got, []string{transport.UDP, transport.UDP, transport.UDP}) {
		t.Errorf("server received queries over %v, want 3 over udp", got)
	}
}

func TestQueryMakesOneTCPAttemptAndNoRetryOnATimeout(t *testing.T) {
	r := recorder{drop: func(int) bool { return true }}
	c := client.NewClient(client.ClientConfig{Resolver: r.start(t), Network: "tcp", AttemptTimeout: 50 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.Query(ctx, testQuery)

	wantKind(t, err, client.KindTimeout)
	if got := r.receivedOver(); !slices.Equal(got, []string{transport.TCP}) {
		t.Errorf("server received queries over %v, want one over tcp", got)
	}
}

func TestQueryReturnsATimeoutErrorWhenTheContextExpires(t *testing.T) { // FR-client-12
	// The default AttemptTimeout of 1s is longer than ctx, so ctx stops the attempt.
	c := client.NewClient(client.ClientConfig{
		Resolver: transporttest.StartServer(t, func(string, []byte) [][]byte { return nil }),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()

	_, err := c.Query(ctx, testQuery)

	if elapsed := time.Since(start); elapsed >= 300*time.Millisecond {
		t.Errorf("Query returned after %v, want less than 300ms", elapsed)
	}
	wantKind(t, err, client.KindTimeout)
}

func TestQueryReturnsAnErrorThatWrapsCanceledWhenTheContextIsCancelled(t *testing.T) { // FR-client-13
	tests := []struct {
		name    string
		network string
	}{
		{"udp", transport.UDP},
		{"tcp", transport.TCP},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			// The server cancels ctx when the query arrives, so the cancel comes during
			// the attempt.
			server := transporttest.StartServer(t, func(string, []byte) [][]byte {
				cancel()
				return nil
			})
			c := client.NewClient(client.ClientConfig{Resolver: server, Network: tt.network})

			_, err := c.Query(ctx, testQuery)

			if !errors.Is(err, context.Canceled) {
				t.Errorf("errors.Is(err, context.Canceled) = false for %v", err)
			}
		})
	}
}

func TestQueryReturnsAMalformedErrorForATruncatedTCPResponse(t *testing.T) { // FR-client-22
	whole := reply(func(_, ok transporttest.Message) []transporttest.Message { return []transporttest.Message{ok} })
	// The server frames the cut message with its own length, so the read succeeds and
	// the parse fails.
	cut := func(network string, wire []byte) [][]byte {
		var out [][]byte
		for _, resp := range whole(network, wire) {
			out = append(out, resp[:len(resp)-3])
		}
		return out
	}
	c := client.NewClient(client.ClientConfig{Resolver: transporttest.StartServer(t, cut), Network: "tcp"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.Query(ctx, testQuery)

	wantKind(t, err, client.KindMalformed)
}

func TestQueryReturnsANetworkErrorForAClosedTCPPort(t *testing.T) { // FR-client-23
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	c := client.NewClient(client.ClientConfig{Resolver: server, Network: "tcp"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = c.Query(ctx, testQuery)

	wantKind(t, err, client.KindNetwork)
}

func TestDNSErrorNamesTheKindTheNameAndTheProtocolKey(t *testing.T) {
	err := &client.DNSError{Kind: client.KindMismatch, Name: testName, Key: testKey, Err: transport.ErrMismatch}

	msg := err.Error()

	for _, want := range []string{"mismatch", testName, "65280/65280"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, want %q in it", msg, want)
		}
	}
	if !errors.Is(err, transport.ErrMismatch) {
		t.Error("errors.Is(DNSError, its Err) = false")
	}
}

func TestAppErrorNamesTheStatus(t *testing.T) { // FR-client-24
	err := client.AppError{Status: 1, ProtocolID: "org.qmux.firmware", Version: 1}

	msg := err.Error()

	for _, want := range []string{"status 1", "org.qmux.firmware"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, want %q in it", msg, want)
		}
	}
}

func TestVerifyErrorNamesTheReason(t *testing.T) { // FR-client-25
	cause := errors.New("signature time is past")
	err := client.VerifyError{Reason: "expired", Err: cause}

	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("Error() = %q, want the reason expired", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Error("errors.Is(VerifyError, its Err) = false")
	}
}
