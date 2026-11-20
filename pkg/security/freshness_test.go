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
