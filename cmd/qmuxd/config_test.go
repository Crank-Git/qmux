package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/registry"
	"github.com/Crank-Git/qmux/pkg/security"
	"github.com/Crank-Git/qmux/pkg/server"
)

// testRecord is the QNAME of the record in the test configuration file.
const testRecord = "device42.fleet.example."

// baseConfig returns a valid configuration file as a JSON object, with one protocol
// that has one record. A test changes it before it writes the file.
func baseConfig(listen string) map[string]any {
	return map[string]any{
		"listen":      listen,
		"zones":       []any{testZone},
		"signing_key": "qmuxd.key",
		"protocols": []any{map[string]any{
			"id":       "org.qmux.bootstrap",
			"versions": []any{1},
			"class":    65280,
			"type":     65280,
			"records": map[string]any{
				testRecord: map[string]any{"payload": map[string]any{"1": "https://mgmt.fleet.example/"}},
			},
		}},
	}
}

// firstProtocol returns the first entry of the protocols field of cfg.
func firstProtocol(cfg map[string]any) map[string]any {
	return cfg["protocols"].([]any)[0].(map[string]any)
}

// firstRecord returns the record of testRecord in the first protocol of cfg.
func firstRecord(cfg map[string]any) map[string]any {
	return firstProtocol(cfg)["records"].(map[string]any)[testRecord].(map[string]any)
}

