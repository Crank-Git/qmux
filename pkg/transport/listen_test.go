package transport

import (
	"net"
	"testing"
)

// busyTCP returns a TCP bind function that holds each address before it binds it. Each
// bind then fails with the real error of a port in use. The function records each
// address. The held listeners close at the end of the test.
func busyTCP(t *testing.T, calls *[]string) func(network, address string) (net.Listener, error) {
	t.Helper()
	return func(network, address string) (net.Listener, error) {
		*calls = append(*calls, address)
		// A hold that fails means that another process already holds the port, and the
		// bind below fails for the same reason.
		if held, err := net.Listen(network, address); err == nil {
			t.Cleanup(func() { _ = held.Close() }) //nolint:errcheck // The listener only holds the port.
		}
		return net.Listen(network, address)
	}
}

// requireUDPFree fails the test when a UDP socket is still open on address.
func requireUDPFree(t *testing.T, address string) {
	t.Helper()
	pc, err := net.ListenPacket(UDP, address)
	if err != nil {
		t.Errorf("UDP %s is still open after the failed Listen: %v", address, err)
		return
	}
	_ = pc.Close() //nolint:errcheck // The socket only proved that the port is free.
}

func TestListenTakesANewPortWhenTheTCPPortOfAPort0BindIsInUse(t *testing.T) {
	var calls []string
	holdFirst := busyTCP(t, &calls)
	pc, l, err := listen("127.0.0.1:0", func(network, address string) (net.Listener, error) {
		if len(calls) == 0 {
			return holdFirst(network, address)
		}
		calls = append(calls, address)
		return net.Listen(network, address)
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close() //nolint:errcheck // The test only reads the addresses.
	defer l.Close()  //nolint:errcheck // The test only reads the addresses.

	if len(calls) < 2 {
		t.Fatalf("TCP binds %v, want a second bind after the held port", calls)
	}
	if got := pc.LocalAddr().String(); got == calls[0] {
		t.Errorf("UDP address %s, want a port other than the held TCP port", got)
	}
	if got, want := l.Addr().(*net.TCPAddr).Port, pc.LocalAddr().(*net.UDPAddr).Port; got != want {
		t.Errorf("TCP port %d, want the UDP port %d", got, want)
	}
	requireUDPFree(t, calls[0])
}

func TestListenReturnsTheBindErrorAfterTenPort0Attempts(t *testing.T) {
	var calls []string
	pc, l, err := listen("127.0.0.1:0", busyTCP(t, &calls))
	if err == nil {
		_ = pc.Close() //nolint:errcheck // The test fails on the nil error.
		_ = l.Close()  //nolint:errcheck // The test fails on the nil error.
		t.Fatal("listen returned nil, want the TCP bind error")
	}
	if len(calls) != listenAttempts {
		t.Errorf("TCP binds %d, want %d", len(calls), listenAttempts)
	}
	for _, address := range calls {
		requireUDPFree(t, address)
	}
}

func TestListenOnABusyFixedPortReturnsTheBindErrorAndLeavesNoSocketOpen(t *testing.T) {
	held, err := net.Listen(TCP, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("hold a TCP port: %v", err)
	}
	defer held.Close() //nolint:errcheck // The listener only holds the port.
	address := held.Addr().String()

	binds := 0
	pc, l, err := listen(address, func(network, address string) (net.Listener, error) {
		binds++
		return net.Listen(network, address)
	})
	if err == nil {
		_ = pc.Close() //nolint:errcheck // The test fails on the nil error.
		_ = l.Close()  //nolint:errcheck // The test fails on the nil error.
		t.Fatalf("listen %s returned nil, want the TCP bind error", address)
	}
	if binds != 1 {
		t.Errorf("TCP binds %d, want 1 for a fixed port", binds)
	}
	requireUDPFree(t, address)
}
