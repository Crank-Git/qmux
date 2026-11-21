package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Crank-Git/qmux/tests/interoperability/baseline"
)

// startServers starts the baseline servers on loopback and returns their flags for
// the lookups command.
func startServers(t *testing.T) []string {
	t.Helper()
	r, err := baseline.Start(t.Context(), baseline.Config{DNSAddr: "127.0.0.1:0", CoAPAddr: "127.0.0.1:0", HTTPSAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	tg := r.Target()
	return []string{"--dns", tg.DNS, "--coap", tg.CoAP, "--https", tg.HTTPS}
}

func TestLookupsPrintsOneRunWithOneRequestPerLookupAtLossZero(t *testing.T) {
	addrs := startServers(t)
	stats := t.TempDir()
	writeStats(t, stats, "0", "0", "0", "0")
	for _, name := range baseline.Names {
		var out, errOut bytes.Buffer
		args := append([]string{"--baseline", name, "--n", "4", "--stats", stats}, addrs...)
		if code := runLookups(t.Context(), args, &out, &errOut); code != 0 {
			t.Fatalf("%s: exit %d: %s", name, code, errOut.String())
		}
		var r Run
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			t.Fatalf("%s: output %q: %v", name, out.String(), err)
		}
		if r.Baseline != name || r.N != 4 || r.SuccessRate != 1 || r.RequestsPerLookup != 1 {
			t.Errorf("%s: run = %+v, want n 4, success_rate 1 and 1 request per lookup", name, r)
		}
		if r.P50MS == nil || r.P95MS == nil {
			t.Errorf("%s: p50_ms or p95_ms is null after 4 successful lookups", name)
		}
	}
}

func TestLookupsFailsWhenTheClientContainerHasNoEth0(t *testing.T) {
	addrs := startServers(t)
	var out, errOut bytes.Buffer
	args := append([]string{"--baseline", baseline.QMuxPrivate, "--n", "1", "--stats", t.TempDir() + "/absent"}, addrs...)
	if code := runLookups(t.Context(), args, &out, &errOut); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "eth0") {
		t.Errorf("stderr %q names no eth0", errOut.String())
	}
}

func TestReadyReportsTheServerAsNotReadyAfterTheTimeout(t *testing.T) {
	var out, errOut bytes.Buffer
	// Port 9 on loopback has no listener, so every query of the ready check fails.
	args := []string{"--dns", "127.0.0.1:9", "--coap", "127.0.0.1:9", "--https", "127.0.0.1:9", "--timeout", "300ms"}
	if code := runReady(t.Context(), args, &out, &errOut); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "bench-server not ready after 0.3 s") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestReadySucceedsWhenTheServerAnswers(t *testing.T) {
	addrs := startServers(t)
	var out, errOut bytes.Buffer
	if code := runReady(t.Context(), addrs, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
}
