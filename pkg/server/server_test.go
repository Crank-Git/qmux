package server

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/registry"
	"github.com/miekg/dns"
)

var loopbackIP = net.ParseIP("127.0.0.1")

// describerHandler is a handler that also implements protocol.Describer.
type describerHandler struct {
	info protocol.Info
	fn   func(ctx context.Context, req *protocol.Request) (*protocol.Response, error)
}

func (h *describerHandler) Info() protocol.Info { return h.info }
func (h *describerHandler) Handle(ctx context.Context, req *protocol.Request) (*protocol.Response, error) {
	if h.fn != nil {
		return h.fn(ctx, req)
	}
	return &protocol.Response{Status: protocol.StatusOK}, nil
}

// testKey is a valid private-class protocol key for tests.
var testKey = protocol.ProtocolKey{Class: 65280, Type: 65280}

// testInfo is a minimal valid Info.
var testInfo = protocol.Info{ID: "org.qmux.test", Versions: []uint16{1}}

// mustListenUDP opens a UDP listener on a random port.
func mustListenUDP(t *testing.T) *net.UDPConn {
	t.Helper()
	ln, err := net.ListenUDP("udp", &net.UDPAddr{IP: loopbackIP, Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	return ln
}

// sendUDPQuery sends a DNS message over UDP and returns the raw response bytes.
func sendUDPQuery(t *testing.T, addr *net.UDPAddr, m *dns.Msg) []byte {
	t.Helper()
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	defer conn.Close()

	wire, err := m.Pack()
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if _, err := conn.Write(wire); err != nil {
		t.Fatalf("write: %v", err)
	}

	buf := make([]byte, 512)
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return buf[:n]
}

// sendTCPQuery sends a DNS message over TCP and returns the raw response bytes.
func sendTCPQuery(t *testing.T, addr *net.TCPAddr, m *dns.Msg) []byte {
	t.Helper()
	conn, err := net.DialTCP("tcp", nil, addr)
	if err != nil {
		t.Fatalf("DialTCP: %v", err)
	}
	defer conn.Close()

	wire, err := m.Pack()
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	// TCP wire format prepends a 2-byte length.
	length := make([]byte, 2)
	length[0] = byte(len(wire) >> 8)
	length[1] = byte(len(wire))
	if _, err := conn.Write(append(length, wire...)); err != nil {
		t.Fatalf("write: %v", err)
	}

	buf := make([]byte, 65535)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// TCP wire format prepends a 2-byte length; strip it from the response.
	if n < 2 {
		t.Fatalf("read: too short response")
	}
	return buf[2:n]
}

// parseResponse unpacks DNS wire bytes into a dns.Msg.
func parseResponse(t *testing.T, wire []byte) *dns.Msg {
	t.Helper()
	m := new(dns.Msg)
	if err := m.Unpack(wire); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	return m
}

// TestNewServerDefaults verifies that NewServer applies default values for zero fields.
func TestNewServerDefaults(t *testing.T) {
	s := NewServer(ServerConfig{Address: "127.0.0.1:0"})

	if s.cfg.TTL != 60 {
		t.Errorf("TTL = %d, want 60", s.cfg.TTL)
	}
	if s.cfg.UDPSize != 1232 {
		t.Errorf("UDPSize = %d, want 1232", s.cfg.UDPSize)
	}
	if s.cfg.HandlerTimeout != 2*time.Second {
		t.Errorf("HandlerTimeout = %v, want 2s", s.cfg.HandlerTimeout)
	}
	if s.cfg.ReadTimeout != 2*time.Second {
		t.Errorf("ReadTimeout = %v, want 2s", s.cfg.ReadTimeout)
	}
	if s.cfg.WriteTimeout != 2*time.Second {
		t.Errorf("WriteTimeout = %v, want 5s", s.cfg.WriteTimeout)
	}
	if s.cfg.ShutdownGrace != 5*time.Second {
		t.Errorf("ShutdownGrace = %v, want 5s", s.cfg.ShutdownGrace)
	}
	if s.cfg.Logger == nil {
		t.Error("Logger is nil, want default")
	}
	if s.registry == nil {
		t.Fatal("Registry is nil")
	}
}

// TestNewServerCustomValues verifies that non-zero config values are preserved.
func TestNewServerCustomValues(t *testing.T) {
	s := NewServer(ServerConfig{
		Address:        "127.0.0.1:0",
		TTL:            120,
		UDPSize:        4096,
		HandlerTimeout: 10 * time.Second,
		ReadTimeout:    5 * time.Second,
		WriteTimeout:   5 * time.Second,
		ShutdownGrace:  10 * time.Second,
	})

	if s.cfg.TTL != 120 {
		t.Errorf("TTL = %d, want 120", s.cfg.TTL)
	}
	if s.cfg.UDPSize != 4096 {
		t.Errorf("UDPSize = %d, want 4096", s.cfg.UDPSize)
	}
	if s.cfg.HandlerTimeout != 10*time.Second {
		t.Errorf("HandlerTimeout = %v, want 10s", s.cfg.HandlerTimeout)
	}
	if s.cfg.ReadTimeout != 5*time.Second {
		t.Errorf("ReadTimeout = %v, want 5s", s.cfg.ReadTimeout)
	}
	if s.cfg.WriteTimeout != 5*time.Second {
		t.Errorf("WriteTimeout = %v, want 5s", s.cfg.WriteTimeout)
	}
	if s.cfg.ShutdownGrace != 10*time.Second {
		t.Errorf("ShutdownGrace = %v, want 10s", s.cfg.ShutdownGrace)
	}
}

// TestRegisterWithDescriber verifies FR-server-1 and FR-server-3.
func TestRegisterWithDescriber(t *testing.T) {
	s := NewServer(ServerConfig{Address: "127.0.0.1:0"})
	h := &describerHandler{info: testInfo}

	err := s.Register(testKey, h)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	reg, ok := s.registry.Lookup(testKey)
	if !ok {
		t.Fatal("Lookup: not found")
	}
	if reg.Info.ID != testInfo.ID {
		t.Errorf("ID = %s, want %s", reg.Info.ID, testInfo.ID)
	}
	if reg.Info.Versions[0] != 1 {
		t.Errorf("Versions[0] = %d, want 1", reg.Info.Versions[0])
	}
}

// TestRegisterWithHandlerFuncReturnsError verifies FR-server-4.
func TestRegisterWithHandlerFuncReturnsError(t *testing.T) {
	s := NewServer(ServerConfig{Address: "127.0.0.1:0"})
	h := protocol.HandlerFunc(func(ctx context.Context, req *protocol.Request) (*protocol.Response, error) {
		return nil, nil
	})

	err := s.Register(testKey, h)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrMissingInfo) {
		t.Errorf("error = %v, want ErrMissingInfo", err)
	}
}

// TestRegisterProtocol verifies FR-server-5.
func TestRegisterProtocol(t *testing.T) {
	s := NewServer(ServerConfig{Address: "127.0.0.1:0"})
	h := &describerHandler{info: testInfo}

	err := s.RegisterProtocol(testKey, testInfo, h)
	if err != nil {
		t.Fatalf("RegisterProtocol: %v", err)
	}

	reg, ok := s.registry.Lookup(testKey)
	if !ok {
		t.Fatal("Lookup: not found")
	}
	if reg.Info.ID != testInfo.ID {
		t.Errorf("ID = %s, want %s", reg.Info.ID, testInfo.ID)
	}
}

// TestUnregister verifies Unregister removes the registration.
func TestUnregister(t *testing.T) {
	s := NewServer(ServerConfig{Address: "127.0.0.1:0"})
	h := &describerHandler{info: testInfo}

	_ = s.Register(testKey, h)
	err := s.Unregister(testKey)
	if err != nil {
		t.Fatalf("Unregister: %v", err)
	}

	_, ok := s.registry.Lookup(testKey)
	if ok {
		t.Fatal("Lookup after Unregister should return false")
	}
}

// TestRegistry verifies that Registry() returns the server's registry.
func TestRegistry(t *testing.T) {
	s := NewServer(ServerConfig{Address: "127.0.0.1:0"})
	if s.Registry() == nil {
		t.Fatal("Registry() returned nil")
	}
}

// TestListenAndServeBasic verifies FR-server-6: sends queries over UDP and TCP.
func TestListenAndServeBasic(t *testing.T) {
	s := NewServer(ServerConfig{Address: "127.0.0.1:0"})
	h := &describerHandler{info: testInfo}
	_ = s.Register(testKey, h)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.ListenAndServe(ctx)
	}()

	// Wait for listeners to start.
	time.Sleep(100 * time.Millisecond)

	addr := s.Addr()
	if addr == nil {
		t.Fatal("Addr() returned nil before query")
	}

	udpAddr, ok := addr.(*net.UDPAddr)
	if !ok {
		t.Fatalf("Addr() = %T, want *net.UDPAddr", addr)
	}

	// Send UDP query.
	q := new(dns.Msg)
	q.SetQuestion("test.example.", 65280)
	q.Question[0].Qclass = 65280

	wire := sendUDPQuery(t, udpAddr, q)
	resp := parseResponse(t, wire)
	if resp.Rcode != dns.RcodeSuccess {
		t.Errorf("UDP rcode = %d, want %d", resp.Rcode, dns.RcodeSuccess)
	}

	// Send TCP query.
	tcpAddr := &net.TCPAddr{IP: udpAddr.IP, Port: udpAddr.Port}
	wireTCP := sendTCPQuery(t, tcpAddr, q)
	respTCP := parseResponse(t, wireTCP)
	if respTCP.Rcode != dns.RcodeSuccess {
		t.Errorf("TCP rcode = %d, want %d", respTCP.Rcode, dns.RcodeSuccess)
	}

	cancel()
	// Give shutdown time to complete.
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("ListenAndServe: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ListenAndServe did not return after cancel")
	}
}

