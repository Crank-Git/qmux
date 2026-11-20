package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/registry"
	"github.com/Crank-Git/qmux/pkg/server"
)

// The limits of the configuration file schema table.
const (
	minSignatureValidity = time.Minute
	maxSignatureValidity = 24 * time.Hour
	minTTL               = 1
	maxTTL               = 86400
	minUDPSize           = 512
	maxUDPSize           = 4096
	minHandlerTimeout    = 10 * time.Millisecond
	maxHandlerTimeout    = 30 * time.Second
	// minProtocolStatus is the first protocol-specific status value. A record can use
	// a lower value only through its status name.
	minProtocolStatus = 256
)

// statusNames maps the names of the status table to the status values.
var statusNames = map[string]protocol.Status{
	"ok":                  protocol.StatusOK,
	"not-found":           protocol.StatusNotFound,
	"bad-request":         protocol.StatusBadRequest,
	"version-unsupported": protocol.StatusVersionUnsupported,
	"unavailable":         protocol.StatusUnavailable,
}

// logLevels maps the log_level values of the schema table to the slog levels.
var logLevels = map[string]slog.Level{
	"debug": slog.LevelDebug,
	"info":  slog.LevelInfo,
	"warn":  slog.LevelWarn,
	"error": slog.LevelError,
}

// config is the configuration file of qmuxd serve after validation. Every path is
// resolved from the directory of the file, and every name is in lowercase.
type config struct {
	Listen            string
	Zones             []string
	SigningKey        string
	SignatureValidity time.Duration
	TTL               uint32
	UDPSize           uint16
	HandlerTimeout    time.Duration
	RateLimit         server.RateLimit
	Registry          registry.Options
	Discovery         discoveryConfig
	// ObservationLog is the path of the observation log. Empty disables it.
	//
	// TODO(#59): Open the file and set server.ServerConfig.ObservationLog after #58
	// adds the field.
	ObservationLog string
	LogLevel       slog.Level
	LogFormat      string
	Protocols      []protocolConfig
}

// discoveryConfig is the discovery field of the configuration file.
type discoveryConfig struct {
	Enabled bool `json:"enabled"`
	InClass bool `json:"in_class"`
}

// protocolConfig is one entry of the protocols field after validation.
type protocolConfig struct {
	Key  protocol.ProtocolKey
	Info protocol.Info
	// Records maps each record QNAME, in lowercase, to its record.
	Records map[string]record
}

// record is one record of a protocol after validation.
type record struct {
	// Payload holds the payload bytes: the CBOR encoding of the payload field, or the
	// decoded payload_hex field.
	Payload []byte
	Status  protocol.Status
	// Version is the one version that the record answers. 0 means every version.
	Version uint16
}

// fileConfig is the configuration file as it decodes. Each field holds the default of
// the schema table before the decode. The nested objects stay raw, so that each error
// names its JSON path.
type fileConfig struct {
	Listen               string            `json:"listen"`
	Zones                []string          `json:"zones"`
	SigningKey           string            `json:"signing_key"`
	SignatureValidity    string            `json:"signature_validity"`
	TTL                  uint32            `json:"ttl"`
	UDPSize              uint16            `json:"udp_size"`
	HandlerTimeout       string            `json:"handler_timeout"`
	RateLimit            json.RawMessage   `json:"rate_limit"`
	AllowReservedClasses bool              `json:"allow_reserved_classes"`
	AllowNonPrivateTypes bool              `json:"allow_non_private_types"`
	Discovery            json.RawMessage   `json:"discovery"`
	ObservationLog       *string           `json:"observation_log"`
	LogLevel             string            `json:"log_level"`
	LogFormat            string            `json:"log_format"`
	Protocols            []json.RawMessage `json:"protocols"`
}

// fileRateLimit is the rate_limit field as it decodes.
type fileRateLimit struct {
	QPS   float64 `json:"qps"`
	Burst int     `json:"burst"`
}

