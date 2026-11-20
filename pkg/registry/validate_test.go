package registry_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/registry"
)

var (
	privateKey = protocol.ProtocolKey{Class: 65280, Type: 65280}
	validInfo  = protocol.Info{ID: "org.qmux.bootstrap", Versions: []uint16{1}}
	okHandler  = protocol.HandlerFunc(func(context.Context, *protocol.Request) (*protocol.Response, error) {
		return &protocol.Response{}, nil
	})
)

func register(opts registry.Options, key protocol.ProtocolKey, info protocol.Info, h protocol.Handler) error {
	return registry.New(opts).Register(key, info, h)
}

func TestRegisterAcceptsTheProtocolIDOrgQmuxBootstrap(t *testing.T) {
	if err := register(registry.Options{}, privateKey, validInfo, okHandler); err != nil {
		t.Fatalf("Register(%q) = %v, want nil", validInfo.ID, err)
	}
}

func TestRegisterRejectsAnInvalidProtocolID(t *testing.T) {
	ids := map[string]string{
		"a single label":       "qmux",
		"uppercase letters":    "Org.Qmux.x",
		"an empty label":       "org..qmux",
		"a leading hyphen":     "-org.qmux",
		"a 129-byte ID":        strings.Repeat("a", 125) + ".org",
		"a trailing hyphen":    "org.qmux-",
		"a trailing dot":       "org.qmux.",
		"a trailing newline":   "org.qmux\n",
		"an empty ID":          "",
		"a non-ASCII letter":   "org.qmüx",
		"an underscore":        "org.q_mux",
		"a hyphen after a dot": "org.-qmux",
	}
	for name, id := range ids {
		t.Run(name, func(t *testing.T) {
			info := protocol.Info{ID: id, Versions: []uint16{1}}
			if err := register(registry.Options{}, privateKey, info, okHandler); !errors.Is(err, registry.ErrInvalidID) {
				t.Fatalf("Register(%q) = %v, want ErrInvalidID", id, err)
			}
		})
	}
}

func TestRegisterAcceptsA128ByteProtocolID(t *testing.T) {
	id := strings.Repeat("a", 124) + ".org"
	info := protocol.Info{ID: id, Versions: []uint16{1}}
	if err := register(registry.Options{}, privateKey, info, okHandler); err != nil {
		t.Fatalf("Register(128-byte ID) = %v, want nil", err)
	}
}

func TestRegisterRejectsInvalidVersions(t *testing.T) {
	cases := map[string][]uint16{
		"no versions":         nil,
		"an empty list":       {},
		"version 0":           {0},
		"version 0 in a list": {2, 0, 1},
		"a duplicate version": {1, 1},
	}
	for name, versions := range cases {
		t.Run(name, func(t *testing.T) {
			info := protocol.Info{ID: "org.qmux.bootstrap", Versions: versions}
			if err := register(registry.Options{}, privateKey, info, okHandler); !errors.Is(err, registry.ErrInvalidVersions) {
				t.Fatalf("Register(versions %v) = %v, want ErrInvalidVersions", versions, err)
			}
		})
	}
}

func TestLookupReturnsTheVersionsInAscendingOrder(t *testing.T) {
	r := registry.New(registry.Options{})
	versions := []uint16{3, 1, 2}
	if err := r.Register(privateKey, protocol.Info{ID: "org.qmux.bootstrap", Versions: versions}, okHandler); err != nil {
		t.Fatalf("Register = %v, want nil", err)
	}
	reg, ok := r.Lookup(privateKey)
	if !ok {
		t.Fatal("Lookup returned false after a valid Register")
	}
	if want := []uint16{1, 2, 3}; !slices.Equal(reg.Info.Versions, want) {
		t.Fatalf("Lookup versions = %v, want %v", reg.Info.Versions, want)
	}
	if want := []uint16{3, 1, 2}; !slices.Equal(versions, want) {
		t.Fatalf("Register changed the caller's slice to %v, want %v", versions, want)
	}
}

func TestRegisterKeepsItsOwnCopyOfTheVersions(t *testing.T) {
	r := registry.New(registry.Options{})
	versions := []uint16{1, 2}
	if err := r.Register(privateKey, protocol.Info{ID: "org.qmux.bootstrap", Versions: versions}, okHandler); err != nil {
		t.Fatalf("Register = %v, want nil", err)
	}
	versions[0] = 9
	reg, _ := r.Lookup(privateKey)
	if want := []uint16{1, 2}; !slices.Equal(reg.Info.Versions, want) {
		t.Fatalf("Lookup versions = %v after the caller changed its slice, want %v", reg.Info.Versions, want)
	}
}

func TestRegisterAcceptsTheClassesINAndPrivate(t *testing.T) {
	for _, class := range []uint16{1, 65280, 65534} {
		key := protocol.ProtocolKey{Class: class, Type: 65280}
		if err := register(registry.Options{}, key, validInfo, okHandler); err != nil {
			t.Errorf("Register(%s) = %v, want nil", key, err)
		}
	}
}

