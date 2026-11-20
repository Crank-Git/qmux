package security

import (
	"reflect"
	"testing"

	"github.com/Crank-Git/qmux/pkg/codec"
)

func TestSigningInputIsADeterministicArrayOf14ItemsInTheSpecOrder(t *testing.T) { // FR-security-14
	s := newSignedEnvelope(t)
	s.env.Payload = nil
	in, err := signingInput(s.env, "_v1.Device42.FLEET.example.", testProtocolKey)
	if err != nil {
		t.Fatal(err)
	}
	// codec.Unmarshal rejects bytes that are not the core deterministic encoding.
	var got []any
	if err := codec.Unmarshal(in, &got); err != nil {
		t.Fatal(err)
	}
	sig := s.env.Signature
	want := []any{
		"qmux-sig/1", testQName, uint64(65280), uint64(65281), uint64(1), testProtocolID,
		uint64(1), uint64(1), uint64(0), []byte{}, sig.KeyID, int64(-8), sig.IssuedAt, sig.ExpiresAt,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("signing input = %#v, want %#v", got, want)
	}
}

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
