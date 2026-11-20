package schema_test

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/Crank-Git/qmux/examples/embedded-bootstrap/schema"
	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/protocol"
)

// Each payload type is the registry schema of its protocol (FR-demo-6).
var (
	_ protocol.Schema = schema.Bootstrap{}
	_ protocol.Schema = schema.Configuration{}
	_ protocol.Schema = schema.Firmware{}
)

// digest returns a 32-byte value that is not all zero, so a test sees a lost byte.
func digest() []byte {
	d := make([]byte, 32)
	for i := range d {
		d[i] = byte(i + 1)
	}
	return d
}

func bootstrap() schema.Bootstrap {
	return schema.Bootstrap{
		ManagementURL:       "https://127.0.0.1:53413/mgmt",
		ManagementTransport: "https",
	}
}

func configuration() schema.Configuration {
	return schema.Configuration{
		Generation: 7,
		Digest:     digest(),
		URL:        "https://127.0.0.1:53413/config-7.bin",
	}
}

func firmware() schema.Firmware {
	return schema.Firmware{
		Version: "2.4.1",
		Digest:  digest(),
		URL:     "https://127.0.0.1:53413/firmware-2.4.1.img",
		KeyID:   []byte{0x9c, 0x1e, 0x44, 0xa0, 0xb2, 0xd3, 0x7f, 0x15},
	}
}

// mustMarshal returns the codec encoding of v, so a test can build a payload that the
// schema types refuse to encode.
func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := codec.Marshal(v)
	if err != nil {
		t.Fatalf("codec.Marshal: %v", err)
	}
	return b
}