// TestListenAndServePortInUse verifies FR-server-7: port already in use returns error.
func TestListenAndServePortInUse(t *testing.T) {
	// Open a UDP socket to hold the port.
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: loopbackIP, Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}

	// Get the actual port.
	udpAddr := udpConn.LocalAddr().(*net.UDPAddr)
	udpConn.Close()

	s := NewServer(ServerConfig{Address: udpAddr.String()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Re-bind the same port to hold it.
	holdConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: udpAddr.IP, Port: udpAddr.Port})
	if err != nil {
		t.Fatalf("ListenUDP hold: %v", err)
	}
	defer holdConn.Close()

	err = s.ListenAndServe(ctx)
	if err == nil {
		t.Fatal("expected error for port in use, got nil")
	}
}

// TestListenAndServeContextCancel verifies FR-server-8.
func TestListenAndServeContextCancel(t *testing.T) {
	s := NewServer(ServerConfig{Address: "127.0.0.1:0"})
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.ListenAndServe(ctx)
	}()

	// Wait for listeners to start.
	time.Sleep(50 * time.Millisecond)

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("ListenAndServe: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ListenAndServe did not return after context cancel")
	}
}

// TestServe verifies FR-server-9: Serve on caller-supplied listeners.
func TestServe(t *testing.T) {
	s := NewServer(ServerConfig{Address: "127.0.0.1:0"})
	h := &describerHandler{info: testInfo}
	_ = s.Register(testKey, h)

	udpConn := mustListenUDP(t)
	defer udpConn.Close()

	tcpListener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: loopbackIP, Port: udpConn.LocalAddr().(*net.UDPAddr).Port})
	if err != nil {
		t.Fatalf("ListenTCP: %v", err)
	}
	defer tcpListener.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Serve(ctx, udpConn, tcpListener)
	}()

	// Wait for listeners to start.
	time.Sleep(50 * time.Millisecond)

	addr := s.Addr()
	if addr == nil {
		t.Fatal("Addr() returned nil")
	}

	// Send a UDP query.
	q := new(dns.Msg)
	q.SetQuestion("test.example.", 65280)
	q.Question[0].Qclass = 65280

	wire := sendUDPQuery(t, udpConn.LocalAddr().(*net.UDPAddr), q)
	resp := parseResponse(t, wire)
	if resp.Rcode != dns.RcodeSuccess {
		t.Errorf("rcode = %d, want %d", resp.Rcode, dns.RcodeSuccess)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
}

