// Command https-reuse is the minimal client of the baseline https-reuse (FR-bench-31).
// It does 100 GETs of /fw/device42 on one kept TCP connection and TLS 1.3 session, and
// verifies the envelope signature of each response.
//
// Usage:
//
//	https-reuse --addr HOST:PORT
//
// It exits with code 0 after 100 verified lookups and with code 1 at the first error.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/Crank-Git/qmux/pkg/security"
	"github.com/Crank-Git/qmux/tests/interoperability/baseline/fixture"
)

func main() {
	addr := flag.String("addr", "", "TCP address of the HTTPS baselines")
	flag.Parse()
	tlsConfig, err := fixture.TLSClientConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "https-reuse: %v\n", err)
		os.Exit(1)
	}
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig}}
	verifier := &security.Verifier{Anchors: []security.TrustAnchor{fixture.TrustAnchor()}}
	for i := range 100 {
		// The deadline of one lookup is 5 s (Flow 2 of the benchmark spec).
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := lookup(ctx, hc, *addr, verifier)
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "https-reuse: lookup %d: %v\n", i+1, err)
			os.Exit(1)
		}
	}
}

// lookup sends GET /fw/device42 to addr and verifies the envelope in the response.
// The HTTPS baselines carry the exact envelope of qmux-private (FR-bench-10). The
// lookup reads the body to the end, so the transport keeps the connection.
func lookup(ctx context.Context, hc *http.Client, addr string, verifier *security.Verifier) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr+fixture.Path, nil)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", fixture.Path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // The body is read-only.
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", fixture.Path, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, fixture.MaxEnvelope+1))
	if err != nil {
		return fmt.Errorf("read %s: %w", fixture.Path, err)
	}
	if len(raw) > fixture.MaxEnvelope {
		return fmt.Errorf("GET %s: envelope longer than %d bytes", fixture.Path, fixture.MaxEnvelope)
	}
	if _, err := verifier.Verify(raw, fixture.PrivateName, fixture.PrivateKey); err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	return nil
}