// writeConfigFile writes the test signing key with mode 0600 and the configuration
// file qmuxd.json with the content cfg into a new directory. It returns the path of
// the configuration file.
func writeConfigFile(t *testing.T, cfg map[string]any) string {
	t.Helper()
	dir := t.TempDir()
	if err := security.WritePrivateKey(filepath.Join(dir, "qmuxd.key"), ed25519.NewKeyFromSeed(testSeed)); err != nil {
		t.Fatalf("WritePrivateKey: %v", err)
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	path := filepath.Join(dir, "qmuxd.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// heldAddress returns a loopback UDP address that the test holds until it ends. A
// qmuxd that tries to listen there exits with code 1, so an exit code of 2 shows that
// qmuxd stopped before it listened.
func heldAddress(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	return pc.LocalAddr().String()
}

func TestServeExitsTwoBeforeItListensForAnInvalidConfigFile(t *testing.T) {
	cases := []struct {
		name string
		edit func(cfg map[string]any)
		// want is a part of the error message, usually the JSON path of the error.
		want string
	}{
		{"FR-qmuxd-8 unknown field listn", func(c map[string]any) { c["listn"] = c["listen"] }, `unknown field "listn"`},
		{"unknown field in rate_limit", func(c map[string]any) { c["rate_limit"] = map[string]any{"qps": 1, "brust": 2} }, `rate_limit: json: unknown field "brust"`},
		{"unknown field in a protocol", func(c map[string]any) { firstProtocol(c)["klass"] = 1 }, `protocols[0]: json: unknown field "klass"`},
		{"unknown field in a record", func(c map[string]any) { firstRecord(c)["ttl"] = 1 }, `protocols[0].records["device42.fleet.example."]: json: unknown field "ttl"`},
		{"FR-qmuxd-10 null payload", func(c map[string]any) { firstRecord(c)["payload"] = nil }, `protocols[0].records["device42.fleet.example."].payload`},
		{"class that does not fit 16 bits", func(c map[string]any) { firstProtocol(c)["class"] = 70000 }, "protocols[0].class"},
		{"negative version", func(c map[string]any) { firstProtocol(c)["versions"] = []any{1, -2} }, "protocols[0].versions[1]"},
		{"FR-qmuxd-11 no signing_key", func(c map[string]any) { delete(c, "signing_key") }, "signing_key"},
		{"signing_key that does not load", func(c map[string]any) { c["signing_key"] = "missing.key" }, "signing_key"},
		{"FR-qmuxd-13 class 300", func(c map[string]any) { firstProtocol(c)["class"] = 300 }, "protocols[0]: "},
		{"FR-qmuxd-13 two protocols at one protocol key", func(c map[string]any) {
			c["protocols"] = append(c["protocols"].([]any), map[string]any{
				"id": "org.qmux.firmware", "versions": []any{1}, "class": 65280, "type": 65280,
			})
		}, "protocols[1]: "},
		{"FR-qmuxd-14 record outside every zone", func(c map[string]any) {
			firstProtocol(c)["records"] = map[string]any{"x.other.example.": map[string]any{"payload": 1}}
		}, `protocols[0].records["x.other.example."]`},
		{"record QNAME that is not fully qualified", func(c map[string]any) {
			firstProtocol(c)["records"] = map[string]any{"device42.fleet.example": map[string]any{"payload": 1}}
		}, `protocols[0].records["device42.fleet.example"]`},
		{"two record QNAMEs that differ only in case", func(c map[string]any) {
			firstProtocol(c)["records"].(map[string]any)["DEVICE42.fleet.example."] = map[string]any{"payload": 1}
		}, `protocols[0].records["device42.fleet.example."]`},
		{"no zones", func(c map[string]any) { delete(c, "zones") }, "zones"},
		{"zone that is not fully qualified", func(c map[string]any) { c["zones"] = []any{"fleet.example"} }, "zones[0]"},
		{"two zones that differ only in case", func(c map[string]any) { c["zones"] = []any{testZone, "FLEET.example."} }, "zones[1]"},
		{"listen without a port", func(c map[string]any) { c["listen"] = "127.0.0.1" }, "listen"},
		{"signature_validity below 1m", func(c map[string]any) { c["signature_validity"] = "59s" }, "signature_validity"},
		{"signature_validity above 24h", func(c map[string]any) { c["signature_validity"] = "24h1s" }, "signature_validity"},
		{"signature_validity that does not parse", func(c map[string]any) { c["signature_validity"] = "one hour" }, "signature_validity"},
		{"ttl above 86400", func(c map[string]any) { c["ttl"] = 86401 }, "ttl"},
		{"udp_size below 512", func(c map[string]any) { c["udp_size"] = 511 }, "udp_size"},
		{"udp_size above 4096", func(c map[string]any) { c["udp_size"] = 4097 }, "udp_size"},
		{"handler_timeout below 10ms", func(c map[string]any) { c["handler_timeout"] = "9ms" }, "handler_timeout"},
		{"handler_timeout above 30s", func(c map[string]any) { c["handler_timeout"] = "31s" }, "handler_timeout"},
		{"rate_limit burst below qps", func(c map[string]any) { c["rate_limit"] = map[string]any{"qps": 10, "burst": 9} }, "rate_limit.burst"},
		{"rate_limit default burst below qps", func(c map[string]any) { c["rate_limit"] = map[string]any{"qps": 101} }, "rate_limit.burst"},
		{"negative rate_limit qps", func(c map[string]any) { c["rate_limit"] = map[string]any{"qps": -1} }, "rate_limit.qps"},
		{"empty observation_log", func(c map[string]any) { c["observation_log"] = "" }, "observation_log"},
		{"unknown log_level", func(c map[string]any) { c["log_level"] = "trace" }, "log_level"},
		{"unknown log_format", func(c map[string]any) { c["log_format"] = "xml" }, "log_format"},
		{"no protocols", func(c map[string]any) { c["protocols"] = []any{} }, "protocols"},
		{"versions not sorted", func(c map[string]any) { firstProtocol(c)["versions"] = []any{2, 1} }, "protocols[0].versions"},
		{"repeated version", func(c map[string]any) { firstProtocol(c)["versions"] = []any{1, 1} }, "protocols[0].versions"},
		{"version 0", func(c map[string]any) { firstProtocol(c)["versions"] = []any{0, 1} }, "protocols[0].versions"},
		{"record version not in the versions", func(c map[string]any) { firstRecord(c)["version"] = 2 }, `protocols[0].records["device42.fleet.example."].version`},
		{"unknown status name", func(c map[string]any) { firstRecord(c)["status"] = "gone" }, `protocols[0].records["device42.fleet.example."].status`},
		{"status integer below 256", func(c map[string]any) { firstRecord(c)["status"] = 4 }, `protocols[0].records["device42.fleet.example."].status`},
		{"status integer above 65535", func(c map[string]any) { firstRecord(c)["status"] = 65536 }, `protocols[0].records["device42.fleet.example."].status`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseConfig(heldAddress(t))
			tc.edit(cfg)
			var stdout, stderr bytes.Buffer
			if code := run([]string{"qmuxd", "serve", "--config", writeConfigFile(t, cfg)}, &stdout, &stderr); code != 2 {
				t.Errorf("exit code = %d, want 2; stderr = %q", code, stderr.String())
			}
			if !strings.HasPrefix(stderr.String(), "qmuxd: config: ") || !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("stderr = %q, want a config error that contains %q", stderr.String(), tc.want)
			}
		})
	}
}

func TestServeExitsTwoForTrailingDataAfterTheConfigObject(t *testing.T) {
	path := writeConfigFile(t, baseConfig(heldAddress(t)))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if _, err := f.WriteString(`{"listen": "127.0.0.1:0"}`); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"qmuxd", "serve", "--config", path}, &stdout, &stderr); code != 2 {
		t.Errorf("exit code = %d, want 2; stderr = %q", code, stderr.String())
	}
}

