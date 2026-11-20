package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/security"
)

func TestPayloadCBORConvertsEachJSONValueKindToTheStatedCBORKind(t *testing.T) { // FR-qmuxd-21
	cases := []struct {
		name, json string
		// want is the CBOR encoding in hex.
		want string
	}{
		{"object with decimal integer keys is a map with unsigned keys", `{"10": 3, "1": 2}`, "a201020a03"},
		{"object with one text key is a map with text keys", `{"1": 2, "a": 3}`, "a26131026161 03"},
		{"key with a leading zero is a text key", `{"01": 1}`, "a1623031 01"},
		{"negative key is a text key", `{"-1": 1}`, "a1622d31 01"},
		{"empty object is an empty map", `{}`, "a0"},
		{"integral number is an unsigned integer", `7`, "07"},
		{"largest unsigned integer", `18446744073709551615`, "1bffffffffffffffff"},
		{"integral number below 0 is a negative integer", `-3`, "22"},
		{"number with a fraction is a float", `1.5`, "f93e00"},
		{"number with an exponent is a float", `1e3`, "f963d0"},
		{"hex: string is a byte string", `"hex:0a0B"`, "420a0b"},
		{"empty hex: string is an empty byte string", `"hex:"`, "40"},
		{"other string is a text string", `"abc"`, "63616263"},
		{"array is an array", `[1, "a"]`, "82016161"},
		{"true is a boolean", `true`, "f5"},
		{"false is a boolean", `false`, "f4"},
		{"nested values convert by the same rules", `{"1": [{"x": "hex:ff"}], "2": -1.25}`, "a20181a1617841ff02f9bd00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := payloadCBOR(json.RawMessage(tc.json), "p")
			if err != nil {
				t.Fatalf("payloadCBOR(%s): %v", tc.json, err)
			}
			if want := strings.ReplaceAll(tc.want, " ", ""); hex.EncodeToString(got) != want {
				t.Errorf("payloadCBOR(%s) = %x, want %s", tc.json, got, want)
			}
		})
	}
}

func TestPayloadCBORRejectsAValueWithNoCBORKindAndNamesItsJSONPath(t *testing.T) { // FR-qmuxd-21
	cases := []struct {
		name, json, want string
	}{
		{"null", `null`, "p: null"},
		{"null in an array in an object", `{"a": [1, null]}`, `p["a"][1]: null`},
		{"hex: string that is not hex", `{"1": "hex:0g"}`, `p["1"]: `},
		{"integer above the largest unsigned integer", `18446744073709551616`, "p: "},
		{"integer below the smallest negative integer", `-9223372036854775809`, "p: "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := payloadCBOR(json.RawMessage(tc.json), "p")
			if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
				t.Errorf("payloadCBOR(%s) error = %v, want an error that starts with %q", tc.json, err, tc.want)
			}
		})
	}
}

func TestLoadConfigDecodesPayloadHexIntoThePayloadBytes(t *testing.T) { // FR-qmuxd-20
	c := baseConfig("127.0.0.1:0")
	firstProtocol(c)["records"] = map[string]any{testRecord: map[string]any{"payload_hex": "0a0b"}}
	cfg, err := loadConfig(writeConfigFile(t, c), "")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got := cfg.Protocols[0].Records[testRecord].Payload; !bytes.Equal(got, []byte{0x0a, 0x0b}) {
		t.Errorf("payload = %x, want 0a0b", got)
	}
}

func TestTwoLoadsOfOneFileGiveIdenticalPayloadBytes(t *testing.T) { // FR-qmuxd-22
	c := baseConfig("127.0.0.1:0")
	// A map with many keys makes a random key order likely to show in the bytes.
	payload := map[string]any{}
	for _, k := range []string{"a", "bb", "c", "dd", "e", "ff", "g", "hh", "i", "jj"} {
		payload[k] = map[string]any{"9": k, "10": k, "200": []any{k, 1.5}}
	}
	firstRecord(c)["payload"] = payload
	path := writeConfigFile(t, c)
	var loads [2][]byte
	for i := range loads {
		cfg, err := loadConfig(path, "")
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		loads[i] = cfg.Protocols[0].Records[testRecord].Payload
	}
	if !bytes.Equal(loads[0], loads[1]) {
		t.Errorf("payloads differ:\n%x\n%x", loads[0], loads[1])
	}
}

// startDaemon builds the daemon of the configuration file cfg and serves it on a free
// loopback port until the test ends. It returns the listen address.
func startDaemon(t *testing.T, cfg map[string]any) string {
	t.Helper()
	cfg["listen"] = "127.0.0.1:0"
	d, err := newDaemon(writeConfigFile(t, cfg), "", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("newDaemon: %v", err)
	}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	l, err := net.Listen("tcp", pc.LocalAddr().String())
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.srv.Serve(ctx, pc, l) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return pc.LocalAddr().String()
}

