package discovery

import (
	"errors"
	"fmt"

	"github.com/Crank-Git/qmux/pkg/protocol"
)

// frameworkVersion is the only manifest framework version that this package accepts.
const frameworkVersion = 1

// ErrInvalidManifest is the error that manifest validation wraps for every rule that
// a manifest breaks.
var ErrInvalidManifest = errors.New("discovery: invalid manifest")

// Entry is one registration in a manifest: the protocol ID, the supported versions
// and the protocol key. The versions are in ascending order with no repeats.
type Entry struct {
	ID       string   `cbor:"1,keyasint"`
	Versions []uint16 `cbor:"2,keyasint"`
	Class    uint16   `cbor:"3,keyasint"`
	Type     uint16   `cbor:"4,keyasint"`
}

// Manifest is the manifest payload (message type 2). It lists every registration of
// one server for one zone. Encode it with codec.Marshal and decode it with
// codec.Unmarshal.
type Manifest struct {
	FrameworkVersion uint64  `cbor:"1,keyasint"`
	Zone             string  `cbor:"2,keyasint"`
	Protocols        []Entry `cbor:"3,keyasint"`
}

// Lookup returns the entry for the protocol ID id. The boolean is false when the
// manifest has no entry for id.
func (m *Manifest) Lookup(id string) (Entry, bool) {
	for _, e := range m.Protocols {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// validate returns an error that wraps ErrInvalidManifest when m breaks a manifest
// rule. The zone is the zone that the client queried. The zone comparison ignores
// letter case, because DNS names are not case-sensitive.
func (m *Manifest) validate(zone string) error {
	if m.FrameworkVersion != frameworkVersion {
		return fmt.Errorf("%w: framework version %d, want %d",
			ErrInvalidManifest, m.FrameworkVersion, frameworkVersion)
	}
	if protocol.FoldName(m.Zone) != protocol.FoldName(zone) {
		return fmt.Errorf("%w: zone %q, want %q", ErrInvalidManifest, m.Zone, zone)
	}
	ids := make(map[string]bool, len(m.Protocols))
	keys := make(map[protocol.ProtocolKey]bool, len(m.Protocols))
	for _, e := range m.Protocols {
		if ids[e.ID] {
			return fmt.Errorf("%w: protocol ID %q appears twice", ErrInvalidManifest, e.ID)
		}
		ids[e.ID] = true
		key := protocol.ProtocolKey{Class: e.Class, Type: e.Type}
		if keys[key] {
			return fmt.Errorf("%w: protocol key %s appears twice", ErrInvalidManifest, key)
		}
		keys[key] = true
		if err := validateVersions(e.Versions); err != nil {
			return fmt.Errorf("%w: protocol ID %q: %w", ErrInvalidManifest, e.ID, err)
		}
	}
	return nil
}

// validateVersions returns an error when the version list is empty, holds version 0,
// or is not in strictly ascending order.
func validateVersions(versions []uint16) error {
	if len(versions) == 0 {
		return errors.New("empty version list")
	}
	// Strictly ascending order from a start of 0 also rejects version 0 and repeats.
	var prev uint16
	for _, v := range versions {
		if v <= prev {
			return fmt.Errorf("version list %v is not strictly ascending from 1", versions)
		}
		prev = v
	}
	return nil
}