func TestBootstrapRoundTripReturnsTheSameFieldValues(t *testing.T) { // FR-demo-1
	want := bootstrap()
	b, err := want.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := schema.UnmarshalBootstrap(b)
	if err != nil {
		t.Fatalf("UnmarshalBootstrap: %v", err)
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestConfigurationRoundTripReturnsTheSameFieldValues(t *testing.T) { // FR-demo-2
	want := configuration()
	b, err := want.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := schema.UnmarshalConfiguration(b)
	if err != nil {
		t.Fatalf("UnmarshalConfiguration: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestFirmwareRoundTripReturnsTheSameFieldValues(t *testing.T) { // FR-demo-3
	want := firmware()
	b, err := want.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := schema.UnmarshalFirmware(b)
	if err != nil {
		t.Fatalf("UnmarshalFirmware: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestConfigurationEncodesAsAMapWithUintKeys(t *testing.T) { // FR-demo-2
	c := schema.Configuration{Generation: 7, Digest: digest(), URL: "u"}
	b, err := c.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	// a3: a map with 3 pairs. 01 07: key 1, uint 7. 02 58 20: key 2, a 32-byte string.
	want := append([]byte{0xa3, 0x01, 0x07, 0x02, 0x58, 0x20}, digest()...)
	// 03 61 75: key 3, the 1-byte text "u".
	want = append(want, 0x03, 0x61, 0x75)
	if !bytes.Equal(b, want) {
		t.Fatalf("got %x, want %x", b, want)
	}
}

func TestMarshalGivesIdenticalBytesForTheSamePayload(t *testing.T) { // FR-demo-4
	payloads := map[string]interface{ Marshal() ([]byte, error) }{
		"bootstrap":     bootstrap(),
		"configuration": configuration(),
		"firmware":      firmware(),
	}
	for name, p := range payloads {
		t.Run(name, func(t *testing.T) {
			first, err := p.Marshal()
			if err != nil {
				t.Fatalf("first Marshal: %v", err)
			}
			second, err := p.Marshal()
			if err != nil {
				t.Fatalf("second Marshal: %v", err)
			}
			if !bytes.Equal(first, second) {
				t.Fatalf("first %x, second %x", first, second)
			}
			if want := mustMarshal(t, p); !bytes.Equal(first, want) {
				t.Fatalf("Marshal %x, codec.Marshal %x", first, want)
			}
		})
	}
}

// The structs below give the schema payloads with one key added, one key removed, or
// one key with a different CBOR type.
type configurationExtraKey struct {
	Generation uint64 `cbor:"1,keyasint"`
	Digest     []byte `cbor:"2,keyasint"`
	URL        string `cbor:"3,keyasint"`
	Extra      string `cbor:"4,keyasint"`
}

type configurationNoURL struct {
	Generation uint64 `cbor:"1,keyasint"`
	Digest     []byte `cbor:"2,keyasint"`
}

type configurationTextGeneration struct {
	Generation string `cbor:"1,keyasint"`
	Digest     []byte `cbor:"2,keyasint"`
	URL        string `cbor:"3,keyasint"`
}

type firmwareFields struct {
	Version string `cbor:"1,keyasint"`
	Digest  []byte `cbor:"2,keyasint"`
	URL     string `cbor:"3,keyasint"`
	KeyID   []byte `cbor:"4,keyasint"`
}

type bootstrapExtraKey struct {
	ManagementURL       string `cbor:"1,keyasint"`
	ManagementTransport string `cbor:"2,keyasint"`
	Extra               uint64 `cbor:"3,keyasint"`
}

func TestUnmarshalConfigurationRejectsAnExtraKey(t *testing.T) { // FR-demo-5
	b := mustMarshal(t, configurationExtraKey{Generation: 7, Digest: digest(), URL: "u", Extra: "x"})
	if _, err := schema.UnmarshalConfiguration(b); err == nil {
		t.Fatal("got nil error, want an error for key 4")
	}
}

func TestUnmarshalBootstrapRejectsAnExtraKey(t *testing.T) { // FR-demo-5
	b := mustMarshal(t, bootstrapExtraKey{ManagementURL: "u", ManagementTransport: "https", Extra: 1})
	if _, err := schema.UnmarshalBootstrap(b); err == nil {
		t.Fatal("got nil error, want an error for key 3")
	}
}

func TestUnmarshalConfigurationRejectsAMissingKey(t *testing.T) { // FR-demo-5
	b := mustMarshal(t, configurationNoURL{Generation: 7, Digest: digest()})
	if _, err := schema.UnmarshalConfiguration(b); err == nil {
		t.Fatal("got nil error, want an error for the missing key 3")
	}
}

func TestUnmarshalConfigurationRejectsATextGeneration(t *testing.T) { // FR-demo-5
	b := mustMarshal(t, configurationTextGeneration{Generation: "7", Digest: digest(), URL: "u"})
	if _, err := schema.UnmarshalConfiguration(b); err == nil {
		t.Fatal("got nil error, want an error for a text generation")
	}
}

func TestUnmarshalConfigurationRejectsA31ByteDigest(t *testing.T) { // FR-demo-5
	b := mustMarshal(t, struct {
		Generation uint64 `cbor:"1,keyasint"`
		Digest     []byte `cbor:"2,keyasint"`
		URL        string `cbor:"3,keyasint"`
	}{7, digest()[:31], "u"})
	_, err := schema.UnmarshalConfiguration(b)
	if !errors.Is(err, schema.ErrDigestLength) {
		t.Fatalf("got %v, want ErrDigestLength", err)
	}
}

func TestUnmarshalFirmwareRejectsA31ByteDigest(t *testing.T) { // FR-demo-5
	f := firmware()
	b := mustMarshal(t, firmwareFields{f.Version, f.Digest[:31], f.URL, f.KeyID})
	if _, err := schema.UnmarshalFirmware(b); !errors.Is(err, schema.ErrDigestLength) {
		t.Fatalf("got %v, want ErrDigestLength", err)
	}
}

func TestUnmarshalFirmwareRejectsAKeyIDThatIsNot8Bytes(t *testing.T) { // FR-demo-3
	f := firmware()
	b := mustMarshal(t, firmwareFields{f.Version, f.Digest, f.URL, f.KeyID[:7]})
	if _, err := schema.UnmarshalFirmware(b); !errors.Is(err, schema.ErrKeyIDLength) {
		t.Fatalf("got %v, want ErrKeyIDLength", err)
	}
}

func TestUnmarshalFirmwareRejectsAFirmwareVersionThatDoesNotParse(t *testing.T) { // FR-demo-3
	f := firmware()
	b := mustMarshal(t, firmwareFields{"2.4", f.Digest, f.URL, f.KeyID})
	if _, err := schema.UnmarshalFirmware(b); !errors.Is(err, schema.ErrFirmwareVersionSyntax) {
		t.Fatalf("got %v, want ErrFirmwareVersionSyntax", err)
	}
}

func TestMarshalRefusesAPayloadThatBreaksTheSchema(t *testing.T) {
	c := configuration()
	c.Digest = c.Digest[:31]
	if _, err := c.Marshal(); !errors.Is(err, schema.ErrDigestLength) {
		t.Fatalf("Configuration.Marshal: got %v, want ErrDigestLength", err)
	}
	f := firmware()
	f.KeyID = nil
	if _, err := f.Marshal(); !errors.Is(err, schema.ErrKeyIDLength) {
		t.Fatalf("Firmware.Marshal: got %v, want ErrKeyIDLength", err)
	}
}

func TestValidateAcceptsAValidPayloadOfProtocolVersion1(t *testing.T) { // FR-demo-6
	for name, tc := range map[string]struct {
		s       protocol.Schema
		payload interface{ Marshal() ([]byte, error) }
	}{
		"bootstrap":     {schema.Bootstrap{}, bootstrap()},
		"configuration": {schema.Configuration{}, configuration()},
		"firmware":      {schema.Firmware{}, firmware()},
	} {
		t.Run(name, func(t *testing.T) {
			b, err := tc.payload.Marshal()
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if err := tc.s.Validate(1, b); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

func TestValidateRejectsAProtocolVersionOtherThan1(t *testing.T) { // FR-demo-6
	b, err := configuration().Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := (schema.Configuration{}).Validate(2, b); !errors.Is(err, schema.ErrProtocolVersionUnsupported) {
		t.Fatalf("got %v, want ErrProtocolVersionUnsupported", err)
	}
}

func TestValidateRejectsAnInvalidPayload(t *testing.T) { // FR-demo-6
	for name, s := range map[string]protocol.Schema{
		"bootstrap":     schema.Bootstrap{},
		"configuration": schema.Configuration{},
		"firmware":      schema.Firmware{},
	} {
		t.Run(name, func(t *testing.T) {
			if err := s.Validate(1, []byte("not cbor")); err == nil {
				t.Fatal("got nil error, want an error")
			}
		})
	}
}

func TestParseFirmwareVersionReturnsTheThreeFields(t *testing.T) {
	v, err := schema.ParseFirmwareVersion("2.4.1")
	if err != nil {
		t.Fatalf("ParseVersion: %v", err)
	}
	if want := (schema.FirmwareVersion{Major: 2, Minor: 4, Patch: 1}); v != want {
		t.Fatalf("got %+v, want %+v", v, want)
	}
	if v.String() != "2.4.1" {
		t.Fatalf("String = %q, want 2.4.1", v.String())
	}
}

func TestParseFirmwareVersionRejectsTextThatIsNotMajorMinorPatch(t *testing.T) {
	for _, s := range []string{"", "2", "2.4", "2.4.1.0", "2.4.x", "v2.4.1", "-1.0.0", "+1.0.0", "2..1", "02.4.1", " 2.4.1", "99999999999999999999.0.0"} {
		if _, err := schema.ParseFirmwareVersion(s); !errors.Is(err, schema.ErrFirmwareVersionSyntax) {
			t.Errorf("ParseFirmwareVersion(%q): got %v, want ErrFirmwareVersionSyntax", s, err)
		}
	}
}

func TestFirmwareVersionCompareUsesMajorThenMinorThenPatch(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"2.4.1", "2.3.0", 1},
		{"2.3.0", "2.4.1", -1},
		{"2.4.1", "2.4.1", 0},
		{"10.0.0", "9.9.9", 1},
		{"2.10.0", "2.9.0", 1},
		{"2.4.10", "2.4.9", 1},
		{"0.0.0", "0.0.1", -1},
	} {
		a, err := schema.ParseFirmwareVersion(tc.a)
		if err != nil {
			t.Fatalf("ParseFirmwareVersion(%q): %v", tc.a, err)
		}
		b, err := schema.ParseFirmwareVersion(tc.b)
		if err != nil {
			t.Fatalf("ParseFirmwareVersion(%q): %v", tc.b, err)
		}
		if got := a.Compare(b); got != tc.want {
			t.Errorf("%s.Compare(%s) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
