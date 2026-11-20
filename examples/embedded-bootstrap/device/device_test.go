package device_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/examples/embedded-bootstrap/device"
	"github.com/Crank-Git/qmux/examples/embedded-bootstrap/schema"
	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/discovery"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/security"
	"github.com/Crank-Git/qmux/pkg/server"
)

// signingKey signs every envelope of the test server. A fixed seed keeps the key ID
// the same in each run.
var signingKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x51}, ed25519.SeedSize))

// firmwareKey is the firmware signing key. The test uses only its key ID, because
// the device checks the key ID and not a firmware signature.
var firmwareKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x52}, ed25519.SeedSize))

var (
	bootstrapKey     = protocol.ProtocolKey{Class: 65280, Type: 65280}
	configurationKey = protocol.ProtocolKey{Class: 65281, Type: 65280}
	firmwareProtoKey = protocol.ProtocolKey{Class: 65282, Type: 65280}
)

var (
	bundle = []byte("configuration bundle")
	image  = []byte("firmware image")
)

// options selects what the test server and the mock HTTPS service publish.
type options struct {
	generation uint64
	firmware   string
	// tamperHTTPS serves a configuration bundle whose bytes differ from the signed
	// digest.
	tamperHTTPS bool
	// status is the HTTP status of each resource response. 0 means 200.
	status int
	// noFirmware leaves the firmware protocol out of the server and the manifest.
	noFirmware bool
}

// fixture is a running test server and mock HTTPS service.
type fixture struct {
	addr  string
	https *httptest.Server

	mu       sync.Mutex
	names    []string
	requests map[string]int
}

// start starts an authoritative server for fleet.example. that signs with signingKey,
// and a mock HTTPS service. Both stop when the test ends.
func start(t *testing.T, o options) *fixture {
	t.Helper()
	f := &fixture{requests: map[string]int{}}
	f.https = httptest.NewTLSServer(http.HandlerFunc(f.serveHTTPS(o)))
	t.Cleanup(f.https.Close)

	srv := server.NewServer(server.ServerConfig{
		Zones:  []string{"fleet.example."},
		Logger: slog.New(slog.DiscardHandler),
		Signer: &security.Signer{Key: signingKey},
	})
	if err := discovery.Register(srv, "fleet.example.", discovery.Options{}); err != nil {
		t.Fatalf("discovery.Register: %v", err)
	}
	f.register(t, srv, bootstrapKey, "org.qmux.bootstrap", schema.Bootstrap{},
		schema.Bootstrap{ManagementURL: f.https.URL + "/mgmt", ManagementTransport: "https"})
	f.register(t, srv, configurationKey, "org.qmux.configuration", schema.Configuration{},
		schema.Configuration{Generation: o.generation, Digest: sum(bundle), URL: fmt.Sprintf("%s/config-%d.bin", f.https.URL, o.generation)})
	if !o.noFirmware {
		kid := security.KeyID(firmwareKey.Public().(ed25519.PublicKey))
		f.register(t, srv, firmwareProtoKey, "org.qmux.firmware", schema.Firmware{},
			schema.Firmware{Version: o.firmware, Digest: sum(image), URL: fmt.Sprintf("%s/firmware-%s.img", f.https.URL, o.firmware), KeyID: kid[:]})
	}

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ctx, pc, l) }()
	t.Cleanup(func() {
		cancel()
		if err := <-errc; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	f.addr = pc.LocalAddr().String()
	return f
}

// payload is a schema type that encodes itself.
type payload interface{ Marshal() ([]byte, error) }

// register registers one demonstration protocol whose handler records the resource
// name and returns p.
func (f *fixture) register(t *testing.T, srv *server.Server, key protocol.ProtocolKey, id string, s protocol.Schema, p payload) {
	t.Helper()
	b, err := p.Marshal()
	if err != nil {
		t.Fatalf("Marshal %s: %v", id, err)
	}
	h := protocol.HandlerFunc(func(_ context.Context, req *protocol.Request) (*protocol.Response, error) {
		f.mu.Lock()
		f.names = append(f.names, req.Name)
		f.mu.Unlock()
		return &protocol.Response{Payload: b}, nil
	})
	if err := srv.RegisterProtocol(key, protocol.Info{ID: id, Versions: []uint16{1}, Schema: s}, h); err != nil {
		t.Fatalf("RegisterProtocol %s: %v", id, err)
	}
}

// serveHTTPS returns the handler of the mock HTTPS service. It counts each request by
// the kind of resource.
func (f *fixture) serveHTTPS(o options) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		switch {
		case strings.HasPrefix(r.URL.Path, "/config-"):
			body = bundle
			if o.tamperHTTPS {
				body = append([]byte{body[0] ^ 0xff}, body[1:]...)
			}
		case strings.HasPrefix(r.URL.Path, "/firmware-"):
			body = image
		default:
			http.NotFound(w, r)
			return
		}
		f.mu.Lock()
		f.requests[strings.SplitN(r.URL.Path[1:], "-", 2)[0]]++
		f.mu.Unlock()
		if o.status != 0 {
			w.WriteHeader(o.status)
			return
		}
		_, _ = w.Write(body)
	}
}

