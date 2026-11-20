package registry

import (
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/Crank-Git/qmux/pkg/protocol"
)

// The sentinel errors of the registry. Every error that the registry returns
// wraps one of them, so callers test it with errors.Is.
var (
	ErrInvalidKey      = errors.New("registry: invalid protocol key")
	ErrInvalidID       = errors.New("registry: invalid protocol ID")
	ErrInvalidVersions = errors.New("registry: invalid protocol versions")
	ErrInvalidHandler  = errors.New("registry: nil handler")
	ErrKeyConflict     = errors.New("registry: protocol key already registered")
	ErrIDConflict      = errors.New("registry: protocol ID already registered")
	ErrNotFound        = errors.New("registry: not found")
)

// Options holds the switches that widen the set of protocol keys the registry
// accepts. The zero value accepts only private types in private classes or IN.
type Options struct {
	// AllowReservedClasses accepts the reserved classes CH (3) and HS (4).
	AllowReservedClasses bool
	// AllowNonPrivateTypes accepts a type outside 65280–65534. The meta-types
	// stay rejected.
	AllowNonPrivateTypes bool
}

// Registration is one protocol key bound to one handler and to one protocol ID
// with its supported versions.
type Registration struct {
	Key     protocol.ProtocolKey
	Info    protocol.Info
	Handler protocol.Handler
}

// Registry is the set of registrations of one server. It is safe for
// concurrent use.
type Registry struct {
	opts  Options
	mu    sync.RWMutex
	byKey map[protocol.ProtocolKey]Registration
}

// New returns an empty registry that applies opts to every protocol key.
func New(opts Options) *Registry {
	return &Registry{opts: opts, byKey: make(map[protocol.ProtocolKey]Registration)}
}

// Register adds a registration for key.
//
// It checks the rules in this order, and the first failed rule decides the
// error: handler, protocol ID, versions, protocol key. The registry stores its
// own copy of info.Versions in ascending order.
//
// Errors wrap ErrInvalidHandler, ErrInvalidID, ErrInvalidVersions or
// ErrInvalidKey.
func (r *Registry) Register(key protocol.ProtocolKey, info protocol.Info, h protocol.Handler) error {
	if h == nil {
		return fmt.Errorf("register %s %q: %w", key, info.ID, ErrInvalidHandler)
	}
	// The length check runs first, so the pattern never scans a long input.
	if len(info.ID) > maxIDLen || !idPattern.MatchString(info.ID) {
		return fmt.Errorf("register %s %q: %w", key, info.ID, ErrInvalidID)
	}
	if len(info.Versions) == 0 {
		return fmt.Errorf("register %s %q: no versions: %w", key, info.ID, ErrInvalidVersions)
	}
	// The registry sorts a copy, so a later change to the caller's slice does
	// not change the registration.
	versions := slices.Clone(info.Versions)
	slices.Sort(versions)
	if versions[0] == 0 {
		return fmt.Errorf("register %s %q: version 0: %w", key, info.ID, ErrInvalidVersions)
	}
	for i := 1; i < len(versions); i++ {
		if versions[i] == versions[i-1] {
			return fmt.Errorf("register %s %q: duplicate version %d: %w", key, info.ID, versions[i], ErrInvalidVersions)
		}
	}
	if err := validKey(key, r.opts); err != nil {
		return fmt.Errorf("register %s %q: %w", key, info.ID, err)
	}
	info.Versions = versions

	r.mu.Lock()
	defer r.mu.Unlock()
	r.byKey[key] = Registration{Key: key, Info: info, Handler: h}
	return nil
}

// Lookup returns the registration of key and true, or a zero value and false.
func (r *Registry) Lookup(key protocol.ProtocolKey) (Registration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	reg, ok := r.byKey[key]
	return reg, ok
}
