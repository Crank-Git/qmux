package schema

import (
	"cmp"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrFirmwareVersionSyntax is the error for a firmware version that is not
// MAJOR.MINOR.PATCH.
var ErrFirmwareVersionSyntax = errors.New("schema: firmware version is not MAJOR.MINOR.PATCH")

// FirmwareVersion is a parsed firmware version. Compare orders two firmware versions.
type FirmwareVersion struct {
	Major, Minor, Patch uint64
}

// ParseFirmwareVersion returns the firmware version that s holds as MAJOR.MINOR.PATCH.
// Each field is a decimal uint64 with no sign and no leading zero.
//
// It returns ErrFirmwareVersionSyntax for any other text. A leading zero is an error
// because "2.04.1" and "2.4.1" would compare equal but name different files.
func ParseFirmwareVersion(s string) (FirmwareVersion, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return FirmwareVersion{}, fmt.Errorf("%w: %q", ErrFirmwareVersionSyntax, s)
	}
	var fields [3]uint64
	for i, part := range parts {
		n, ok := parseField(part)
		if !ok {
			return FirmwareVersion{}, fmt.Errorf("%w: %q", ErrFirmwareVersionSyntax, s)
		}
		fields[i] = n
	}
	return FirmwareVersion{Major: fields[0], Minor: fields[1], Patch: fields[2]}, nil
}

// parseField returns the value of one firmware version field, and false when part is
// not a decimal number without a sign or a leading zero.
func parseField(part string) (uint64, bool) {
	if len(part) > 1 && part[0] == '0' {
		return 0, false
	}
	// ParseUint with base 10 accepts only digits: no sign, no space, no underscore.
	n, err := strconv.ParseUint(part, 10, 64)
	return n, err == nil
}

// Compare returns -1 when v is older than w, 0 when they are equal, and +1 when v is
// newer. It compares MAJOR, then MINOR, then PATCH.
func (v FirmwareVersion) Compare(w FirmwareVersion) int {
	return cmp.Or(cmp.Compare(v.Major, w.Major), cmp.Compare(v.Minor, w.Minor), cmp.Compare(v.Patch, w.Patch))
}

// String returns v as MAJOR.MINOR.PATCH.
func (v FirmwareVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}
