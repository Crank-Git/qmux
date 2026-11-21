package transport

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"
)

// listenAttempts is the number of port choices that Listen tries for port 0. The
// kernel picks a free UDP port, and the same TCP port can still be in use.
const listenAttempts = 10

// ServeConfig holds the settings of Serve and ServeMessage.
type ServeConfig struct {
	// UDPSize is the UDP size limit of the server for a query with EDNS(0). It is also
	// the UDP size in the OPT RR of each response.
	UDPSize uint16
	// ReadTimeout limits the wait for each message on a TCP connection. 0 means no
	// limit.
	ReadTimeout time.Duration
	// WriteTimeout limits each response write. 0 means no limit.
	WriteTimeout time.Duration
	// ShutdownGrace is the time that Serve waits for in-flight handlers after its
	// context ends.
	ShutdownGrace time.Duration
	// Logger receives the debug records. nil selects slog.Default().
	Logger *slog.Logger
}

// Listen opens a UDP socket on addr and a TCP listener on the same port.
//
// When the port in addr is 0, the kernel picks the UDP port, and another process can
// hold the same TCP port. Then Listen closes the UDP socket and tries a new port, at
// most 10 times. When the port is not 0, Listen tries once.
//
// Listen returns the last bind error when no attempt opens both sockets. It then leaves
// no socket open.
func Listen(addr string) (net.PacketConn, net.Listener, error) {
	return listen(addr, net.Listen)
}

// listen is Listen with the TCP bind as a parameter. A test can only make the TCP port
// of a port-0 bind busy from inside the TCP bind, because the kernel picks the port.
func listen(addr string, listenTCP func(network, address string) (net.Listener, error)) (net.PacketConn, net.Listener, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, nil, fmt.Errorf("transport: listen on %q: %w", addr, err)
	}
	attempts := 1
	if port == "0" {
		attempts = listenAttempts
	}

	var last error
	for range attempts {
		pc, err := net.ListenPacket(UDP, addr)
		if err != nil {
			return nil, nil, fmt.Errorf("transport: listen udp %s: %w", addr, err)
		}
		l, err := listenTCP(TCP, pc.LocalAddr().String())
		if err == nil {
			return pc, l, nil
		}
		last = errors.Join(fmt.Errorf("transport: listen tcp %s: %w", pc.LocalAddr(), err), pc.Close())
	}
	return nil, nil, last
}

// ServeMessage answers one query in wire through h, with no socket. It returns the
// response bytes that h wrote, or nil when h wrote no response.
//
// network is UDP or TCP and selects the size limit. ctx is the handler context.
// ServeMessage returns an error that wraps ErrMalformed when wire is shorter than the
// 12-byte header.
func ServeMessage(ctx context.Context, wire []byte, src netip.AddrPort, network string,
	h Handler, cfg ServeConfig) ([]byte, error) {
	if network != UDP && network != TCP {
		return nil, fmt.Errorf("transport: unknown transport %q", network)
	}
	if len(wire) < headerSize {
		return nil, fmt.Errorf("transport: message of %d bytes is shorter than the DNS header: %w", len(wire), ErrMalformed)
	}

	var out []byte
	q, req := parseQuery(wire, src, network, time.Now())
	w := &writer{q: q, req: req, udpSize: cfg.UDPSize, send: func(b []byte) error {
		out = b
		return nil
	}}
	h.ServeQuery(ctx, q, w)

	// Write holds the lock while it sets out.
	w.mu.Lock()
	defer w.mu.Unlock()
	return out, nil
}

// Serve answers the queries on pc and l through h until ctx ends. It reads each UDP
// packet and each TCP message into a 65535-byte buffer, and calls h in a new goroutine.
//
// Serve drops a message shorter than the 12-byte header and logs it at debug level.
//
// After ctx ends, Serve stops reading and closes l. It waits cfg.ShutdownGrace for
// in-flight handlers, then cancels their contexts. Serve closes pc and returns after
// every handler returns. Handler contexts keep the values of ctx and do not end when
// ctx ends.
//
// Serve returns nil after a shutdown, or the first read or accept error.
func Serve(ctx context.Context, pc net.PacketConn, l net.Listener, h Handler, cfg ServeConfig) error {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	handlerCtx, cancelHandlers := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelHandlers()

	s := &server{h: h, cfg: cfg, logger: logger, ctx: handlerCtx, conns: make(map[net.Conn]struct{})}
	errc := make(chan error, 2)
	var loops sync.WaitGroup
	loops.Go(func() { errc <- s.serveUDP(pc) })
	loops.Go(func() { errc <- s.serveTCP(l) })

	var err error
	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	stopErr := s.stop(pc, l)
	loops.Wait()

	idle := make(chan struct{})
	go func() {
		s.inflight.Wait()
		close(idle)
	}()
	grace := time.NewTimer(cfg.ShutdownGrace)
	defer grace.Stop()
	select {
	case <-idle:
	case <-grace.C:
		cancelHandlers()
		<-idle
	}
	return errors.Join(err, stopErr, closeErr("udp socket", pc.Close()))
}