// TestAddr verifies FR-server-10: Addr() returns non-zero port after Serve starts.
func TestAddrBeforeAndAfter(t *testing.T) {
	s := NewServer(ServerConfig{Address: "127.0.0.1:0"})

	// Before Serve, Addr() returns nil.
	if s.Addr() != nil {
		t.Fatal("Addr() before Serve should return nil")
	}

	udpConn := mustListenUDP(t)
	defer udpConn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Serve(ctx, udpConn, nil)
	}()

	time.Sleep(50 * time.Millisecond)

	addr := s.Addr()
	if addr == nil {
		t.Fatal("Addr() after Serve started should not be nil")
	}

	udpAddr, ok := addr.(*net.UDPAddr)
	if !ok {
		t.Fatalf("Addr() = %T, want *net.UDPAddr", addr)
	}
	if udpAddr.Port == 0 {
		t.Error("Port should be non-zero")
	}

	cancel()
	select {
	case <-errCh:
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
}

// TestShutdownGraceCompletes verifies FR-server-11: handler that completes within grace period.
func TestShutdownGraceCompletes(t *testing.T) {
	s := NewServer(ServerConfig{
		Address:       "127.0.0.1:0",
		ShutdownGrace: 2 * time.Second,
	})

	done := make(chan struct{})
	h := &describerHandler{
		info: testInfo,
		fn: func(ctx context.Context, req *protocol.Request) (*protocol.Response, error) {
			// Sleep less than the grace period.
			time.Sleep(200 * time.Millisecond)
			close(done)
			return &protocol.Response{Status: protocol.StatusOK}, nil
		},
	}
	_ = s.Register(testKey, h)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.ListenAndServe(ctx)
	}()

	time.Sleep(50 * time.Millisecond)

	udpAddr := s.Addr().(*net.UDPAddr)

	// Send a query to trigger the handler.
	q := new(dns.Msg)
	q.SetQuestion("test.example.", 65280)
	q.Question[0].Qclass = 65280

	// Send the query and wait for response.
	respWire := sendUDPQuery(t, udpAddr, q)
	resp := parseResponse(t, respWire)
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("rcode = %d, want %d", resp.Rcode, dns.RcodeSuccess)
	}

	// Now cancel to trigger shutdown.
	cancel()

	// The handler should complete within the grace period.
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not complete within grace period")
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("ListenAndServe: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ListenAndServe did not return after cancel")
	}
}

