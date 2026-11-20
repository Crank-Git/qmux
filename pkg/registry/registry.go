package registry

import (
	"fmt"
	"regexp"
	"slices"
	"sync"

	"github.com/Crank-Git/qmux/pkg/protocol"
)

// validIDPattern matches the allowed protocol ID format.
var validIDPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// reservedTypes lists DNS types that the registry always rejects.
var reservedTypes = map[uint16]bool{
	0: true, 41: true, 250: true, 251: true, 252: true, 255: true,
}

// Options configures the registry.
type Options struct {
	AllowReservedClasses bool
	AllowNonPrivateTypes bool
}

// Registration holds a single protocol registration.
type Registration struct {
	Key     protocol.ProtocolKey
	Info    protocol.Info
	Handler protocol.Handler
}

// Registry manages protocol registrations.
type Registry struct {
	mu    sync.RWMutex
	byKey map[protocol.ProtocolKey]Registration
	byID  map[string]protocol.ProtocolKey
	opts  Options
}

// New creates a new Registry with the given options.
func New(opts Options) *Registry {
	return &Registry{
		byKey: make(map[protocol.ProtocolKey]Registration),
		byID:  make(map[string]protocol.ProtocolKey),
		opts:  opts,
	}
}

// Register stores a protocol registration. It validates the handler, ID, versions,
// and key before storing. It rejects duplicate keys and duplicate IDs.
//
// The validation order is: handler, ID, versions, key, key conflict, ID conflict.
// The first failed rule decides the error value.
func (r *Registry) Register(key protocol.ProtocolKey, info protocol.Info, handler protocol.Handler) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Validate handler.
	if handler == nil {
		return fmt.Errorf("%s (%s): %w", info.ID, key, ErrInvalidHandler)
	}

	// Validate ID.
	if err := validateID(info.ID); err != nil {
		return fmt.Errorf("%s (%s): %w", info.ID, key, err)
	}

	// Validate versions.
	if err := r.validateVersions(info.Versions); err != nil {
		return fmt.Errorf("%s (%s): %w", info.ID, key, err)
	}

	// Validate key.
	if err := r.validateKey(key); err != nil {
		return fmt.Errorf("%s (%s): %w", info.ID, key, err)
	}

	// Check key conflict.
	if _, exists := r.byKey[key]; exists {
		return fmt.Errorf("%s (%s): %w", info.ID, key, ErrKeyConflict)
	}

	// Check ID conflict.
	if existingKey, exists := r.byID[info.ID]; exists {
		return fmt.Errorf("%s (%s): %w (conflicts with %s)", info.ID, key, ErrIDConflict, existingKey)
	}

	// Store the registration with sorted versions.
	sorted := make([]uint16, len(info.Versions))
	copy(sorted, info.Versions)
	slices.Sort(sorted)

	r.byKey[key] = Registration{
		Key:     key,
		Info:    protocol.Info{ID: info.ID, Versions: sorted, Schema: info.Schema},
		Handler: handler,
	}
	r.byID[info.ID] = key

	return nil
}

// Lookup returns the registration for the given key.
func (r *Registry) Lookup(key protocol.ProtocolKey) (Registration, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	reg, ok := r.byKey[key]
	if !ok {
		return Registration{}, fmt.Errorf("%s: %w", key, ErrNotFound)
	}
	return reg, nil
}

// validateID checks protocol ID validity (FR-registry-12).
func validateID(id string) error {
	if len(id) == 0 || len(id) > 128 {
		return fmt.Errorf("%w: ID length %d", ErrInvalidID, len(id))
	}
	if !validIDPattern.MatchString(id) {
		return fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	return nil
}

// validateVersions checks versions validity (FR-registry-13, 14, 15, 16).
// It does not sort. Register sorts before storing.
func (r *Registry) validateVersions(versions []uint16) error {
	if len(versions) == 0 {
		return fmt.Errorf("%w: empty", ErrInvalidVersions)
	}
	seen := make(map[uint16]bool)
	for _, v := range versions {
		if v == 0 {
			return fmt.Errorf("%w: version 0", ErrInvalidVersions)
		}
		if seen[v] {
			return fmt.Errorf("%w: duplicate %d", ErrInvalidVersions, v)
		}
		seen[v] = true
	}
	return nil
}

// validateKey checks protocol key validity (FR-registry-17, 18, 19, 20, 21, 22).
func (r *Registry) validateKey(key protocol.ProtocolKey) error {
	class := key.Class
	typ := key.Type

	// Validate class.
	if class == 1 {
		// IN class always accepted.
	} else if class >= 65280 && class <= 65534 {
		// Private class always accepted.
	} else if class == 3 && r.opts.AllowReservedClasses {
		// CH class allowed with option.
	} else {
		return fmt.Errorf("%w: class %d", ErrInvalidKey, class)
	}

	// Reserved types always rejected.
	if reservedTypes[typ] {
		return fmt.Errorf("%w: type %d is reserved", ErrInvalidKey, typ)
	}

	// Private types always accepted.
	if typ >= 65280 && typ <= 65534 {
		return nil
	}

	// Non-private types accepted only with option.
	if r.opts.AllowNonPrivateTypes {
		return nil
	}

	return fmt.Errorf("%w: type %d", ErrInvalidKey, typ)
}
