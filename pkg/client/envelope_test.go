package client_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/transport"
	"github.com/Crank-Git/qmux/pkg/transport/transporttest"
)

// answerWith returns a recorder edit that sends rdata as the RDATA of the answer RR.
func answerWith(rdata []byte) func(string, *transporttest.Message) {
	return func(_ string, m *transporttest.Message) { m.Answer[0].Data = rdata }
}

// envelopeWith returns testEnvelope after edit changes it.
func envelopeWith(edit func(*codec.Envelope)) codec.Envelope {
	e := testEnvelope
	edit(&e)
	return e
}

// queryEnvelope sends q through a client of a recorder with edit, and returns the result and
// the error of Query.
func queryEnvelope(t *testing.T, edit func(string, *transporttest.Message), q client.Query) (*client.Result, error) {
	t.Helper()
	r := recorder{edit: edit}
	c := client.NewClient(client.ClientConfig{Resolver: r.start(t)})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.Query(ctx, q)
}

// wantEnvelopeKind fails the test when err is not a *client.DNSError of kind want.
func wantEnvelopeKind(t *testing.T, err error, want client.ErrorKind) {
	t.Helper()
	var de *client.DNSError
	if !errors.As(err, &de) || de.Kind != want {
		t.Fatalf("error = %v, want a DNSError of kind %s", err, want)
	}
}

func TestQueryReturnsAMalformedDNSErrorForRDATAThatIsNotAnEnvelope(t *testing.T) { // FR-codec-37
	for name, rdata := range map[string][]byte{
		"the byte 0xff":              {0xff},
		"no byte":                    {},
		"an envelope and extra byte": append(append([]byte(nil), testRDATA...), 0x00),
	} {
		t.Run(name, func(t *testing.T) {
			res, err := queryEnvelope(t, answerWith(rdata), testQuery)
			wantEnvelopeKind(t, err, client.KindMalformed)
			if res != nil {
				t.Fatalf("result = %+v, want nil", res)
			}
			if !errors.Is(err, transport.ErrMalformed) {
				t.Fatalf("errors.Is(%v, transport.ErrMalformed) = false, want true", err)
			}
		})
	}
}

func TestQueryReturnsAMalformedDNSErrorThatWrapsTheCodecError(t *testing.T) { // FR-codec-37
	// Byte 2 of the envelope is the value of key 0, the format.
	rdata := append([]byte(nil), testRDATA...)
	rdata[2] = 0x02
	_, err := queryEnvelope(t, answerWith(rdata), testQuery)
	wantEnvelopeKind(t, err, client.KindMalformed)
	if !errors.Is(err, codec.ErrFormat) {
		t.Fatalf("errors.Is(%v, codec.ErrFormat) = false, want true", err)
	}
}

func TestResultCarriesTheFieldsOfTheEnvelope(t *testing.T) { // FR-codec-38
	e := envelopeWith(func(e *codec.Envelope) { e.Version, e.MessageType = 7, 2 })
	res, err := queryEnvelope(t, answerWith(mustEncode(e)), testQuery)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.ProtocolID != "org.qmux.firmware" || res.Version != 7 || res.MessageType != protocol.MessageManifest ||
		res.Status != protocol.StatusOK || !bytes.Equal(res.Payload, testPayload) {
		t.Fatalf("result = %q, %d, %d, %d, %x; want org.qmux.firmware, 7, 2, 0, %x",
			res.ProtocolID, res.Version, res.MessageType, res.Status, res.Payload, testPayload)
	}
}

func TestQueryReturnsTheResultAndAnAppErrorForANotFoundEnvelope(t *testing.T) { // FR-codec-39
	e := envelopeWith(func(e *codec.Envelope) { e.Status = uint64(protocol.StatusNotFound) })
	res, err := queryEnvelope(t, answerWith(mustEncode(e)), testQuery)
	if res == nil {
		t.Fatal("result = nil, want the result of the envelope")
	}
	var ae *client.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("error = %v, want a *client.AppError", err)
	}
	if ae.Status != protocol.StatusNotFound || ae.ProtocolID != "org.qmux.firmware" || ae.Version != 1 {
		t.Fatalf("AppError = %+v, want status 1, protocol ID org.qmux.firmware, version 1", ae)
	}
	if res.Status != protocol.StatusNotFound {
		t.Fatalf("Result.Status = %d, want 1", res.Status)
	}
}

func TestQueryReturnsAnAppErrorForAReservedAndAProtocolSpecificStatus(t *testing.T) { // FR-codec-39
	for _, status := range []uint64{5, 255, 300} {
		e := envelopeWith(func(e *codec.Envelope) { e.Status = status })
		res, err := queryEnvelope(t, answerWith(mustEncode(e)), testQuery)
		var ae *client.AppError
		if res == nil || !errors.As(err, &ae) || uint64(ae.Status) != status {
			t.Fatalf("status %d: result %v, error %v; want a result and an AppError with the status", status, res, err)
		}
	}
}

func TestQueryReturnsTheResultAndAnAppErrorAfterATCPFallback(t *testing.T) { // FR-codec-39
	rdata := mustEncode(envelopeWith(func(e *codec.Envelope) { e.Status = uint64(protocol.StatusUnavailable) }))
	res, err := queryEnvelope(t, func(network string, m *transporttest.Message) {
		m.Answer[0].Data = rdata
		truncateUDP(network, m)
	}, testQuery)
	var ae *client.AppError
	if res == nil || !res.FellBack || !errors.As(err, &ae) {
		t.Fatalf("result %+v, error %v; want a result with FellBack and an AppError", res, err)
	}
}

func TestQueryReturnsAMismatchDNSErrorForAnUnexpectedProtocolID(t *testing.T) { // FR-codec-40
	q := testQuery
	q.ExpectedProtocolID = "org.qmux.other"
	res, err := queryEnvelope(t, nil, q)
	wantEnvelopeKind(t, err, client.KindMismatch)
	if res != nil {
		t.Fatalf("result = %+v, want nil", res)
	}
}

func TestQueryAcceptsTheExpectedProtocolID(t *testing.T) { // FR-codec-40
	q := testQuery
	q.ExpectedProtocolID = "org.qmux.firmware"
	if _, err := queryEnvelope(t, nil, q); err != nil {
		t.Fatalf("Query: %v", err)
	}
}

func TestQueryReturnsAMismatchDNSErrorWhenTheEnvelopeVersionDiffersFromTheLabel(t *testing.T) { // FR-codec-41
	q := testQuery
	q.Version = 1
	e := envelopeWith(func(e *codec.Envelope) { e.Version = 2 })
	_, err := queryEnvelope(t, answerWith(mustEncode(e)), q)
	wantEnvelopeKind(t, err, client.KindMismatch)
}

func TestQueryAcceptsAnEnvelopeVersionThatEqualsTheLabel(t *testing.T) { // FR-codec-41
	q := testQuery
	q.Version = 2
	e := envelopeWith(func(e *codec.Envelope) { e.Version = 2 })
	if _, err := queryEnvelope(t, answerWith(mustEncode(e)), q); err != nil {
		t.Fatalf("Query: %v", err)
	}
}

func TestQueryChecksTheProtocolIDBeforeTheStatus(t *testing.T) { // FR-codec-40, FR-codec-39
	q := testQuery
	q.ExpectedProtocolID = "org.qmux.other"
	e := envelopeWith(func(e *codec.Envelope) { e.Status = uint64(protocol.StatusNotFound) })
	_, err := queryEnvelope(t, answerWith(mustEncode(e)), q)
	wantEnvelopeKind(t, err, client.KindMismatch)
}