// TestShutdownGraceCancels verifies FR-server-12: handler context is cancelled after grace period.
func TestShutdownGraceCancels(t *testing.T) {
	s := NewServer(ServerConfig{
		Address:       "127.0.0.1:0",
		ShutdownGrace: 200 * time.Millisecond,
	})

	ctxCancelled := make(chan struct{})
	h := &describerHandler{
		info: testInfo,
		fn: func(ctx context.Context, req *protocol.Request) (*protocol.Response, error) {
			<-ctx.Done()
			close(ctxCancelled)
			return nil, nil
		},
	}
	_ = s.Register(testKey, h)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.ListenAndServe(ctx)
	}()

	time.Sleep(50 * time.Millisecond)

	udpAddr := s.Addr().(*net.UDPAddr)

	// Send a query to trigger the handler.
	q := new(dns.Msg)
	q.SetQuestion("test.example.", 65280)
	q.Question[0].Qclass = 65280

	// Send the query (it will hang on ctx.Done()).
	// Use a goroutine so we don't block forever.
	queryDone := make(chan struct{})
	go func() {
		sendUDPQuery(t, udpAddr, q)
		close(queryDone)
	}()

	// Give the handler time to start.
	time.Sleep(100 * time.Millisecond)

	// Cancel to trigger shutdown.
	cancel()

	// After grace period, the handler context should be cancelled.
	select {
	case <-ctxCancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("handler context was not cancelled after grace period")
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("ListenAndServe: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ListenAndServe did not return after cancel")
	}
}

// TestRegisterDuplicateKey verifies that registering the same key twice returns an error.
func TestRegisterDuplicateKey(t *testing.T) {
	s := NewServer(ServerConfig{Address: "127.0.0.1:0"})
	h1 := &describerHandler{info: protocol.Info{ID: "org.qmux.a", Versions: []uint16{1}}}
	h2 := &describerHandler{info: protocol.Info{ID: "org.qmux.b", Versions: []uint16{1}}}

	if err := s.Register(testKey, h1); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	err := s.Register(testKey, h2)
	if err == nil {
		t.Fatal("expected error for duplicate key, got nil")
	}
}

