package security

import "testing"

func TestIsVersionLabelAcceptsOnlyVersions1To65535(t *testing.T) {
	cases := map[string]bool{
		"_v1": true, "_v65535": true, "_v0": false, "_v01": false,
		"_v65536": false, "_v": false, "_vx": false, "device42": false, "_v-1": false,
	}
	for label, want := range cases {
		if got := isVersionLabel(label); got != want {
			t.Errorf("isVersionLabel(%q) = %v, want %v", label, got, want)
		}
	}
}

func TestFreshnessKeyGivesTwoInvalidUTF8NamesTwoEntries(t *testing.T) { // RFC 4343: case folding is ASCII only
	a := newFreshnessKey(testProtocolID, "_v1.\xff.zone.")
	b := newFreshnessKey(testProtocolID, "_v1.\xfe.zone.")
	if a == b {
		t.Fatalf("newFreshnessKey gives one key %+v for two different names", a)
	}
}

func TestFreshnessKeyGivesTheKelvinSignAndKTwoEntries(t *testing.T) { // RFC 4343: case folding is ASCII only
	a := newFreshnessKey(testProtocolID, "_v1.Kiosk.zone.")
	b := newFreshnessKey(testProtocolID, "_v1.kiosk.zone.")
	if a == b {
		t.Fatalf("newFreshnessKey gives one key %+v for the Kelvin sign and k", a)
	}
}

func TestFreshnessKeyFoldsASCIILetterCase(t *testing.T) {
	a := newFreshnessKey(testProtocolID, "_v1.DEVICE42.Fleet.Example.")
	b := newFreshnessKey(testProtocolID, "_v1.device42.fleet.example.")
	if a != b {
		t.Fatalf("newFreshnessKey = %+v and %+v, want one key", a, b)
	}
}
