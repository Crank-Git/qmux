package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/security"
)

// testProbeZone is the probe zone of every probe test configuration file.
const testProbeZone = "probe.fleet.example."

// probeConfig returns baseConfig with a probe section for classes and types.
func probeConfig(classes, types []any) map[string]any {
	c := baseConfig("127.0.0.1:0")
	c["probe"] = map[string]any{"zone": testProbeZone, "classes": classes, "types": types}
	return c
}

// queryProbe queries name at key over network, and verifies the envelope with the test
// key. A status other than ok is a result, not a test failure.
func queryProbe(t *testing.T, addr, network, name string, key protocol.ProtocolKey) *client.Result {
	t.Helper()
	pub := ed25519.NewKeyFromSeed(testSeed).Public().(ed25519.PublicKey)
	c := client.NewClient(client.ClientConfig{
		Resolver:       addr,
		Network:        network,
		AttemptTimeout: time.Second,
		Attempts:       1,
		TrustAnchors:   &security.Verifier{Anchors: []security.TrustAnchor{{KeyID: security.KeyID(pub), PublicKey: pub}}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	res, err := c.Query(ctx, client.Query{Name: name, Class: key.Class, Type: key.Type, ExpectedProtocolID: probeID})
	// Query returns a *client.AppError with the result for a status other than ok.
	var appErr *client.AppError
	if err != nil && !errors.As(err, &appErr) {
		t.Fatalf("query %s at %s over %s: %v", name, key, network, err)
	}
	if !res.Verified {
		t.Fatalf("query %s at %s over %s: the envelope is not verified", name, key, network)
	}
	return res
}

func TestServeExitsTwoForAnInvalidProbeSection(t *testing.T) {
	cases := []struct {
		name string
		edit func(cfg map[string]any)
		// want is a part of the error message.
		want string
	}{
		{"FR-interop-3 class 3 without allow_reserved_classes", func(c map[string]any) {
			c["probe"].(map[string]any)["classes"] = []any{65281, 3}
		}, "probe.classes[1]: class 3 is reserved"},
		{"FR-interop-3 class 4 without allow_reserved_classes", func(c map[string]any) {
			c["probe"].(map[string]any)["classes"] = []any{4}
		}, "probe.classes[0]: class 4 is reserved"},
		{"FR-interop-4 type 16 without allow_non_private_types", func(c map[string]any) {
			c["probe"].(map[string]any)["types"] = []any{16}
		}, "probe.types[0]: type 16 is not a private type"},
		{"FR-interop-1 unknown field in the probe section", func(c map[string]any) {
			c["probe"].(map[string]any)["zones"] = []any{testProbeZone}
		}, `probe: json: unknown field "zones"`},
		{"FR-interop-1 probe section that is not an object", func(c map[string]any) { c["probe"] = 5 }, "probe: number is not a valid object"},
		{"FR-interop-1 class that does not fit 16 bits", func(c map[string]any) {
			c["probe"].(map[string]any)["classes"] = []any{70000}
		}, "probe.classes[0]: "},
		{"FR-interop-1 no classes", func(c map[string]any) { delete(c["probe"].(map[string]any), "classes") }, "probe.classes: "},
		{"FR-interop-1 no types", func(c map[string]any) { c["probe"].(map[string]any)["types"] = []any{} }, "probe.types: "},
		{"FR-interop-1 no zone", func(c map[string]any) { delete(c["probe"].(map[string]any), "zone") }, "probe.zone: "},
		{"probe zone that is not fully qualified", func(c map[string]any) {
			c["probe"].(map[string]any)["zone"] = "probe.fleet.example"
		}, "probe.zone: "},
		{"probe zone outside every zone", func(c map[string]any) {
			c["probe"].(map[string]any)["zone"] = "probe.other.example."
		}, "probe.zone: "},
		{"FR-interop-9 protocol with type 16", func(c map[string]any) {
			c["allow_non_private_types"] = true
			firstProtocol(c)["type"] = 16
		}, "protocols[0].type: type 16 is TXT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := probeConfig([]any{65281}, []any{65280})
			cfg["listen"] = heldAddress(t)
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

func TestLoadConfigDecodesTheProbeSection(t *testing.T) { // FR-interop-1, FR-interop-3, FR-interop-4
	c := probeConfig([]any{3}, []any{16})
	c["probe"].(map[string]any)["zone"] = "Probe.FLEET.example."
	c["allow_reserved_classes"] = true
	c["allow_non_private_types"] = true
	cfg, err := loadConfig(writeConfigFile(t, c), "")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	want := &probeSection{Zone: testProbeZone, Keys: []protocol.ProtocolKey{{Class: 3, Type: 16}}}
	if !reflect.DeepEqual(cfg.Probe, want) {
		t.Errorf("probe = %+v, want %+v", cfg.Probe, want)
	}
}

func TestLoadConfigWithoutAProbeSectionHasNoProbe(t *testing.T) { // FR-interop-1
	cfg, err := loadConfig(writeConfigFile(t, baseConfig("127.0.0.1:0")), "")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Probe != nil {
		t.Errorf("probe = %+v, want nil", cfg.Probe)
	}
}

func TestServeRegistersTheProbeProtocolAtTheProbeKey(t *testing.T) { // FR-interop-2
	d, err := newDaemon(writeConfigFile(t, probeConfig([]any{65281}, []any{65280})), "", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("newDaemon: %v", err)
	}
	reg, ok := d.srv.Registry().LookupID(probeID)
	want := protocol.Info{ID: probeID, Versions: []uint16{1}}
	if !ok || reg.Key != (protocol.ProtocolKey{Class: 65281, Type: 65280}) || !reflect.DeepEqual(reg.Info, want) {
		t.Errorf("probe registration = %s %+v, %v; want 65281/65280 %+v, true", reg.Key, reg.Info, ok, want)
	}
}

func TestProbeAnswersEveryQNAMEAtOrBelowTheProbeZoneWithTheDigestOfTheLowercaseQNAME(t *testing.T) { // FR-interop-5, FR-interop-6
	addr := startDaemon(t, probeConfig([]any{65281}, []any{65280}))
	key := protocol.ProtocolKey{Class: 65281, Type: 65280}
	for _, name := range []string{"abc.probe.fleet.example.", "ABC.Probe.Fleet.Example.", "x.y.probe.fleet.example.", testProbeZone} {
		res := queryProbe(t, addr, "udp", name, key)
		digest := sha256.Sum256([]byte(protocol.FoldName(name)))
		if res.Status != protocol.StatusOK || !bytes.Equal(res.Payload, digest[:]) {
			t.Errorf("%s: status, payload = %v, %x; want ok, %x", name, res.Status, res.Payload, digest)
		}
	}
}

func TestProbeAnswersNotFoundForAQNAMEOutsideTheProbeZone(t *testing.T) { // FR-interop-5
	addr := startDaemon(t, probeConfig([]any{65281}, []any{65280}))
	res := queryProbe(t, addr, "udp", "device42.fleet.example.", protocol.ProtocolKey{Class: 65281, Type: 65280})
	if res.Status != protocol.StatusNotFound || len(res.Payload) != 0 {
		t.Errorf("status, payload = %v, %x; want not-found, empty", res.Status, res.Payload)
	}
}

func TestProbeAnswersAQTYPE16QueryWithAVerifiedEnvelope(t *testing.T) { // FR-interop-7, FR-interop-8
	c := probeConfig([]any{1, 65281}, []any{16})
	c["allow_non_private_types"] = true
	// The registry holds one protocol key for each protocol ID, so a test of two keys
	// starts two daemons.
	for _, class := range []uint16{1, 65281} {
		c["probe"].(map[string]any)["classes"] = []any{class}
		addr := startDaemon(t, c)
		key := protocol.ProtocolKey{Class: class, Type: 16}
		for _, network := range []string{"udp", "tcp"} {
			const name = "abc.probe.fleet.example."
			res := queryProbe(t, addr, network, name, key)
			digest := sha256.Sum256([]byte(name))
			if res.Status != protocol.StatusOK || !bytes.Equal(res.Payload, digest[:]) {
				t.Errorf("%s over %s: status, payload = %v, %x; want ok, %x", key, network, res.Status, res.Payload, digest)
			}
		}
	}
}

func TestNoRegistrationOutsideTheProbeProtocolHasQTYPE16(t *testing.T) { // FR-interop-9
	c := probeConfig([]any{1}, []any{16})
	c["allow_non_private_types"] = true
	c["discovery"] = map[string]any{"in_class": true}
	d, err := newDaemon(writeConfigFile(t, c), "", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("newDaemon: %v", err)
	}
	var txt []string
	for _, r := range d.srv.Registry().List() {
		if r.Key.Type == 16 {
			txt = append(txt, r.Info.ID)
		}
	}
	if !reflect.DeepEqual(txt, []string{probeID}) {
		t.Errorf("registrations with QTYPE 16 = %v, want only %s", txt, probeID)
	}
}

func TestQmuxctlDiscoverListsTheProbeProtocolAtTheProbeKey(t *testing.T) { // FR-interop-1, FR-interop-2
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
	path := writeConfigFile(t, probeConfig([]any{65281}, []any{65280}))
	p := startQmuxd(t, filepath.Dir(path))
	addr := readyAddr(t, p.waitReady(t))

	out, err := exec.Command(bin, "discover", "--server", addr, "--name", testZone, "--trust-anchors", anchors, "--json").Output()
	if err != nil {
		t.Fatalf("qmuxctl discover: %v", err)
	}
	var m struct {
		Protocols []struct {
			ID    string `json:"id"`
			Class uint16 `json:"class"`
			Type  uint16 `json:"type"`
		} `json:"protocols"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("decode the qmuxctl output %q: %v", out, err)
	}
	found := false
	for _, e := range m.Protocols {
		if e.ID == probeID {
			found = e.Class == 65281 && e.Type == 65280
		}
	}
	if !found {
		t.Errorf("manifest = %s, want %s at 65281/65280", out, probeID)
	}
}
