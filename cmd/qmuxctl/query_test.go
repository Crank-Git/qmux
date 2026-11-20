package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/security"
	"github.com/Crank-Git/qmux/pkg/server"
)

const testName = "device42.fleet.example."

var (
	testKey     = protocol.ProtocolKey{Class: 65280, Type: 65280}
	testInfo    = protocol.Info{ID: "test.device", Versions: []uint16{1, 2}}
	testPayload = []byte{0xa1, 0x01, 0x6b, 0xff}
)

// startServer runs a server that answers testKey with testPayload on 127.0.0.1:0 until
// the test ends. It returns the server address.
func startServer(t *testing.T) string {
	t.Helper()
	return startServerWith(t, protocol.Response{Payload: testPayload, TTL: 60})
}

// startServerWith runs a server that answers testKey with resp on 127.0.0.1:0 until the
// test ends. It returns the server address.
func startServerWith(t *testing.T, resp protocol.Response) string {
	t.Helper()
	s := server.NewServer(server.ServerConfig{
		Address: "127.0.0.1:0",
		Logger:  slog.New(slog.DiscardHandler),
	})
	h := protocol.HandlerFunc(func(context.Context, *protocol.Request) (*protocol.Response, error) {
		return &resp, nil
	})
	if err := s.RegisterProtocol(testKey, testInfo, h); err != nil {
		t.Fatalf("RegisterProtocol: %v", err)
	}
	return serve(t, s)
}

// serve runs s until the test ends and returns the server address.
func serve(t *testing.T, s *server.Server) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- s.ListenAndServe(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-errc; err != nil {
			t.Errorf("ListenAndServe: %v", err)
		}
	})

	// Addr is non-nil only after both listeners are bound, so it is the readiness signal.
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if a := s.Addr(); a != nil {
			return a.String()
		}
		select {
		case err := <-errc:
			t.Fatalf("server returned before its listeners started: %v", err)
		case <-deadline:
			t.Fatal("Addr is nil 2s after the start")
		case <-tick.C:
		}
	}
}

// queryCmd runs "qmuxctl query" with args and returns the exit code, stdout and stderr.
func queryCmd(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(append([]string{"qmuxctl", "query"}, args...), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func keyArgs(addr string) []string {
	return []string{"--server", addr, "--class", "65280", "--type", "65280", "--name", testName}
}

func TestQueryHelpListsTheEightFlags(t *testing.T) {
	code, stdout, _ := queryCmd("--help")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	for _, f := range []string{"--server", "--class", "--type", "--name", "--version", "--tcp", "--timeout", "--json"} {
		if !strings.Contains(stdout, f) {
			t.Errorf("help text has no %s:\n%s", f, stdout)
		}
	}
}

func TestQueryPrintsOneKeyValueLinePerResultFieldWithTheHexPayloadAndExitsZero(t *testing.T) {
	addr := startServer(t)
	code, stdout, stderr := queryCmd(keyArgs(addr)...)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	want := []string{
		"name", "class", "type", "rcode", "ttl", "transport", "fell_back", "rtt_ms",
		"query_bytes", "response_bytes", "verified", "protocol_id", "version", "message_type",
		"status", "payload_bytes", "payload",
	}
	if len(lines) != len(want) {
		t.Fatalf("stdout has %d lines, want %d:\n%s", len(lines), len(want), stdout)
	}
	for i, k := range want {
		if !strings.HasPrefix(lines[i], k+": ") {
			t.Errorf("line %d = %q, want the key %q", i+1, lines[i], k)
		}
	}
	for _, l := range []string{
		"name: " + testName, "class: 65280", "type: 65280", "rcode: NOERROR", "ttl: 60",
		"transport: udp", "fell_back: false", "verified: false", "protocol_id: test.device",
		"version: 2", "message_type: record", "status: ok", "payload_bytes: 4", "payload: a1016bff",
	} {
		if !strings.Contains(stdout, l+"\n") {
			t.Errorf("stdout has no line %q:\n%s", l, stdout)
		}
	}
}

func TestQueryJSONPrintsTheResultAsOneJSONObject(t *testing.T) {
	addr := startServer(t)
	code, stdout, stderr := queryCmd(append(keyArgs(addr), "--json")...)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr)
	}
	dec := json.NewDecoder(strings.NewReader(stdout))
	var got map[string]any
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		t.Errorf("stdout holds more than one JSON value: %q", stdout)
	}
	if got["payload"] != "a1016bff" || got["name"] != testName || got["class"] != 65280.0 {
		t.Errorf("JSON = %v, want payload a1016bff, name %s, class 65280", got, testName)
	}
	if got["protocol_id"] != "test.device" || got["version"] != 2.0 || got["message_type"] != "record" ||
		got["status"] != "ok" || got["payload_bytes"] != 4.0 {
		t.Errorf("JSON = %v, want protocol_id test.device, version 2, message_type record, status ok, payload_bytes 4", got)
	}
}