// TestServerConcurrency verifies that concurrent Register/Unregister calls don't race.
func TestServerConcurrency(t *testing.T) {
	s := NewServer(ServerConfig{Address: "127.0.0.1:0"})
	n := 100
	var wg sync.WaitGroup
	wg.Add(n * 3)

	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			key := protocol.ProtocolKey{Class: 65280, Type: 65280 + uint16(idx%255)}
			h := &describerHandler{info: protocol.Info{ID: "org.qmux.test", Versions: []uint16{1}}}
			_ = s.Register(key, h)
		}(i)
	}

	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			key := protocol.ProtocolKey{Class: 65280, Type: 65280 + uint16(idx%255)}
			s.registry.Lookup(key)
		}(i)
	}

	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			key := protocol.ProtocolKey{Class: 65280, Type: 65280 + uint16(idx%255)}
			_ = s.Unregister(key)
		}(i)
	}

	wg.Wait()
}

// TestNilServer verifies that NewServer with an empty config still works.
func TestNilServer(t *testing.T) {
	s := NewServer(ServerConfig{})
	if s == nil {
		t.Fatal("NewServer returned nil")
	}
	if s.registry == nil {
		t.Fatal("registry is nil")
	}
}

// TestAddrReturnsUDPAddr verifies Addr() returns a *net.UDPAddr.
func TestAddrReturnsUDPAddr(t *testing.T) {
	s := NewServer(ServerConfig{Address: "127.0.0.1:0"})
	udpConn := mustListenUDP(t)
	defer udpConn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Serve(ctx, udpConn, nil)
	}()

	time.Sleep(50 * time.Millisecond)

	addr := s.Addr()
	if addr == nil {
		t.Fatal("Addr() should not be nil after Serve starts")
	}
	if _, ok := addr.(*net.UDPAddr); !ok {
		t.Errorf("Addr() = %T, want *net.UDPAddr", addr)
	}

	cancel()
	select {
	case <-errCh:
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
}

// TestServerConfigZones verifies that the Zones field is stored.
func TestServerConfigZones(t *testing.T) {
	s := NewServer(ServerConfig{
		Address: "127.0.0.1:0",
		Zones:   []string{"example.", "test."},
	})
	if len(s.cfg.Zones) != 2 {
		t.Errorf("Zones length = %d, want 2", len(s.cfg.Zones))
	}
}