// fileProtocol is one entry of the protocols field as it decodes.
type fileProtocol struct {
	ID       string                     `json:"id"`
	Versions []uint16                   `json:"versions"`
	Class    uint16                     `json:"class"`
	Type     uint16                     `json:"type"`
	Records  map[string]json.RawMessage `json:"records"`
}

// fileRecord is one record as it decodes.
type fileRecord struct {
	Payload    json.RawMessage `json:"payload"`
	PayloadHex *string         `json:"payload_hex"`
	Status     json.RawMessage `json:"status"`
	Version    *uint16         `json:"version"`
}

// loadConfig decodes and validates the configuration file at path. A non-empty listen
// replaces the listen field. The error names the JSON path of the first error.
func loadConfig(path, listen string) (config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return config{}, fmt.Errorf("read: %w", err)
	}
	f := fileConfig{
		Listen:            defaultListen,
		SignatureValidity: "1h",
		TTL:               60,
		UDPSize:           1232,
		HandlerTimeout:    "2s",
		LogLevel:          "info",
		LogFormat:         "text",
	}
	if err := decodeStrict(b, &f, ""); err != nil {
		return config{}, err
	}
	if listen != "" {
		f.Listen = listen
	}
	dir := filepath.Dir(path)
	cfg := config{
		TTL:      f.TTL,
		UDPSize:  f.UDPSize,
		Registry: registry.Options{AllowReservedClasses: f.AllowReservedClasses, AllowNonPrivateTypes: f.AllowNonPrivateTypes},
	}
	if err := f.serverFields(&cfg, dir); err != nil {
		return config{}, err
	}
	if err := f.logFields(&cfg, dir); err != nil {
		return config{}, err
	}
	if err := f.protocolFields(&cfg); err != nil {
		return config{}, err
	}
	return cfg, nil
}

// serverFields validates the fields that configure the server and the signer, and
// sets them in cfg.
func (f *fileConfig) serverFields(cfg *config, dir string) error {
	if _, _, err := net.SplitHostPort(f.Listen); err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	cfg.Listen = f.Listen
	zones, err := validZones(f.Zones)
	if err != nil {
		return err
	}
	cfg.Zones = zones
	if f.SigningKey == "" {
		return errors.New("signing_key: the field is required, because qmuxd never serves unsigned envelopes")
	}
	cfg.SigningKey = resolvePath(dir, f.SigningKey)
	if cfg.SignatureValidity, err = durationIn("signature_validity", f.SignatureValidity, minSignatureValidity, maxSignatureValidity); err != nil {
		return err
	}
	if cfg.HandlerTimeout, err = durationIn("handler_timeout", f.HandlerTimeout, minHandlerTimeout, maxHandlerTimeout); err != nil {
		return err
	}
	// The server replaces TTL 0 with its default TTL, so 0 cannot give TTL 0 answers.
	if f.TTL < minTTL || f.TTL > maxTTL {
		return fmt.Errorf("ttl: %d is outside %d to %d", f.TTL, minTTL, maxTTL)
	}
	if f.UDPSize < minUDPSize || f.UDPSize > maxUDPSize {
		return fmt.Errorf("udp_size: %d is outside %d to %d", f.UDPSize, minUDPSize, maxUDPSize)
	}
	if cfg.RateLimit, err = validRateLimit(f.RateLimit); err != nil {
		return err
	}
	cfg.Discovery = discoveryConfig{Enabled: true}
	if len(f.Discovery) > 0 {
		if err := decodeStrict(f.Discovery, &cfg.Discovery, "discovery"); err != nil {
			return err
		}
	}
	return nil
}

// logFields validates the observation log and the log fields, and sets them in cfg.
func (f *fileConfig) logFields(cfg *config, dir string) error {
	if f.ObservationLog != nil {
		if *f.ObservationLog == "" {
			return errors.New("observation_log: the path is empty; leave out the field to disable the observation log")
		}
		cfg.ObservationLog = resolvePath(dir, *f.ObservationLog)
	}
	level, ok := logLevels[f.LogLevel]
	if !ok {
		return fmt.Errorf("log_level: %q is not debug, info, warn or error", f.LogLevel)
	}
	cfg.LogLevel = level
	if f.LogFormat != "text" && f.LogFormat != "json" {
		return fmt.Errorf("log_format: %q is not text or json", f.LogFormat)
	}
	cfg.LogFormat = f.LogFormat
	return nil
}

