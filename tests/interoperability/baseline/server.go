package baseline

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/mux"
	coapnet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"

	"github.com/Crank-Git/qmux/examples/embedded-bootstrap/schema"
	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/registry"
	"github.com/Crank-Git/qmux/pkg/security"
	"github.com/Crank-Git/qmux/pkg/server"
	"github.com/Crank-Git/qmux/pkg/transport"
)

// Config holds the settings of Start.
type Config struct {
	// DNSAddr is the UDP and TCP address of qmux-private, dns-in and dns-in-txt.
	DNSAddr string
	// CoAPAddr is the UDP address of coap.
	CoAPAddr string
	// HTTPSAddr is the TCP address of https-cold and https-reuse.
	HTTPSAddr string
	// Logger receives the query log and the server errors. Nil means no log.
	Logger *slog.Logger
	// Tamper changes the last byte of every envelope after the server signs it. That
	// byte is in the signature, so every lookup fails with security.ErrBadSignature.
	// DNS answers change only over UDP, because they fit in one UDP message.
	Tamper bool
}

// Target holds the addresses of the baseline servers, as a client dials them.
type Target struct {
	// DNS is the UDP and TCP address of the DNS baselines.
	DNS string
	// CoAP is the UDP address of the coap baseline.
	CoAP string
	// HTTPS is the TCP address of the HTTPS baselines.
	HTTPS string
}

// Running is a started set of baseline servers. Call Close to stop them.
type Running struct {
	target Target
	cancel context.CancelFunc
	served chan error
}

// Target returns the bound addresses of the baseline servers.
func (r *Running) Target() Target {
	return r.target
}

// Close stops every baseline server and returns the joined serve errors. Call Close
// once: a second call blocks.
func (r *Running) Close() error {
	r.cancel()
	return errors.Join(<-r.served, <-r.served, <-r.served)
}

// Start starts the servers of the six baselines and returns after every listener is
// bound. The servers answer until ctx ends or Close is called.
//
// The servers sign every envelope at the start time, so each lookup of qmux-private,
// coap, https-cold and https-reuse gets the same envelope bytes (FR-bench-10). Start
// returns the bind error of the first listener that fails, and then leaves no listener
// open.
func Start(ctx context.Context, cfg Config) (*Running, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	payload, err := Payload()
	if err != nil {
		return nil, err
	}
	signedAt := time.Now()
	signer := &security.Signer{Key: signingKey(), Validity: signingValidity, Now: func() time.Time { return signedAt }}

	dns, err := newDNSServer(signer, payload, logger)
	if err != nil {
		return nil, err
	}
	envelope, err := newPrivateEnvelope(signer, payload, cfg.Tamper)
	if err != nil {
		return nil, err
	}
	router, err := coapRouter(envelope, logger)
	if err != nil {
		return nil, err
	}
	cert, err := TLSCertificate()
	if err != nil {
		return nil, err
	}

	pc, l, err := transport.Listen(cfg.DNSAddr)
	if err != nil {
		return nil, fmt.Errorf("baseline: listen dns %s: %w", cfg.DNSAddr, err)
	}
	coapConn, err := coapnet.NewListenUDP("udp", cfg.CoAPAddr)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("baseline: listen coap %s: %w", cfg.CoAPAddr, err), pc.Close(), l.Close())
	}
	httpsListener, err := net.Listen("tcp", cfg.HTTPSAddr)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("baseline: listen https %s: %w", cfg.HTTPSAddr, err), pc.Close(), l.Close(), coapConn.Close())
	}

	r := &Running{
		target: Target{DNS: pc.LocalAddr().String(), CoAP: coapConn.LocalAddr().String(), HTTPS: httpsListener.Addr().String()},
		served: make(chan error, 3),
	}
	ctx, r.cancel = context.WithCancel(ctx)
	if cfg.Tamper {
		pc = tamperConn{pc}
	}
	go func() { r.served <- dns.Serve(ctx, pc, l) }()
	serveCoAP(ctx, r.served, coapConn, router, logger)
	serveHTTPS(ctx, r.served, httpsListener, cert, envelope, logger)
	return r, nil
}

