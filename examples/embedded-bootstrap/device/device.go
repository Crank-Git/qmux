// Package device is the device of the embedded recovery demonstration. Reconcile
// brings the device state up to the verified metadata of the server.
//
// The device runs discovery for fleet.example., negotiates version 1 of the
// bootstrap, configuration and firmware protocols, and queries each one for
// device42.fleet.example. It downloads a resource only when the verified metadata
// shows a change, and compares the SHA-256 digest of each resource with the signed
// digest. It writes the device state last, with a rename. It stores the firmware
// image and never flashes it.
package device

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Crank-Git/qmux/examples/embedded-bootstrap/schema"
	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/discovery"
	"github.com/Crank-Git/qmux/pkg/security"
)

const (
	zone = "fleet.example."
	name = "device42.fleet.example."

	bootstrapID     = "org.qmux.bootstrap"
	configurationID = "org.qmux.configuration"
	firmwareID      = "org.qmux.firmware"

	// maxResourceSize limits the bytes that the device reads for one resource, so
	// that a hostile service cannot fill the memory of the device.
	maxResourceSize = 64 << 20
)

// supported holds the protocol versions that the device speaks for each protocol.
var supported = []uint16{1}

var (
	// ErrInvalidState is the error for a device state file that does not hold a valid
	// device state. Reconcile does not change the file.
	ErrInvalidState = errors.New("device state is not valid")
	// ErrDigestMismatch is the error for a downloaded resource whose SHA-256 digest
	// differs from the digest in the verified metadata. Reconcile discards the
	// resource.
	ErrDigestMismatch = errors.New("digest mismatch, resource discarded")
	// ErrHTTPStatus is the error for a resource response with an HTTP status other
	// than 200.
	ErrHTTPStatus = errors.New("HTTPS status is not 200")
)

// State is the device state: the configuration generation and the firmware version
// that the device holds.
type State struct {
	ConfigGeneration uint64 `json:"config_generation"`
	FirmwareVersion  string `json:"firmware_version"`
}

// String returns the state as "config_generation=<n> firmware_version=<v>".
func (s State) String() string {
	return fmt.Sprintf("config_generation=%d firmware_version=%s", s.ConfigGeneration, s.FirmwareVersion)
}

// Config holds the settings of Reconcile.
type Config struct {
	// Resolver is the IP address and port of the authoritative server.
	Resolver string
	// TrustAnchors verifies every envelope. Reconcile returns discovery.ErrInsecure
	// when it is nil, because the device never acts on unverified metadata.
	TrustAnchors *security.Verifier
	// HTTPClient downloads the resources. It must trust only the certificate of the
	// resource service.
	HTTPClient *http.Client
	// StatePath is the path of the device state file.
	StatePath string
	// DataDir is the directory for the downloaded resources. Reconcile creates it
	// with mode 0700.
	DataDir string
	// AllowedFirmwareKeyIDs holds the firmware signing key IDs that the device
	// accepts. An empty list allows no firmware image.
	AllowedFirmwareKeyIDs [][8]byte
	// Log receives one line per step, for example
	// "state: config_generation=0 firmware_version=0.0.0". nil discards the lines.
	Log func(line string)
}

// Report is the result of a Reconcile that returned no error.
type Report struct {
	// Before is the device state that Reconcile read.
	Before State
	// After is the device state after the reconcile. It equals Before when no
	// resource changed.
	After State
	// ManagementURL and ManagementTransport come from the bootstrap result.
	ManagementURL       string
	ManagementTransport string
	// Downloads is the number of resources that Reconcile downloaded and stored.
	Downloads int
	// StateWritten is true when Reconcile wrote a new device state file.
	StateWritten bool
}

// StepError is a failure of one step of Reconcile. The step is "state",
// "discovery", "negotiate", "bootstrap", "configuration" or "firmware".
type StepError struct {
	Step string
	Err  error
}

// Error returns the step and the cause, for example
// "configuration: digest mismatch, resource discarded". For a *client.VerifyError,
// the cause is "verification failed: " and the check that failed, for example
// "configuration: verification failed: bad signature".
func (e *StepError) Error() string {
	var ve *client.VerifyError
	if errors.As(e.Err, &ve) {
		return e.Step + ": verification failed: " + strings.TrimPrefix(ve.Reason.Error(), "security: ")
	}
	return e.Step + ": " + e.Err.Error()
}