// protocolFields validates each entry of the protocols field and sets them in cfg. It
// needs cfg.Zones, so it runs after serverFields.
func (f *fileConfig) protocolFields(cfg *config) error {
	if len(f.Protocols) == 0 {
		return errors.New("protocols: at least one protocol is required")
	}
	cfg.Protocols = make([]protocolConfig, len(f.Protocols))
	for i, raw := range f.Protocols {
		p, err := validProtocol(raw, fmt.Sprintf("protocols[%d]", i), cfg.Zones)
		if err != nil {
			return err
		}
		cfg.Protocols[i] = p
	}
	return nil
}

// validZones returns the zones in lowercase. It returns an error when no zone exists,
// when a zone is not fully qualified, or when two zones differ only in case.
func validZones(zones []string) ([]string, error) {
	if len(zones) == 0 {
		return nil, errors.New("zones: at least one zone is required")
	}
	out := make([]string, len(zones))
	for i, z := range zones {
		name := protocol.FoldName(z)
		switch {
		case !strings.HasSuffix(name, "."):
			return nil, fmt.Errorf("zones[%d]: %q is not a fully qualified name", i, z)
		case slices.Contains(out[:i], name):
			return nil, fmt.Errorf("zones[%d]: %q repeats an earlier zone", i, z)
		}
		out[i] = name
	}
	return out, nil
}

// validRateLimit returns the rate limit of the rate_limit field raw. A field that raw
// does not hold keeps its default, qps 50 and burst 100.
func validRateLimit(raw json.RawMessage) (server.RateLimit, error) {
	rl := fileRateLimit{QPS: 50, Burst: 100}
	if len(raw) > 0 {
		if err := decodeStrict(raw, &rl, "rate_limit"); err != nil {
			return server.RateLimit{}, err
		}
	}
	switch {
	case rl.QPS < 0:
		return server.RateLimit{}, fmt.Errorf("rate_limit.qps: %v is below 0", rl.QPS)
	case rl.Burst < 0:
		return server.RateLimit{}, fmt.Errorf("rate_limit.burst: %d is below 0", rl.Burst)
	// A bucket that holds fewer tokens than qps refuses queries that the rate allows.
	case rl.QPS > 0 && float64(rl.Burst) < rl.QPS:
		return server.RateLimit{}, fmt.Errorf("rate_limit.burst: %d is below qps %v", rl.Burst, rl.QPS)
	}
	return server.RateLimit{QPS: rl.QPS, Burst: rl.Burst}, nil
}

// validProtocol decodes and validates the protocol entry raw at the JSON path path.
// Each record QNAME must be at or below one of zones.
func validProtocol(raw json.RawMessage, path string, zones []string) (protocolConfig, error) {
	var fp fileProtocol
	if err := decodeStrict(raw, &fp, path); err != nil {
		return protocolConfig{}, err
	}
	for i, v := range fp.Versions {
		if v == 0 || (i > 0 && v <= fp.Versions[i-1]) {
			return protocolConfig{}, fmt.Errorf("%s.versions: %v is not a sorted list of unique versions 1 to 65535", path, fp.Versions)
		}
	}
	p := protocolConfig{
		Key:     protocol.ProtocolKey{Class: fp.Class, Type: fp.Type},
		Info:    protocol.Info{ID: fp.ID, Versions: fp.Versions},
		Records: make(map[string]record, len(fp.Records)),
	}
	// The sorted order makes the first error the same at each start.
	for _, qname := range slices.Sorted(maps.Keys(fp.Records)) {
		rpath := fmt.Sprintf("%s.records[%q]", path, qname)
		name := protocol.FoldName(qname)
		if !inZones(name, zones) {
			return protocolConfig{}, fmt.Errorf("%s: %s is not at or below a configured zone", rpath, qname)
		}
		if _, ok := p.Records[name]; ok {
			return protocolConfig{}, fmt.Errorf("%s: %s repeats another record QNAME of the protocol, without regard to case", rpath, qname)
		}
		r, err := validRecord(fp.Records[qname], rpath, fp.Versions)
		if err != nil {
			return protocolConfig{}, err
		}
		p.Records[name] = r
	}
	return p, nil
}

