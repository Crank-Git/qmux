package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/registry"
	"github.com/Crank-Git/qmux/pkg/transport"
	"github.com/Crank-Git/qmux/pkg/transport/transporttest"
)

var testKey = protocol.ProtocolKey{Class: 65280, Type: 65280}

var testInfo = protocol.Info{ID: "test.qmux", Versions: []uint16{1}}

// describedHandler is a handler that states its own Info.
type describedHandler struct {
	protocol.HandlerFunc
	info protocol.Info
}

func (h describedHandler) Info() protocol.Info { return h.info }

// payloadHandler returns a handler that answers every request with payload.
func payloadHandler(payload []byte) protocol.HandlerFunc {
	return func(context.Context, *protocol.Request) (*protocol.Response, error) {
		return &protocol.Response{Payload: payload}, nil
	}
}

// quietLogger discards every record, so that the test output holds only failures.
func quietLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestNewServerRegistryAcceptsValidRegistration(t *testing.T) {
	s := NewServer(ServerConfig{})
	if err := s.Registry().Register(testKey, testInfo, payloadHandler(nil)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, ok := s.Registry().Lookup(testKey); !ok {
		t.Fatal("Lookup: no registration after Register")
	}
}

func TestNewServerBuildsRegistryWithConfiguredOptions(t *testing.T) {
	inClassA := protocol.ProtocolKey{Class: 1, Type: 1}

	strict := NewServer(ServerConfig{})
	if err := strict.Registry().Register(inClassA, testInfo, payloadHandler(nil)); !errors.Is(err, registry.ErrInvalidKey) {
		t.Fatalf("Register with zero options: got %v, want ErrInvalidKey", err)
	}

	wide := NewServer(ServerConfig{Registry: registry.Options{AllowNonPrivateTypes: true}})
	if err := wide.Registry().Register(inClassA, testInfo, payloadHandler(nil)); err != nil {
		t.Fatalf("Register with AllowNonPrivateTypes: %v", err)
	}
}

func TestNewServerAppliesDefaultsToZeroFields(t *testing.T) {
	cfg := NewServer(ServerConfig{}).cfg
	if cfg.TTL != 60 {
		t.Errorf("TTL = %d, want 60", cfg.TTL)
	}
	if cfg.UDPSize != 1232 {
		t.Errorf("UDPSize = %d, want 1232", cfg.UDPSize)
	}
	if cfg.HandlerTimeout != 2*time.Second {
		t.Errorf("HandlerTimeout = %v, want 2s", cfg.HandlerTimeout)
	}
	if cfg.ReadTimeout != 2*time.Second {
		t.Errorf("ReadTimeout = %v, want 2s", cfg.ReadTimeout)
	}
	if cfg.WriteTimeout != 2*time.Second {
		t.Errorf("WriteTimeout = %v, want 2s", cfg.WriteTimeout)
	}
	if cfg.ShutdownGrace != 5*time.Second {
		t.Errorf("ShutdownGrace = %v, want 5s", cfg.ShutdownGrace)
	}
	if cfg.Logger != slog.Default() {
		t.Error("Logger is not slog.Default()")
	}
}

func TestNewServerKeepsNonZeroFields(t *testing.T) {
	logger := quietLogger()
	in := ServerConfig{
		TTL:            5,
		UDPSize:        4096,
		HandlerTimeout: time.Second,
		ReadTimeout:    3 * time.Second,
		WriteTimeout:   4 * time.Second,
		ShutdownGrace:  time.Millisecond,
		Logger:         logger,
	}
	cfg := NewServer(in).cfg
	if cfg.TTL != in.TTL || cfg.UDPSize != in.UDPSize || cfg.HandlerTimeout != in.HandlerTimeout ||
		cfg.ReadTimeout != in.ReadTimeout || cfg.WriteTimeout != in.WriteTimeout ||
		cfg.ShutdownGrace != in.ShutdownGrace || cfg.Logger != logger {
		t.Fatalf("NewServer changed a non-zero field: got %+v, want %+v", cfg, in)
	}
}

func TestRegisterUsesTheInfoOfADescriberHandler(t *testing.T) {
	s := NewServer(ServerConfig{})
	h := describedHandler{HandlerFunc: payloadHandler(nil), info: testInfo}
	if err := s.Register(testKey, h); err != nil {
		t.Fatalf("Register: %v", err)
	}
	reg, ok := s.Registry().Lookup(testKey)
	if !ok {
		t.Fatal("Lookup: no registration after Register")
	}
	if reg.Info.ID != testInfo.ID || len(reg.Info.Versions) != 1 || reg.Info.Versions[0] != 1 {
		t.Fatalf("Info = %+v, want %+v", reg.Info, testInfo)
	}
}

func TestRegisterReturnsErrMissingInfoForAPlainHandlerFunc(t *testing.T) {
	s := NewServer(ServerConfig{})
	if err := s.Register(testKey, payloadHandler(nil)); !errors.Is(err, ErrMissingInfo) {
		t.Fatalf("Register: got %v, want ErrMissingInfo", err)
	}
	if _, ok := s.Registry().Lookup(testKey); ok {
		t.Fatal("Lookup: Register added a registration after ErrMissingInfo")
	}
}

func TestRegisterReturnsTheRegistryError(t *testing.T) {
	s := NewServer(ServerConfig{})
	h := describedHandler{HandlerFunc: payloadHandler(nil), info: testInfo}
	if err := s.Register(testKey, h); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := s.Register(testKey, h); !errors.Is(err, registry.ErrKeyConflict) {
		t.Fatalf("second Register: got %v, want ErrKeyConflict", err)
	}
}

func TestRegisterProtocolUsesTheGivenInfo(t *testing.T) {
	s := NewServer(ServerConfig{})
	if err := s.RegisterProtocol(testKey, testInfo, payloadHandler(nil)); err != nil {
		t.Fatalf("RegisterProtocol: %v", err)
	}
	reg, ok := s.Registry().Lookup(testKey)
	if !ok {
		t.Fatal("Lookup: no registration after RegisterProtocol")
	}
	if reg.Info.ID != testInfo.ID {
		t.Fatalf("Info.ID = %q, want %q", reg.Info.ID, testInfo.ID)
	}
}

func TestUnregisterRemovesTheRegistration(t *testing.T) {
	s := NewServer(ServerConfig{})
	if err := s.RegisterProtocol(testKey, testInfo, payloadHandler(nil)); err != nil {
		t.Fatalf("RegisterProtocol: %v", err)
	}
	if err := s.Unregister(testKey); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	if _, ok := s.Registry().Lookup(testKey); ok {
		t.Fatal("Lookup: registration remains after Unregister")
	}
	if err := s.Unregister(testKey); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("second Unregister: got %v, want ErrNotFound", err)
	}
}