// newDNSServer returns the server of the three DNS baselines. Each answers its QNAME
// with payload and every other QNAME with status not-found.
func newDNSServer(signer *security.Signer, payload []byte, logger *slog.Logger) (*server.Server, error) {
	srv := server.NewServer(server.ServerConfig{
		Zones:  []string{Zone},
		Logger: logger,
		Signer: signer,
		// QTYPE 16 is not a private type, so dns-in-txt needs this option.
		Registry: registry.Options{AllowNonPrivateTypes: true},
	})
	dnsBaselines := []struct {
		id   string
		key  protocol.ProtocolKey
		name string
	}{
		{PrivateID, PrivateKey, PrivateName},
		{INID, INKey, INName},
		{TXTID, TXTKey, INName},
	}
	for _, b := range dnsBaselines {
		info := protocol.Info{ID: b.id, Versions: []uint16{1}, Schema: schema.Firmware{}}
		name := b.name
		h := protocol.HandlerFunc(func(_ context.Context, req *protocol.Request) (*protocol.Response, error) {
			if req.Name != name {
				return &protocol.Response{Status: protocol.StatusNotFound}, nil
			}
			return &protocol.Response{Payload: payload}, nil
		})
		if err := srv.RegisterProtocol(b.key, info, h); err != nil {
			return nil, fmt.Errorf("baseline: register %s: %w", b.id, err)
		}
	}
	return srv, nil
}

// newPrivateEnvelope returns a function that signs and encodes the qmux-private
// envelope. It builds the envelope as pkg/server does, so the bytes equal the DNS
// answer. The CoAP and HTTPS servers call it for each lookup, so they do the same
// signing work as the DNS server. It returns the error of a first trial call.
func newPrivateEnvelope(signer *security.Signer, payload []byte, tamper bool) (func() ([]byte, error), error) {
	envelope := func() ([]byte, error) {
		e := codec.Envelope{
			Format:      1,
			ProtocolID:  PrivateID,
			Version:     1,
			MessageType: uint64(protocol.MessageRecord),
			Status:      uint64(protocol.StatusOK),
			Payload:     payload,
		}
		if err := signer.Sign(&e, PrivateName, PrivateKey); err != nil {
			return nil, fmt.Errorf("baseline: sign envelope: %w", err)
		}
		b, err := codec.Encode(e, codec.Limits{})
		if err != nil {
			return nil, fmt.Errorf("baseline: encode envelope: %w", err)
		}
		if tamper {
			b[len(b)-1] ^= 0xff
		}
		return b, nil
	}
	if _, err := envelope(); err != nil {
		return nil, err
	}
	return envelope, nil
}

// coapRouter returns the router of the coap baseline. It answers a confirmable GET on
// Path with the envelope, in content-format 60 (FR-bench-7).
func coapRouter(envelope func() ([]byte, error), logger *slog.Logger) (*mux.Router, error) {
	router := mux.NewRouter()
	err := router.Handle(Path, mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		code, body := codes.Content, []byte(nil)
		switch {
		case r.Code() != codes.GET:
			code = codes.MethodNotAllowed
		case r.Type() != message.Confirmable:
			// FR-bench-7 measures the confirmable exchange, with its retransmissions.
			code = codes.BadRequest
		default:
			b, err := envelope()
			if err != nil {
				logger.Error("baseline: coap envelope", "err", err)
				code = codes.InternalServerError
			}
			body = b
		}
		if err := w.SetResponse(code, message.AppCBOR, bytes.NewReader(body)); err != nil {
			logger.Warn("baseline: coap response", "err", err)
		}
	}))
	if err != nil {
		return nil, fmt.Errorf("baseline: coap route %s: %w", Path, err)
	}
	return router, nil
}

// serveCoAP serves router over conn until ctx ends. It sends the result of the serve
// loop to served.
func serveCoAP(ctx context.Context, served chan<- error, conn *coapnet.UDPConn, router *mux.Router, logger *slog.Logger) {
	s := udp.NewServer(options.WithMux(router), options.WithErrors(func(err error) {
		logger.Warn("baseline: coap", "err", err)
	}))
	go func() {
		<-ctx.Done()
		s.Stop()
	}()
	go func() {
		err := s.Serve(conn)
		if err != nil {
			err = fmt.Errorf("baseline: serve coap: %w", err)
		}
		served <- errors.Join(err, conn.Close())
	}()
}

// serveHTTPS serves GET Path over TLS 1.3 on l until ctx ends. It sends the result of
// the serve loop to served.
func serveHTTPS(ctx context.Context, served chan<- error, l net.Listener, cert tls.Certificate, envelope func() ([]byte, error), logger *slog.Logger) {
	m := http.NewServeMux()
	m.HandleFunc("GET "+Path, func(w http.ResponseWriter, _ *http.Request) {
		b, err := envelope()
		if err != nil {
			logger.Error("baseline: https envelope", "err", err)
			http.Error(w, "envelope", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/cbor")
		if _, err := w.Write(b); err != nil {
			logger.Warn("baseline: https response", "err", err)
		}
	})
	hs := &http.Server{
		Handler:           m,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13},
		ReadHeaderTimeout: 5 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	go func() {
		<-ctx.Done()
		// Close ends the serve loop, and its error is ErrServerClosed.
		_ = hs.Close()
	}()
	go func() {
		err := hs.ServeTLS(l, "", "")
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		} else {
			err = fmt.Errorf("baseline: serve https: %w", err)
		}
		served <- err
	}()
}
