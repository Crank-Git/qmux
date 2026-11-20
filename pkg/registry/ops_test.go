package registry

import (
	"errors"
	"testing"

	"github.com/Crank-Git/qmux/pkg/protocol"
)

func validInfo(id string) protocol.Info {
	return protocol.Info{ID: id, Versions: []uint16{1}}
}

// FR-registry-24: Register succeeds and Lookup returns it.
func TestRegisterAndLookup(t *testing.T) {
	r := New(Options{})
	key := protocol.ProtocolKey{Class: 65280, Type: 65280}
	err := r.Register(key, validInfo("org.qmux.test"), noopHandler)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	reg, ok := r.Lookup(key)
	if !ok {
		t.Fatal("Lookup: not found")
	}
	if reg.Info.ID != "org.qmux.test" {
		t.Errorf("ID = %s, want org.qmux.test", reg.Info.ID)
	}
}

// FR-registry-25: Key conflict.
func TestRegisterKeyConflict(t *testing.T) {
	r := New(Options{})
	key := protocol.ProtocolKey{Class: 65280, Type: 65280}
	_ = r.Register(key, validInfo("org.qmux.a"), noopHandler)
	err := r.Register(key, validInfo("org.qmux.b"), noopHandler)
	if err == nil {
		t.Fatal("expected ErrKeyConflict")
	}
	if !errors.Is(err, ErrKeyConflict) {
		t.Errorf("error = %v, want %v", err, ErrKeyConflict)
	}
}

// FR-registry-26: ID conflict.
func TestRegisterIDConflict(t *testing.T) {
	r := New(Options{})
	key1 := protocol.ProtocolKey{Class: 65280, Type: 65280}
	key2 := protocol.ProtocolKey{Class: 65280, Type: 65281}
	_ = r.Register(key1, validInfo("org.qmux.test"), noopHandler)
	err := r.Register(key2, validInfo("org.qmux.test"), noopHandler)
	if err == nil {
		t.Fatal("expected ErrIDConflict")
	}
	if !errors.Is(err, ErrIDConflict) {
		t.Errorf("error = %v, want %v", err, ErrIDConflict)
	}
}

// FR-registry-27: Same class, different types.
func TestRegisterSameClassDifferentTypes(t *testing.T) {
	r := New(Options{})
	key1 := protocol.ProtocolKey{Class: 65280, Type: 65280}
	key2 := protocol.ProtocolKey{Class: 65280, Type: 65534}
	if err := r.Register(key1, validInfo("org.qmux.a"), noopHandler); err != nil {
		t.Fatalf("key1: %v", err)
	}
	if err := r.Register(key2, validInfo("org.qmux.b"), noopHandler); err != nil {
		t.Fatalf("key2: %v", err)
	}
}

// FR-registry-28: Unregister removes registration.
func TestUnregister(t *testing.T) {
	r := New(Options{})
	key := protocol.ProtocolKey{Class: 65280, Type: 65280}
	_ = r.Register(key, validInfo("org.qmux.test"), noopHandler)
	err := r.Unregister(key)
	if err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	_, ok := r.Lookup(key)
	if ok {
		t.Fatal("Lookup after Unregister should return false")
	}
}

// FR-registry-29: Unregister unknown key.
func TestUnregisterNotFound(t *testing.T) {
	r := New(Options{})
	key := protocol.ProtocolKey{Class: 65280, Type: 65280}
	err := r.Unregister(key)
	if err == nil {
		t.Fatal("expected ErrNotFound")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want %v", err, ErrNotFound)
	}
}

// FR-registry-30: Lookup unknown key.
func TestLookupNotFound(t *testing.T) {
	r := New(Options{})
	key := protocol.ProtocolKey{Class: 65280, Type: 65280}
	_, ok := r.Lookup(key)
	if ok {
		t.Fatal("Lookup unknown key should return false")
	}
}

// FR-registry-31: LookupID.
func TestLookupID(t *testing.T) {
	r := New(Options{})
	key := protocol.ProtocolKey{Class: 65280, Type: 65280}
	_ = r.Register(key, validInfo("org.qmux.bootstrap"), noopHandler)
	reg, ok := r.LookupID("org.qmux.bootstrap")
	if !ok {
		t.Fatal("LookupID: not found")
	}
	if reg.Key != key {
		t.Errorf("key = %v, want %v", reg.Key, key)
	}
}

// FR-registry-32: List sorted by class then type.
func TestListSorted(t *testing.T) {
	r := New(Options{})
	type entry struct {
		key protocol.ProtocolKey
		id  string
	}
	entries := []entry{
		{key: protocol.ProtocolKey{Class: 65281, Type: 65280}, id: "org.qmux.c"},
		{key: protocol.ProtocolKey{Class: 65280, Type: 65534}, id: "org.qmux.b"},
		{key: protocol.ProtocolKey{Class: 65280, Type: 65280}, id: "org.qmux.a"},
	}
	for _, e := range entries {
		if err := r.Register(e.key, validInfo(e.id), noopHandler); err != nil {
			t.Fatalf("Register(%v): %v", e.key, err)
		}
	}
	list := r.List()
	if len(list) != 3 {
		t.Fatalf("List returned %d, want 3", len(list))
	}
	want := []protocol.ProtocolKey{
		{Class: 65280, Type: 65280},
		{Class: 65280, Type: 65534},
		{Class: 65281, Type: 65280},
	}
	for i, k := range want {
		if list[i].Key != k {
			t.Errorf("[%d] key = %v, want %v", i, list[i].Key, k)
		}
	}
}

// FR-registry-33: List returns a copy.
func TestListReturnsCopy(t *testing.T) {
	r := New(Options{})
	key := protocol.ProtocolKey{Class: 65280, Type: 65280}
	_ = r.Register(key, validInfo("org.qmux.test"), noopHandler)
	list := r.List()
	list[0].Info.ID = "modified"
	list2 := r.List()
	if list2[0].Info.ID == "modified" {
		t.Fatal("modifying List result changed the registry")
	}
}