// Unwrap returns the cause, so that errors.Is and errors.As find the sentinels of
// this package, discovery and client.
func (e *StepError) Unwrap() error { return e.Err }

// reconciler holds the inputs and the progress of one Reconcile.
type reconciler struct {
	cfg      Config
	disc     *discovery.Client
	manifest *discovery.Manifest
	report   Report
}

// Reconcile brings the device state up to the verified metadata of the server at
// cfg.Resolver. It writes the accepted resources to cfg.DataDir and then writes the
// new device state to cfg.StatePath. It writes the device state only after every
// check passes, so on an error the device state file keeps its bytes.
//
// Reconcile returns a *StepError for each failure. The cause is, for example:
//   - ErrInvalidState for a device state file that does not decode.
//   - A *client.VerifyError for an envelope that does not verify.
//   - discovery.ErrInsecure when cfg.TrustAnchors is nil.
//   - discovery.ErrProtocolNotFound when the manifest lacks one of the three protocols.
//   - ErrHTTPStatus or ErrDigestMismatch for a resource download.
func Reconcile(ctx context.Context, cfg Config) (Report, error) {
	if cfg.Log == nil {
		cfg.Log = func(string) {}
	}
	r := &reconciler{
		cfg:  cfg,
		disc: &discovery.Client{Client: client.NewClient(client.ClientConfig{Resolver: cfg.Resolver, TrustAnchors: cfg.TrustAnchors})},
	}
	steps := []struct {
		name string
		run  func(context.Context) error
	}{
		{"state", r.readState},
		{"discovery", r.discover},
		{"negotiate", r.negotiate},
		{"bootstrap", r.bootstrap},
		{"configuration", r.configuration},
		{"firmware", r.firmware},
		{"state", r.writeState},
	}
	for _, s := range steps {
		if err := s.run(ctx); err != nil {
			return Report{}, &StepError{Step: s.name, Err: err}
		}
	}
	return r.report, nil
}

func (r *reconciler) logf(format string, args ...any) {
	r.cfg.Log(fmt.Sprintf(format, args...))
}

// readState reads the device state file and creates the data directory. A missing
// file gives generation 0 and firmware 0.0.0 (FR-demo-20).
func (r *reconciler) readState(context.Context) error {
	s := State{FirmwareVersion: "0.0.0"}
	b, err := os.ReadFile(r.cfg.StatePath)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return fmt.Errorf("read %s: %w", r.cfg.StatePath, err)
	default:
		if err := json.Unmarshal(b, &s); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrInvalidState, r.cfg.StatePath, err)
		}
		if _, err := schema.ParseFirmwareVersion(s.FirmwareVersion); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrInvalidState, r.cfg.StatePath, err)
		}
	}
	r.report.Before, r.report.After = s, s
	r.logf("state: %s", s)
	if err := os.MkdirAll(r.cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	return nil
}

func (r *reconciler) discover(ctx context.Context) error {
	m, err := r.disc.Discover(ctx, zone)
	if err != nil {
		return err
	}
	r.manifest = m
	r.logf("discovery: verified manifest, %d protocols", len(m.Protocols))
	return nil
}

// negotiate checks all three protocols before the first protocol query, so that a
// manifest that lacks one stops the device before it acts on the others.
func (r *reconciler) negotiate(context.Context) error {
	parts := make([]string, 0, 3)
	for _, id := range []string{bootstrapID, configurationID, firmwareID} {
		_, v, err := discovery.Negotiate(r.manifest, id, supported)
		if err != nil {
			return err
		}
		parts = append(parts, fmt.Sprintf("%s v%d", id, v))
	}
	r.logf("negotiate: %s", strings.Join(parts, ", "))
	return nil
}

// query returns the verified payload of the protocol id for device42.
func (r *reconciler) query(ctx context.Context, id string) ([]byte, error) {
	res, err := r.disc.QueryProtocol(ctx, r.manifest, id, name, supported)
	if err != nil {
		return nil, err
	}
	return res.Payload, nil
}

