package discovery

import (
	"errors"
	"reflect"
	"testing"

	"github.com/Crank-Git/qmux/pkg/codec"
)

// validManifest returns a new manifest for the zone fleet.example. that passes
// validation. Each test changes one field of its own copy.
func validManifest() *Manifest {
	return &Manifest{
		FrameworkVersion: 1,
		Zone:             "fleet.example.",
		Protocols: []Entry{
			{ID: "org.qmux.bootstrap", Versions: []uint16{1}, Class: 65280, Type: 65280},
			{ID: "org.qmux.discovery", Versions: []uint16{1}, Class: 65280, Type: 65534},
			{ID: "org.qmux.configuration", Versions: []uint16{1, 2}, Class: 65281, Type: 65280},
			{ID: "org.qmux.firmware", Versions: []uint16{1, 3, 7}, Class: 65282, Type: 65280},
		},
	}
}

func TestManifestRoundTripThroughCodecGivesAnEqualValue(t *testing.T) {
	want := validManifest()
	b, err := codec.Marshal(want)
	if err != nil {
		t.Fatalf("codec.Marshal: %v", err)
	}
	var got Manifest
	if err := codec.Unmarshal(b, &got); err != nil {
		t.Fatalf("codec.Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(&got, want) {
		t.Errorf("round trip = %+v, want %+v", got, *want)
	}
}

func TestManifestEncodesWithTheIntegerKeysOfTheDataModel(t *testing.T) {
	m := &Manifest{
		FrameworkVersion: 1,
		Zone:             "z.",
		Protocols:        []Entry{{ID: "a", Versions: []uint16{1}, Class: 1, Type: 2}},
	}
	got, err := codec.Marshal(m)
	if err != nil {
		t.Fatalf("codec.Marshal: %v", err)
	}
	// {1: 1, 2: "z.", 3: [{1: "a", 2: [1], 3: 1, 4: 2}]}
	want := []byte{
		0xa3,
		0x01, 0x01,
		0x02, 0x62, 'z', '.',
		0x03, 0x81, 0xa4,
		0x01, 0x61, 'a',
		0x02, 0x81, 0x01,
		0x03, 0x01,
		0x04, 0x02,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("codec.Marshal = % x, want % x", got, want)
	}
}

func TestValidateAcceptsAValidManifest(t *testing.T) {
	if err := validManifest().validate("fleet.example."); err != nil {
		t.Errorf("validate = %v, want nil", err)
	}
}

func TestValidateRejectsFrameworkVersion2(t *testing.T) {
	m := validManifest()
	m.FrameworkVersion = 2
	if err := m.validate("fleet.example."); !errors.Is(err, ErrInvalidManifest) {
		t.Errorf("validate = %v, want ErrInvalidManifest", err)
	}
}

func TestValidateRejectsAZoneThatDiffersFromTheQueriedZone(t *testing.T) {
	m := validManifest()
	m.Zone = "other.example."
	if err := m.validate("fleet.example."); !errors.Is(err, ErrInvalidManifest) {
		t.Errorf("validate = %v, want ErrInvalidManifest", err)
	}
}

func TestValidateIgnoresLetterCaseInTheZone(t *testing.T) {
	m := validManifest()
	m.Zone = "FLEET.example."
	if err := m.validate("fleet.example."); err != nil {
		t.Errorf("validate = %v, want nil", err)
	}
}

func TestValidateRejectsAZoneThatDiffersOnlyByAKelvinSign(t *testing.T) { // RFC 4343: case folding is ASCII only
	m := validManifest()
	m.Zone = "kiosk.example."
	if err := m.validate("Kiosk.example."); !errors.Is(err, ErrInvalidManifest) {
		t.Errorf("validate = %v, want ErrInvalidManifest", err)
	}
}

func TestValidateRejectsTwoEntriesWithOneProtocolID(t *testing.T) {
	m := validManifest()
	m.Protocols = append(m.Protocols,
		Entry{ID: "org.qmux.firmware", Versions: []uint16{1}, Class: 65283, Type: 65280})
	if err := m.validate("fleet.example."); !errors.Is(err, ErrInvalidManifest) {
		t.Errorf("validate = %v, want ErrInvalidManifest", err)
	}
}

func TestValidateRejectsTwoEntriesWithOneProtocolKey(t *testing.T) {
	m := validManifest()
	m.Protocols = append(m.Protocols,
		Entry{ID: "org.qmux.other", Versions: []uint16{1}, Class: 65281, Type: 65280})
	if err := m.validate("fleet.example."); !errors.Is(err, ErrInvalidManifest) {
		t.Errorf("validate = %v, want ErrInvalidManifest", err)
	}
}

func TestValidateRejectsABadVersionList(t *testing.T) {
	cases := map[string][]uint16{
		"empty":    {},
		"nil":      nil,
		"zero":     {0},
		"unsorted": {2, 1},
		"repeated": {1, 1},
	}
	for name, versions := range cases {
		t.Run(name, func(t *testing.T) {
			m := validManifest()
			m.Protocols[3].Versions = versions
			if err := m.validate("fleet.example."); !errors.Is(err, ErrInvalidManifest) {
				t.Errorf("validate with versions %v = %v, want ErrInvalidManifest", versions, err)
			}
		})
	}
}

func TestLookupReturnsTheEntryForAKnownProtocolID(t *testing.T) {
	m := validManifest()
	got, ok := m.Lookup("org.qmux.firmware")
	if !ok {
		t.Fatal("Lookup(org.qmux.firmware) ok = false, want true")
	}
	if !reflect.DeepEqual(got, m.Protocols[3]) {
		t.Errorf("Lookup(org.qmux.firmware) = %+v, want %+v", got, m.Protocols[3])
	}
}

func TestLookupReturnsFalseForAnUnknownProtocolID(t *testing.T) {
	if _, ok := validManifest().Lookup("x.y"); ok {
		t.Error("Lookup(x.y) ok = true, want false")
	}
}
