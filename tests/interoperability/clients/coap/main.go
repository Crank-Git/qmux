// Command coap is the minimal client of the baseline coap (FR-bench-31). It does 100
// confirmable GETs of /fw/device42, each from a new UDP socket, and verifies the
// envelope signature of each response.
//
// Usage:
//
//	coap --addr HOST:PORT
//
// It exits with code 0 after 100 verified lookups and with code 1 at the first error.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/udp"

	"github.com/Crank-Git/qmux/pkg/security"
	"github.com/Crank-Git/qmux/tests/interoperability/baseline/fixture"
)

func main() {
	addr := flag.String("addr", "", "UDP address of the coap baseline")
	flag.Parse()
	verifier := &security.Verifier{Anchors: []security.TrustAnchor{fixture.TrustAnchor()}}
	for i := range 100 {
		// The deadline of one lookup is 5 s (Flow 2 of the benchmark spec).
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := lookup(ctx, *addr, verifier)
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "coap: lookup %d: %v\n", i+1, err)
			os.Exit(1)
		}
	}
}

// lookup sends one confirmable GET to addr and verifies the envelope in the response.
// The coap baseline carries the exact envelope of qmux-private (FR-bench-10).
func lookup(ctx context.Context, addr string, verifier *security.Verifier) error {
	conn, err := udp.Dial(addr)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close() //nolint:errcheck // The lookup reads nothing more from the socket.
	resp, err := conn.Get(ctx, fixture.Path)
	if err != nil {
		return fmt.Errorf("GET %s: %w", fixture.Path, err)
	}
	if resp.Code() != codes.Content {
		return fmt.Errorf("GET %s: code %v", fixture.Path, resp.Code())
	}
	if cf, err := resp.ContentFormat(); err != nil || cf != message.AppCBOR {
		return fmt.Errorf("GET %s: content-format %v, want 60", fixture.Path, cf)
	}
	raw, err := resp.ReadBody()
	if err != nil {
		return fmt.Errorf("read %s: %w", fixture.Path, err)
	}
	if _, err := verifier.Verify(raw, fixture.PrivateName, fixture.PrivateKey); err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	return nil
}
