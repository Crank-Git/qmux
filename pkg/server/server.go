package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/registry"
	"github.com/Crank-Git/qmux/pkg/security"
	"github.com/Crank-Git/qmux/pkg/transport"
)

// The defaults from the timeouts table of the spec.
const (
	defaultTTL            = 60
	defaultUDPSize        = 1232
	defaultHandlerTimeout = 2 * time.Second
	defaultReadTimeout    = 2 * time.Second
	defaultWriteTimeout   = 2 * time.Second
	defaultShutdownGrace  = 5 * time.Second
)

const (
	// opcodeQuery is the DNS opcode QUERY. The server answers NOTIMP to every other
	// opcode.
	opcodeQuery = 0
	// noEDNSQuerySize is the size limit of a query without EDNS(0).
	noEDNSQuerySize = 512
	// rootZone is the Request.Zone when cfg.Zones is empty.
	rootZone = "."
)

// ErrMissingInfo is the error that Register returns for a handler that does not
// implement protocol.Describer.
var ErrMissingInfo = errors.New("server: handler does not implement protocol.Describer")

// ServerConfig holds the settings of a Server. NewServer applies a default to each
// zero field except Address, Zones and Registry.
type ServerConfig struct {
	// Address is the host and port of the UDP and TCP listeners, for example
	// "127.0.0.1:5353". ListenAndServe uses it.
	Address string
	// Zones holds the zones that the server answers for, as fully qualified names.
	// The server compares them without regard to case. Empty means every QNAME.
	Zones []string
	// TTL is the answer TTL when the handler gives TTL 0. The default is 60.
	TTL uint32
	// UDPSize is the UDP size limit for a query with EDNS(0). The default is 1232.
	UDPSize uint16
	// HandlerTimeout limits each handler call. The default is 2s. At the timeout, the
	// server answers SERVFAIL, also when the handler ignores its context.
	// The server cannot stop a handler goroutine. A handler that never returns keeps
	// its goroutine until the process ends, so a handler must observe its context.
	HandlerTimeout time.Duration
	// ReadTimeout limits the wait for each message on a TCP connection. The default
	// is 2s.
	ReadTimeout time.Duration
	// WriteTimeout limits each response write. The default is 2s.
	WriteTimeout time.Duration
	// ShutdownGrace is the time that the server waits for in-flight handlers after its
	// context ends. The default is 5s.
	ShutdownGrace time.Duration
	// Logger receives the log records. The default is slog.Default(). The server logs
	// one info record "query" per answered query. The record never holds payload bytes.
	Logger *slog.Logger
	// Registry holds the options of the registry of the server.
	Registry registry.Options
	// Signer signs every envelope that the server returns, for every application
	// status. The server signs with the full QNAME of the question, with the version
	// label. When signing fails, the server answers SERVFAIL. Nil means that the
	// server returns unsigned envelopes. Handlers never sign.
	Signer *security.Signer
}

// Server is a QMux authoritative server. It answers each registered protocol key over
// UDP and TCP.
type Server struct {
	cfg ServerConfig
	reg *registry.Registry

	mu   sync.Mutex
	addr net.Addr
}

// NewServer returns a server with a new registry built with cfg.Registry. It applies
// the defaults of ServerConfig to every zero field of cfg.
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
	// The copy keeps a later change to the caller's slice out of the server.
	zones := make([]string, len(cfg.Zones))
	for i, z := range cfg.Zones {
		zones[i] = protocol.FoldName(z)
	}
	cfg.Zones = zones
	return &Server{cfg: cfg, reg: registry.New(cfg.Registry)}
}

// Register registers h for key with the Info that h.Info() returns.
//
// It returns ErrMissingInfo when h does not implement protocol.Describer. Other errors
// wrap a sentinel error of package registry.
func (s *Server) Register(key protocol.ProtocolKey, h protocol.Handler) error {
	d, ok := h.(protocol.Describer)
	if !ok {
		return ErrMissingInfo
	}
	return s.RegisterProtocol(key, d.Info(), h)
}

// RegisterProtocol registers h for key with info. Errors wrap a sentinel error of
// package registry.
func (s *Server) RegisterProtocol(key protocol.ProtocolKey, info protocol.Info, h protocol.Handler) error {
	if err := s.reg.Register(key, info, h); err != nil {
		return fmt.Errorf("server: register %s: %w", key, err)
	}
	return nil
}

// Unregister removes the registration of key. The error wraps registry.ErrNotFound
// when key has no registration.
func (s *Server) Unregister(key protocol.ProtocolKey) error {
	if err := s.reg.Unregister(key); err != nil {
		return fmt.Errorf("server: unregister %s: %w", key, err)
	}
	return nil
}

// Registry returns the registry of the server.
func (s *Server) Registry() *registry.Registry {
	return s.reg
}

// ListenAndServe answers queries over UDP and TCP on cfg.Address until ctx ends.
//
// It returns the bind error when either listener fails to start, and then starts no
// listener. After ctx ends, it shuts down as Serve does and returns nil.
func (s *Server) ListenAndServe(ctx context.Context) error {
	pc, l, err := transport.Listen(s.cfg.Address)
	if err != nil {
		return fmt.Errorf("server: listen: %w", err)
	}
	return s.Serve(ctx, pc, l)
}