// exchange sends one query to addr over network and returns the parsed response.
func exchange(ctx context.Context, network, addr string, id uint16) (transporttest.Message, error) {
	wire, err := transporttest.Pack(transporttest.Message{
		ID:       id,
		Question: []transporttest.RR{{Name: "device42.fleet.example.", Key: testKey}},
	})
	if err != nil {
		return transporttest.Message{}, err
	}
	resp, err := transporttest.Exchange(ctx, network, addr, wire)
	if err != nil {
		return transporttest.Message{}, err
	}
	return transporttest.Unpack(resp)
}

// checkAnswer fails the test when m is not a NOERROR answer whose RDATA is an envelope
// with payload.
func checkAnswer(t *testing.T, network string, m transporttest.Message, id uint16, payload []byte) {
	t.Helper()
	if m.ID != id || m.RCode != transport.RCodeNoError || len(m.Answer) != 1 {
		t.Fatalf("%s: got ID %d, RCODE %v, %d answers; want ID %d, NOERROR, 1 answer",
			network, m.ID, m.RCode, len(m.Answer), id)
	}
	if got := envelopePayload(t, m.Answer[0].Data); !bytes.Equal(got, payload) {
		t.Fatalf("%s: envelope payload = %q, want %q", network, got, payload)
	}
}

