// Package fleet is the server side of the embedded recovery demonstration. It starts
// the authoritative server for the zone fleet.example. and the mock HTTPS service.
//
// The server registers discovery and the three protocols org.qmux.bootstrap,
// org.qmux.configuration and org.qmux.firmware. It signs every envelope with a signing
// key that Start generates. The mock HTTPS service serves one configuration bundle and
// one firmware image.
package fleet

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"

	"github.com/Crank-Git/qmux/examples/embedded-bootstrap/schema"
	"github.com/Crank-Git/qmux/pkg/discovery"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/security"
	"github.com/Crank-Git/qmux/pkg/server"
)

// The names that the server answers for.
const (
	// Zone is the zone that the server is authoritative for (FR-demo-8).
	Zone = "fleet.example."
	// DeviceName is the QNAME that the three protocol handlers answer (FR-demo-15).
	DeviceName = "device42.fleet.example."
)

// The protocol IDs of the three protocols (FR-demo-10).
const (
	// BootstrapID is the protocol ID of the bootstrap protocol.
	BootstrapID = "org.qmux.bootstrap"
	// ConfigurationID is the protocol ID of the configuration protocol.
	ConfigurationID = "org.qmux.configuration"
	// FirmwareID is the protocol ID of the firmware protocol.
	FirmwareID = "org.qmux.firmware"
)

// The default flag values (FR-demo-17, FR-demo-18).
const (
	// DefaultRemoteGeneration is the default of --remote-generation.
	DefaultRemoteGeneration = 7
	// DefaultRemoteFirmware is the default of --remote-firmware.
	DefaultRemoteFirmware = "2.4.1"
)

// firmwareKeySeed is the seed of the mock firmware signing key. A fixed seed gives the
// same key ID at each start, so the allowed firmware key IDs can be a list in the program.
const firmwareKeySeed = "qmux embedded-bootstrap firmware"

// listenAttempts limits the attempts to find a port that is free for UDP and TCP.
const listenAttempts = 10

// Options holds the settings of Start.
type Options struct {
	// RemoteGeneration is the configuration generation that the server publishes.
	RemoteGeneration uint64
	// RemoteFirmware is the firmware version that the server publishes, as
	// MAJOR.MINOR.PATCH.
	RemoteFirmware string
	// Out receives the [server] line and the [https] line. Nil means io.Discard.
	Out io.Writer
	// Logger receives the query log of the server. Nil means no query log.
	Logger *slog.Logger
	// TamperDNS makes the server change one byte of each configuration envelope after
	// it signs it (FR-demo-40).
	TamperDNS bool
	// TamperHTTPS makes the mock HTTPS service serve a configuration bundle whose bytes
	// differ from the signed digest (FR-demo-42).
	TamperHTTPS bool
}

// BindFlags defines the flags --remote-generation, --remote-firmware, --tamper-dns and
// --tamper-https on fs. The flags write to o. The first two default to
// DefaultRemoteGeneration and DefaultRemoteFirmware, and the tamper flags to false.
func (o *Options) BindFlags(fs *flag.FlagSet) {
	fs.Uint64Var(&o.RemoteGeneration, "remote-generation", DefaultRemoteGeneration,
		"configuration generation that the server publishes")
	fs.StringVar(&o.RemoteFirmware, "remote-firmware", DefaultRemoteFirmware,
		"firmware version that the server publishes")
	fs.BoolVar(&o.TamperDNS, "tamper-dns", false,
		"change one byte of the configuration envelope after signing")
	fs.BoolVar(&o.TamperHTTPS, "tamper-https", false,
		"serve a configuration bundle that does not match its digest")
}

// Running is a started authoritative server and mock HTTPS service. Call Close to stop
// both.
type Running struct {
	// Addr is the loopback address of the server, for UDP and TCP on one port.
	Addr string
	// KeyID is the key ID of the signing key.
	KeyID [8]byte
	// TrustAnchor is the public key of the signing key. Give it to the device as its one
	// trust anchor (FR-demo-12).
	TrustAnchor security.TrustAnchor
	// HTTPSURL is the base URL of the mock HTTPS service, with no trailing slash.
	HTTPSURL string
	// HTTPClient trusts only the certificate of the mock HTTPS service (FR-demo-14).
	HTTPClient *http.Client
	// FirmwareKeyID is the key ID of the mock firmware signing key. The firmware
	// payload carries it.
	FirmwareKeyID [8]byte

	requests atomic.Int64
	https    *httptest.Server
	cancel   context.CancelFunc
	served   chan error
}

// Requests returns the number of HTTPS requests that the mock HTTPS service received.
// A request that fails the TLS handshake does not reach the service and does not count.
func (r *Running) Requests() int64 {
	return r.requests.Load()
}

// Close stops the server and the mock HTTPS service. It returns the error of
// server.Server.Serve. Call Close once: a second call blocks.
func (r *Running) Close() error {
	r.cancel()
	err := <-r.served
	r.https.Close()
	if err != nil {
		return fmt.Errorf("fleet: serve: %w", err)
	}
	return nil
}

