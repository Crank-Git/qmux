package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/Crank-Git/qmux/tests/interoperability/baseline"
)

// Run is the result of one benchmark run: n lookups of one baseline at one loss
// level. The JSON names are the names of the runs array in the results file.
type Run struct {
	Baseline string  `json:"baseline"`
	Loss     float64 `json:"loss"`
	N        int     `json:"n"`
	// SuccessRate is the fraction of the lookups that returned a verified envelope in
	// the deadline, from 0 to 1 (FR-bench-24).
	SuccessRate float64 `json:"success_rate"`
	// P50MS and P95MS are the nearest-rank percentiles of the latency of the
	// successful lookups (FR-bench-25). They are null when no lookup succeeded.
	P50MS *float64 `json:"p50_ms"`
	P95MS *float64 `json:"p95_ms"`
	// PacketsPerLookup and BytesPerLookup come from the eth0 counters of the client
	// container, received plus sent, divided by n (FR-bench-26).
	PacketsPerLookup float64 `json:"packets_per_lookup"`
	BytesPerLookup   float64 `json:"bytes_per_lookup"`
	// RequestsPerLookup is the application requests of the run divided by n
	// (FR-bench-27).
	RequestsPerLookup float64 `json:"requests_per_lookup"`
	// Retry states the retry settings of the baseline (FR-bench-20a).
	Retry baseline.Retry `json:"retry"`
}

// summarize returns the run of n lookups of the baseline name. latencies holds the
// latency in milliseconds of each successful lookup, requests is the request count
// of all n lookups, and delta is the change of the interface counters.
func summarize(name string, n int, latencies []float64, requests int, delta counters) Run {
	r := Run{
		Baseline:          name,
		N:                 n,
		SuccessRate:       float64(len(latencies)) / float64(n),
		PacketsPerLookup:  float64(delta.rxPackets+delta.txPackets) / float64(n),
		BytesPerLookup:    float64(delta.rxBytes+delta.txBytes) / float64(n),
		RequestsPerLookup: float64(requests) / float64(n),
	}
	if len(latencies) > 0 {
		sorted := slices.Sorted(slices.Values(latencies))
		r.P50MS = nearestRank(sorted, 0.50)
		r.P95MS = nearestRank(sorted, 0.95)
	}
	return r
}

// nearestRank returns the p-th percentile of the ascending values sorted: the
// smallest value with at least the fraction p of the values at or below it.
func nearestRank(sorted []float64, p float64) *float64 {
	i := int(math.Ceil(p*float64(len(sorted)))) - 1
	v := sorted[max(i, 0)]
	return &v
}

// counters holds the eth0 statistics of FR-bench-26.
type counters struct {
	rxPackets, txPackets, rxBytes, txBytes uint64
}

// readCounters reads the four counters from dir, the statistics directory of eth0.
// The error names eth0 when dir does not exist.
func readCounters(dir string) (counters, error) {
	if _, err := os.Stat(dir); err != nil {
		return counters{}, fmt.Errorf("interface eth0 not found in the client container: %w", err)
	}
	var c counters
	for name, dst := range map[string]*uint64{
		"rx_packets": &c.rxPackets, "tx_packets": &c.txPackets, "rx_bytes": &c.rxBytes, "tx_bytes": &c.txBytes,
	} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return counters{}, fmt.Errorf("read eth0 %s: %w", name, err)
		}
		v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
		if err != nil {
			return counters{}, fmt.Errorf("parse eth0 %s: %w", name, err)
		}
		*dst = v
	}
	return c, nil
}

// sub returns c minus before. A counter that is smaller than before is an error,
// because it means that the counter wrapped or the interface changed.
func (c counters) sub(before counters) (counters, error) {
	pairs := []struct {
		name          string
		after, before uint64
	}{
		{"rx_packets", c.rxPackets, before.rxPackets},
		{"tx_packets", c.txPackets, before.txPackets},
		{"rx_bytes", c.rxBytes, before.rxBytes},
		{"tx_bytes", c.txBytes, before.txBytes},
	}
	for _, p := range pairs {
		if p.after < p.before {
			return counters{}, fmt.Errorf("eth0 %s went from %d to %d: negative delta", p.name, p.before, p.after)
		}
	}
	return counters{
		rxPackets: c.rxPackets - before.rxPackets,
		txPackets: c.txPackets - before.txPackets,
		rxBytes:   c.rxBytes - before.rxBytes,
		txBytes:   c.txBytes - before.txBytes,
	}, nil
}