func (r *reconciler) bootstrap(ctx context.Context) error {
	b, err := r.query(ctx, bootstrapID)
	if err != nil {
		return err
	}
	p, err := schema.UnmarshalBootstrap(b)
	if err != nil {
		return err
	}
	r.report.ManagementURL, r.report.ManagementTransport = p.ManagementURL, p.ManagementTransport
	r.logf("bootstrap: management %s (transport %s)", p.ManagementURL, p.ManagementTransport)
	return nil
}

func (r *reconciler) configuration(ctx context.Context) error {
	b, err := r.query(ctx, configurationID)
	if err != nil {
		return err
	}
	p, err := schema.UnmarshalConfiguration(b)
	if err != nil {
		return err
	}
	local := r.report.Before.ConfigGeneration
	switch {
	case p.Generation < local:
		r.logf("configuration: remote generation %d < local %d, rollback refused", p.Generation, local)
		return nil
	case p.Generation == local:
		r.logf("configuration: remote generation %d = local %d, up to date", p.Generation, local)
		return nil
	}
	r.logf("configuration: remote generation %d > local %d, downloading", p.Generation, local)
	file := fmt.Sprintf("config-%d.bin", p.Generation)
	if err := r.store(ctx, p.URL, p.Digest, file); err != nil {
		return err
	}
	r.report.After.ConfigGeneration = p.Generation
	r.logf("configuration: digest ok, wrote %s", file)
	return nil
}

func (r *reconciler) firmware(ctx context.Context) error {
	b, err := r.query(ctx, firmwareID)
	if err != nil {
		return err
	}
	p, err := schema.UnmarshalFirmware(b)
	if err != nil {
		return err
	}
	remote, err := schema.ParseFirmwareVersion(p.Version)
	if err != nil {
		return err
	}
	local, err := schema.ParseFirmwareVersion(r.report.Before.FirmwareVersion)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidState, err)
	}
	switch remote.Compare(local) {
	case -1:
		r.logf("firmware: remote %s < local %s, up to date", remote, local)
		return nil
	case 0:
		r.logf("firmware: remote %s = local %s, up to date", remote, local)
		return nil
	}
	kid := [8]byte(p.KeyID)
	if !slices.Contains(r.cfg.AllowedFirmwareKeyIDs, kid) {
		r.logf("firmware: remote %s > local %s, key id %x not allowed, skipped", remote, local, kid)
		return nil
	}
	r.logf("firmware: remote %s > local %s, key id %x allowed, downloading", remote, local, kid)
	file := fmt.Sprintf("firmware-%s.img", remote)
	if err := r.store(ctx, p.URL, p.Digest, file); err != nil {
		return err
	}
	r.report.After.FirmwareVersion = remote.String()
	r.logf("firmware: digest ok, stored %s (not flashed)", file)
	return nil
}

// store downloads the resource at url and writes it to file in the data directory
// when its SHA-256 digest equals digest. It writes nothing for a mismatch.
func (r *reconciler) store(ctx context.Context, url string, digest []byte, file string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	resp, err := r.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // The body is read-only.
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %d for %s", ErrHTTPStatus, resp.StatusCode, url)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResourceSize))
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	if sum := sha256.Sum256(body); !bytes.Equal(sum[:], digest) {
		return ErrDigestMismatch
	}
	if err := writeAtomic(filepath.Join(r.cfg.DataDir, file), body); err != nil {
		return err
	}
	r.report.Downloads++
	return nil
}

// writeState writes the new device state when a resource changed it.
func (r *reconciler) writeState(context.Context) error {
	if r.report.After == r.report.Before {
		return nil
	}
	b, err := json.Marshal(r.report.After)
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	if err := writeAtomic(r.cfg.StatePath, append(b, '\n')); err != nil {
		return err
	}
	r.report.StateWritten = true
	r.logf("state written: %s", r.report.After)
	return nil
}

// writeAtomic writes b to path with mode 0600. It writes a temporary file in the
// same directory and renames it, so a crash leaves the old file or the new file
// and never a part of one (FR-demo-35). On a failure it removes the temporary file.
func writeAtomic(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", path, errors.Join(err, os.Remove(f.Name())))
	}
	return nil
}
