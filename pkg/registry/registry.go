package registry

import (
	"cmp"
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
	// AllowNonPrivateTypes accepts a type outside 65280–65534. The types 0,
	// 41, 250, 251, 252, 255 and 65535 stay rejected.
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
	// byID maps each protocol ID to the protocol key of its registration.
	// Register, Unregister and Remap update byKey and byID under one write
	// lock.
	byID map[string]protocol.ProtocolKey
}

// New returns an empty registry that applies opts to every protocol key.
func New(opts Options) *Registry {
	return &Registry{
		opts:  opts,
		byKey: make(map[protocol.ProtocolKey]Registration),
		byID:  make(map[string]protocol.ProtocolKey),
	}
}

// Register adds a registration for key.
//
// It checks the rules in this order, and the first failed rule decides the
// error: handler, protocol ID, versions, protocol key, key conflict, ID
// conflict. The registry stores its own copy of info.Versions in ascending
// order.
//
// Errors wrap one of these sentinels:
//   - ErrInvalidHandler, ErrInvalidID, ErrInvalidVersions or ErrInvalidKey
//     when the arguments are invalid.
//   - ErrKeyConflict when key already has a registration.
//   - ErrIDConflict when info.ID already has a registration at another key.
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
	if _, ok := r.byKey[key]; ok {
		return fmt.Errorf("register %s %q: %w", key, info.ID, ErrKeyConflict)
	}
	if held, ok := r.byID[info.ID]; ok {
		return fmt.Errorf("register %s %q: held by %s: %w", key, info.ID, held, ErrIDConflict)
	}
	r.byKey[key] = Registration{Key: key, Info: info, Handler: h}
	r.byID[info.ID] = key
	return nil
}

// Unregister removes the registration of key. The protocol ID of the
// registration becomes free for a later Register.
//
// The error wraps ErrNotFound when key has no registration.
func (r *Registry) Unregister(key protocol.ProtocolKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	reg, ok := r.byKey[key]
	if !ok {
		return fmt.Errorf("unregister %s: %w", key, ErrNotFound)
	}
	delete(r.byKey, key)
	delete(r.byID, reg.Info.ID)
	return nil
}

// Remap moves the registration of the protocol ID id to newKey and frees the
// old protocol key. The registration keeps its info and its handler. Remap to
// the protocol key that id already holds returns nil and changes nothing.
//
// It checks the rules in this order, and the first failed rule decides the
// error. A failed Remap leaves the registry unchanged. Errors wrap one of
// these sentinels:
//   - ErrNotFound when id has no registration.
//   - ErrInvalidKey when the options do not allow newKey.
//   - ErrKeyConflict when another protocol ID holds newKey.
func (r *Registry) Remap(id string, newKey protocol.ProtocolKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	oldKey, ok := r.byID[id]
	if !ok {
		return fmt.Errorf("remap %q to %s: %w", id, newKey, ErrNotFound)
	}
	if err := validKey(newKey, r.opts); err != nil {
		return fmt.Errorf("remap %q to %s: %w", id, newKey, err)
	}
	if newKey == oldKey {
		return nil
	}
	if held, ok := r.byKey[newKey]; ok {
		return fmt.Errorf("remap %q to %s: held by %q: %w", id, newKey, held.Info.ID, ErrKeyConflict)
	}
	reg := r.byKey[oldKey]
	reg.Key = newKey
	delete(r.byKey, oldKey)
	r.byKey[newKey] = reg
	r.byID[id] = newKey
	return nil
}

// Lookup returns a copy of the registration of key and true, or a zero value
// and false.
func (r *Registry) Lookup(key protocol.ProtocolKey) (Registration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	reg, ok := r.byKey[key]
	return reg.clone(), ok
}

// LookupID returns a copy of the registration of the protocol ID id and true,
// or a zero value and false.
func (r *Registry) LookupID(id string) (Registration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	key, ok := r.byID[id]
	if !ok {
		return Registration{}, false
	}
	return r.byKey[key].clone(), true
}

// List returns a copy of every registration, ordered by class and then by
// type. A change to the returned slice or to its Versions slices does not
// change the registry.
func (r *Registry) List() []Registration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	regs := make([]Registration, 0, len(r.byKey))
	for _, reg := range r.byKey {
		regs = append(regs, reg.clone())
	}
	slices.SortFunc(regs, func(a, b Registration) int {
		return cmp.Or(cmp.Compare(a.Key.Class, b.Key.Class), cmp.Compare(a.Key.Type, b.Key.Type))
	})
	return regs
}

// clone returns reg with its own Versions slice. A caller that changes the
// Versions of a returned registration then cannot change the registry.
func (reg Registration) clone() Registration {
	reg.Info.Versions = slices.Clone(reg.Info.Versions)
	return reg
}
