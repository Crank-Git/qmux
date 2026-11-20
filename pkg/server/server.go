// Package server implements the QMux DNS server that listens for queries,
// resolves service names through the registry, and returns answers.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/registry"
	"github.com/miekg/dns"
)

// ErrMissingInfo reports that a handler does not implement protocol.Describer.
var ErrMissingInfo = errors.New("server: handler does not implement protocol.Describer")

// ServerConfig holds the configuration for a Server.
type ServerConfig struct {
	// Address is the network address to listen on, e.g. "127.0.0.1:5353".
	Address string

	// Zones is the list of zones the server is authoritative for.
	// An empty list accepts every QNAME.
	Zones []string

	// TTL is the default time-to-live in seconds for answer RRs.
	TTL uint32

	// UDPSize is the advertised EDNS(0) UDP size.
	UDPSize uint16

	// HandlerTimeout is the maximum time to wait for a handler to respond.
	HandlerTimeout time.Duration

	// ReadTimeout is the maximum time to wait for a read operation.
	ReadTimeout time.Duration

	// WriteTimeout is the maximum time to wait for a write operation.
	WriteTimeout time.Duration

	// ShutdownGrace is the time to wait for in-flight handlers during shutdown.
	ShutdownGrace time.Duration

	// Logger is the logger to use. When nil, slog.Default() is used.
	Logger *slog.Logger

	// Registry configures the underlying registry.
	Registry registry.Options
}

// Server is the QMux DNS server.
type Server struct {
	addr          net.Addr
	addrMu        sync.RWMutex
	cfg           ServerConfig
	registry      *registry.Registry
	udpServer     *dns.Server
	tcpServer     *dns.Server
	mu            sync.Mutex
	wg            sync.WaitGroup
	srvCtx        context.Context
	srvCtxMu      sync.RWMutex
	handlerCancel func()
	handlerCancelMu sync.RWMutex
}

var (
	defaultTTL            = uint32(60)
	defaultUDPSize        = uint16(1232)
	defaultHandlerTimeout = 2 * time.Second
	defaultReadTimeout    = 2 * time.Second
	defaultWriteTimeout   = 2 * time.Second
	defaultShutdownGrace  = 5 * time.Second
)

// NewServer creates a new Server with the given configuration.
// Zero fields receive sensible defaults.
func NewServer(cfg ServerConfig) *Server {
	if cfg.TTL == 0 {
		cfg.TTL = defaultTTL
	}
	if cfg.UDPSize == 0 {
		cfg.UDPSize = defaultUDPSize
	}
	if cfg.HandlerTimeout == 0 {
		cfg.HandlerTimeout = defaultHandlerTimeout
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = defaultReadTimeout
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = defaultWriteTimeout
	}
	if cfg.ShutdownGrace == 0 {
		cfg.ShutdownGrace = defaultShutdownGrace
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	// FR-server-21: log warning when Zones is empty.
	if len(cfg.Zones) == 0 {
		cfg.Logger.Warn("no zones configured; accepting every QNAME")
	}

	return &Server{
		cfg:      cfg,
		registry: registry.New(cfg.Registry),
	}
}

// Register registers a handler with the protocol key.
// If the handler implements protocol.Describer, its Info() is used.
// Returns ErrMissingInfo when the handler does not implement Describer.
func (s *Server) Register(key protocol.ProtocolKey, h protocol.Handler) error {
	// Check nil first (per spec validation order: handler before interface check).
	if h == nil {
		return fmt.Errorf("%w", registry.ErrInvalidHandler)
	}
	d, ok := h.(protocol.Describer)
	if !ok {
		return fmt.Errorf("%w", ErrMissingInfo)
	}
	return s.registry.Register(key, d.Info(), h)
}

// RegisterProtocol registers a handler with an explicit Info.
func (s *Server) RegisterProtocol(key protocol.ProtocolKey, info protocol.Info, h protocol.Handler) error {
	return s.registry.Register(key, info, h)
}

// Unregister removes the registration of the protocol key.
func (s *Server) Unregister(key protocol.ProtocolKey) error {
	return s.registry.Unregister(key)
}

// Registry returns the server's registry.
func (s *Server) Registry() *registry.Registry {
	return s.registry
}

// ListenAndServe starts both UDP and TCP listeners on cfg.Address
// and serves incoming queries until the context is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	udpAddr, err := net.ResolveUDPAddr("udp", s.cfg.Address)
	if err != nil {
		return fmt.Errorf("server: resolve udp address: %w", err)
	}

	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("server: listen udp: %w", err)
	}

	// Use the same port as the UDP listener for TCP (important when port is 0).
	tcpPort := udpConn.LocalAddr().(*net.UDPAddr).Port
	var tcpIP net.IP
	if udpAddr.IP != nil {
		tcpIP = udpAddr.IP
	} else {
		tcpIP = net.IPv4zero // 0.0.0.0 → bind all interfaces
	}
	tcpAddr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf("%s:%d", tcpIP.String(), tcpPort))
	if err != nil {
		udpConn.Close()
		return fmt.Errorf("server: resolve tcp address: %w", err)
	}

	tcpListener, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		udpConn.Close()
		return fmt.Errorf("server: listen tcp: %w", err)
	}

	return s.Serve(ctx, udpConn, tcpListener)
}

