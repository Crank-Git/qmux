package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Crank-Git/qmux"
	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/security"
	"github.com/Crank-Git/qmux/pkg/transport/transporttest"
)

// The names of the Phase 3 test. phase3OtherName is in the same zone, so the server
// answers it, but no envelope of the test is signed for it.
const (
	phase3Name      = "device42.fleet.example."
	phase3OtherName = "device43.fleet.example."
)

// The two Phase 3 registrations. They have the same handler, so only the signature
// tells their envelopes apart.
var (
	phase3Firmware      = qmux.ProtocolKey{Class: 65282, Type: 65280}
	phase3Configuration = qmux.ProtocolKey{Class: 65281, Type: 65280}
	phase3Payload       = []byte("phase 3 signed payload")
)

// The fixed seeds of the test keys. Only the public key of phase3AnchorSeed is a
// trust anchor.
var (
	phase3AnchorSeed = bytes.Repeat([]byte{0x51}, ed25519.SeedSize)
	phase3OtherSeed  = bytes.Repeat([]byte{0x52}, ed25519.SeedSize)
)

// startPhase3Server starts a server that signs with signer and answers both Phase 3
// registrations with phase3Payload. A nil signer means no signature. It returns the
// server address.
func startPhase3Server(t *testing.T, signer *security.Signer) string {
	t.Helper()
	s := qmux.NewServer(qmux.ServerConfig{
		Address: "127.0.0.1:0",
		Zones:   []string{"fleet.example."},
		Logger:  slog.New(slog.DiscardHandler),
		Signer:  signer,
	})
	for id, key := range map[string]qmux.ProtocolKey{
		"org.qmux.firmware": phase3Firmware, "org.qmux.configuration": phase3Configuration,
	} {
		if err := s.RegisterProtocol(key, protocol.Info{ID: id, Versions: []uint16{1}}, fixedPayload(phase3Payload)); err != nil {
			t.Fatalf("RegisterProtocol %s: %v", id, err)
		}
	}
	addr, _ := startServer(t, s)
	return addr
}

// signerAt returns a signer with the key of seed whose clock is offset from the real
// clock. The verifier uses the real clock, so the offset moves the validity window.
func signerAt(seed []byte, offset time.Duration) *security.Signer {
	return &security.Signer{
		Key: ed25519.NewKeyFromSeed(seed),
		Now: func() time.Time { return time.Now().Add(offset) },
	}
}

// newPhase3Client returns a client for addr with a new verifier whose only trust
// anchor is the public key of phase3AnchorSeed.
func newPhase3Client(addr string) *client.Client {
	pub := ed25519.NewKeyFromSeed(phase3AnchorSeed).Public().(ed25519.PublicKey)
	v := &security.Verifier{Anchors: []security.TrustAnchor{{KeyID: security.KeyID(pub), PublicKey: pub}}}
	return qmux.NewClient(qmux.ClientConfig{Resolver: addr, TrustAnchors: v})
}