func TestLoadConfigAppliesTheDefaultsOfTheSchemaTable(t *testing.T) { // FR-qmuxd-9
	path := writeConfigFile(t, baseConfig("127.0.0.1:0"))
	cfg, err := loadConfig(path, "")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.SignatureValidity != time.Hour || cfg.TTL != 60 || cfg.UDPSize != 1232 || cfg.HandlerTimeout != 2*time.Second {
		t.Errorf("signature_validity, ttl, udp_size, handler_timeout = %v, %d, %d, %v; want 1h, 60, 1232, 2s",
			cfg.SignatureValidity, cfg.TTL, cfg.UDPSize, cfg.HandlerTimeout)
	}
	if cfg.RateLimit != (server.RateLimit{QPS: 50, Burst: 100}) {
		t.Errorf("rate_limit = %+v, want QPS 50 and Burst 100", cfg.RateLimit)
	}
	if cfg.Registry != (registry.Options{}) {
		t.Errorf("registry options = %+v, want the zero value", cfg.Registry)
	}
	if !cfg.Discovery.Enabled || cfg.Discovery.InClass {
		t.Errorf("discovery = %+v, want enabled and not in_class", cfg.Discovery)
	}
	if cfg.ObservationLog != "" || cfg.LogLevel != slog.LevelInfo || cfg.LogFormat != "text" {
		t.Errorf("observation_log, log_level, log_format = %q, %v, %q; want \"\", INFO, text",
			cfg.ObservationLog, cfg.LogLevel, cfg.LogFormat)
	}
	if got := cfg.Protocols[0].Records[testRecord].Status; got != protocol.StatusOK {
		t.Errorf("record status = %d, want ok", got)
	}
}

func TestLoadConfigDecodesEveryFieldOfTheSchemaTable(t *testing.T) { // FR-qmuxd-9
	c := baseConfig("127.0.0.1:0")
	c["zones"] = []any{"Fleet.Example.", "lab.example."}
	c["signature_validity"] = "10m"
	c["ttl"] = 0
	c["udp_size"] = 4096
	c["handler_timeout"] = "500ms"
	c["rate_limit"] = map[string]any{"qps": 0, "burst": 0}
	c["allow_reserved_classes"] = true
	c["allow_non_private_types"] = true
	c["discovery"] = map[string]any{"enabled": false, "in_class": true}
	c["observation_log"] = "logs/observations.jsonl"
	c["log_level"] = "debug"
	c["log_format"] = "json"
	p := firstProtocol(c)
	p["versions"] = []any{1, 2}
	p["records"] = map[string]any{
		"Device42.FLEET.example.": map[string]any{"payload_hex": "0a0b", "status": "version-unsupported", "version": 2},
		"x.lab.example.":          map[string]any{"payload": true, "status": 300},
	}
	path := writeConfigFile(t, c)
	cfg, err := loadConfig(path, "")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	dir := filepath.Dir(path)
	payloadHex := "0a0b"
	want := config{
		Listen:            "127.0.0.1:0",
		Zones:             []string{"fleet.example.", "lab.example."},
		SigningKey:        filepath.Join(dir, "qmuxd.key"),
		SignatureValidity: 10 * time.Minute,
		TTL:               0,
		UDPSize:           4096,
		HandlerTimeout:    500 * time.Millisecond,
		RateLimit:         server.RateLimit{},
		Registry:          registry.Options{AllowReservedClasses: true, AllowNonPrivateTypes: true},
		Discovery:         discoveryConfig{Enabled: false, InClass: true},
		ObservationLog:    filepath.Join(dir, "logs", "observations.jsonl"),
		LogLevel:          slog.LevelDebug,
		LogFormat:         "json",
		Protocols: []protocolConfig{{
			Key:  protocol.ProtocolKey{Class: 65280, Type: 65280},
			Info: protocol.Info{ID: "org.qmux.bootstrap", Versions: []uint16{1, 2}},
			Records: map[string]record{
				"device42.fleet.example.": {PayloadHex: &payloadHex, Status: protocol.StatusVersionUnsupported, Version: 2},
				"x.lab.example.":          {Payload: json.RawMessage("true"), Status: 300},
			},
		}},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("loadConfig =\n%+v\nwant\n%+v", cfg, want)
	}
}

