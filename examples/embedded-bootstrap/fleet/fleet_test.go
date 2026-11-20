package fleet_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/examples/embedded-bootstrap/fleet"
	"github.com/Crank-Git/qmux/examples/embedded-bootstrap/schema"
	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/discovery"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/security"
)

// start starts the fleet side with the default flag values and returns it with its
// output. The cleanup stops it.
func start(t *testing.T, args ...string) (*fleet.Running, string) {
	t.Helper()
	var opts fleet.Options
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	opts.BindFlags(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var out bytes.Buffer
	opts.Out = &out
	r, err := fleet.Start(context.Background(), opts)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return r, out.String()
}

// device returns a discovery client that trusts only anchor, and the verified manifest
// of the zone at addr.
func device(t *testing.T, addr string, anchor security.TrustAnchor) (*discovery.Client, *discovery.Manifest) {
	t.Helper()
	c := &discovery.Client{Client: client.NewClient(client.ClientConfig{
		Resolver:     addr,
		TrustAnchors: &security.Verifier{Anchors: []security.TrustAnchor{anchor}},
	})}
	m, err := c.Discover(ctxT(t), fleet.Zone)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	return c, m
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// queryDevice42 sends the query for device42 with the protocol id at the key that the
// manifest gives.
func queryDevice42(t *testing.T, r *fleet.Running, id string) *client.Result {
	t.Helper()
	c, m := device(t, r.Addr, r.TrustAnchor)
	res, err := c.QueryProtocol(ctxT(t), m, id, fleet.DeviceName, []uint16{1})
	if err != nil {
		t.Fatalf("QueryProtocol %s: %v", id, err)
	}
	if !res.Verified {
		t.Fatalf("result of %s is not verified", id)
	}
	return res
}

func configuration(t *testing.T, r *fleet.Running) schema.Configuration {
	t.Helper()
	p, err := schema.UnmarshalConfiguration(queryDevice42(t, r, fleet.ConfigurationID).Payload)
	if err != nil {
		t.Fatalf("UnmarshalConfiguration: %v", err)
	}
	return p
}

func firmware(t *testing.T, r *fleet.Running) schema.Firmware {
	t.Helper()
	p, err := schema.UnmarshalFirmware(queryDevice42(t, r, fleet.FirmwareID).Payload)
	if err != nil {
		t.Fatalf("UnmarshalFirmware: %v", err)
	}
	return p
}

// get downloads url with the HTTPS client of r and returns the body.
func get(t *testing.T, r *fleet.Running, url string) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(ctxT(t), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := r.HTTPClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close() //nolint:errcheck // The test only reads the body.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200", url, resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return b
}

func firstLine(t *testing.T, out string) string {
	t.Helper()
	line, _, _ := strings.Cut(out, "\n")
	return line
}

// keyIDOf returns the key ID that the [server] line prints.
func keyIDOf(t *testing.T, out string) string {
	t.Helper()
	_, id, ok := strings.Cut(firstLine(t, out), "key id ")
	if !ok {
		t.Fatalf("first line %q has no key id", firstLine(t, out))
	}
	return id
}

func TestStartPrintsTheZoneAndALoopbackAddressOnTheFirstLine(t *testing.T) { // FR-demo-7, FR-demo-8
	r, out := start(t)
	line := firstLine(t, out)
	rest, ok := strings.CutPrefix(line, "[server]  zone fleet.example. on ")
	if !ok {
		t.Fatalf("first line = %q, want the [server] zone line", line)
	}
	addr, _, _ := strings.Cut(rest, " (udp, tcp), key id ")
	ap, err := netip.ParseAddrPort(addr)
	if err != nil || !ap.Addr().IsLoopback() || ap.Port() == 0 {
		t.Fatalf("address %q is not a loopback address with a port", addr)
	}
	if addr != r.Addr {
		t.Fatalf("printed address %q, Running.Addr %q", addr, r.Addr)
	}
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Scan()
	sc.Scan()
	if want := "[https]   mock service on " + r.HTTPSURL; sc.Text() != want {
		t.Fatalf("second line = %q, want %q", sc.Text(), want)
	}
}

func TestServerAnswersOverUDPAndTCPOnTheSamePort(t *testing.T) { // FR-demo-7
	r, _ := start(t)
	for _, network := range []string{"udp", "tcp"} {
		c := client.NewClient(client.ClientConfig{Resolver: r.Addr, Network: network})
		res, err := c.Query(ctxT(t), client.Query{Name: "_qmux." + fleet.Zone, Class: 65280, Type: 65534, Version: 1})
		if err != nil {
			t.Fatalf("%s query: %v", network, err)
		}
		if res.Transport != network {
			t.Fatalf("transport = %s, want %s", res.Transport, network)
		}
	}
}

func TestManifestListsTheFourProtocolsAtTheirProtocolKeys(t *testing.T) { // FR-demo-9, FR-demo-10
	r, _ := start(t)
	_, m := device(t, r.Addr, r.TrustAnchor)
	want := map[string]protocol.ProtocolKey{
		"org.qmux.discovery":     {Class: 65280, Type: 65534},
		"org.qmux.bootstrap":     {Class: 65280, Type: 65280},
		"org.qmux.configuration": {Class: 65281, Type: 65280},
		"org.qmux.firmware":      {Class: 65282, Type: 65280},
	}
	if len(m.Protocols) != len(want) {
		t.Fatalf("manifest lists %d protocols, want %d: %+v", len(m.Protocols), len(want), m.Protocols)
	}
	if m.Zone != "fleet.example." {
		t.Fatalf("manifest zone = %q, want fleet.example.", m.Zone)
	}
	for id, key := range want {
		e, ok := m.Lookup(id)
		if !ok {
			t.Fatalf("manifest has no entry for %s", id)
		}
		if got := (protocol.ProtocolKey{Class: e.Class, Type: e.Type}); got != key {
			t.Fatalf("%s at %v, want %v", id, got, key)
		}
		if len(e.Versions) != 1 || e.Versions[0] != 1 {
			t.Fatalf("%s versions = %v, want [1]", id, e.Versions)
		}
	}
}

func TestTwoStartsPrintTwoDifferentKeyIDs(t *testing.T) { // FR-demo-11
	a, outA := start(t)
	b, outB := start(t)
	idA, idB := keyIDOf(t, outA), keyIDOf(t, outB)
	if idA == idB {
		t.Fatalf("both starts print key id %s", idA)
	}
	if idA != hex.EncodeToString(a.KeyID[:]) || idB != hex.EncodeToString(b.KeyID[:]) {
		t.Fatalf("printed key ids %s, %s do not match Running.KeyID", idA, idB)
	}
}

func TestTrustAnchorIsThePublicKeyOfTheSigningKey(t *testing.T) { // FR-demo-11, FR-demo-12
	r, _ := start(t)
	if r.TrustAnchor.KeyID != r.KeyID || security.KeyID(r.TrustAnchor.PublicKey) != r.KeyID {
		t.Fatalf("trust anchor key id %x, signing key id %x", r.TrustAnchor.KeyID, r.KeyID)
	}
	for _, id := range []string{fleet.BootstrapID, fleet.ConfigurationID, fleet.FirmwareID} {
		queryDevice42(t, r, id)
	}
}

func TestEnvelopeThatADifferentKeySignsFailsVerification(t *testing.T) { // FR-demo-12
	a, _ := start(t)
	b, _ := start(t)
	c := client.NewClient(client.ClientConfig{
		Resolver:     b.Addr,
		TrustAnchors: &security.Verifier{Anchors: []security.TrustAnchor{a.TrustAnchor}},
	})
	_, err := c.Query(ctxT(t), client.Query{Name: fleet.DeviceName, Class: 65281, Type: 65280, Version: 1})
	var verr *client.VerifyError
	if !errors.As(err, &verr) {
		t.Fatalf("got %v, want a *client.VerifyError", err)
	}
}

func TestClientWithTheSystemCertificatePoolCannotConnectToTheMockService(t *testing.T) { // FR-demo-13, FR-demo-14
	r, _ := start(t)
	pool, err := x509.SystemCertPool()
	if err != nil {
		t.Fatalf("SystemCertPool: %v", err)
	}
	sys := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	req, err := http.NewRequestWithContext(ctxT(t), http.MethodGet, firmware(t, r).URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := sys.Do(req)
	if err == nil {
		resp.Body.Close() //nolint:errcheck // The request must not succeed at all.
		t.Fatal("a client with the system certificate pool connected to the mock service")
	}
	var unknown x509.UnknownAuthorityError
	if !errors.As(err, &unknown) {
		t.Fatalf("got %v, want x509.UnknownAuthorityError", err)
	}
	if n := r.Requests(); n != 0 {
		t.Fatalf("mock service counts %d requests, want 0", n)
	}
}

func TestMockServiceIsOnLoopback(t *testing.T) { // FR-demo-13
	r, _ := start(t)
	host, _, err := net.SplitHostPort(strings.TrimPrefix(r.HTTPSURL, "https://"))
	if err != nil {
		t.Fatalf("HTTPSURL %q: %v", r.HTTPSURL, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		t.Fatalf("mock service host %q is not loopback", host)
	}
}

func TestConfigurationResultHoldsTheURLAndDigestOfTheServedBundle(t *testing.T) { // FR-demo-15
	r, _ := start(t)
	p := configuration(t, r)
	if p.Generation != 7 {
		t.Fatalf("generation = %d, want 7", p.Generation)
	}
	if p.URL != r.HTTPSURL+"/config-7.bin" {
		t.Fatalf("URL = %q, want %s/config-7.bin", p.URL, r.HTTPSURL)
	}
	sum := sha256.Sum256(get(t, r, p.URL))
	if !bytes.Equal(p.Digest, sum[:]) {
		t.Fatalf("digest %x, served bundle hashes to %x", p.Digest, sum)
	}
}

func TestFirmwareResultHoldsTheURLDigestAndKeyIDOfTheServedImage(t *testing.T) { // FR-demo-15
	r, _ := start(t)
	p := firmware(t, r)
	if p.Version != "2.4.1" {
		t.Fatalf("version = %q, want 2.4.1", p.Version)
	}
	if p.URL != r.HTTPSURL+"/firmware-2.4.1.img" {
		t.Fatalf("URL = %q, want %s/firmware-2.4.1.img", p.URL, r.HTTPSURL)
	}
	if !bytes.Equal(p.KeyID, r.FirmwareKeyID[:]) {
		t.Fatalf("key id %x, want the mock firmware key id %x", p.KeyID, r.FirmwareKeyID)
	}
	sum := sha256.Sum256(get(t, r, p.URL))
	if !bytes.Equal(p.Digest, sum[:]) {
		t.Fatalf("digest %x, served image hashes to %x", p.Digest, sum)
	}
}

func TestMockFirmwareKeyIDIsTheSameAtEachStart(t *testing.T) { // behaviour rule: allowed firmware key IDs
	a, _ := start(t)
	b, _ := start(t)
	if a.FirmwareKeyID != b.FirmwareKeyID {
		t.Fatalf("firmware key ids %x and %x differ", a.FirmwareKeyID, b.FirmwareKeyID)
	}
}

func TestBootstrapResultHoldsTheManagementEndpoint(t *testing.T) { // FR-demo-10
	r, _ := start(t)
	p, err := schema.UnmarshalBootstrap(queryDevice42(t, r, fleet.BootstrapID).Payload)
	if err != nil {
		t.Fatalf("UnmarshalBootstrap: %v", err)
	}
	if p.ManagementURL != r.HTTPSURL+"/mgmt" || p.ManagementTransport != "https" {
		t.Fatalf("bootstrap = %+v, want %s/mgmt over https", p, r.HTTPSURL)
	}
}

func TestOtherNamesGetNoConfiguration(t *testing.T) { // FR-demo-15
	r, _ := start(t)
	c, m := device(t, r.Addr, r.TrustAnchor)
	_, err := c.QueryProtocol(ctxT(t), m, fleet.ConfigurationID, "device43.fleet.example.", []uint16{1})
	// The handler answers status not-found with no payload. The server calls the
	// schema only for status ok (FR-server-28), so the client gets the status.
	var appErr *client.AppError
	if !errors.As(err, &appErr) || appErr.Status != protocol.StatusNotFound {
		t.Fatalf("got %v, want an AppError with status not-found", err)
	}
}

func TestMockServiceCountsEachResourceRequest(t *testing.T) { // FR-demo-16
	r, _ := start(t)
	if n := r.Requests(); n != 0 {
		t.Fatalf("count before any request = %d, want 0", n)
	}
	get(t, r, configuration(t, r).URL)
	get(t, r, firmware(t, r).URL)
	if n := r.Requests(); n != 2 {
		t.Fatalf("count = %d, want 2", n)
	}
}

func TestRemoteGenerationFlagSetsThePublishedGeneration(t *testing.T) { // FR-demo-17
	r, _ := start(t, "--remote-generation", "9")
	p := configuration(t, r)
	if p.Generation != 9 || p.URL != r.HTTPSURL+"/config-9.bin" {
		t.Fatalf("configuration = generation %d at %q, want generation 9 at config-9.bin", p.Generation, p.URL)
	}
}

func TestRemoteFirmwareFlagSetsThePublishedVersion(t *testing.T) { // FR-demo-18
	r, _ := start(t, "--remote-firmware", "3.0.0")
	if v := firmware(t, r).Version; v != "3.0.0" {
		t.Fatalf("version = %q, want 3.0.0", v)
	}
}

func TestFlagDefaultsAreGeneration7AndFirmware241(t *testing.T) { // FR-demo-17, FR-demo-18
	var opts fleet.Options
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	opts.BindFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if opts.RemoteGeneration != 7 || opts.RemoteFirmware != "2.4.1" {
		t.Fatalf("defaults = %d, %q, want 7, 2.4.1", opts.RemoteGeneration, opts.RemoteFirmware)
	}
}

func TestStartRefusesAFirmwareVersionThatDoesNotParse(t *testing.T) { // FR-demo-18
	_, err := fleet.Start(context.Background(), fleet.Options{RemoteGeneration: 7, RemoteFirmware: "02.4.1"})
	if !errors.Is(err, schema.ErrFirmwareVersionSyntax) {
		t.Fatalf("got %v, want ErrFirmwareVersionSyntax", err)
	}
}