// queryRecord queries name at the protocol key 65280/65280 of addr with version, and
// verifies the envelope with the test key. A status other than ok is a result, not a
// test failure.
func queryRecord(t *testing.T, addr, name string, version uint16) *client.Result {
	t.Helper()
	pub := ed25519.NewKeyFromSeed(testSeed).Public().(ed25519.PublicKey)
	c := client.NewClient(client.ClientConfig{
		Resolver:       addr,
		AttemptTimeout: time.Second,
		Attempts:       1,
		TrustAnchors:   &security.Verifier{Anchors: []security.TrustAnchor{{KeyID: security.KeyID(pub), PublicKey: pub}}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	res, err := c.Query(ctx, client.Query{
		Name: name, Class: 65280, Type: 65280, Version: version, ExpectedProtocolID: "org.qmux.bootstrap",
	})
	// Query returns a *client.AppError with the result for a status other than ok.
	var appErr *client.AppError
	if err != nil && !errors.As(err, &appErr) {
		t.Fatalf("query %s version %d: %v", name, version, err)
	}
	if !res.Verified {
		t.Fatalf("query %s version %d: the envelope is not verified", name, version)
	}
	return res
}

func TestStaticRecordHandlerAnswersEachQueryWithTheRecordOrNotFound(t *testing.T) { // FR-qmuxd-16, 17, 18, 23
	c := baseConfig("")
	p := firstProtocol(c)
	p["versions"] = []any{1, 2}
	p["records"] = map[string]any{
		testRecord:                   map[string]any{"payload_hex": "0a0b", "version": 1},
		"any.fleet.example.":         map[string]any{"payload": 7},
		"unavailable.fleet.example.": map[string]any{"payload": "x", "status": "unavailable"},
	}
	addr := startDaemon(t, c)

	cases := []struct {
		name, qname string
		version     uint16
		wantStatus  protocol.Status
		wantPayload string
	}{
		{"FR-qmuxd-16 QNAME in another case answers the record", "DEVICE42.fleet.example.", 1, protocol.StatusOK, "0a0b"},
		{"FR-qmuxd-17 QNAME not in records is not-found", "device99.fleet.example.", 1, protocol.StatusNotFound, ""},
		{"FR-qmuxd-18 record of version 1 is not-found for _v2", testRecord, 2, protocol.StatusNotFound, ""},
		{"FR-qmuxd-23 record without status answers ok", "any.fleet.example.", 2, protocol.StatusOK, "07"},
		{"record without version answers every version", "any.fleet.example.", 1, protocol.StatusOK, "07"},
		{"FR-qmuxd-16 record answers with its status", "unavailable.fleet.example.", 2, protocol.StatusUnavailable, "6178"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := queryRecord(t, addr, tc.qname, tc.version)
			if res.Status != tc.wantStatus || hex.EncodeToString(res.Payload) != tc.wantPayload || res.Version != tc.version {
				t.Errorf("status, payload, version = %v, %x, %d; want %v, %s, %d",
					res.Status, res.Payload, res.Version, tc.wantStatus, tc.wantPayload, tc.version)
			}
		})
	}
}

func TestNewDaemonRejectsARecordThatMakesTheEnvelopeLargerThanTheLimit(t *testing.T) {
	c := baseConfig("127.0.0.1:0")
	// The default envelope limit is 16384 bytes, so 16384 payload bytes cannot fit.
	firstRecord(c)["payload_hex"] = strings.Repeat("ab", 16384)
	delete(firstRecord(c), "payload")
	_, err := newDaemon(writeConfigFile(t, c), "", &bytes.Buffer{})
	want := `protocols[0].records["device42.fleet.example."]: `
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("newDaemon error = %v, want an error that starts with %q", err, want)
	}
	if !errors.Is(err, codec.ErrTooLarge) {
		t.Errorf("newDaemon error = %v, want codec.ErrTooLarge", err)
	}
}

// nestedArrays returns a JSON value of n nested arrays around the number 1.
func nestedArrays(n int) any {
	var v any = 1
	for range n {
		v = []any{v}
	}
	return v
}

// textMap returns a JSON object with n pairs. No key is a decimal integer, so the
// payload is a map with text keys.
func textMap(n int) map[string]any {
	m := make(map[string]any, n)
	for i := range n {
		m[fmt.Sprintf("k%d", i)] = i
	}
	return m
}

// numberArray returns a JSON array of the n numbers 0 to n-1.
func numberArray(n int) []any {
	a := make([]any, n)
	for i := range a {
		a[i] = i
	}
	return a
}

func TestLoadConfigLoadsAPayloadAtEachCodecDecodeLimit(t *testing.T) { // #159
	cases := []struct {
		name    string
		payload any
	}{
		{"8 nesting levels", nestedArrays(8)},
		{"1024 array elements", numberArray(1024)},
		{"64 map pairs", textMap(64)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := baseConfig("127.0.0.1:0")
			firstRecord(c)["payload"] = tc.payload
			cfg, err := loadConfig(writeConfigFile(t, c), "")
			if err != nil {
				t.Fatalf("loadConfig: %v", err)
			}
			var v any
			if err := codec.Unmarshal(cfg.Protocols[0].Records[testRecord].Payload, &v); err != nil {
				t.Errorf("codec.Unmarshal of the loaded payload: %v", err)
			}
		})
	}
}