// Serve answers queries on pc and l until ctx ends. Serve closes pc and l before it
// returns. When cfg.Zones is empty, Serve logs one warning at the start.
//
// After ctx ends, Serve stops reading new queries. It waits cfg.ShutdownGrace for
// in-flight handlers, then cancels their contexts. Serve returns nil after the
// shutdown, or the first read or accept error.
func (s *Server) Serve(ctx context.Context, pc net.PacketConn, l net.Listener) error {
	s.mu.Lock()
	s.addr = pc.LocalAddr()
	s.mu.Unlock()

	if len(s.cfg.Zones) == 0 {
		s.cfg.Logger.Warn("server: no zones configured, so the server answers every QNAME")
	}
	err := transport.Serve(ctx, pc, l, transport.HandlerFunc(s.serveQuery), transport.ServeConfig{
		UDPSize:       s.cfg.UDPSize,
		ReadTimeout:   s.cfg.ReadTimeout,
		WriteTimeout:  s.cfg.WriteTimeout,
		ShutdownGrace: s.cfg.ShutdownGrace,
		Logger:        s.cfg.Logger,
	})
	if err != nil {
		return fmt.Errorf("server: serve: %w", err)
	}
	return nil
}

// Addr returns the UDP address that the server listens on. It returns nil before the
// listeners start.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// serveQuery answers q with the handler of the registration of q.Key. It applies the
// checks in the spec order, so a query that fails two checks gets the RCODE of the
// first check.
func (s *Server) serveQuery(ctx context.Context, q *transport.Query, w transport.Writer) {
	// An answer to a response can start a loop between two servers.
	if q.QR {
		return
	}
	if rcode, ok := s.checkHeader(q); !ok {
		s.write(ctx, q, w, transport.Answer{RCode: rcode}, "")
		return
	}
	zone, ok := s.matchZone(q.Name)
	if !ok {
		s.write(ctx, q, w, transport.Answer{RCode: transport.RCodeRefused}, "")
		return
	}
	reg, ok := s.reg.Lookup(q.Key)
	if !ok {
		s.write(ctx, q, w, transport.Answer{RCode: transport.RCodeRefused}, "")
		return
	}
	s.dispatch(ctx, q, zone, reg, w)
}

// checkHeader returns the RCODE of the first check that q fails, and false. It checks
// the opcode, the question count, the size limit and the EDNS version, in that order.
// It returns true when q passes every check.
func (s *Server) checkHeader(q *transport.Query) (transport.RCode, bool) {
	if q.Opcode != opcodeQuery {
		return transport.RCodeNotImp, false
	}
	// A body that does not parse has no question that the server can read.
	if q.Err != nil || q.QuestionCount != 1 {
		return transport.RCodeFormErr, false
	}
	limit := noEDNSQuerySize
	if q.EDNS {
		limit = int(s.cfg.UDPSize)
	}
	if q.WireSize > limit {
		return transport.RCodeFormErr, false
	}
	if q.EDNSVersion > 0 {
		return transport.RCodeBadVers, false
	}
	return transport.RCodeNoError, true
}

// matchZone returns the zone in cfg.Zones with the most labels that matches name, and
// true. A zone matches a name when the last labels of the name equal the labels of the
// zone. matchZone returns false when no zone matches. When cfg.Zones is empty, it
// returns the root zone and true.
func (s *Server) matchZone(name string) (string, bool) {
	if len(s.cfg.Zones) == 0 {
		return rootZone, true
	}
	labels := splitLabels(protocol.FoldName(name))
	best, bestLabels := "", -1
	for _, z := range s.cfg.Zones {
		zl := splitLabels(z)
		n := len(labels) - len(zl)
		if n >= 0 && len(zl) > bestLabels && slices.Equal(labels[n:], zl) {
			best, bestLabels = z, len(zl)
		}
	}
	return best, bestLabels >= 0
}

// write sends a through w as the response to q, and logs one info record for q. A write
// error ends only this query, so the server logs it at debug level and continues.
//
// status is the application status of the envelope in a, in decimal, or empty when a
// holds no envelope. The record takes the RCODE and the size from transport.Sent,
// because the transport can change the RCODE after the size rules. The record has the
// status only when the sent response carries the answer RR. The record holds no
// payload bytes.
func (s *Server) write(ctx context.Context, q *transport.Query, w transport.Writer, a transport.Answer, status string) {
	sent, err := w.Write(a)
	if err != nil {
		s.cfg.Logger.Debug("server: write response", "error", err)
		return
	}
	if sent.RCode != transport.RCodeNoError || sent.Truncated {
		status = ""
	}
	s.cfg.Logger.LogAttrs(ctx, slog.LevelInfo, "query",
		slog.String("qname", q.Name),
		slog.Int("qclass", int(q.Key.Class)),
		slog.Int("qtype", int(q.Key.Type)),
		slog.String("transport", q.Transport),
		slog.String("source", q.Source.String()),
		slog.String("rcode", sent.RCode.String()),
		slog.String("status", status),
		slog.Int("bytes_in", q.WireSize),
		slog.Int("bytes_out", sent.WireSize),
		slog.Float64("duration_ms", float64(time.Since(q.ReceivedAt))/float64(time.Millisecond)),
	)
}
