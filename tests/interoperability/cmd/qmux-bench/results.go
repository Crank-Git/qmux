package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Crank-Git/qmux/tests/interoperability/baseline"
	"github.com/Crank-Git/qmux/tests/interoperability/footprint"
)

// Environment is the environment block of the results file (FR-bench-30 and
// FR-bench-20a).
type Environment struct {
	// CPU is the CPU model of the host that runs Docker.
	CPU string `json:"cpu"`
	// OS is the operating system of the host that runs Docker.
	OS string `json:"os"`
	// Kernel is the kernel version of the client container. On macOS it is the kernel
	// of the Docker virtual machine.
	Kernel string `json:"kernel"`
	// Docker is the version of the Docker server.
	Docker string `json:"docker"`
	// Go is the Go version of qmux-bench.
	Go string `json:"go"`
	// Commit is the commit of the repository.
	Commit string `json:"commit"`
	// Date is the UTC start time of the benchmark, in RFC 3339 form.
	Date string `json:"date"`
	// TCPSynRetries is net.ipv4.tcp_syn_retries of the client container.
	TCPSynRetries string `json:"tcp_syn_retries"`
}

// Assumption is one value that the benchmark sets and does not measure.
type Assumption struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Results is the content of the results file.
type Results struct {
	Environment Environment        `json:"environment"`
	Runs        []Run              `json:"runs"`
	Clients     []footprint.Result `json:"clients"`
	Dispatch    []Dispatch         `json:"dispatch"`
	Assumptions []Assumption       `json:"assumptions"`
}

// benchAssumptions are the values of qmux-bench itself that the benchmark does not
// measure.
var benchAssumptions = []Assumption{
	{"lookup deadline", "5 s per lookup; a lookup that misses it is a failure"},
	{"latency", "wall-clock time from the start of the lookup to the verified envelope, socket creation included"},
	{"percentiles", "nearest-rank p50 and p95 of the successful lookups"},
	{"coap request count", "a loopback relay in the client counts the CoAP transmissions; it adds one loopback hop in each direction"},
	{"coap retransmission check", "every 10 ms; the go-coap default is every 4 s"},
	{"packets and bytes", "eth0 rx plus tx of the client container over the whole run, divided by n"},
	{"client footprint", "each client does 100 lookups against baseline servers on the loopback of the host, with no netem"},
	{"loss shaping", "tc netem delay 10ms loss X% on eth0 of both containers, so each direction gets X%"},
}

// assumptions returns the assumptions of the baseline servers and of qmux-bench.
func assumptions() []Assumption {
	var out []Assumption
	for _, a := range baseline.Assumptions() {
		out = append(out, Assumption{Name: a.Name, Value: a.Value})
	}
	return append(out, benchAssumptions...)
}

// label returns the row label of the baseline name in every table.
func label(name string) string {
	switch name {
	case baseline.DNSInTXT:
		return name + " (reference)"
	case baseline.HTTPSReuse:
		return name + " (connection reuse)"
	}
	return name
}

// ms returns a latency cell: the value with one decimal, or "-" for null.
func ms(v *float64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%.1f", *v)
}

// line returns the stdout line of one run.
func line(r Run) string {
	return fmt.Sprintf("loss %-4s %-32s success %5.1f%%  p50 %s ms  p95 %s ms  %.1f pkt  %.0f B  %.2f req",
		fmt.Sprintf("%g%%", r.Loss), label(r.Baseline), 100*r.SuccessRate, ms(r.P50MS), ms(r.P95MS),
		r.PacketsPerLookup, r.BytesPerLookup, r.RequestsPerLookup)
}

// markdown returns the markdown tables of res (FR-bench-29).
func markdown(res Results) string {
	var b strings.Builder
	b.WriteString("## Runs\n\n")
	b.WriteString("| Baseline | Loss % | n | Success % | p50 ms | p95 ms | Packets per lookup | Bytes per lookup | Requests per lookup |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, r := range res.Runs {
		fmt.Fprintf(&b, "| %s | %g | %d | %.1f | %s | %s | %.1f | %.0f | %.2f |\n",
			label(r.Baseline), r.Loss, r.N, 100*r.SuccessRate, ms(r.P50MS), ms(r.P95MS),
			r.PacketsPerLookup, r.BytesPerLookup, r.RequestsPerLookup)
	}
	b.WriteString("\n## Retry settings\n\n| Baseline | Retry |\n|---|---|\n")
	for _, name := range baseline.Names {
		fmt.Fprintf(&b, "| %s | %s |\n", label(name), baseline.RetryPolicy(name).Policy)
	}
	b.WriteString("\n## Client footprint\n\n| Baseline | Binary bytes | Max RSS KiB | CPU ms |\n|---|---:|---:|---:|\n")
	for _, c := range res.Clients {
		fmt.Fprintf(&b, "| %s | %d | %d | %.1f |\n", label(c.Baseline), c.BinaryBytes, c.MaxRSSKiB, c.CPUMs)
	}
	b.WriteString("\n## Dispatch\n\n| Benchmark | ns/op | allocs/op |\n|---|---:|---:|\n")
	for _, d := range res.Dispatch {
		fmt.Fprintf(&b, "| %s | %.1f | %d |\n", d.Name, d.NsPerOp, d.AllocsPerOp)
	}
	e := res.Environment
	fmt.Fprintf(&b, "\n## Environment\n\n- CPU: %s\n- OS: %s\n- Kernel: %s\n- Docker: %s\n- Go: %s\n- Commit: %s\n- Date: %s\n- net.ipv4.tcp_syn_retries: %s\n",
		e.CPU, e.OS, e.Kernel, e.Docker, e.Go, e.Commit, e.Date, e.TCPSynRetries)
	return b.String()
}

// writeResults writes res to dir/<stem>.json and the markdown tables to
// dir/<stem>.md (FR-bench-28 and FR-bench-29), and returns both paths.
func writeResults(dir, stem string, res Results) (string, string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("create %s: %w", dir, err)
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("encode results: %w", err)
	}
	jsonPath := filepath.Join(dir, stem+".json")
	if err := os.WriteFile(jsonPath, append(b, '\n'), 0o644); err != nil {
		return "", "", fmt.Errorf("write %s: %w", jsonPath, err)
	}
	mdPath := filepath.Join(dir, stem+".md")
	if err := os.WriteFile(mdPath, []byte(markdown(res)), 0o644); err != nil {
		return "", "", fmt.Errorf("write %s: %w", mdPath, err)
	}
	return jsonPath, mdPath, nil
}
