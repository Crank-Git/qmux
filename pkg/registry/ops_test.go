package registry

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Crank-Git/qmux/pkg/protocol"
)

// noopHandler is a no-op handler for tests.
var noopHandler protocol.HandlerFunc = func(_ context.Context, _ *protocol.Request) (*protocol.Response, error) {
	return &protocol.Response{Status: protocol.StatusOK}, nil
}

// validInfo returns a minimal Info with the given protocol ID.
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

// FR-registry-34: Remap moves registration to new key.
func TestRemap(t *testing.T) {
	r := New(Options{})
	oldKey := protocol.ProtocolKey{Class: 65280, Type: 65280}
	newKey := protocol.ProtocolKey{Class: 65283, Type: 65280}
	_ = r.Register(oldKey, validInfo("org.qmux.bootstrap"), noopHandler)

	err := r.Remap("org.qmux.bootstrap", newKey)
	if err != nil {
		t.Fatalf("Remap: %v", err)
	}

	// Old key should be gone.
	_, ok := r.Lookup(oldKey)
	if ok {
		t.Fatal("Lookup(oldKey) should return false after Remap")
	}

	// New key should have the registration.
	reg, ok := r.Lookup(newKey)
	if !ok {
		t.Fatal("Lookup(newKey) should return true after Remap")
	}
	if reg.Info.ID != "org.qmux.bootstrap" {
		t.Errorf("ID = %s, want org.qmux.bootstrap", reg.Info.ID)
	}
}

// FR-registry-35: Remap unknown ID returns ErrNotFound.
func TestRemapNotFound(t *testing.T) {
	r := New(Options{})
	key := protocol.ProtocolKey{Class: 65281, Type: 65280}
	err := r.Remap("org.qmux.nonexistent", key)
	if err == nil {
		t.Fatal("expected ErrNotFound")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want %v", err, ErrNotFound)
	}
}

// FR-registry-36: Remap onto key held by another ID returns ErrKeyConflict.
func TestRemapKeyConflict(t *testing.T) {
	r := New(Options{})
	key1 := protocol.ProtocolKey{Class: 65280, Type: 65280}
	key2 := protocol.ProtocolKey{Class: 65280, Type: 65281}
	_ = r.Register(key1, validInfo("org.qmux.a"), noopHandler)
	_ = r.Register(key2, validInfo("org.qmux.b"), noopHandler)

	err := r.Remap("org.qmux.a", key2)
	if err == nil {
		t.Fatal("expected ErrKeyConflict")
	}
	if !errors.Is(err, ErrKeyConflict) {
		t.Errorf("error = %v, want %v", err, ErrKeyConflict)
	}
}

// FR-registry-37: Remap onto invalid key returns ErrInvalidKey.
func TestRemapInvalidKey(t *testing.T) {
	r := New(Options{})
	key := protocol.ProtocolKey{Class: 65280, Type: 65280}
	_ = r.Register(key, validInfo("org.qmux.bootstrap"), noopHandler)

	invalidKey := protocol.ProtocolKey{Class: 65535, Type: 65280}
	err := r.Remap("org.qmux.bootstrap", invalidKey)
	if err == nil {
		t.Fatal("expected ErrInvalidKey")
	}
	if !errors.Is(err, ErrInvalidKey) {
		t.Errorf("error = %v, want %v", err, ErrInvalidKey)
	}
}

// FR-registry-38: Failed Remap leaves registry unchanged.
func TestRemapFailureNoChange(t *testing.T) {
	r := New(Options{})
	key1 := protocol.ProtocolKey{Class: 65280, Type: 65280}
	key2 := protocol.ProtocolKey{Class: 65280, Type: 65281}
	_ = r.Register(key1, validInfo("org.qmux.a"), noopHandler)
	_ = r.Register(key2, validInfo("org.qmux.b"), noopHandler)

	// Attempt a failed Remap.
	invalidKey := protocol.ProtocolKey{Class: 65535, Type: 65280}
	_ = r.Remap("org.qmux.a", invalidKey)

	// List should be unchanged.
	list := r.List()
	if len(list) != 2 {
		t.Fatalf("List length = %d, want 2", len(list))
	}
	if _, ok := r.Lookup(key1); !ok {
		t.Fatal("key1 should still exist after failed Remap")
	}
	if _, ok := r.Lookup(key2); !ok {
		t.Fatal("key2 should still exist after failed Remap")
	}

	// ID conflict should also leave registry unchanged.
	_ = r.Remap("org.qmux.a", key2)
	list2 := r.List()
	if len(list2) != 2 {
		t.Fatalf("List length = %d after ID conflict, want 2", len(list2))
	}
}

// Remap to the same key is a no-op.
func TestRemapSameKey(t *testing.T) {
	r := New(Options{})
	key := protocol.ProtocolKey{Class: 65280, Type: 65280}
	_ = r.Register(key, validInfo("org.qmux.bootstrap"), noopHandler)

	err := r.Remap("org.qmux.bootstrap", key)
	if err != nil {
		t.Fatalf("Remap same key: %v, want nil", err)
	}

	reg, ok := r.Lookup(key)
	if !ok {
		t.Fatal("Lookup should still find the registration")
	}
	if reg.Info.ID != "org.qmux.bootstrap" {
		t.Errorf("ID = %s, want org.qmux.bootstrap", reg.Info.ID)
	}
}

// FR-registry-39: Concurrency safety — 100 goroutines calling every method.
func TestConcurrency(t *testing.T) {
	r := New(Options{})
	n := 100
	var wg sync.WaitGroup
	wg.Add(n * 3) // Register + Lookup + List goroutines

	// Register goroutines.
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			key := protocol.ProtocolKey{
				Class: 65280 + uint16(idx/256),
				Type:  65280 + uint16(idx%256),
			}
			id := "org.qmux.test"
			if idx > 0 {
				id = "org.qmux.test"
			}
			_ = r.Register(key, validInfo(id), noopHandler)
		}(i)
	}

	// Lookup goroutines.
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			r.Lookup(protocol.ProtocolKey{Class: 65280, Type: 65280})
		}()
	}

	// List goroutines.
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			r.List()
		}()
	}

	wg.Wait()
}
