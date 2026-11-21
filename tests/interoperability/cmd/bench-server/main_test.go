package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/Crank-Git/qmux/tests/interoperability/baseline"
)

// syncBuffer is a bytes.Buffer that the server goroutine and the test share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

var readyLine = regexp.MustCompile(`^bench-server: dns (\S+) \(udp, tcp\), coap (\S+) \(udp\), https (\S+) \(tcp, TLS 1\.3\)$`)

func TestBenchServerServesEveryBaselineAfterTheReadyLine(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	stdoutR, stdoutW := io.Pipe()
	var stderr syncBuffer
	code := make(chan int, 1)
	go func() {
		code <- run(ctx, []string{"--dns", "127.0.0.1:0", "--coap", "127.0.0.1:0", "--https", "127.0.0.1:0"}, stdoutW, &stderr)
		stdoutW.Close() //nolint:errcheck // A pipe writer Close never fails.
	}()

	line, err := bufio.NewReader(stdoutR).ReadString('\n')
	if err != nil {
		cancel()
		t.Fatalf("read the ready line: %v; stderr:\n%s", err, stderr.String())
	}
	m := readyLine.FindStringSubmatch(strings.TrimSuffix(line, "\n"))
	if m == nil {
		cancel()
		t.Fatalf("ready line = %q, want the form %s", line, readyLine)
	}
	c, err := baseline.NewClient(baseline.Target{DNS: m[1], CoAP: m[2], HTTPS: m[3]})
	if err != nil {
		cancel()
		t.Fatalf("NewClient: %v", err)
	}
	for _, name := range baseline.Names {
		if _, err := c.Lookup(t.Context(), name); err != nil {
			t.Errorf("Lookup(%s): %v", name, err)
		}
	}
	c.Close()

	cancel()
	if got := <-code; got != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", got, stderr.String())
	}
	for _, a := range baseline.Assumptions() {
		if !strings.Contains(stderr.String(), `msg=assumption name="`+a.Name+`"`) {
			t.Errorf("stderr has no assumption record for %q:\n%s", a.Name, stderr.String())
		}
	}
}

func TestBenchServerExitsWithCode2ForAnUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run(t.Context(), []string{"--loss", "5"}, &stdout, &stderr); got != 2 {
		t.Fatalf("exit code = %d, want 2", got)
	}
}

func TestBenchServerExitsWithCode1WhenAListenerFails(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run(t.Context(), []string{"--dns", "256.0.0.1:0"}, &stdout, &stderr); got != 1 {
		t.Fatalf("exit code = %d, want 1; stderr:\n%s", got, stderr.String())
	}
}
