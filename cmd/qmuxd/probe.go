package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/registry"
	"github.com/Crank-Git/qmux/pkg/server"
)

// probeID returns the protocol ID of the probe protocol at key, for example
// org.qmux.probe.65281-65280 (FR-interop-2). The registry holds one protocol key for
// each protocol ID, so each key needs its own ID.
func probeID(key protocol.ProtocolKey) string {
	return fmt.Sprintf("org.qmux.probe.%d-%d", key.Class, key.Type)
}

// The DNS numbers that the configuration checks.
const (
	classCH = 3
	classHS = 4
	typeTXT = 16
)

// validProbe decodes and validates the probe section raw. The probe zone must be at or
// below one of zones. opts holds the allow_reserved_classes and allow_non_private_types
// fields.
//
// The error names the JSON path of the first error. A reserved class without
// allow_reserved_classes, and type 16 without allow_non_private_types, are errors
// (FR-interop-3, FR-interop-4).
func validProbe(raw json.RawMessage, zones []string, opts registry.Options) (*probeSection, error) {
	var fp fileProbe
	if err := decodeStrict(raw, &fp, "probe"); err != nil {
		return nil, err
	}
	zone := protocol.FoldName(fp.Zone)
	switch {
	case fp.Zone == "":
		return nil, errors.New("probe.zone: the field is required")
	case !strings.HasSuffix(zone, "."):
		return nil, fmt.Errorf("probe.zone: %q is not a fully qualified name", fp.Zone)
	case !inZones(zone, zones):
		return nil, fmt.Errorf("probe.zone: %s is not at or below a configured zone", fp.Zone)
	}
	classes, err := probeNumbers(fp.Classes, "probe.classes", "class", func(c uint16) bool {
		return (c == classCH || c == classHS) && !opts.AllowReservedClasses
	}, "is reserved; set allow_reserved_classes to true")
	if err != nil {
		return nil, err
	}
	types, err := probeNumbers(fp.Types, "probe.types", "type", func(t uint16) bool {
		return t == typeTXT && !opts.AllowNonPrivateTypes
	}, "is not a private type; set allow_non_private_types to true")
	if err != nil {
		return nil, err
	}
	p := &probeSection{Zone: zone, Keys: make([]protocol.ProtocolKey, 0, len(classes)*len(types))}
	for _, c := range classes {
		for _, t := range types {
			p.Keys = append(p.Keys, protocol.ProtocolKey{Class: c, Type: t})
		}
	}
	return p, nil
}

// probeNumbers decodes the classes or the types raw at the JSON path path. noun is
// "class" or "type". It returns an error when raw is empty, and an error that ends with
// reason for the first number that rejected reports true.
func probeNumbers(raw []json.RawMessage, path, noun string, rejected func(uint16) bool, reason string) ([]uint16, error) {
	nums, err := decodeArray[uint16](raw, path)
	if err != nil {
		return nil, err
	}
	if len(nums) == 0 {
		return nil, fmt.Errorf("%s: at least one %s is required", path, noun)
	}
	for i, n := range nums {
		if rejected(n) {
			return nil, fmt.Errorf("%s[%d]: %s %d %s", path, i, noun, n, reason)
		}
	}
	return nums, nil
}

// registerProbe registers the probe protocol, version 1, at each key of p with the
// protocol ID of probeID (FR-interop-2). Each registration uses the same handler. A nil p registers nothing. The error wraps the registry error.
func registerProbe(srv *server.Server, p *probeSection) error {
	if p == nil {
		return nil
	}
	h := probeHandler(p.Zone)
	for _, key := range p.Keys {
		if err := srv.RegisterProtocol(key, protocol.Info{ID: probeID(key), Versions: []uint16{1}}, h); err != nil {
			return fmt.Errorf("probe: %w", err)
		}
	}
	return nil
}

// probeHandler returns the handler of the probe protocol for the lowercase probe zone.
//
// The handler answers every QNAME at or below zone with status ok and the SHA-256
// digest of the lowercase QNAME (FR-interop-5, FR-interop-6). It answers not-found for
// another QNAME.
func probeHandler(zone string) protocol.Handler {
	zones := []string{zone}
	return protocol.HandlerFunc(func(_ context.Context, req *protocol.Request) (*protocol.Response, error) {
		// The server gives QName in lowercase, so the digest ignores the case of the
		// query.
		if !inZones(req.QName, zones) {
			return &protocol.Response{Status: protocol.StatusNotFound, Version: req.Version}, nil
		}
		digest := sha256.Sum256([]byte(req.QName))
		return &protocol.Response{Payload: digest[:], Status: protocol.StatusOK, Version: req.Version}, nil
	})
}