// server holds the state of one Serve call.
type server struct {
	h      Handler
	cfg    ServeConfig
	logger *slog.Logger
	// ctx is the parent of every handler context.
	ctx      context.Context
	inflight sync.WaitGroup

	mu       sync.Mutex
	stopping bool
	conns    map[net.Conn]struct{}
}

// stop ends the read loops. It leaves pc open, so that in-flight UDP handlers can
// still send their responses.
func (s *server) stop(pc net.PacketConn, l net.Listener) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopping = true
	// A deadline in the past unblocks a read and leaves writes possible.
	errs := []error{closeErr("tcp listener", l.Close()), pc.SetReadDeadline(time.Now())}
	for c := range s.conns {
		errs = append(errs, c.SetReadDeadline(time.Now()))
	}
	return errors.Join(errs...)
}

func closeErr(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("transport: close %s: %w", what, err)
}

func (s *server) isStopping() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopping
}

func (s *server) serveUDP(pc net.PacketConn) error {
	buf := make([]byte, maxMessageSize)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			if s.isStopping() {
				return nil
			}
			return fmt.Errorf("transport: read udp: %w", err)
		}
		src := addrPort(addr)
		if n < headerSize {
			s.logShort(UDP, src, n)
			continue
		}
		wire := bytes.Clone(buf[:n])
		s.inflight.Go(func() {
			s.handle(wire, src, UDP, func(resp []byte) error {
				if err := s.setWriteDeadline(pc); err != nil {
					return err
				}
				_, err := pc.WriteTo(resp, addr)
				return err
			})
		})
	}
}

func (s *server) serveTCP(l net.Listener) error {
	for {
		c, err := l.Accept()
		if err != nil {
			if s.isStopping() {
				return nil
			}
			return fmt.Errorf("transport: accept tcp: %w", err)
		}
		s.inflight.Go(func() { s.serveConn(c) })
	}
}

// serveConn answers the queries on one TCP connection, one at a time, until the
// client closes it, a read fails, or Serve stops.
func (s *server) serveConn(c net.Conn) {
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		_ = c.Close() // The client gets each response before the close, so a close error changes nothing.
	}()
	src := addrPort(c.RemoteAddr())
	for s.armRead(c) {
		var size [2]byte
		if _, err := io.ReadFull(c, size[:]); err != nil {
			s.logRead(src, err)
			return
		}
		wire := make([]byte, binary.BigEndian.Uint16(size[:]))
		if _, err := io.ReadFull(c, wire); err != nil {
			s.logRead(src, err)
			return
		}
		if len(wire) < headerSize {
			s.logShort(TCP, src, len(wire))
			continue
		}
		s.handle(wire, src, TCP, func(resp []byte) error {
			if err := s.setWriteDeadline(c); err != nil {
				return err
			}
			framed := binary.BigEndian.AppendUint16(make([]byte, 0, 2+len(resp)), uint16(len(resp)))
			_, err := c.Write(append(framed, resp...))
			return err
		})
	}
}

// armRead sets the read deadline for the next message on c. It returns false when
// Serve stops. The lock orders it with stop, so stop always sets the last deadline.
func (s *server) armRead(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return false
	}
	s.conns[c] = struct{}{}
	var deadline time.Time
	if s.cfg.ReadTimeout > 0 {
		deadline = time.Now().Add(s.cfg.ReadTimeout)
	}
	if err := c.SetReadDeadline(deadline); err != nil {
		s.logger.Debug("transport: set tcp read deadline", "source", c.RemoteAddr().String(), "error", err)
		return false
	}
	return true
}

func (s *server) setWriteDeadline(c interface{ SetWriteDeadline(time.Time) error }) error {
	if s.cfg.WriteTimeout <= 0 {
		return nil
	}
	if err := c.SetWriteDeadline(time.Now().Add(s.cfg.WriteTimeout)); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}
	return nil
}

// handle calls the handler for one query. send writes the response bytes.
func (s *server) handle(wire []byte, src netip.AddrPort, network string, send func([]byte) error) {
	q, req := parseQuery(wire, src, network, time.Now())
	s.h.ServeQuery(s.ctx, q, &writer{q: q, req: req, udpSize: s.cfg.UDPSize, send: send})
}

func (s *server) logShort(network string, src netip.AddrPort, n int) {
	s.logger.Debug("transport: drop message shorter than the DNS header",
		"transport", network, "source", src.String(), "bytes", n)
}

// logRead logs a TCP read that ends a connection. A client close and a shutdown are
// normal ends and get no record.
func (s *server) logRead(src netip.AddrPort, err error) {
	if errors.Is(err, io.EOF) || s.isStopping() {
		return
	}
	s.logger.Debug("transport: read tcp message", "source", src.String(), "error", err)
}

// addrPort returns the address and port of addr, with an IPv4-mapped address as IPv4.
// It returns the zero value for an address that has no IP and port.
func addrPort(addr net.Addr) netip.AddrPort {
	var ap netip.AddrPort
	switch a := addr.(type) {
	case *net.UDPAddr:
		ap = a.AddrPort()
	case *net.TCPAddr:
		ap = a.AddrPort()
	default:
		// A PacketConn from a caller can use another net.Addr type.
		ap, _ = netip.ParseAddrPort(addr.String())
	}
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
}
