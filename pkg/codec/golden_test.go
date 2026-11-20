package codec

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

// vectorDir holds the v1 golden vectors. A change to the wire format of format 1
// makes a vector test fail, because format 1 never changes.
const vectorDir = "testdata/v1"

// vector is one <name>.json file. A valid vector has Envelope and no Error. An
// invalid vector has Error and no Envelope.
type vector struct {
	Description string          `json:"description"`
	Envelope    *vectorEnvelope `json:"envelope"`
	Error       string          `json:"error"`
}

// vectorEnvelope holds the expected field values of a valid vector. The payload is
// hex, so the JSON file stays readable.
type vectorEnvelope struct {
	Format      uint64 `json:"format"`
	ProtocolID  string `json:"protocol_id"`
	Version     uint64 `json:"version"`
	MessageType uint64 `json:"message_type"`
	Status      uint64 `json:"status"`
	PayloadHex  string `json:"payload_hex"`
}

// vectorErrors maps each error name that a JSON file can state to a match function.
// A CBOR library error is named by its type, because the library has no sentinels.
var vectorErrors = map[string]func(error) bool{
	"ErrNotDeterministic":        func(err error) bool { return errors.Is(err, ErrNotDeterministic) },
	"ErrFormat":                  func(err error) bool { return errors.Is(err, ErrFormat) },
	"cbor.DupMapKeyError":        errorAs[*cbor.DupMapKeyError],
	"cbor.UnknownFieldError":     errorAs[*cbor.UnknownFieldError],
	"cbor.IndefiniteLengthError": errorAs[*cbor.IndefiniteLengthError],
	"cbor.TagsMdError":           errorAs[*cbor.TagsMdError],
	"cbor.SemanticError":         errorAs[*cbor.SemanticError],
}

func errorAs[T error](err error) bool {
	var target T
	return errors.As(err, &target)
}

// requiredVectors are the cases that FR-codec-26 names. The status vectors cover
// each row of the status table in docs/specs/spec.md § Data model.
var requiredVectors = []string{
	"minimal-record",
	"manifest",
	"status-ok",
	"status-not-found",
	"status-bad-request",
	"status-version-unsupported",
	"status-unavailable",
	"status-protocol-specific-256",
	"duplicate-key",
	"unknown-key",
	"indefinite-length",
	"tag",
	"invalid-utf8",
	"non-deterministic-key-order",
	"non-shortest-integer",
	"format-2",
}

// vectorNames returns the base name of each JSON file in vectorDir, sorted.
func vectorNames(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(vectorDir, "*.json"))
	if err != nil {
		t.Fatalf("list vectors: %v", err)
	}
	names := make([]string, 0, len(paths))
	for _, p := range paths {
		names = append(names, strings.TrimSuffix(filepath.Base(p), ".json"))
	}
	slices.Sort(names)
	return names
}

func readVector(t *testing.T, name string) (vector, []byte) {
	t.Helper()
	j, err := os.ReadFile(filepath.Join(vectorDir, name+".json"))
	if err != nil {
		t.Fatalf("read %s.json: %v", name, err)
	}
	d := json.NewDecoder(bytes.NewReader(j))
	d.DisallowUnknownFields()
	var v vector
	if err := d.Decode(&v); err != nil {
		t.Fatalf("parse %s.json: %v", name, err)
	}
	if (v.Envelope == nil) == (v.Error == "") {
		t.Fatalf("%s.json must state exactly one of envelope and error", name)
	}
	b, err := os.ReadFile(filepath.Join(vectorDir, name+".cbor"))
	if err != nil {
		t.Fatalf("read %s.cbor: %v", name, err)
	}
	return v, b
}

func TestGoldenVectorsIncludeEveryRequiredCase(t *testing.T) {
	names := vectorNames(t)
	for _, want := range requiredVectors {
		if !slices.Contains(names, want) {
			t.Errorf("%s has no vector %q", vectorDir, want)
		}
	}
}

func TestEveryGoldenCBORFileHasAJSONFile(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(vectorDir, "*.cbor"))
	if err != nil {
		t.Fatalf("list vectors: %v", err)
	}
	for _, p := range paths {
		if _, err := os.Stat(strings.TrimSuffix(p, ".cbor") + ".json"); err != nil {
			t.Errorf("%s has no JSON file: %v", p, err)
		}
	}
}

// TestGoldenVectors decodes each vector and checks the expected values or error.
// It also encodes each valid vector again and requires the bytes of the file.
func TestGoldenVectors(t *testing.T) {
	names := vectorNames(t)
	if len(names) == 0 {
		t.Fatalf("%s holds no vectors", vectorDir)
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			v, b := readVector(t, name)
			got, err := Decode(b, Limits{})
			if v.Error != "" {
				checkVectorError(t, v.Error, err)
				return
			}
			if err != nil {
				t.Fatalf("Decode(% x) error = %v, want nil", b, err)
			}
			checkVectorEnvelope(t, v.Envelope, got)
			again, err := Encode(got, Limits{})
			if err != nil {
				t.Fatalf("Encode error = %v, want nil", err)
			}
			if !bytes.Equal(again, b) {
				t.Errorf("Encode = % x, want the file bytes % x", again, b)
			}
		})
	}
}

func checkVectorError(t *testing.T, name string, err error) {
	t.Helper()
	match, ok := vectorErrors[name]
	if !ok {
		t.Fatalf("the JSON file names the unknown error %q", name)
	}
	if !match(err) {
		t.Errorf("Decode error = %v, want %s", err, name)
	}
}

func checkVectorEnvelope(t *testing.T, want *vectorEnvelope, got Envelope) {
	t.Helper()
	payload, err := hex.DecodeString(want.PayloadHex)
	if err != nil {
		t.Fatalf("payload_hex %q: %v", want.PayloadHex, err)
	}
	wantEnv := Envelope{
		Format:      want.Format,
		ProtocolID:  want.ProtocolID,
		Version:     want.Version,
		MessageType: want.MessageType,
		Status:      want.Status,
		Payload:     payload,
	}
	if got.Format != wantEnv.Format || got.ProtocolID != wantEnv.ProtocolID ||
		got.Version != wantEnv.Version || got.MessageType != wantEnv.MessageType ||
		got.Status != wantEnv.Status || !bytes.Equal(got.Payload, wantEnv.Payload) ||
		got.Signature != nil {
		t.Errorf("Decode = %+v, want %+v", got, wantEnv)
	}
}
