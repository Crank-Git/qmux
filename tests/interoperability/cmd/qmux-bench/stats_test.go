package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSummaryHoldsTheSuccessRateAndTheNearestRankPercentiles(t *testing.T) {
	lat := []float64{5, 1, 4, 2, 3, 10, 9, 8, 7, 6}
	r := summarize("dns-in", 20, lat, 25, counters{rxPackets: 20, txPackets: 20, rxBytes: 4000, txBytes: 2000})
	if r.SuccessRate != 0.5 {
		t.Errorf("success_rate = %v, want 0.5", r.SuccessRate)
	}
	if r.P50MS == nil || *r.P50MS != 5 {
		t.Errorf("p50_ms = %v, want 5", r.P50MS)
	}
	if r.P95MS == nil || *r.P95MS != 10 {
		t.Errorf("p95_ms = %v, want 10", r.P95MS)
	}
	if r.PacketsPerLookup != 2 || r.BytesPerLookup != 300 || r.RequestsPerLookup != 1.25 {
		t.Errorf("per lookup = %v pkt, %v B, %v req, want 2, 300, 1.25",
			r.PacketsPerLookup, r.BytesPerLookup, r.RequestsPerLookup)
	}
}

func TestARunWithNoSuccessfulLookupHasRateZeroAndNullPercentiles(t *testing.T) {
	r := summarize("coap", 10, nil, 30, counters{txPackets: 30})
	if r.SuccessRate != 0 || r.P50MS != nil || r.P95MS != nil {
		t.Errorf("run = %+v, want success_rate 0 and null p50_ms and p95_ms", r)
	}
}

// writeStats writes the four interface counters of FR-bench-26 to dir.
func writeStats(t *testing.T, dir string, rxp, txp, rxb, txb string) {
	t.Helper()
	for name, v := range map[string]string{"rx_packets": rxp, "tx_packets": txp, "rx_bytes": rxb, "tx_bytes": txb} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(v+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadCountersReadsTheFourInterfaceStatistics(t *testing.T) {
	dir := t.TempDir()
	writeStats(t, dir, "10", "11", "1200", "900")
	c, err := readCounters(dir)
	if err != nil {
		t.Fatalf("readCounters: %v", err)
	}
	if c != (counters{rxPackets: 10, txPackets: 11, rxBytes: 1200, txBytes: 900}) {
		t.Errorf("counters = %+v", c)
	}
}

func TestReadCountersNamesTheMissingInterface(t *testing.T) {
	_, err := readCounters(filepath.Join(t.TempDir(), "absent"))
	if err == nil || !strings.Contains(err.Error(), "eth0") {
		t.Errorf("err = %v, want an error that names eth0", err)
	}
}

func TestANegativeCounterDeltaIsAnError(t *testing.T) {
	before := counters{rxPackets: 10, txPackets: 10, rxBytes: 100, txBytes: 100}
	after := counters{rxPackets: 5, txPackets: 12, rxBytes: 200, txBytes: 200}
	if _, err := after.sub(before); err == nil {
		t.Error("sub returned no error for a counter that went back")
	}
}
