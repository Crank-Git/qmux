package protocol_test

import (
	"testing"

	"github.com/Crank-Git/qmux/pkg/protocol"
)

func TestFoldNameLowersOnlyASCIILetters(t *testing.T) { // RFC 4343: case folding is ASCII only
	tests := []struct {
		name, in, want string
	}{
		{"ASCII letters", "DEVICE42.Fleet.Example.", "device42.fleet.example."},
		{"Kelvin sign", "Kiosk.example.", "Kiosk.example."},
		{"invalid UTF-8 byte", "a\xffB.", "a\xffb."},
		{"escape and digits", `\065Z9.`, `\065z9.`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := protocol.FoldName(tc.in); got != tc.want {
				t.Errorf("FoldName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
