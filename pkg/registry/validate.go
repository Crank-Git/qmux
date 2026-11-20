package registry

import (
	"fmt"
	"regexp"
	"slices"

	"github.com/Crank-Git/qmux/pkg/protocol"
)

// A protocol ID is a reverse-DNS name of two or more lowercase labels
// (FR-registry-12).
var idPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

const maxIDLen = 128

const classIN = 1

// The reserved classes CH and HS. Options.AllowReservedClasses enables them.
const (
	classCH = 3
	classHS = 4
)

// rejectedTypes are the types that no option enables (FR-registry-22): 0,
// the meta-types OPT (41), TSIG (250), IXFR (251), AXFR (252) and ANY (255),
// and 65535, which DNS reserves.
var rejectedTypes = []uint16{0, 41, 250, 251, 252, 255, 65535}

// validKey returns nil when opts allow the class and the type of key.
// The error wraps ErrInvalidKey and names the field that fails.
func validKey(key protocol.ProtocolKey, opts Options) error {
	switch {
	case protocol.IsPrivateClass(key.Class), key.Class == classIN:
	case (key.Class == classCH || key.Class == classHS) && opts.AllowReservedClasses:
	default:
		return fmt.Errorf("class %d: %w", key.Class, ErrInvalidKey)
	}
	switch {
	case slices.Contains(rejectedTypes, key.Type):
		return fmt.Errorf("reserved type %d: %w", key.Type, ErrInvalidKey)
	case protocol.IsPrivateType(key.Type), opts.AllowNonPrivateTypes:
		return nil
	default:
		return fmt.Errorf("type %d: %w", key.Type, ErrInvalidKey)
	}
}