func TestRegisterAcceptsAReservedClassOnlyWithAllowReservedClasses(t *testing.T) {
	for _, class := range []uint16{3, 4} {
		key := protocol.ProtocolKey{Class: class, Type: 65280}
		if err := register(registry.Options{}, key, validInfo, okHandler); !errors.Is(err, registry.ErrInvalidKey) {
			t.Errorf("Register(%s) with default options = %v, want ErrInvalidKey", key, err)
		}
		if err := register(registry.Options{AllowReservedClasses: true}, key, validInfo, okHandler); err != nil {
			t.Errorf("Register(%s) with AllowReservedClasses = %v, want nil", key, err)
		}
	}
}

func TestRegisterRejectsEveryOtherClass(t *testing.T) {
	opts := registry.Options{AllowReservedClasses: true, AllowNonPrivateTypes: true}
	for _, class := range []uint16{0, 2, 5, 254, 255, 65279, 65535} {
		key := protocol.ProtocolKey{Class: class, Type: 65280}
		if err := register(opts, key, validInfo, okHandler); !errors.Is(err, registry.ErrInvalidKey) {
			t.Errorf("Register(%s) = %v, want ErrInvalidKey", key, err)
		}
	}
}

func TestRegisterAcceptsThePrivateTypes(t *testing.T) {
	for _, typ := range []uint16{65280, 65534} {
		key := protocol.ProtocolKey{Class: 65280, Type: typ}
		if err := register(registry.Options{}, key, validInfo, okHandler); err != nil {
			t.Errorf("Register(%s) = %v, want nil", key, err)
		}
	}
}

func TestRegisterAcceptsANonPrivateTypeOnlyWithAllowNonPrivateTypes(t *testing.T) {
	for _, typ := range []uint16{16, 65279, 65535} {
		key := protocol.ProtocolKey{Class: 65280, Type: typ}
		if err := register(registry.Options{}, key, validInfo, okHandler); !errors.Is(err, registry.ErrInvalidKey) {
			t.Errorf("Register(%s) with default options = %v, want ErrInvalidKey", key, err)
		}
	}
	key := protocol.ProtocolKey{Class: 65280, Type: 16}
	if err := register(registry.Options{AllowNonPrivateTypes: true}, key, validInfo, okHandler); err != nil {
		t.Errorf("Register(%s) with AllowNonPrivateTypes = %v, want nil", key, err)
	}
}

func TestRegisterAlwaysRejectsTheMetaTypes(t *testing.T) {
	opts := registry.Options{AllowReservedClasses: true, AllowNonPrivateTypes: true}
	for _, typ := range []uint16{0, 41, 250, 251, 252, 255} {
		key := protocol.ProtocolKey{Class: 65280, Type: typ}
		if err := register(opts, key, validInfo, okHandler); !errors.Is(err, registry.ErrInvalidKey) {
			t.Errorf("Register(%s) with both options = %v, want ErrInvalidKey", key, err)
		}
	}
}

func TestRegisterRejectsANilHandler(t *testing.T) {
	if err := register(registry.Options{}, privateKey, validInfo, nil); !errors.Is(err, registry.ErrInvalidHandler) {
		t.Fatalf("Register(nil handler) = %v, want ErrInvalidHandler", err)
	}
}

func TestRegisterChecksTheRulesInTheSpecOrder(t *testing.T) {
	badKey := protocol.ProtocolKey{Class: 0, Type: 0}
	cases := []struct {
		name    string
		info    protocol.Info
		handler protocol.Handler
		want    error
	}{
		{"a nil handler wins over every other rule", protocol.Info{ID: "qmux"}, nil, registry.ErrInvalidHandler},
		{"an invalid ID wins over versions and key", protocol.Info{ID: "qmux"}, okHandler, registry.ErrInvalidID},
		{"invalid versions win over the key", protocol.Info{ID: "org.qmux.bootstrap"}, okHandler, registry.ErrInvalidVersions},
		{"the key fails last", validInfo, okHandler, registry.ErrInvalidKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := register(registry.Options{}, badKey, tc.info, tc.handler); !errors.Is(err, tc.want) {
				t.Fatalf("Register = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestRegisterErrorTextNamesTheKeyAndTheID(t *testing.T) {
	key := protocol.ProtocolKey{Class: 0, Type: 65280}
	cases := map[string]struct {
		key     protocol.ProtocolKey
		info    protocol.Info
		handler protocol.Handler
	}{
		"a nil handler":    {privateKey, validInfo, nil},
		"an invalid ID":    {privateKey, protocol.Info{ID: "Org.Qmux.x", Versions: []uint16{1}}, okHandler},
		"invalid versions": {privateKey, protocol.Info{ID: "org.qmux.bootstrap"}, okHandler},
		"an invalid key":   {key, validInfo, okHandler},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := register(registry.Options{}, tc.key, tc.info, tc.handler)
			if err == nil {
				t.Fatal("Register = nil, want an error")
			}
			if msg := err.Error(); !strings.Contains(msg, tc.key.String()) || !strings.Contains(msg, tc.info.ID) {
				t.Fatalf("error %q does not name the key %s and the ID %q", msg, tc.key, tc.info.ID)
			}
		})
	}
}
