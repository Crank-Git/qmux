package server

import "testing"

func TestMatchZoneFoldsOnlyASCIILetters(t *testing.T) { // RFC 4343: case folding is ASCII only
	tests := []struct {
		name, zone, qname string
		want              bool
	}{
		{"zone with a Kelvin sign", "Kiosk.example.", "x.kiosk.example.", false},
		{"QNAME in uppercase", "fleet.example.", "DEVICE42.Fleet.Example.", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NewServer(ServerConfig{Zones: []string{tc.zone}})
			if _, ok := s.matchZone(tc.qname); ok != tc.want {
				t.Errorf("matchZone(%q) with zone %q gives %v, want %v", tc.qname, tc.zone, ok, tc.want)
			}
		})
	}
}