// Serve serves on caller-supplied listeners.
func (s *Server) Serve(ctx context.Context, pc net.PacketConn, l net.Listener) error {
	if pc == nil && l == nil {
		return nil
	}

	s.mu.Lock()

	// Set up the DNS handler.
	handler := &serverHandler{s: s}

	// UDP server.
	if pc != nil {
		s.udpServer = &dns.Server{
			Net:           "udp",
			Handler:       handler,
			UDPSize:       int(s.cfg.UDPSize),
			ReadTimeout:   s.cfg.ReadTimeout,
			WriteTimeout:  s.cfg.WriteTimeout,
			PacketConn:    pc,
			MsgAcceptFunc: msgAcceptFunc(),
		}

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.udpServer.ActivateAndServe()
		}()

		// Store the address once the listener is up.
		s.addrMu.Lock()
		s.addr = pc.LocalAddr()
		s.addrMu.Unlock()
	}

	// TCP server.
	if l != nil {
		s.tcpServer = &dns.Server{
			Net:           "tcp",
			Handler:       handler,
			ReadTimeout:   s.cfg.ReadTimeout,
			WriteTimeout:  s.cfg.WriteTimeout,
			Listener:      l,
			MsgAcceptFunc: msgAcceptFunc(),
		}

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.tcpServer.ActivateAndServe()
		}()

		// Store the TCP address if UDP wasn't set.
		s.addrMu.RLock()
		if s.addr == nil {
			s.addrMu.RUnlock()
			s.addrMu.Lock()
			s.addr = l.Addr()
			s.addrMu.Unlock()
		} else {
			s.addrMu.RUnlock()
		}
	}

	s.srvCtxMu.Lock()
	s.srvCtx = ctx
	s.srvCtxMu.Unlock()
	s.mu.Unlock()

	// Wait for context cancellation.
	<-ctx.Done()

	// Graceful shutdown: cancel in-flight handler contexts first,
	// then shut down the DNS servers.
	s.handlerCancelMu.Lock()
	if s.handlerCancel != nil {
		s.handlerCancel()
	}
	s.handlerCancelMu.Unlock()

	s.mu.Lock()
	if s.udpServer != nil {
		_ = s.udpServer.ShutdownContext(context.Background())
	}
	if s.tcpServer != nil {
		_ = s.tcpServer.ShutdownContext(context.Background())
	}
	s.mu.Unlock()

	// Wait for goroutines to finish.
	s.wg.Wait()

	return nil
}

// Addr returns the network address the server is listening on.
// Returns nil before the listeners start.
func (s *Server) Addr() net.Addr {
	s.addrMu.RLock()
	defer s.addrMu.RUnlock()
	return s.addr
}

// serverHandler implements dns.Handler by dispatching to protocol handlers.
type serverHandler struct {
	s *Server
}