// queried returns the resource names of the protocol queries that the server answered.
func (f *fixture) queried() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.names)
}

// count returns the number of HTTPS requests for kind ("config" or "firmware"), or
// for every kind when kind is empty.
func (f *fixture) count(kind string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if kind != "" {
		return f.requests[kind]
	}
	n := 0
	for _, c := range f.requests {
		n += c
	}
	return n
}

// testDevice holds the paths and the log of one device run.
type testDevice struct {
	root  string
	state string
	data  string
	log   []string
	cfg   device.Config
}

// newDevice returns a device whose state file and data directory are under a new
// temporary directory. The device trusts signingKey for every protocol, and allows
// the key ID of firmwareKey.
func newDevice(t *testing.T, f *fixture) *testDevice {
	t.Helper()
	root := t.TempDir()
	d := &testDevice{root: root, state: filepath.Join(root, "device-state.json"), data: filepath.Join(root, "device-data")}
	pub := signingKey.Public().(ed25519.PublicKey)
	d.cfg = device.Config{
		Resolver:              f.addr,
		TrustAnchors:          &security.Verifier{Anchors: []security.TrustAnchor{{KeyID: security.KeyID(pub), PublicKey: pub}}},
		HTTPClient:            f.https.Client(),
		StatePath:             d.state,
		DataDir:               d.data,
		AllowedFirmwareKeyIDs: [][8]byte{security.KeyID(firmwareKey.Public().(ed25519.PublicKey))},
		Log:                   func(line string) { d.log = append(d.log, line) },
	}
	return d
}

