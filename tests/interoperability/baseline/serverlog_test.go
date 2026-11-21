package baseline_test

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/Crank-Git/qmux/tests/interoperability/baseline"
)

// lockedBuffer is a bytes.Buffer that the server goroutines can write while the test
// reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// logged starts the baseline servers with a logger that writes to the returned
// buffer, and returns a client for them.
func logged(t *testing.T) (*baseline.Client, *lockedBuffer) {
	t.Helper()
	buf := &lockedBuffer{}
	r, err := baseline.Start(t.Context(), baseline.Config{
		DNSAddr: "127.0.0.1:0", CoAPAddr: "127.0.0.1:0", HTTPSAddr: "127.0.0.1:0",
		Logger: slog.New(slog.NewTextHandler(buf, nil)),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return newClient(t, r), buf
}

// sources returns the source attribute of each log line with the message msg.
func sources(log, msg string) []string {
	var out []string
	for _, line := range strings.Split(log, "\n") {
		if !strings.Contains(line, "msg="+msg+" ") && !strings.Contains(line, `msg="`+msg+`"`) {
			continue
		}
		for _, f := range strings.Fields(line) {
			if s, ok := strings.CutPrefix(f, "source="); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func TestTheServerLogsOneTLSHandshakeForAConnectionReuseRun(t *testing.T) {
	c, buf := logged(t)
	for i := range 5 {
		if _, err := c.Lookup(t.Context(), baseline.HTTPSReuse); err != nil {
			t.Fatalf("lookup %d: %v", i, err)
		}
	}
	if got := strings.Count(buf.String(), `msg="https handshake"`); got != 1 {
		t.Errorf("the server logged %d TLS handshakes for 5 https-reuse lookups, want 1", got)
	}
}

func TestTheServerLogsATLSHandshakeForEachHTTPSColdLookup(t *testing.T) {
	c, buf := logged(t)
	for i := range 3 {
		if _, err := c.Lookup(t.Context(), baseline.HTTPSCold); err != nil {
			t.Fatalf("lookup %d: %v", i, err)
		}
	}
	if got := strings.Count(buf.String(), `msg="https handshake"`); got != 3 {
		t.Errorf("the server logged %d TLS handshakes for 3 https-cold lookups, want 3", got)
	}
}

func TestTheServerSeesANewSourcePortForEachDNSAndCoAPLookup(t *testing.T) {
	c, buf := logged(t)
	const n = 5
	for _, name := range []string{baseline.QMuxPrivate, baseline.DNSIn, baseline.DNSInTXT, baseline.CoAP} {
		for i := range n {
			if _, err := c.Lookup(t.Context(), name); err != nil {
				t.Fatalf("%s lookup %d: %v", name, i, err)
			}
		}
	}
	for msg, want := range map[string]int{"query": 3 * n, "coap request": n} {
		got := sources(buf.String(), msg)
		seen := make(map[string]bool)
		for _, s := range got {
			seen[s] = true
		}
		if len(got) != want || len(seen) != want {
			t.Errorf("%s: %d log lines from %d source ports, want %d of each", msg, len(got), len(seen), want)
		}
	}
}
