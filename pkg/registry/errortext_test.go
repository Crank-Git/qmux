package registry_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/registry"
)

// The error text is a user-visible output: an operator reads it to correct a
// configuration. These tests compare the whole text, so a change to it is a
// deliberate change.

func TestRegisterErrorForAKeyTheOptionsRejectNamesTheRangeAndTheOption(t *testing.T) {
	cases := []struct {
		name string
		opts registry.Options
		key  protocol.ProtocolKey
		want string
	}{
		{
			"a reserved class without AllowReservedClasses",
			registry.Options{},
			protocol.ProtocolKey{Class: 3, Type: 65280},
			`register 3/65280 "org.qmux.bootstrap": class 3 is reserved; set Options.AllowReservedClasses to accept the classes CH (3) and HS (4): registry: invalid protocol key`,
		},
		{
			"a class outside every range",
			registry.Options{AllowReservedClasses: true},
			protocol.ProtocolKey{Class: 0, Type: 65280},
			`register 0/65280 "org.qmux.bootstrap": class 0 is not IN (1), CH (3), HS (4) or private (65280-65534): registry: invalid protocol key`,
		},
		{
			"a class outside every range without AllowReservedClasses",
			registry.Options{},
			protocol.ProtocolKey{Class: 2, Type: 65280},
			`register 2/65280 "org.qmux.bootstrap": class 2 is not IN (1) or private (65280-65534); Options.AllowReservedClasses adds only CH (3) and HS (4): registry: invalid protocol key`,
		},
		{
			"a non-private type without AllowNonPrivateTypes",
			registry.Options{},
			protocol.ProtocolKey{Class: 65280, Type: 16},
			`register 65280/16 "org.qmux.bootstrap": type 16 is not private (65280-65534); set Options.AllowNonPrivateTypes to accept it: registry: invalid protocol key`,
		},
		{
			"a reserved type",
			registry.Options{AllowNonPrivateTypes: true},
			protocol.ProtocolKey{Class: 65280, Type: 255},
			`register 65280/255 "org.qmux.bootstrap": type 255 is reserved; no option accepts it: registry: invalid protocol key`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := register(tc.opts, tc.key, validInfo, okHandler)
			if !errors.Is(err, registry.ErrInvalidKey) {
				t.Fatalf("Register = %v, want ErrInvalidKey", err)
			}
			if got := err.Error(); got != tc.want {
				t.Fatalf("error text\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestRegisterErrorForAnInvalidProtocolIDNamesTheRuleThatFails(t *testing.T) {
	cases := []struct {
		name string
		id   string
		want string
	}{
		{"an uppercase letter", "org.Example.KV", `the protocol ID has an uppercase letter; use a-z`},
		{"an empty ID", "", `the protocol ID is empty`},
		{"a 129-byte ID", strings.Repeat("a", 125) + ".org", `the protocol ID is 129 bytes; the maximum is 128`},
		{"an underscore", "org.q_mux", `the protocol ID has the character '_'; use a-z, 0-9, a hyphen or a dot`},
		{"a non-ASCII letter", "org.qmüx", `the protocol ID has the character 'ü'; use a-z, 0-9, a hyphen or a dot`},
		{"a single label", "qmux", `the protocol ID has 1 label; it needs 2 or more`},
		{"an empty label", "org..qmux", `the protocol ID has an empty label`},
		{"a trailing dot", "org.qmux.", `the protocol ID has an empty label`},
		{"a leading hyphen", "-org.qmux", `the label "-org" starts or ends with a hyphen`},
		{"a hyphen after a dot", "org.-qmux", `the label "-qmux" starts or ends with a hyphen`},
		{"a trailing hyphen", "org.qmux-", `the label "qmux-" starts or ends with a hyphen`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := protocol.Info{ID: tc.id, Versions: []uint16{1}}
			err := register(registry.Options{}, privateKey, info, okHandler)
			if !errors.Is(err, registry.ErrInvalidID) {
				t.Fatalf("Register(%q) = %v, want ErrInvalidID", tc.id, err)
			}
			want := `register 65280/65280 "` + tc.id + `": ` + tc.want + `: registry: invalid protocol ID`
			if got := err.Error(); got != want {
				t.Fatalf("error text\n got %s\nwant %s", got, want)
			}
		})
	}
}

func TestRegisterErrorForAKeyConflictNamesTheProtocolIDThatHoldsTheKey(t *testing.T) {
	r := registry.New(registry.Options{})
	if err := r.Register(privateKey, protocol.Info{ID: "org.example.a", Versions: []uint16{1}}, okHandler); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	err := r.Register(privateKey, protocol.Info{ID: "org.example.other", Versions: []uint16{1}}, okHandler)
	if !errors.Is(err, registry.ErrKeyConflict) {
		t.Fatalf("second Register = %v, want ErrKeyConflict", err)
	}
	want := `register 65280/65280 "org.example.other": held by "org.example.a": registry: protocol key already registered`
	if got := err.Error(); got != want {
		t.Fatalf("error text\n got %s\nwant %s", got, want)
	}
}
