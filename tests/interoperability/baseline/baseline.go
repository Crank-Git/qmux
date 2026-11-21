// Package baseline holds the baseline servers and the baseline lookups of the QMux
// benchmark (FR-bench-4 to FR-bench-11).
//
// Every baseline serves one signed envelope with the same firmware payload. Start runs
// the servers for all six baselines. Client does one lookup of a baseline and verifies
// the envelope signature before it returns the envelope bytes.
//
// The names, the protocol keys and the fixed keys come from package fixture. This
// package gives them under the same names, so a caller needs only one import.
package baseline

import (
	"crypto/tls"
	"errors"
	"time"

	"github.com/Crank-Git/qmux/pkg/security"
	"github.com/Crank-Git/qmux/tests/interoperability/baseline/fixture"
)

// The names of the baselines, as the benchmark tables show them.
const (
	// QMuxPrivate is the private-class path: QCLASS 65282, QTYPE 65280 (FR-bench-4).
	QMuxPrivate = fixture.QMuxPrivate
	// DNSIn is the IN-class path with the private type 65280 (FR-bench-5).
	DNSIn = fixture.DNSIn
	// DNSInTXT is the reference row: IN-class with QTYPE 16 TXT (FR-bench-6).
	DNSInTXT = fixture.DNSInTXT
	// CoAP is a confirmable CoAP GET over UDP (FR-bench-7).
	CoAP = fixture.CoAP
	// HTTPSCold is an HTTPS GET on a new TCP connection and TLS session (FR-bench-8).
	HTTPSCold = fixture.HTTPSCold
	// HTTPSReuse is an HTTPS GET on one kept connection (FR-bench-8).
	HTTPSReuse = fixture.HTTPSReuse
)

// Names lists every baseline in the fixed order of a benchmark run. Two runs in the
// same order are comparable.
var Names = fixture.Names

// The names and protocol keys at which the baseline servers answer.
const (
	// Zone is the zone of the DNS baselines.
	Zone = fixture.Zone
	// PrivateName is the QNAME of qmux-private.
	PrivateName = fixture.PrivateName
	// INName is the QNAME of dns-in and dns-in-txt.
	INName = fixture.INName
	// Path is the CoAP and HTTPS path of the firmware metadata.
	Path = fixture.Path
	// ServerName is the TLS server name of the HTTPS baselines.
	ServerName = fixture.ServerName
)

// The protocol keys of the DNS baselines.
var (
	// PrivateKey is the protocol key of qmux-private.
	PrivateKey = fixture.PrivateKey
	// INKey is the protocol key of dns-in.
	INKey = fixture.INKey
	// TXTKey is the protocol key of dns-in-txt.
	TXTKey = fixture.TXTKey
)

// The protocol IDs of the DNS baselines.
const (
	// PrivateID is the protocol ID at PrivateKey. CoAP and HTTPS carry its envelope.
	PrivateID = fixture.PrivateID
	// INID is the protocol ID at INKey.
	INID = fixture.INID
	// TXTID is the protocol ID at TXTKey.
	TXTID = fixture.TXTID
)

// TrustAnchor returns the trust anchor of the envelope signing key, the value of
// fixture.TrustAnchor.
func TrustAnchor() security.TrustAnchor {
	return fixture.TrustAnchor()
}

// Payload returns the firmware payload that every baseline carries (FR-bench-9), the
// value of fixture.Payload.
func Payload() ([]byte, error) {
	return fixture.Payload()
}

// TLSCertificate returns the self-signed certificate of the HTTPS baselines, the value
// of fixture.TLSCertificate.
func TLSCertificate() (tls.Certificate, error) {
	return fixture.TLSCertificate()
}

// TLSClientConfig returns the TLS 1.3 client configuration of the HTTPS baselines, the
// value of fixture.TLSClientConfig.
func TLSClientConfig() (*tls.Config, error) {
	return fixture.TLSClientConfig()
}

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