// envelopePayload fails the test when rdata is not a valid envelope, and returns the
// payload of the envelope.
func envelopePayload(t *testing.T, rdata []byte) []byte {
	t.Helper()
	e, err := codec.Decode(rdata, codec.Limits{})
	if err != nil {
		t.Fatalf("Decode(RDATA): %v", err)
	}
	return e.Payload
}

// waitAddr returns the address of s after its listeners start. Addr is the readiness
// signal: it is non-nil only after both sockets are bound.
func waitAddr(t *testing.T, s *Server, errc <-chan error) string {
	t.Helper()
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

// startServer runs s.ListenAndServe on 127.0.0.1:0 until the test ends. It returns the
// server address and the channel that receives the ListenAndServe result.
func startServer(t *testing.T, s *Server) (context.CancelFunc, string, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- s.ListenAndServe(ctx)
		close(errc)
	}()
	t.Cleanup(func() {
		cancel()
		// A test that already read the result receives the zero value of the closed channel.
		if err := <-errc; err != nil {
			t.Errorf("ListenAndServe: %v", err)
		}
	})
	return cancel, waitAddr(t, s, errc), errc
}

func newTestServer(t *testing.T, cfg ServerConfig, h protocol.Handler) *Server {
	t.Helper()
	if cfg.Address == "" {
		cfg.Address = "127.0.0.1:0"
	}
	if cfg.Logger == nil {
		cfg.Logger = quietLogger()
	}
	s := NewServer(cfg)
	if err := s.RegisterProtocol(testKey, testInfo, h); err != nil {
		t.Fatalf("RegisterProtocol: %v", err)
	}
	return s
}