// Start starts the mock HTTPS service and the authoritative server on loopback ports
// that the operating system selects. It writes the [server] line and the [https] line
// to opts.Out. The server answers until ctx ends or Close is called.
//
// Start generates a new signing key at each call (FR-demo-11). It returns an error
// that wraps schema.ErrFirmwareVersionSyntax when opts.RemoteFirmware does not parse.
func Start(ctx context.Context, opts Options) (*Running, error) {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	key, err := security.GenerateKey()
	if err != nil {
		return nil, fmt.Errorf("fleet: generate signing key: %w", err)
	}
	pub := key.Public().(ed25519.PublicKey)
	r := &Running{
		KeyID:         security.KeyID(pub),
		FirmwareKeyID: security.KeyID(ed25519.NewKeyFromSeed([]byte(firmwareKeySeed)).Public().(ed25519.PublicKey)),
	}
	r.TrustAnchor = security.TrustAnchor{KeyID: r.KeyID, PublicKey: pub}

	mux := http.NewServeMux()
	r.https = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.requests.Add(1)
		mux.ServeHTTP(w, req)
	}))
	r.HTTPSURL = r.https.URL
	r.HTTPClient = r.https.Client()

	srv := server.NewServer(server.ServerConfig{
		Zones:  []string{Zone},
		Logger: logger,
		Signer: &security.Signer{Key: key},
	})
	if err := register(srv, mux, r, opts, logger); err != nil {
		r.https.Close()
		return nil, err
	}
	pc, l, err := listen()
	if err != nil {
		r.https.Close()
		return nil, err
	}
	r.Addr = pc.LocalAddr().String()
	if opts.TamperDNS {
		pc = tamperConn{pc}
	}

	ctx, r.cancel = context.WithCancel(ctx)
	r.served = make(chan error, 1)
	go func() { r.served <- srv.Serve(ctx, pc, l) }()

	_, err = fmt.Fprintf(out, "[server]  zone %s on %s (udp, tcp), key id %s\n[https]   mock service on %s\n",
		Zone, r.Addr, hex.EncodeToString(r.KeyID[:]), r.HTTPSURL)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("fleet: write start lines: %w", err), r.Close())
	}
	return r, nil
}

// register adds the resources to mux and registers discovery and the three protocols
// on srv.
func register(srv *server.Server, mux *http.ServeMux, r *Running, opts Options, logger *slog.Logger) error {
	bundle := fmt.Appendf(nil, "configuration generation %d\n", opts.RemoteGeneration)
	image := fmt.Appendf(nil, "firmware image %s\n", opts.RemoteFirmware)
	bundlePath := fmt.Sprintf("/config-%d.bin", opts.RemoteGeneration)
	imagePath := fmt.Sprintf("/firmware-%s.img", opts.RemoteFirmware)
	bundleSum := sha256.Sum256(bundle)
	imageSum := sha256.Sum256(image)
	if opts.TamperHTTPS {
		// The digest stays the digest of the original bundle.
		bundle = append(bundle, "tampered\n"...)
	}
	mux.HandleFunc("GET "+bundlePath, serveBytes(bundle, logger))
	mux.HandleFunc("GET "+imagePath, serveBytes(image, logger))

	protocols := []struct {
		id  string
		key protocol.ProtocolKey
		// The payload value is also the schema of its protocol.
		payload interface {
			protocol.Schema
			Marshal() ([]byte, error)
		}
	}{
		{BootstrapID, protocol.ProtocolKey{Class: 65280, Type: 65280},
			schema.Bootstrap{ManagementURL: r.HTTPSURL + "/mgmt", ManagementTransport: "https"}},
		{ConfigurationID, protocol.ProtocolKey{Class: configurationClass, Type: configurationType},
			schema.Configuration{Generation: opts.RemoteGeneration, Digest: bundleSum[:], URL: r.HTTPSURL + bundlePath}},
		{FirmwareID, protocol.ProtocolKey{Class: 65282, Type: 65280},
			schema.Firmware{Version: opts.RemoteFirmware, Digest: imageSum[:], URL: r.HTTPSURL + imagePath, KeyID: r.FirmwareKeyID[:]}},
	}
	for _, p := range protocols {
		payload, err := p.payload.Marshal()
		if err != nil {
			return fmt.Errorf("fleet: encode %s payload: %w", p.id, err)
		}
		info := protocol.Info{ID: p.id, Versions: []uint16{1}, Schema: p.payload}
		if err := srv.RegisterProtocol(p.key, info, answerDevice42(payload)); err != nil {
			return fmt.Errorf("fleet: register %s: %w", p.id, err)
		}
	}
	if err := discovery.Register(srv, Zone, discovery.Options{Key: discovery.DefaultKey}); err != nil {
		return fmt.Errorf("fleet: register discovery: %w", err)
	}
	return nil
}

// answerDevice42 returns a handler that answers DeviceName with payload and every
// other QNAME with status not-found. The server applies the schema to the empty
// payload of not-found, so a client gets SERVFAIL for another QNAME.
func answerDevice42(payload []byte) protocol.Handler {
	return protocol.HandlerFunc(func(_ context.Context, req *protocol.Request) (*protocol.Response, error) {
		if req.Name != DeviceName {
			return &protocol.Response{Status: protocol.StatusNotFound}, nil
		}
		return &protocol.Response{Payload: payload}, nil
	})
}

// serveBytes returns a handler that writes b. The device detects a short body by its
// digest, so a failed write is only logged.
func serveBytes(b []byte, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		if _, err := w.Write(b); err != nil {
			logger.Warn("fleet: write resource", "path", req.URL.Path, "err", err)
		}
	}
}

// listen opens a UDP socket on a loopback port that the operating system selects, and
// a TCP listener on the same port. The client falls back to TCP at the address of the
// UDP server, so both must share the port. Another process can hold the TCP port, so
// listen tries again with a new UDP port.
func listen() (net.PacketConn, net.Listener, error) {
	var errs []error
	for range listenAttempts {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			return nil, nil, fmt.Errorf("fleet: listen udp: %w", err)
		}
		l, err := net.Listen("tcp", pc.LocalAddr().String())
		if err == nil {
			return pc, l, nil
		}
		errs = append(errs, fmt.Errorf("fleet: listen tcp %s: %w", pc.LocalAddr(), err), pc.Close())
	}
	return nil, nil, errors.Join(errs...)
}