func TestLoadConfigListenArgumentReplacesTheListenField(t *testing.T) { // FR-qmuxd-2
	cfg, err := loadConfig(writeConfigFile(t, baseConfig("127.0.0.1:1")), "127.0.0.1:2")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Listen != "127.0.0.1:2" {
		t.Errorf("listen = %q, want 127.0.0.1:2", cfg.Listen)
	}
}

func TestExampleConfigDecodesWithEveryFieldOfTheSchemaTableThatItUses(t *testing.T) { // FR-qmuxd-9
	path := filepath.Join("..", "..", "examples", "qmuxd.json")
	cfg, err := loadConfig(path, "")
	if err != nil {
		t.Fatalf("loadConfig(%s): %v", path, err)
	}
	if !reflect.DeepEqual(cfg.Zones, []string{"fleet.example."}) || len(cfg.Protocols) != 3 {
		t.Errorf("zones = %v, protocols = %d; want [fleet.example.] and 3", cfg.Zones, len(cfg.Protocols))
	}
	if want := filepath.Join("..", "..", "examples", "keys", "qmuxd.key"); cfg.SigningKey != want {
		t.Errorf("signing_key = %q, want %q", cfg.SigningKey, want)
	}
}

func TestServeLogsTheKeyFileWarningAsOneWarnRecord(t *testing.T) { // FR-qmuxd-12
	path := writeConfigFile(t, baseConfig("127.0.0.1:0"))
	if err := os.Chmod(filepath.Join(filepath.Dir(path), "qmuxd.key"), 0o644); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	var stderr bytes.Buffer
	d, err := newDaemon(path, "", &stderr)
	if err != nil {
		t.Fatalf("newDaemon: %v", err)
	}
	if d == nil {
		t.Fatal("newDaemon returned no daemon")
	}
	if n := strings.Count(stderr.String(), "level=WARN"); n != 1 {
		t.Errorf("stderr has %d warn records, want 1:\n%s", n, stderr.String())
	}
}

func TestServeLogFormatJSONWritesJSONRecordsAtTheLogLevel(t *testing.T) {
	c := baseConfig("127.0.0.1:0")
	c["log_format"] = "json"
	c["log_level"] = "error"
	path := writeConfigFile(t, c)
	if err := os.Chmod(filepath.Join(filepath.Dir(path), "qmuxd.key"), 0o644); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	var stderr bytes.Buffer
	if _, err := newDaemon(path, "", &stderr); err != nil {
		t.Fatalf("newDaemon: %v", err)
	}
	// The key file warning is below the level error, so the logger drops it.
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want no record below level error", stderr.String())
	}
	d, err := newDaemon(path, "", &stderr)
	if err != nil {
		t.Fatalf("newDaemon: %v", err)
	}
	d.logger.Error("probe")
	var rec map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &rec); err != nil || rec["msg"] != "probe" {
		t.Errorf("record = %q, want one JSON record with msg probe: %v", stderr.String(), err)
	}
}

