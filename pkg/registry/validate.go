package registry

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Crank-Git/qmux/pkg/protocol"
)

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
// The error wraps ErrInvalidKey. Its text names the field that fails, the
// accepted range and the option that widens the range.
func validKey(key protocol.ProtocolKey, opts Options) error {
	switch {
	case protocol.IsPrivateClass(key.Class), key.Class == classIN:
	case key.Class == classCH || key.Class == classHS:
		if !opts.AllowReservedClasses {
			return fmt.Errorf("class %d is reserved; set Options.AllowReservedClasses to accept the classes CH (3) and HS (4): %w", key.Class, ErrInvalidKey)
		}
	case opts.AllowReservedClasses:
		return fmt.Errorf("class %d is not IN (1), CH (3), HS (4) or private (65280-65534): %w", key.Class, ErrInvalidKey)
	default:
		return fmt.Errorf("class %d is not IN (1) or private (65280-65534); Options.AllowReservedClasses adds only CH (3) and HS (4): %w", key.Class, ErrInvalidKey)
	}
	switch {
	case slices.Contains(rejectedTypes, key.Type):
		return fmt.Errorf("type %d is reserved; no option accepts it: %w", key.Type, ErrInvalidKey)
	case protocol.IsPrivateType(key.Type), opts.AllowNonPrivateTypes:
		return nil
	default:
		return fmt.Errorf("type %d is not private (65280-65534); set Options.AllowNonPrivateTypes to accept it: %w", key.Type, ErrInvalidKey)
	}
}

// validID returns nil when id is a valid protocol ID. The error wraps
// ErrInvalidID, and its text names the rule that fails.
func validID(id string) error {
	if rule := idRule(id); rule != "" {
		return fmt.Errorf("%s: %w", rule, ErrInvalidID)
	}
	return nil
}

// idRule returns a description of the first rule that id fails, or "" when id
// is a valid protocol ID. A valid protocol ID is a reverse-DNS name of two or
// more lowercase labels and is 1-128 bytes long (FR-registry-12). FuzzIDRule
// checks that idRule agrees with the pattern of FR-registry-12.
func idRule(id string) string {
	switch {
	case id == "":
		return "the protocol ID is empty"
	case len(id) > maxIDLen:
		return fmt.Sprintf("the protocol ID is %d bytes; the maximum is %d", len(id), maxIDLen)
	}
	for _, c := range id {
		switch {
		case c >= 'A' && c <= 'Z':
			return "the protocol ID has an uppercase letter; use a-z"
		case (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '.':
			return fmt.Sprintf("the protocol ID has the character %q; use a-z, 0-9, a hyphen or a dot", c)
		}
	}
	return labelRule(id)
}

// labelRule returns a description of the first label rule that id fails, or
// "" when every label is valid. It is part of idRule, split out to keep the
// complexity of idRule under the lint limit.
func labelRule(id string) string {
	labels := strings.Split(id, ".")
	if len(labels) < 2 {
		return "the protocol ID has 1 label; it needs 2 or more"
	}
	for _, l := range labels {
		switch {
		case l == "":
			return "the protocol ID has an empty label"
		case strings.HasPrefix(l, "-") || strings.HasSuffix(l, "-"):
			return fmt.Sprintf("the label %q starts or ends with a hyphen", l)
		}
	}
	return ""
}