func TestQueryPrintsTheResultAndTheAppErrorAndExitsTwoForANotFoundStatus(t *testing.T) { // FR-codec-42
	addr := startServerWith(t, protocol.Response{Status: protocol.StatusNotFound})
	code, stdout, stderr := queryCmd(keyArgs(addr)...)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "name: "+testName+"\n") || !strings.Contains(stdout, "status: not-found\n") {
		t.Errorf("stdout = %q, want the full result with the line %q", stdout, "status: not-found")
	}
	want := "error: client: application status 1 from test.device version 2\n"
	if stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
}

func TestQueryPrintsAProtocolSpecificMessageTypeAndStatusAsDecimalNumbers(t *testing.T) { // FR-codec-42
	addr := startServerWith(t, protocol.Response{MessageType: 256, Status: 300})
	code, stdout, stderr := queryCmd(keyArgs(addr)...)
	if code != 2 || !strings.Contains(stdout, "message_type: 256\n") || !strings.Contains(stdout, "status: 300\n") {
		t.Errorf("exit code = %d, stdout = %q, stderr = %q; want 2, message_type 256 and status 300", code, stdout, stderr)
	}
}

func TestStatusNameNamesEachFrameworkStatusAndPrintsOthersInDecimal(t *testing.T) { // FR-codec-42
	for status, want := range map[protocol.Status]string{
		0: "ok", 1: "not-found", 2: "bad-request", 3: "version-unsupported", 4: "unavailable",
		5: "5", 255: "255", 256: "256",
	} {
		if got := statusName(status); got != want {
			t.Errorf("statusName(%d) = %q, want %q", status, got, want)
		}
	}
}

func TestMessageTypeNameNamesRecordAndManifestAndPrintsOthersInDecimal(t *testing.T) { // FR-codec-42
	for mt, want := range map[protocol.MessageType]string{1: "record", 2: "manifest", 256: "256", 65535: "65535"} {
		if got := messageTypeName(mt); got != want {
			t.Errorf("messageTypeName(%d) = %q, want %q", mt, got, want)
		}
	}
}

func TestQueryTCPSendsTheQueryOverTCP(t *testing.T) {
	addr := startServer(t)
	code, stdout, stderr := queryCmd(append(keyArgs(addr), "--tcp")...)
	if code != 0 || !strings.Contains(stdout, "transport: tcp\n") {
		t.Errorf("exit code = %d, stdout = %q, stderr = %q; want 0 and transport tcp", code, stdout, stderr)
	}
}

func TestQueryVersionAddsTheVersionLabel(t *testing.T) {
	addr := startServer(t)
	code, stdout, stderr := queryCmd(append(keyArgs(addr), "--version", "2")...)
	if code != 0 || !strings.Contains(stdout, "name: _v2."+testName+"\n") {
		t.Errorf("exit code = %d, stdout = %q, stderr = %q; want 0 and the name _v2.%s", code, stdout, stderr, testName)
	}
}

