package registry

import "errors"

var (
	// ErrInvalidKey reports an invalid protocol key.
	ErrInvalidKey = errors.New("registry: invalid protocol key")

	// ErrInvalidID reports an invalid protocol ID.
	ErrInvalidID = errors.New("registry: invalid protocol ID")

	// ErrInvalidVersions reports invalid protocol versions.
	ErrInvalidVersions = errors.New("registry: invalid protocol versions")

	// ErrInvalidHandler reports a nil handler.
	ErrInvalidHandler = errors.New("registry: nil handler")

	// ErrKeyConflict reports a duplicate protocol key registration.
	ErrKeyConflict = errors.New("registry: protocol key already registered")

	// ErrIDConflict reports a duplicate protocol ID registration.
	ErrIDConflict = errors.New("registry: protocol ID already registered")

	// ErrNotFound reports a missing registration.
	ErrNotFound = errors.New("registry: not found")
)