// validRecord decodes and validates the record raw at the JSON path path. versions
// holds the versions of the protocol.
func validRecord(raw json.RawMessage, path string, versions []uint16) (record, error) {
	var fr fileRecord
	if err := decodeStrict(raw, &fr, path); err != nil {
		return record{}, err
	}
	payload, err := recordPayload(fr, path)
	if err != nil {
		return record{}, err
	}
	r := record{Payload: payload}
	if fr.Version != nil {
		if !slices.Contains(versions, *fr.Version) {
			return record{}, fmt.Errorf("%s.version: %d is not a version of the protocol", path, *fr.Version)
		}
		r.Version = *fr.Version
	}
	status, err := validStatus(fr.Status, path+".status")
	if err != nil {
		return record{}, err
	}
	r.Status = status
	return r, nil
}

// validStatus returns the status of the status field raw at the JSON path path. raw
// holds a status name or an integer 256 to 65535. An absent field gives status ok.
func validStatus(raw json.RawMessage, path string) (protocol.Status, error) {
	if len(raw) == 0 {
		return protocol.StatusOK, nil
	}
	var name string
	if json.Unmarshal(raw, &name) == nil {
		s, ok := statusNames[name]
		if !ok {
			return 0, fmt.Errorf("%s: %q is not a status name", path, name)
		}
		return s, nil
	}
	var n uint64
	if err := json.Unmarshal(raw, &n); err != nil || n < minProtocolStatus || n > 65535 {
		return 0, fmt.Errorf("%s: %s is not a status name or an integer %d to 65535", path, raw, minProtocolStatus)
	}
	return protocol.Status(n), nil
}

// inZones reports whether the lowercase name is at or below one of zones.
func inZones(name string, zones []string) bool {
	for _, z := range zones {
		if name == z || strings.HasSuffix(name, "."+z) || (z == "." && strings.HasSuffix(name, ".")) {
			return true
		}
	}
	return false
}

// durationIn parses the Go duration s of the field path and returns an error when it
// is outside lo to hi.
func durationIn(path, s string, lo, hi time.Duration) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	if d < lo || d > hi {
		return 0, fmt.Errorf("%s: %v is outside %v to %v", path, d, lo, hi)
	}
	return d, nil
}

// resolvePath returns p, or p joined to dir when p is relative.
func resolvePath(dir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(dir, p)
}

// decodeStrict decodes the one JSON value data into v and rejects an unknown field. path
// is the JSON path of data, and every error names the JSON path of its cause.
func decodeStrict(data []byte, v any, path string) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil {
		if _, tokErr := dec.Token(); !errors.Is(tokErr, io.EOF) {
			err = errors.New("data after the JSON value")
		}
	}
	var typeErr *json.UnmarshalTypeError
	var syntaxErr *json.SyntaxError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &typeErr):
		return fmt.Errorf("%s: %s is not a valid %s", joinPath(path, typeErr.Field), typeErr.Value, typeErr.Type)
	case errors.As(err, &syntaxErr):
		err = fmt.Errorf("offset %d: %w", syntaxErr.Offset, err)
	}
	if path == "" {
		return err
	}
	return fmt.Errorf("%s: %w", path, err)
}

// joinPath appends the dotted field path of encoding/json to the JSON path path. A
// numeric element is an array index, so it becomes "[n]".
func joinPath(path, field string) string {
	var b strings.Builder
	b.WriteString(path)
	for _, elem := range strings.Split(field, ".") {
		if _, err := strconv.Atoi(elem); err == nil {
			b.WriteString("[" + elem + "]")
			continue
		}
		if b.Len() > 0 {
			b.WriteString(".")
		}
		b.WriteString(elem)
	}
	return b.String()
}
