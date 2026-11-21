package baseline_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/udp"

	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/security"
	"github.com/Crank-Git/qmux/tests/interoperability/baseline"
)

// start starts the baseline servers on loopback ports that the operating system
// selects, and stops them at the end of the test.
func start(t *testing.T, tamper bool) *baseline.Running {
	t.Helper()
	r, err := baseline.Start(t.Context(), baseline.Config{
		DNSAddr:   "127.0.0.1:0",
		CoAPAddr:  "127.0.0.1:0",
		HTTPSAddr: "127.0.0.1:0",
		Tamper:    tamper,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return r
}

// newClient returns a client for r that closes at the end of the test.
func newClient(t *testing.T, r *baseline.Running) *baseline.Client {
	t.Helper()
	c, err := baseline.NewClient(r.Target())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

// lookupAll does one lookup per baseline and returns the verified envelope bytes of
// each baseline.
func lookupAll(t *testing.T, r *baseline.Running) map[string][]byte {
	t.Helper()
	c := newClient(t, r)
	got := make(map[string][]byte)
	for _, name := range baseline.Names {
		raw, err := c.Lookup(t.Context(), name)
		if err != nil {
			t.Fatalf("Lookup(%s): %v", name, err)
		}
		got[name] = raw
	}
	return got
}

func TestBaselinesUseTheProtocolKeysAndNamesOfTheSpec(t *testing.T) {
	tests := []struct {
		name string
		got  any
		want any
	}{
		{"qmux-private key", baseline.PrivateKey, protocol.ProtocolKey{Class: 65282, Type: 65280}},
		{"qmux-private QNAME", baseline.PrivateName, "device42.fleet.example."},
		{"dns-in key", baseline.INKey, protocol.ProtocolKey{Class: 1, Type: 65280}},
		{"dns-in-txt key", baseline.TXTKey, protocol.ProtocolKey{Class: 1, Type: 16}},
		{"IN QNAME", baseline.INName, "_fw.device42.fleet.example."},
		{"CoAP and HTTPS path", baseline.Path, "/fw/device42"},
		{"baseline order", baseline.Names, []string{"qmux-private", "dns-in", "dns-in-txt", "coap", "https-cold", "https-reuse"}},
	}
	for _, tt := range tests {
		if !reflect.DeepEqual(tt.got, tt.want) {
			t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.want)
		}
	}
}

func TestOneLookupPerBaselineSucceedsAtLossZero(t *testing.T) {
	r := start(t, false)
	got := lookupAll(t, r)
	if len(got) != len(baseline.Names) {
		t.Fatalf("got %d baselines, want %d", len(got), len(baseline.Names))
	}
}

func TestEveryBaselineCarriesTheFirmwarePayloadOfQMuxPrivate(t *testing.T) {
	r := start(t, false)
	got := lookupAll(t, r)
	want, err := baseline.Payload()
	if err != nil {
		t.Fatalf("Payload: %v", err)
	}
	private, err := codec.Decode(got[baseline.QMuxPrivate], codec.Limits{})
	if err != nil {
		t.Fatalf("decode qmux-private envelope: %v", err)
	}
	if !bytes.Equal(private.Payload, want) {
		t.Fatalf("qmux-private payload = %x, want %x", private.Payload, want)
	}
	for _, name := range baseline.Names {
		e, err := codec.Decode(got[name], codec.Limits{})
		if err != nil {
			t.Fatalf("decode %s envelope: %v", name, err)
		}
		if !bytes.Equal(e.Payload, private.Payload) {
			t.Errorf("%s payload = %x, want the qmux-private payload %x", name, e.Payload, private.Payload)
		}
	}
}

func TestCoAPAndHTTPSCarryTheEnvelopeBytesOfQMuxPrivate(t *testing.T) {
	r := start(t, false)
	got := lookupAll(t, r)
	for _, name := range []string{baseline.CoAP, baseline.HTTPSCold, baseline.HTTPSReuse} {
		if !bytes.Equal(got[name], got[baseline.QMuxPrivate]) {
			t.Errorf("%s envelope = %x, want the qmux-private envelope %x", name, got[name], got[baseline.QMuxPrivate])
		}
	}
}

func TestALookupFailsWhenTheEnvelopeHasOneChangedByte(t *testing.T) {
	r := start(t, true)
	c := newClient(t, r)
	for _, name := range baseline.Names {
		raw, err := c.Lookup(t.Context(), name)
		if !errors.Is(err, security.ErrBadSignature) {
			t.Errorf("Lookup(%s) = %x, %v, want an error that wraps security.ErrBadSignature", name, raw, err)
		}
	}
}

func TestLookupRejectsAnUnknownBaseline(t *testing.T) {
	r := start(t, false)
	c := newClient(t, r)
	if _, err := c.Lookup(t.Context(), "dns-ch"); !errors.Is(err, baseline.ErrUnknownBaseline) {
		t.Fatalf("Lookup(dns-ch) error = %v, want baseline.ErrUnknownBaseline", err)
	}
}

func TestCoAPServerAnswersANonConfirmableGETWithBadRequest(t *testing.T) {
	r := start(t, false)
	conn, err := udp.Dial(r.Target().CoAP)
	if err != nil {
		t.Fatalf("dial CoAP: %v", err)
	}
	defer conn.Close() //nolint:errcheck // The test reads no state from the closed connection.
	req, err := conn.NewGetRequest(t.Context(), baseline.Path)
	if err != nil {
		t.Fatalf("new GET request: %v", err)
	}
	req.SetType(message.NonConfirmable)
	resp, err := conn.Do(req)
	if err != nil {
		t.Fatalf("NON GET: %v", err)
	}
	if resp.Code() != codes.BadRequest {
		t.Fatalf("NON GET code = %v, want %v", resp.Code(), codes.BadRequest)
	}
}

func TestCoAPServerAnswersWithContentFormatCBOR(t *testing.T) {
	r := start(t, false)
	conn, err := udp.Dial(r.Target().CoAP)
	if err != nil {
		t.Fatalf("dial CoAP: %v", err)
	}
	defer conn.Close() //nolint:errcheck // The test reads no state from the closed connection.
	resp, err := conn.Get(t.Context(), baseline.Path)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	cf, err := resp.ContentFormat()
	if err != nil || cf != 60 {
		t.Fatalf("content-format = %v, %v, want 60 (application/cbor)", cf, err)
	}
}

func TestHTTPSServerRefusesTLS12(t *testing.T) {
	r := start(t, false)
	cfg, err := baseline.TLSClientConfig()
	if err != nil {
		t.Fatalf("TLSClientConfig: %v", err)
	}
	cfg.MinVersion = tls.VersionTLS12
	cfg.MaxVersion = tls.VersionTLS12
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+r.Target().HTTPS+baseline.Path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := hc.Do(req)
	if err == nil {
		resp.Body.Close() //nolint:errcheck // The test only needs the handshake result.
		t.Fatal("a TLS 1.2 client got a response, want a failed handshake")
	}
}

func TestTLSCertificateIsTheSameAtEachCall(t *testing.T) {
	a, err := baseline.TLSCertificate()
	if err != nil {
		t.Fatalf("TLSCertificate: %v", err)
	}
	b, err := baseline.TLSCertificate()
	if err != nil {
		t.Fatalf("TLSCertificate: %v", err)
	}
	if !bytes.Equal(a.Certificate[0], b.Certificate[0]) {
		t.Fatal("two calls gave different certificates, so a client in another process cannot trust the server")
	}
}

func TestStartReturnsTheBindErrorForAnAddressInUse(t *testing.T) {
	r := start(t, false)
	_, err := baseline.Start(context.Background(), baseline.Config{
		DNSAddr:   "127.0.0.1:0",
		CoAPAddr:  "127.0.0.1:0",
		HTTPSAddr: r.Target().HTTPS,
	})
	if err == nil {
		t.Fatal("Start on a used HTTPS address returned nil, want the bind error")
	}
}