// relay starts a fake DNS server that forwards each query to addr and returns the
// answer to the client, and returns the fake server address.
//
// The forwarded query asks for name at key. The answer goes back with the question of
// the original query, so the client matches it. edit changes the RDATA of each answer
// RR. A failure drops the query, and the client then reports a timeout.
func relay(t *testing.T, addr, name string, key protocol.ProtocolKey, edit func([]byte) []byte) string {
	t.Helper()
	return transporttest.StartServer(t, func(network string, query []byte) [][]byte {
		q, err := transporttest.Unpack(query)
		if err != nil || len(q.Question) != 1 {
			return nil
		}
		fwd := q
		fwd.Question = []transporttest.RR{{Name: name, Key: key}}
		wire, err := transporttest.Pack(fwd)
		if err != nil {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		resp, err := transporttest.Exchange(ctx, network, addr, wire)
		if err != nil {
			return nil
		}
		m, err := transporttest.Unpack(resp)
		if err != nil {
			return nil
		}
		m.Question = q.Question
		for i := range m.Answer {
			m.Answer[i].Name, m.Answer[i].Key = q.Question[0].Name, q.Question[0].Key
			m.Answer[i].Data = edit(m.Answer[i].Data)
		}
		out, err := transporttest.Pack(m)
		if err != nil {
			return nil
		}
		return [][]byte{out}
	})
}

// unchanged is the edit function of a relay that does not change the RDATA.
func unchanged(b []byte) []byte { return b }

// flipPayloadByte returns a copy of rdata with one bit changed in the first byte of
// phase3Payload. The CBOR structure stays valid, so only the signature check fails.
func flipPayloadByte(rdata []byte) []byte {
	out := bytes.Clone(rdata)
	if i := bytes.Index(out, phase3Payload); i >= 0 {
		out[i] ^= 0x01
	}
	return out
}

func phase3Query(name string, key protocol.ProtocolKey) client.Query {
	return client.Query{Name: name, Class: key.Class, Type: key.Type, Version: 1}
}

// wantVerifyError fails the test unless err is a *client.VerifyError whose Reason is
// want and res is nil.
func wantVerifyError(t *testing.T, res *client.Result, err, want error) {
	t.Helper()
	var verifyErr *client.VerifyError
	if !errors.As(err, &verifyErr) {
		t.Fatalf("error = %v, want a *client.VerifyError with %v", err, want)
	}
	if verifyErr.Reason != want || !errors.Is(err, want) {
		t.Errorf("Reason = %v, want %v", verifyErr.Reason, want)
	}
	if res != nil {
		t.Errorf("result = %+v, want nil", res)
	}
}

func TestPhase3AcceptsAnEnvelopeSignedByATrustAnchor(t *testing.T) {
	addr := startPhase3Server(t, signerAt(phase3AnchorSeed, 0))

	res, err := newPhase3Client(addr).Query(context.Background(), phase3Query(phase3Name, phase3Firmware))

	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if !res.Verified || !bytes.Equal(res.Payload, phase3Payload) {
		t.Errorf("Verified = %t, payload = %q; want true and %q", res.Verified, res.Payload, phase3Payload)
	}
}

func TestPhase3RejectsEachBadEnvelopeWithItsOwnError(t *testing.T) { // FR-security-53
	signed := startPhase3Server(t, signerAt(phase3AnchorSeed, 0))
	cases := []struct {
		name string
		// addr returns the address that the client queries.
		addr  func(t *testing.T) string
		query client.Query
		want  error
	}{
		{
			name:  "a tampered payload byte",
			addr:  func(t *testing.T) string { return relay(t, signed, "_v1."+phase3Name, phase3Firmware, flipPayloadByte) },
			query: phase3Query(phase3Name, phase3Firmware), want: security.ErrBadSignature,
		},
		{
			name:  "a key that is not an anchor",
			addr:  func(t *testing.T) string { return startPhase3Server(t, signerAt(phase3OtherSeed, 0)) },
			query: phase3Query(phase3Name, phase3Firmware), want: security.ErrUnknownKey,
		},
		{
			// The window is 1 hour, so it ended 1 hour ago.
			name:  "an expired envelope",
			addr:  func(t *testing.T) string { return startPhase3Server(t, signerAt(phase3AnchorSeed, -2*time.Hour)) },
			query: phase3Query(phase3Name, phase3Firmware), want: security.ErrExpired,
		},
		{
			name:  "an envelope issued 10 minutes in the future",
			addr:  func(t *testing.T) string { return startPhase3Server(t, signerAt(phase3AnchorSeed, 10*time.Minute)) },
			query: phase3Query(phase3Name, phase3Firmware), want: security.ErrNotYetValid,
		},
		{
			// The client asks for phase3OtherName, and the relay returns the envelope
			// that the server signed for phase3Name.
			name:  "an envelope copied to another QNAME",
			addr:  func(t *testing.T) string { return relay(t, signed, "_v1."+phase3Name, phase3Firmware, unchanged) },
			query: phase3Query(phase3OtherName, phase3Firmware), want: security.ErrBadSignature,
		},
		{
			// The client asks at phase3Configuration, and the relay returns the envelope
			// that the server signed for phase3Firmware.
			name:  "an envelope copied to another protocol key",
			addr:  func(t *testing.T) string { return relay(t, signed, "_v1."+phase3Name, phase3Firmware, unchanged) },
			query: phase3Query(phase3Name, phase3Configuration), want: security.ErrBadSignature,
		},
		{
			name:  "an unsigned envelope",
			addr:  func(t *testing.T) string { return startPhase3Server(t, nil) },
			query: phase3Query(phase3Name, phase3Firmware), want: security.ErrUnsigned,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			res, err := newPhase3Client(tt.addr(t)).Query(context.Background(), tt.query)
			wantVerifyError(t, res, err, tt.want)
		})
	}

	// The replay case needs two answers to one client, so it is not a table row.
	t.Run("an older envelope after a newer one", func(t *testing.T) {
		var offset atomic.Int64
		signer := &security.Signer{
			Key: ed25519.NewKeyFromSeed(phase3AnchorSeed),
			Now: func() time.Time { return time.Now().Add(time.Duration(offset.Load())) },
		}
		c := newPhase3Client(startPhase3Server(t, signer))
		if _, err := c.Query(context.Background(), phase3Query(phase3Name, phase3Firmware)); err != nil {
			t.Fatalf("Query of the newer envelope: %v", err)
		}

		// The older envelope is inside its validity window, so only the freshness store
		// rejects it.
		offset.Store(int64(-5 * time.Minute))
		res, err := c.Query(context.Background(), phase3Query(phase3Name, phase3Firmware))

		wantVerifyError(t, res, err, security.ErrReplay)
	})
}