func TestQueryExitsOneForAnUnregisteredProtocolKey(t *testing.T) {
	addr := startServer(t)
	code, stdout, stderr := queryCmd("--server", addr, "--class", "65283", "--type", "65280", "--name", testName)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr = %q", code, stderr)
	}
	want := "error: dns: rcode REFUSED for device42.fleet.example. 65283/65280\n"
	if stdout != "" || stderr != want {
		t.Errorf("stdout = %q, stderr = %q; want only the error line %q on stderr", stdout, stderr, want)
	}
}

func TestQueryPrintsTheTimeoutScreenAndExitsOneWhenNoResponseArrives(t *testing.T) {
	// The socket reads nothing and answers nothing, so every attempt times out.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	addr := pc.LocalAddr().String()

	code, stdout, stderr := queryCmd(append(keyArgs(addr), "--timeout", "50ms")...)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr = %q", code, stderr)
	}
	want := "error: dns: timeout after 3 attempts to " + addr + "\n"
	if stdout != "" || stderr != want {
		t.Errorf("stdout = %q, stderr = %q; want only the error line %q on stderr", stdout, stderr, want)
	}
}

func TestQueryWithoutNameExitsSixtyFourAndPrintsTheUsage(t *testing.T) {
	code, stdout, stderr := queryCmd("--server", "127.0.0.1:5353", "--class", "65280", "--type", "65280")
	if code != 64 {
		t.Fatalf("exit code = %d, want 64", code)
	}
	if stdout != "" || !strings.Contains(stderr, "Usage: qmuxctl query") || !strings.Contains(stderr, "--name") {
		t.Errorf("stdout = %q, stderr = %q; want the usage on stderr only", stdout, stderr)
	}
}

func TestQueryWithAnInvalidFlagExitsSixtyFourAndPrintsTheUsage(t *testing.T) {
	base := keyArgs("127.0.0.1:5353")
	cases := map[string][]string{
		"no server":           {"--class", "65280", "--type", "65280", "--name", testName},
		"no class":            {"--server", "127.0.0.1:5353", "--type", "65280", "--name", testName},
		"no type":             {"--server", "127.0.0.1:5353", "--class", "65280", "--name", testName},
		"class above 65535":   append(base, "--class", "65536"),
		"type not a number":   append(base, "--type", "txt"),
		"version above 65535": append(base, "--version", "70000"),
		"negative timeout":    append(base, "--timeout", "-1s"),
		"unknown flag":        append(base, "--bogus"),
		"extra argument":      append(base, "extra"),
		// A label of 64 bytes is longer than DNS allows, so the client cannot build the query.
		"name label too long": {"--server", "127.0.0.1:5353", "--class", "65280", "--type", "65280",
			"--name", strings.Repeat("a", 64) + ".example."},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr := queryCmd(args...)
			if code != 64 {
				t.Fatalf("exit code = %d, want 64; stderr = %q", code, stderr)
			}
			if stdout != "" || !strings.Contains(stderr, "Usage: qmuxctl query") {
				t.Errorf("stdout = %q, stderr = %q; want the usage on stderr only", stdout, stderr)
			}
		})
	}
}

func TestQueryExitsOneWhenStdoutFails(t *testing.T) {
	addr := startServer(t)
	var stderr bytes.Buffer
	if code := run(append([]string{"qmuxctl", "query"}, keyArgs(addr)...), failingWriter{}, &stderr); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}

func TestExitCodeMapsEachOutcomeToItsExitCode(t *testing.T) {
	cases := map[string]struct {
		err  error
		want int
	}{
		"a result":            {nil, 0},
		"a DNSError":          {&client.DNSError{Kind: client.KindTimeout}, 1},
		"an AppError":         {&client.AppError{Status: protocol.StatusNotFound}, 2},
		"a wrapped AppError":  {fmt.Errorf("query: %w", &client.AppError{}), 2},
		"a VerifyError":       {&client.VerifyError{Reason: security.ErrExpired}, 3},
		"a plain query error": {errors.New("client: transport: pack query"), 64},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := exitCode(tc.err); got != tc.want {
				t.Errorf("exitCode(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}