func TestListenAndServeAnswersOverUDPAndTCPOnOneAddress(t *testing.T) {
	payload := []byte("opaque payload")
	s := newTestServer(t, ServerConfig{}, payloadHandler(payload))
	_, addr, _ := startServer(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for i, network := range []string{transport.UDP, transport.TCP} {
		id := uint16(100 + i)
		m, err := exchange(ctx, network, addr, id)
		if err != nil {
			t.Fatalf("%s exchange: %v", network, err)
		}
		checkAnswer(t, network, m, id, payload)
	}
}

func TestListenAndServeReturnsTheBindErrorWhenTheUDPPortIsInUse(t *testing.T) {
	pc, err := net.ListenPacket(transport.UDP, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer pc.Close() //nolint:errcheck // The test only holds the port.
	addr := pc.LocalAddr().String()

	s := newTestServer(t, ServerConfig{Address: addr}, payloadHandler(nil))
	if err := s.ListenAndServe(context.Background()); err == nil {
		t.Fatal("ListenAndServe on a UDP port in use returned nil")
	}
	if s.Addr() != nil {
		t.Fatalf("Addr = %v after a bind error, want nil", s.Addr())
	}
}

func TestListenAndServeReturnsTheBindErrorWhenTheTCPPortIsInUse(t *testing.T) {
	l, err := net.Listen(transport.TCP, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer l.Close() //nolint:errcheck // The test only holds the port.
	addr := l.Addr().String()

	s := newTestServer(t, ServerConfig{Address: addr}, payloadHandler(nil))
	if err := s.ListenAndServe(context.Background()); err == nil {
		t.Fatal("ListenAndServe on a TCP port in use returned nil")
	}
	if s.Addr() != nil {
		t.Fatalf("Addr = %v after a bind error, want nil", s.Addr())
	}

	// The server starts no listener, so the UDP port of addr is free again.
	pc, err := net.ListenPacket(transport.UDP, addr)
	if err != nil {
		t.Fatalf("UDP port stays bound after the bind error: %v", err)
	}
	pc.Close() //nolint:errcheck // The test only checks that the port is free.
}

func TestListenAndServeReturnsNilWithinOneSecondOfACancel(t *testing.T) {
	s := newTestServer(t, ServerConfig{}, payloadHandler(nil))
	cancel, _, errc := startServer(t, s)

	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("ListenAndServe: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ListenAndServe did not return within 1s of the cancel")
	}
}

// serve runs s.Serve on new listeners at 127.0.0.1:0 until the test ends. It returns
// the UDP address of the listeners and the channel that receives the Serve result.
func serve(t *testing.T, s *Server) (context.CancelFunc, string, <-chan error) {
	t.Helper()
	pc, l, err := transport.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- s.Serve(ctx, pc, l)
		close(errc)
	}()
	t.Cleanup(func() {
		cancel()
		// A test that already read the result receives the zero value of the closed channel.
		if err := <-errc; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return cancel, pc.LocalAddr().String(), errc
}

func TestServeAnswersQueriesOnListenersAtPortZero(t *testing.T) {
	payload := []byte("served")
	s := newTestServer(t, ServerConfig{}, payloadHandler(payload))
	_, addr, _ := serve(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for i, network := range []string{transport.UDP, transport.TCP} {
		id := uint16(200 + i)
		m, err := exchange(ctx, network, addr, id)
		if err != nil {
			t.Fatalf("%s exchange: %v", network, err)
		}
		checkAnswer(t, network, m, id, payload)
	}
}

func TestAddrIsNilBeforeServeAndHoldsTheUDPPortAfter(t *testing.T) {
	s := newTestServer(t, ServerConfig{}, payloadHandler(nil))
	if a := s.Addr(); a != nil {
		t.Fatalf("Addr before Serve = %v, want nil", a)
	}
	_, want, errc := serve(t, s)
	got := waitAddr(t, s, errc)
	if got != want {
		t.Fatalf("Addr = %s, want %s", got, want)
	}
	udp, ok := s.Addr().(*net.UDPAddr)
	if !ok || udp.Port == 0 {
		t.Fatalf("Addr = %#v, want a UDP address with a non-zero port", s.Addr())
	}
}

func TestShutdownWaitsForAHandlerThatFinishesWithinTheGrace(t *testing.T) {
	payload := []byte("late but in time")
	started := make(chan struct{})
	h := protocol.HandlerFunc(func(ctx context.Context, _ *protocol.Request) (*protocol.Response, error) {
		close(started)
		select {
		case <-time.After(time.Second):
			return &protocol.Response{Payload: payload}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	s := newTestServer(t, ServerConfig{ShutdownGrace: 5 * time.Second}, h)
	cancelServe, addr, errc := startServer(t, s)

	type result struct {
		m   transporttest.Message
		err error
	}
	resc := make(chan result, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		m, err := exchange(ctx, transport.UDP, addr, 300)
		resc <- result{m, err}
	}()

	<-started
	cancelServe()
	res := <-resc
	if res.err != nil {
		t.Fatalf("exchange during shutdown: %v", res.err)
	}
	checkAnswer(t, transport.UDP, res.m, 300, payload)
	if err := <-errc; err != nil {
		t.Fatalf("ListenAndServe: %v", err)
	}
}

func TestShutdownCancelsTheContextOfAHandlerPastTheGrace(t *testing.T) {
	started := make(chan struct{})
	ended := make(chan error, 1)
	h := protocol.HandlerFunc(func(ctx context.Context, _ *protocol.Request) (*protocol.Response, error) {
		close(started)
		<-ctx.Done()
		ended <- ctx.Err()
		return nil, ctx.Err()
	})
	s := newTestServer(t, ServerConfig{ShutdownGrace: 100 * time.Millisecond}, h)
	cancelServe, addr, errc := startServer(t, s)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		// The handler sends no answer that the test needs, so the result is not read.
		_, _ = exchange(ctx, transport.UDP, addr, 400)
	}()

	<-started
	begin := time.Now()
	cancelServe()
	select {
	case err := <-ended:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("handler context error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler context is not cancelled 2s after the shutdown starts")
	}
	if wait := time.Since(begin); wait < 100*time.Millisecond {
		t.Fatalf("handler context ended %v after the cancel, before the 100ms grace", wait)
	}
	if err := <-errc; err != nil {
		t.Fatalf("ListenAndServe: %v", err)
	}
}
