// Command dns-in is the minimal client of the baseline dns-in (FR-bench-31). It does
// 100 lookups of QNAME _fw.device42.fleet.example. at QCLASS IN, QTYPE 65280, and
// verifies the envelope signature of each.
//
// Usage:
//
//	dns-in --addr HOST:PORT
//
// It exits with code 0 after 100 verified lookups and with code 1 at the first error.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/security"
	"github.com/Crank-Git/qmux/tests/interoperability/baseline/fixture"
)

func main() {
	addr := flag.String("addr", "", "UDP and TCP address of the DNS baselines")
	flag.Parse()
	verifier := &security.Verifier{Anchors: []security.TrustAnchor{fixture.TrustAnchor()}}
	c := client.NewClient(client.ClientConfig{Resolver: *addr, TrustAnchors: verifier})
	q := client.Query{Name: fixture.INName, Class: fixture.INKey.Class, Type: fixture.INKey.Type, ExpectedProtocolID: fixture.INID}
	for i := range 100 {
		// The deadline of one lookup is 5 s (Flow 2 of the benchmark spec).
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := c.Query(ctx, q)
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "dns-in: lookup %d: %v\n", i+1, err)
			os.Exit(1)
		}
	}
}