func (h *serverHandler) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	s := h.s

	// FR-server-13: Ignore messages with QR bit set.
	if req.Response {
		return
	}

	// FR-server-14: NOTIMP for non-QUERY opcodes.
	if req.Opcode != dns.OpcodeQuery {
		resp := new(dns.Msg)
		resp.SetReply(req)
		resp.Rcode = dns.RcodeNotImplemented
		_ = w.WriteMsg(resp)
		return
	}

	// FR-server-15: FORMERR for wrong question count.
	if len(req.Question) != 1 {
		resp := new(dns.Msg)
		resp.SetReply(req)
		resp.Rcode = dns.RcodeFormatError
		_ = w.WriteMsg(resp)
		return
	}

	// FR-server-18: BADVERS for EDNS version above 0.
	edns := req.IsEdns0()
	if edns != nil && edns.Version() > 0 {
		resp := new(dns.Msg)
		resp.SetReply(req)
		resp.SetEdns0(defaultUDPSize, false)
		resp.Rcode = dns.RcodeBadVers
		_ = w.WriteMsg(resp)
		return
	}

	// FR-server-16: FORMERR for query > 512 bytes without EDNS(0).
	// FR-server-17: FORMERR for query > 1232 bytes with EDNS(0).
	msgLen := req.Len()
	if edns == nil && msgLen > dns.MinMsgSize {
		resp := new(dns.Msg)
		resp.SetReply(req)
		resp.Rcode = dns.RcodeFormatError
		_ = w.WriteMsg(resp)
		return
	}
	if edns != nil && msgLen > int(defaultUDPSize) {
		resp := new(dns.Msg)
		resp.SetReply(req)
		resp.Rcode = dns.RcodeFormatError
		_ = w.WriteMsg(resp)
		return
	}

	q := req.Question[0]
	key := protocol.ProtocolKey{Class: q.Qclass, Type: q.Qtype}

	// FR-server-19/20: Zone matching.
	zone := zoneMatch(s.cfg.Zones, q.Name)
	if zone == "" {
		// FR-server-19: REFUSED for QNAME outside every configured zone.
		resp := new(dns.Msg)
		resp.SetReply(req)
		resp.Rcode = dns.RcodeRefused
		_ = w.WriteMsg(resp)
		return
	}

	// Look up the registration.
	reg, ok := s.registry.Lookup(key)
	if !ok {
		// FR-server-22: REFUSED for unregistered key.
		resp := new(dns.Msg)
		resp.SetReply(req)
		resp.Rcode = dns.RcodeRefused
		_ = w.WriteMsg(resp)
		return
	}

	// Build the Request.
	transport := "udp"
	if _, ok := w.RemoteAddr().(*net.TCPAddr); ok {
		transport = "tcp"
	}

	// Create a cancellable handler context. The timeout is derived from the
	// server context so that server shutdown propagates to in-flight handlers.
	s.srvCtxMu.RLock()
	srvCtx := s.srvCtx
	s.srvCtxMu.RUnlock()

	if srvCtx == nil {
		srvCtx = context.Background()
	}
	hctx, hctxCancel := context.WithCancel(srvCtx)
	ctx, cancel := context.WithTimeout(hctx, s.cfg.HandlerTimeout)

	// Store the cancel function so shutdown can abort in-flight handlers.
	s.handlerCancelMu.Lock()
	s.handlerCancel = hctxCancel
	s.handlerCancelMu.Unlock()

	defer func() {
		cancel()
		hctxCancel()
	}()

	request := &protocol.Request{
		QName:      q.Name,
		Key:        key,
		Version:    0,
		Labels:     dns.SplitDomainName(q.Name),
		Zone:       zone,
		Source:     netip.MustParseAddrPort(w.RemoteAddr().String()),
		Transport:  transport,
		UDPSize:    0,
		ID:         req.Id,
		ReceivedAt: time.Now(),
	}

	// Call the handler with panic recovery.
	var response *protocol.Response
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				s.cfg.Logger.Error("handler panic", "error", r)
				err = fmt.Errorf("handler panic: %v", r)
				response = nil
			}
		}()
		response, err = reg.Handler.Handle(ctx, request)
	}()

	if err != nil {
		// SERVFAIL for handler error.
		resp := new(dns.Msg)
		resp.SetReply(req)
		resp.Rcode = dns.RcodeServerFailure
		_ = w.WriteMsg(resp)
		return
	}

	if response == nil {
		// Handler returned nil response — SERVFAIL.
		s.cfg.Logger.Error("handler returned nil response")
		resp := new(dns.Msg)
		resp.SetReply(req)
		resp.Rcode = dns.RcodeServerFailure
		_ = w.WriteMsg(resp)
		return
	}

	// Build the DNS response with opaque RDATA.
	resp := new(dns.Msg)
	resp.SetReply(req)
	resp.Authoritative = true
	resp.RecursionAvailable = false

	ttl := s.cfg.TTL
	if response.TTL != 0 {
		ttl = response.TTL
	}

	// RFC3597 opaque RDATA.
	rr := &dns.RFC3597{
		Hdr: dns.RR_Header{
			Name:     q.Name,
			Rrtype:   q.Qtype,
			Class:    q.Qclass,
			Ttl:      ttl,
			Rdlength: uint16(len(response.Payload)),
		},
		Rdata: string(response.Payload),
	}
	resp.Answer = append(resp.Answer, rr)

	_ = w.WriteMsg(resp)
}

// zoneMatch returns the longest matching zone for a QNAME, or an empty string
// when no zone matches. A zone matches when the QNAME equals the zone or ends
// with "." plus the zone.
func zoneMatch(zones []string, qname string) string {
	if len(zones) == 0 {
		return "."
	}
	var best string
	for _, z := range zones {
		if qname == z || len(qname) >= len(z)+1 && qname[len(qname)-len(z)-1:] == "."+z {
			if len(z) > len(best) {
				best = z
			}
		}
	}
	return best
}

// msgAcceptFunc returns a dns.MsgAcceptFunc that rejects NOTIFY and multiple
// additional RRs. A single additional RR is allowed (for EDNS(0) OPT RR).
//
// The Header struct represents the raw DNS wire header. The QR bit is bit 15
// (0x8000). The opcode occupies bits 11–14.
func msgAcceptFunc() dns.MsgAcceptFunc {
	return func(header dns.Header) dns.MsgAcceptAction {
		// Ignore messages with QR bit set (responses).
		if header.Bits&0x8000 != 0 {
			return dns.MsgIgnore
		}

		// Answer NOTIMP for non-QUERY opcodes.
		opcode := int(header.Bits>>11) & 0xF
		if opcode != dns.OpcodeQuery {
			return dns.MsgRejectNotImplemented
		}

		// Reject messages with more than one additional RR.
		// One additional RR is allowed for EDNS(0) OPT RR.
		if header.Arcount > 1 {
			return dns.MsgReject
		}

		return dns.MsgAccept
	}
}