// writeState writes a device state file with the given content.
func (d *testDevice) writeState(t *testing.T, content string) {
	t.Helper()
	if err := os.WriteFile(d.state, []byte(content), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
}

func (d *testDevice) reconcile(t *testing.T) (device.Report, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return device.Reconcile(ctx, d.cfg)
}

// logged returns true when a log line holds every part.
func (d *testDevice) logged(parts ...string) bool {
	return slices.ContainsFunc(d.log, func(line string) bool {
		for _, p := range parts {
			if !strings.Contains(line, p) {
				return false
			}
		}
		return true
	})
}

func (d *testDevice) readState(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(d.state)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	return string(b)
}

func sum(b []byte) []byte {
	s := sha256.Sum256(b)
	return s[:]
}

func stale() options { return options{generation: 7, firmware: "2.4.1"} }

func TestReconcileWithNoStateFileStartsAtGenerationZeroAndFirmwareZero(t *testing.T) { // FR-demo-19, FR-demo-20
	d := newDevice(t, start(t, stale()))
	if _, err := d.reconcile(t); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !d.logged("state: config_generation=0 firmware_version=0.0.0") {
		t.Fatalf("log = %q, want the zero state line", d.log)
	}
}

func TestReconcileReadsTheStateFromTheStatePath(t *testing.T) { // FR-demo-19
	d := newDevice(t, start(t, stale()))
	d.writeState(t, `{"config_generation":3,"firmware_version":"2.3.0"}`)
	if _, err := d.reconcile(t); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !d.logged("state: config_generation=3 firmware_version=2.3.0") {
		t.Fatalf("log = %q, want the state line of the file", d.log)
	}
}

func TestReconcileNegotiatesVersionOneForEachOfTheThreeProtocols(t *testing.T) { // FR-demo-22
	d := newDevice(t, start(t, stale()))
	if _, err := d.reconcile(t); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !d.logged("negotiate: org.qmux.bootstrap v1, org.qmux.configuration v1, org.qmux.firmware v1") {
		t.Fatalf("log = %q, want v1 for each protocol", d.log)
	}
}

func TestReconcileSendsEachProtocolQueryForDevice42(t *testing.T) { // FR-demo-23
	f := start(t, stale())
	d := newDevice(t, f)
	if _, err := d.reconcile(t); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	names := f.queried()
	if len(names) != 3 {
		t.Fatalf("names = %q, want 3 protocol queries", names)
	}
	for _, n := range names {
		if n != "device42.fleet.example." {
			t.Fatalf("names = %q, want device42.fleet.example. for each query", names)
		}
	}
}

func TestReconcileRecordsTheManagementEndpointAndTransport(t *testing.T) { // FR-demo-24
	f := start(t, stale())
	d := newDevice(t, f)
	r, err := d.reconcile(t)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	want := f.https.URL + "/mgmt"
	if r.ManagementURL != want || r.ManagementTransport != "https" {
		t.Fatalf("report = %+v, want management %s over https", r, want)
	}
	if !d.logged("bootstrap: management "+want, "(transport https)") {
		t.Fatalf("log = %q, want the management endpoint line", d.log)
	}
}

func TestReconcileOfAStaleDeviceStoresBothResourcesAndWritesTheNewState(t *testing.T) { // FR-demo-25, FR-demo-28, FR-demo-31, FR-demo-33, FR-demo-34, FR-demo-36
	f := start(t, stale())
	d := newDevice(t, f)
	d.writeState(t, `{"config_generation":3,"firmware_version":"2.3.0"}`)
	r, err := d.reconcile(t)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for name, want := range map[string][]byte{"config-7.bin": bundle, "firmware-2.4.1.img": image} {
		got, err := os.ReadFile(filepath.Join(d.data, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s = %q, %v, want %q", name, got, err, want)
		}
	}
	if f.count("") != 2 || r.Downloads != 2 {
		t.Fatalf("HTTPS requests = %d, downloads = %d, want 2 and 2", f.count(""), r.Downloads)
	}
	want := device.State{ConfigGeneration: 7, FirmwareVersion: "2.4.1"}
	if r.Before != (device.State{ConfigGeneration: 3, FirmwareVersion: "2.3.0"}) || r.After != want || !r.StateWritten {
		t.Fatalf("report = %+v, want 3/2.3.0 to 7/2.4.1 with the state written", r)
	}
	if got := d.readState(t); !strings.Contains(got, `"config_generation":7`) || !strings.Contains(got, `"firmware_version":"2.4.1"`) {
		t.Fatalf("state = %s, want generation 7 and firmware 2.4.1", got)
	}
	for _, line := range []string{
		"configuration: remote generation 7 > local 3, downloading",
		"configuration: digest ok, wrote config-7.bin",
		"firmware: remote 2.4.1 > local 2.3.0, key id ",
		"firmware: digest ok, stored firmware-2.4.1.img (not flashed)",
		"state written: config_generation=7 firmware_version=2.4.1",
	} {
		if !d.logged(line) {
			t.Fatalf("log = %q, want a line with %q", d.log, line)
		}
	}
}

func TestReconcileWritesOnlyTheStateFileAndTheDataDirectoryAndLeavesNoTemporaryFile(t *testing.T) { // FR-demo-35, FR-demo-36
	d := newDevice(t, start(t, stale()))
	if _, err := d.reconcile(t); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var got []string
	err := filepath.WalkDir(d.root, func(path string, _ os.DirEntry, err error) error {
		rel, _ := filepath.Rel(d.root, path)
		got = append(got, rel)
		return err
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	want := []string{".", "device-data", "device-data/config-7.bin", "device-data/firmware-2.4.1.img", "device-state.json"}
	if !slices.Equal(got, want) {
		t.Fatalf("files = %q, want %q", got, want)
	}
}

func TestReconcileCreatesTheStateFileWithMode0600AndTheDataDirectoryWithMode0700(t *testing.T) {
	d := newDevice(t, start(t, stale()))
	if _, err := d.reconcile(t); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for path, want := range map[string]os.FileMode{d.state: 0o600, d.data: 0o700 | os.ModeDir} {
		fi, err := os.Stat(path)
		if err != nil || fi.Mode() != want {
			t.Fatalf("mode of %s = %v, %v, want %v", path, fi.Mode(), err, want)
		}
	}
}

func TestReconcileRefusesAConfigurationRollbackAndKeepsTheLocalGeneration(t *testing.T) { // FR-demo-26
	f := start(t, stale())
	d := newDevice(t, f)
	d.writeState(t, `{"config_generation":9,"firmware_version":"2.4.1"}`)
	r, err := d.reconcile(t)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !d.logged("configuration: remote generation 7 < local 9, rollback refused") {
		t.Fatalf("log = %q, want rollback refused", d.log)
	}
	if r.After.ConfigGeneration != 9 || f.count("config") != 0 {
		t.Fatalf("generation = %d, config requests = %d, want 9 and 0", r.After.ConfigGeneration, f.count("config"))
	}
	if got := d.readState(t); !strings.Contains(got, `"config_generation":9`) {
		t.Fatalf("state = %s, want generation 9", got)
	}
}

func TestReconcileOfACurrentDeviceSendsNoHTTPSRequest(t *testing.T) { // FR-demo-27, FR-demo-30
	f := start(t, stale())
	d := newDevice(t, f)
	d.writeState(t, `{"config_generation":7,"firmware_version":"2.4.1"}`)
	r, err := d.reconcile(t)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if f.count("") != 0 || r.Downloads != 0 {
		t.Fatalf("HTTPS requests = %d, downloads = %d, want 0 and 0", f.count(""), r.Downloads)
	}
	if !d.logged("configuration: ", "up to date") || !d.logged("firmware: ", "up to date") {
		t.Fatalf("log = %q, want up to date for configuration and firmware", d.log)
	}
}

func TestReconcileSendsNoFirmwareRequestForAnOlderRemoteFirmware(t *testing.T) { // FR-demo-30
	f := start(t, options{generation: 7, firmware: "2.3.9"})
	d := newDevice(t, f)
	d.writeState(t, `{"config_generation":7,"firmware_version":"2.4.0"}`)
	r, err := d.reconcile(t)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if f.count("firmware") != 0 || r.After.FirmwareVersion != "2.4.0" {
		t.Fatalf("firmware requests = %d, firmware = %s, want 0 and 2.4.0", f.count("firmware"), r.After.FirmwareVersion)
	}
}

func TestReconcileSkipsTheFirmwareWhenTheKeyIDIsNotAllowed(t *testing.T) { // FR-demo-29
	f := start(t, stale())
	d := newDevice(t, f)
	d.cfg.AllowedFirmwareKeyIDs = nil
	r, err := d.reconcile(t)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !d.logged("firmware: ", "not allowed, skipped") {
		t.Fatalf("log = %q, want the skip reason", d.log)
	}
	if f.count("firmware") != 0 || r.After.FirmwareVersion != "0.0.0" {
		t.Fatalf("firmware requests = %d, firmware = %s, want 0 and 0.0.0", f.count("firmware"), r.After.FirmwareVersion)
	}
}

func TestReconcileDiscardsATamperedBundleAndLeavesTheStateUnchanged(t *testing.T) { // FR-demo-31, FR-demo-32, FR-demo-34, FR-demo-43
	f := start(t, options{generation: 7, firmware: "2.4.1", tamperHTTPS: true})
	d := newDevice(t, f)
	before := `{"config_generation":3,"firmware_version":"2.3.0"}`
	d.writeState(t, before)
	_, err := d.reconcile(t)
	if !errors.Is(err, device.ErrDigestMismatch) {
		t.Fatalf("Reconcile error = %v, want ErrDigestMismatch", err)
	}
	if got := err.Error(); got != "configuration: digest mismatch, resource discarded" {
		t.Fatalf("error text = %q, want the digest mismatch line", got)
	}
	if got := d.readState(t); got != before {
		t.Fatalf("state = %s, want the bytes before the run", got)
	}
	if _, err := os.Stat(filepath.Join(d.data, "config-7.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat config-7.bin = %v, want no file", err)
	}
}

func TestReconcileStopsOnAnUnverifiedConfigurationEnvelopeWithNoHTTPSRequest(t *testing.T) { // FR-demo-21, FR-demo-34
	f := start(t, stale())
	d := newDevice(t, f)
	// The anchor does not allow the configuration protocol, so its envelope fails
	// verification while discovery and bootstrap verify.
	d.cfg.TrustAnchors.Anchors[0].Protocols = []string{discovery.ProtocolID, "org.qmux.bootstrap", "org.qmux.firmware"}
	before := `{"config_generation":3,"firmware_version":"2.3.0"}`
	d.writeState(t, before)
	_, err := d.reconcile(t)
	var ve *client.VerifyError
	if !errors.As(err, &ve) {
		t.Fatalf("Reconcile error = %v, want a *client.VerifyError", err)
	}
	if got := err.Error(); !strings.HasPrefix(got, "configuration: verification failed: ") {
		t.Fatalf("error text = %q, want the verification failure line", got)
	}
	if f.count("") != 0 || d.readState(t) != before {
		t.Fatalf("HTTPS requests = %d, state = %s, want 0 and the bytes before the run", f.count(""), d.readState(t))
	}
}

func TestReconcileStopsWhenTheManifestDoesNotVerify(t *testing.T) { // FR-demo-21
	f := start(t, stale())
	d := newDevice(t, f)
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x53}, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	d.cfg.TrustAnchors = &security.Verifier{Anchors: []security.TrustAnchor{{KeyID: security.KeyID(other), PublicKey: other}}}
	_, err := d.reconcile(t)
	var ve *client.VerifyError
	if !errors.As(err, &ve) || !strings.HasPrefix(err.Error(), "discovery: verification failed: ") {
		t.Fatalf("Reconcile error = %v, want a discovery verification failure", err)
	}
	if len(f.queried()) != 0 || f.count("") != 0 {
		t.Fatalf("protocol queries = %d, HTTPS requests = %d, want 0 and 0", len(f.queried()), f.count(""))
	}
}

func TestReconcileRefusesToRunWithoutTrustAnchors(t *testing.T) { // FR-demo-21
	f := start(t, stale())
	d := newDevice(t, f)
	d.cfg.TrustAnchors = nil
	if _, err := d.reconcile(t); !errors.Is(err, discovery.ErrInsecure) {
		t.Fatalf("Reconcile error = %v, want ErrInsecure", err)
	}
	if len(f.queried()) != 0 {
		t.Fatalf("protocol queries = %d, want 0", len(f.queried()))
	}
}

func TestReconcileStopsWithProtocolNotFoundWhenTheManifestLacksAProtocol(t *testing.T) {
	f := start(t, options{generation: 7, firmware: "2.4.1", noFirmware: true})
	d := newDevice(t, f)
	_, err := d.reconcile(t)
	if !errors.Is(err, discovery.ErrProtocolNotFound) || !strings.Contains(err.Error(), "protocol not found") {
		t.Fatalf("Reconcile error = %v, want protocol not found", err)
	}
	if len(f.queried()) != 0 {
		t.Fatalf("protocol queries = %d, want 0", len(f.queried()))
	}
}

func TestReconcileStopsOnAStateFileWithInvalidJSONAndKeepsTheFile(t *testing.T) {
	f := start(t, stale())
	d := newDevice(t, f)
	d.writeState(t, `{not json`)
	if _, err := d.reconcile(t); !errors.Is(err, device.ErrInvalidState) {
		t.Fatalf("Reconcile error = %v, want ErrInvalidState", err)
	}
	if got := d.readState(t); got != `{not json` || len(f.queried()) != 0 {
		t.Fatalf("state = %s, protocol queries = %d, want the file unchanged and 0 queries", got, len(f.queried()))
	}
}

func TestReconcileStopsOnAnHTTPSStatusOtherThan200(t *testing.T) {
	f := start(t, options{generation: 7, firmware: "2.4.1", status: http.StatusInternalServerError})
	d := newDevice(t, f)
	_, err := d.reconcile(t)
	if !errors.Is(err, device.ErrHTTPStatus) {
		t.Fatalf("Reconcile error = %v, want ErrHTTPStatus", err)
	}
	if _, err := os.Stat(d.state); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat state = %v, want no state file", err)
	}
}

func TestReconcileStopsBeforeTheStateChangesWhenTheDataDirectoryIsNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to a read-only directory")
	}
	f := start(t, stale())
	d := newDevice(t, f)
	before := `{"config_generation":3,"firmware_version":"2.3.0"}`
	d.writeState(t, before)
	if err := os.Mkdir(d.data, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := d.reconcile(t); err == nil {
		t.Fatal("Reconcile error = nil, want an error")
	}
	if got := d.readState(t); got != before {
		t.Fatalf("state = %s, want the bytes before the run", got)
	}
}
