package registry

import (
	"regexp"
	"strings"
	"testing"
)

// specIDPattern is the protocol ID pattern of FR-registry-12, copied verbatim.
var specIDPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// FuzzIDRule checks that idRule accepts exactly the protocol IDs that
// FR-registry-12 accepts: a match of the pattern that is 1-128 bytes long.
func FuzzIDRule(f *testing.F) {
	for _, id := range []string{
		"org.qmux.bootstrap", "qmux", "Org.Qmux.x", "org..qmux", "-org.qmux", "org.qmux-",
		"org.qmux.", "org.qmux\n", "", "org.qmüx", "org.q_mux", "org.-qmux", "a.b", "0.9-9",
		strings.Repeat("a", 124) + ".org", strings.Repeat("a", 125) + ".org",
	} {
		f.Add(id)
	}
	f.Fuzz(func(t *testing.T, id string) {
		want := len(id) <= maxIDLen && specIDPattern.MatchString(id)
		if rule := idRule(id); (rule == "") != want {
			t.Fatalf("idRule(%q) = %q, want valid = %v", id, rule, want)
		}
	})
}