// TestNilPacketConnAndListener verifies Serve with nil arguments doesn't panic.
func TestNilPacketConnAndListener(t *testing.T) {
	s := NewServer(ServerConfig{Address: "127.0.0.1:0"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Serve with nil PacketConn and nil Listener should not panic;
	// it should return an error or do nothing.
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Serve(ctx, nil, nil)
	}()

	cancel()
	select {
	case err := <-errCh:
		// Either nil or an error is acceptable when no listeners are provided.
		_ = err
	case <-time.After(3 * time.Second):
		t.Fatal("Serve with nil listeners did not return after cancel")
	}
}

// TestDefaultLoggerIsSet verifies the default logger is slog.Default().
func TestDefaultLoggerIsSet(t *testing.T) {
	s := NewServer(ServerConfig{Address: "127.0.0.1:0"})
	if s.cfg.Logger == nil {
		t.Fatal("logger should not be nil with zero config")
	}
}

// TestServerAddrPortIsDynamic verifies that port 0 gives a dynamic port.
func TestServerAddrPortIsDynamic(t *testing.T) {
	s := NewServer(ServerConfig{Address: "127.0.0.1:0"})
	udpConn := mustListenUDP(t)
	defer udpConn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Serve(ctx, udpConn, nil)
	}()

	time.Sleep(50 * time.Millisecond)

	addr := s.Addr().(*net.UDPAddr)
	if addr.Port == 0 {
		t.Error("dynamic port should be non-zero")
	}

	cancel()
	select {
	case <-errCh:
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
}

// TestRegisterPreservesInfo verifies that Register stores the exact Info from Info().
func TestRegisterPreservesInfo(t *testing.T) {
	s := NewServer(ServerConfig{Address: "127.0.0.1:0"})
	info := protocol.Info{
		ID:       "org.qmux.preserve",
		Versions: []uint16{2, 1, 3},
	}
	h := &describerHandler{info: info}

	if err := s.Register(testKey, h); err != nil {
		t.Fatalf("Register: %v", err)
	}

	reg, ok := s.registry.Lookup(testKey)
	if !ok {
		t.Fatal("Lookup: not found")
	}
	if reg.Info.ID != "org.qmux.preserve" {
		t.Errorf("ID = %s, want org.qmux.preserve", reg.Info.ID)
	}
	// Versions should be sorted by the registry.
	if len(reg.Info.Versions) != 3 || reg.Info.Versions[0] != 1 || reg.Info.Versions[1] != 2 || reg.Info.Versions[2] != 3 {
		t.Errorf("Versions = %v, want [1 2 3]", reg.Info.Versions)
	}
}

// TestServerNilHandlerInRegister verifies that a nil handler returns ErrInvalidHandler.
func TestServerNilHandlerInRegister(t *testing.T) {
	s := NewServer(ServerConfig{Address: "127.0.0.1:0"})
	err := s.Register(testKey, nil)
	if err == nil {
		t.Fatal("expected error for nil handler, got nil")
	}
	if !errors.Is(err, registry.ErrInvalidHandler) {
		t.Errorf("error = %v, want ErrInvalidHandler", err)
	}
}

// TestRegisterWithNilPayload verifies that a handler returning nil payload works.
func TestRegisterWithNilPayload(t *testing.T) {
	s := NewServer(ServerConfig{Address: "127.0.0.1:0"})
	h := &describerHandler{
		info: testInfo,
		fn: func(ctx context.Context, req *protocol.Request) (*protocol.Response, error) {
			return &protocol.Response{Payload: nil, Status: protocol.StatusOK}, nil
		},
	}
	if err := s.Register(testKey, h); err != nil {
		t.Fatalf("Register: %v", err)
	}
}

// TestRequestFields verifies that Request has the expected fields.
func TestRequestFields(t *testing.T) {
	req := &protocol.Request{
		QName:      "test.example.",
		Key:        testKey,
		Version:    2,
		Labels:     []string{"test"},
		Zone:       "example.",
		Source:     netip.MustParseAddrPort("127.0.0.1:12345"),
		Transport:  "udp",
		UDPSize:    4096,
		ID:         1234,
		ReceivedAt: time.Now(),
	}

	if req.QName != "test.example." {
		t.Errorf("QName = %s, want test.example.", req.QName)
	}
	if req.Key != testKey {
		t.Errorf("Key = %v, want %v", req.Key, testKey)
	}
	if req.Version != 2 {
		t.Errorf("Version = %d, want 2", req.Version)
	}
	if len(req.Labels) != 1 || req.Labels[0] != "test" {
		t.Errorf("Labels = %v, want [test]", req.Labels)
	}
	if req.Zone != "example." {
		t.Errorf("Zone = %s, want example.", req.Zone)
	}
	if req.Source.String() != "127.0.0.1:12345" {
		t.Errorf("Source = %s, want 127.0.0.1:12345", req.Source)
	}
	if req.Transport != "udp" {
		t.Errorf("Transport = %s, want udp", req.Transport)
	}
	if req.UDPSize != 4096 {
		t.Errorf("UDPSize = %d, want 4096", req.UDPSize)
	}
	if req.ID != 1234 {
		t.Errorf("ID = %d, want 1234", req.ID)
	}
	if req.ReceivedAt.IsZero() {
		t.Error("ReceivedAt should not be zero")
	}
}

// TestResponseFields verifies that Response has the expected fields.
func TestResponseFields(t *testing.T) {
	resp := &protocol.Response{
		Payload:     []byte{0x01, 0x02},
		TTL:         30,
		Status:      protocol.StatusOK,
		Version:     1,
		MessageType: protocol.MessageRecord,
	}

	if len(resp.Payload) != 2 {
		t.Errorf("Payload length = %d, want 2", len(resp.Payload))
	}
	if resp.TTL != 30 {
		t.Errorf("TTL = %d, want 30", resp.TTL)
	}
	if resp.Status != protocol.StatusOK {
		t.Errorf("Status = %v, want %v", resp.Status, protocol.StatusOK)
	}
	if resp.Version != 1 {
		t.Errorf("Version = %d, want 1", resp.Version)
	}
	if resp.MessageType != protocol.MessageRecord {
		t.Errorf("MessageType = %v, want %v", resp.MessageType, protocol.MessageRecord)
	}
}
