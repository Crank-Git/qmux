// Package baseline holds the baseline servers and the baseline lookups of the QMux
// benchmark (FR-bench-4 to FR-bench-11).
//
// Every baseline serves one signed envelope with the same firmware payload. Start runs
// the servers for all six baselines. Client does one lookup of a baseline and verifies
// the envelope signature before it returns the envelope bytes.
//
// The signing key and the TLS certificate come from fixed seeds. A bench-server and a
// client in another process then derive the same keys, with no key file to copy.
// These keys protect nothing: they only give each baseline the same verification work.
package baseline

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/Crank-Git/qmux/examples/embedded-bootstrap/schema"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/security"
)

// The names of the baselines, as the benchmark tables show them.
const (
	// QMuxPrivate is the private-class path: QCLASS 65282, QTYPE 65280 (FR-bench-4).
	QMuxPrivate = "qmux-private"
	// DNSIn is the IN-class path with the private type 65280 (FR-bench-5).
	DNSIn = "dns-in"
	// DNSInTXT is the reference row: IN-class with QTYPE 16 TXT (FR-bench-6).
	DNSInTXT = "dns-in-txt"
	// CoAP is a confirmable CoAP GET over UDP (FR-bench-7).
	CoAP = "coap"
	// HTTPSCold is an HTTPS GET on a new TCP connection and TLS session (FR-bench-8).
	HTTPSCold = "https-cold"
	// HTTPSReuse is an HTTPS GET on one kept connection (FR-bench-8).
	HTTPSReuse = "https-reuse"
)

// Names lists every baseline in the fixed order of a benchmark run. Two runs in the
// same order are comparable.
var Names = []string{QMuxPrivate, DNSIn, DNSInTXT, CoAP, HTTPSCold, HTTPSReuse}

// The names and protocol keys at which the baseline servers answer.
const (
	// Zone is the zone of the DNS baselines.
	Zone = "fleet.example."
	// PrivateName is the QNAME of qmux-private.
	PrivateName = "device42.fleet.example."
	// INName is the QNAME of dns-in and dns-in-txt. The "_fw" label names the protocol,
	// because the IN class cannot.
	INName = "_fw.device42.fleet.example."
	// Path is the CoAP and HTTPS path of the firmware metadata.
	Path = "/fw/device42"
	// ServerName is the TLS server name of the HTTPS baselines.
	ServerName = "bench-server"
)

// The protocol keys of the DNS baselines.
var (
	// PrivateKey is the protocol key of qmux-private.
	PrivateKey = protocol.ProtocolKey{Class: 65282, Type: 65280}
	// INKey is the protocol key of dns-in.
	INKey = protocol.ProtocolKey{Class: 1, Type: 65280}
	// TXTKey is the protocol key of dns-in-txt.
	TXTKey = protocol.ProtocolKey{Class: 1, Type: 16}
)

// The protocol IDs of the DNS baselines. The registry holds one protocol key for each
// protocol ID (ADR 0006), so each key has its own ID. The IDs have the same length, so
// the envelopes of the DNS baselines differ in no size.
const (
	// PrivateID is the protocol ID at PrivateKey. CoAP and HTTPS carry its envelope.
	PrivateID = "org.qmux.firmware.65282-65280"
	// INID is the protocol ID at INKey.
	INID = "org.qmux.firmware.00001-65280"
	// TXTID is the protocol ID at TXTKey.
	TXTID = "org.qmux.firmware.00001-00016"
)

// Assumption is one value of the baseline servers that the benchmark does not
// measure. The harness logs each one, so the results can list it.
type Assumption struct {
	// Name names the value.
	Name string
	// Value states the value.
	Value string
}

// Assumptions returns the values that the baseline servers fix and the benchmark does
// not measure.
func Assumptions() []Assumption {
	return []Assumption{
		{"envelope signing", "Ed25519, one signing time at server start, validity window 24 h"},
		{"protocol IDs", "one protocol ID per DNS protocol key (ADR 0006), all of the same length"},
		{"firmware payload", "schema.Firmware version 2.4.1, the same bytes in every baseline"},
		{"https server", "one self-signed Ed25519 certificate, TLS 1.3 only"},
		{"https client", "HTTP/1.1, no TLS session resumption"},
		{"coap server", "go-coap v3.8.2 UDP server defaults, confirmable GET only"},
		{"dns server", "pkg/server defaults, one UDP and TCP port for the three DNS baselines"},
	}
}

// ErrUnknownBaseline is the error of Client.Lookup for a name that is not in Names.
var ErrUnknownBaseline = errors.New("baseline: unknown baseline")

// signingValidity is the validity window of the envelopes. The server signs at one
// fixed time, so every lookup gets the same envelope bytes. The window must cover the
// longest benchmark, and the default security.Verifier MaxWindow is 24 h.
const signingValidity = 24 * time.Hour

// seed returns a 32-byte seed for ed25519.NewKeyFromSeed from the text s.
func seed(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

// signingKey returns the Ed25519 signing key of the envelopes.
func signingKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(seed("qmux bench envelope signing key"))
}

// TrustAnchor returns the trust anchor of the envelope signing key. It allows the
// protocol IDs of the three DNS baselines.
func TrustAnchor() security.TrustAnchor {
	pub := signingKey().Public().(ed25519.PublicKey)
	return security.TrustAnchor{
		KeyID:     security.KeyID(pub),
		PublicKey: pub,
		Protocols: []string{PrivateID, INID, TXTID},
	}
}

// Payload returns the firmware payload that every baseline carries (FR-bench-9). It is
// a schema.Firmware of protocol version 1. The error is the error of
// schema.Firmware.Marshal.
func Payload() ([]byte, error) {
	image := sha256.Sum256([]byte("qmux bench firmware image 2.4.1"))
	firmwareKey := ed25519.NewKeyFromSeed(seed("qmux bench firmware signing key")).Public().(ed25519.PublicKey)
	keyID := security.KeyID(firmwareKey)
	b, err := schema.Firmware{
		Version: "2.4.1",
		Digest:  image[:],
		URL:     "https://fw.fleet.example/device42/firmware-2.4.1.img",
		KeyID:   keyID[:],
	}.Marshal()
	if err != nil {
		return nil, fmt.Errorf("baseline: encode firmware payload: %w", err)
	}
	return b, nil
}

// TLSCertificate returns the self-signed Ed25519 certificate of the HTTPS baselines,
// for the name ServerName.
//
// Each call returns the same certificate bytes. The key comes from a fixed seed, the
// template is fixed and an Ed25519 signature has no random input.
func TLSCertificate() (tls.Certificate, error) {
	key := ed25519.NewKeyFromSeed(seed("qmux bench TLS key"))
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: ServerName},
		DNSNames:              []string{ServerName},
		NotBefore:             time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(2036, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("baseline: create TLS certificate: %w", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// TLSClientConfig returns the TLS 1.3 client configuration of the HTTPS baselines. It
// trusts only the certificate of TLSCertificate and expects the name ServerName. The
// error is the error of TLSCertificate.
func TLSClientConfig() (*tls.Config, error) {
	cert, err := TLSCertificate()
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("baseline: parse TLS certificate: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return &tls.Config{
		RootCAs:    pool,
		ServerName: ServerName,
		MinVersion: tls.VersionTLS13,
	}, nil
}