func TestServeRegistersEveryProtocolAndDiscoveryForEachZone(t *testing.T) { // FR-qmuxd-13, FR-qmuxd-15
	c := baseConfig("127.0.0.1:0")
	c["zones"] = []any{testZone, "lab.example."}
	c["protocols"] = append(c["protocols"].([]any), map[string]any{
		"id": "org.qmux.firmware", "versions": []any{1, 2}, "class": 65282, "type": 65280,
	})
	d, err := newDaemon(writeConfigFile(t, c), "", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("newDaemon: %v", err)
	}
	var ids []string
	for _, r := range d.srv.Registry().List() {
		ids = append(ids, fmt.Sprintf("%s %s", r.Info.ID, r.Key))
	}
	want := []string{"org.qmux.bootstrap 65280/65280", "org.qmux.discovery 65280/65534", "org.qmux.firmware 65282/65280"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("registry = %v, want %v", ids, want)
	}
}

func TestServeDiscoveryInClassRegistersDiscoveryAtTheINKey(t *testing.T) { // FR-qmuxd-15
	c := baseConfig("127.0.0.1:0")
	c["discovery"] = map[string]any{"in_class": true}
	d, err := newDaemon(writeConfigFile(t, c), "", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("newDaemon: %v", err)
	}
	reg, ok := d.srv.Registry().LookupID("org.qmux.discovery")
	if !ok || reg.Key != (protocol.ProtocolKey{Class: 1, Type: 65534}) {
		t.Errorf("discovery registration = %s, %v; want 1/65534, true", reg.Key, ok)
	}
}

func TestServeDiscoveryDisabledRegistersNoDiscovery(t *testing.T) { // FR-qmuxd-15
	c := baseConfig("127.0.0.1:0")
	c["discovery"] = map[string]any{"enabled": false}
	d, err := newDaemon(writeConfigFile(t, c), "", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("newDaemon: %v", err)
	}
	if _, ok := d.srv.Registry().LookupID("org.qmux.discovery"); ok {
		t.Error("registry holds org.qmux.discovery, want no discovery registration")
	}
}

func TestQmuxctlDiscoverListsEveryConfiguredProtocolForEachZone(t *testing.T) { // FR-qmuxd-15
	if testing.Short() {
		t.Skip("the test builds qmuxctl")
	}
	bin := filepath.Join(t.TempDir(), "qmuxctl")
	if out, err := exec.Command("go", "build", "-o", bin, "github.com/Crank-Git/qmux/cmd/qmuxctl").CombinedOutput(); err != nil {
		t.Fatalf("go build qmuxctl: %v\n%s", err, out)
	}
	anchors := filepath.Join(t.TempDir(), "anchors.json")
	pub := ed25519.NewKeyFromSeed(testSeed).Public().(ed25519.PublicKey)
	if err := security.WriteTrustAnchors(anchors, []security.TrustAnchor{{KeyID: security.KeyID(pub), PublicKey: pub}}); err != nil {
		t.Fatalf("WriteTrustAnchors: %v", err)
	}

	c := baseConfig("127.0.0.1:0")
	c["zones"] = []any{testZone, "lab.example."}
	c["protocols"] = append(c["protocols"].([]any), map[string]any{
		"id": "org.qmux.firmware", "versions": []any{1}, "class": 65282, "type": 65280,
	})
	path := writeConfigFile(t, c)
	p := startQmuxd(t, filepath.Dir(path))
	addr := readyAddr(t, p.waitReady(t))

	for _, zone := range []string{testZone, "lab.example."} {
		out, err := exec.Command(bin, "discover", "--server", addr, "--name", zone, "--trust-anchors", anchors, "--json").Output()
		if err != nil {
			t.Fatalf("qmuxctl discover --name %s: %v", zone, err)
		}
		var m struct {
			Zone      string `json:"zone"`
			Protocols []struct {
				ID string `json:"id"`
			} `json:"protocols"`
		}
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("decode the qmuxctl output %q: %v", out, err)
		}
		var ids []string
		for _, e := range m.Protocols {
			ids = append(ids, e.ID)
		}
		want := []string{"org.qmux.bootstrap", "org.qmux.discovery", "org.qmux.firmware"}
		if m.Zone != zone || !reflect.DeepEqual(ids, want) {
			t.Errorf("manifest for %s = zone %q, protocols %v; want %q, %v", zone, m.Zone, ids, zone, want)
		}
	}
}
