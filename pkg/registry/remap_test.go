package registry_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/registry"
)

// namedHandler is a comparable handler. A test can then compare whole
// registrations, handler included, with reflect.DeepEqual.
type namedHandler string

func (namedHandler) Handle(context.Context, *protocol.Request) (*protocol.Response, error) {
	return &protocol.Response{}, nil
}

var (
	bootstrapKey = protocol.ProtocolKey{Class: 65280, Type: 65280}
	remappedKey  = protocol.ProtocolKey{Class: 65283, Type: 65280}
	otherKey     = protocol.ProtocolKey{Class: 65281, Type: 65280}
)

// newRemapRegistry returns a registry with org.qmux.bootstrap at 65280/65280
// and org.qmux.other at 65281/65280.
func newRemapRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	r := registry.New(registry.Options{})
	for key, id := range map[protocol.ProtocolKey]string{bootstrapKey: "org.qmux.bootstrap", otherKey: "org.qmux.other"} {
		if err := r.Register(key, protocol.Info{ID: id, Versions: []uint16{1, 2}}, namedHandler(id)); err != nil {
			t.Fatalf("Register(%s, %q) = %v, want nil", key, id, err)
		}
	}
	return r
}

func TestRemapMovesTheRegistrationToTheNewKey(t *testing.T) {
	r := newRemapRegistry(t)
	before, _ := r.Lookup(bootstrapKey)
	if err := r.Remap("org.qmux.bootstrap", remappedKey); err != nil {
		t.Fatalf("Remap = %v, want nil", err)
	}
	reg, ok := r.Lookup(remappedKey)
	if !ok {
		t.Fatalf("Lookup(%s) returned false after Remap", remappedKey)
	}
	want := before
	want.Key = remappedKey
	if !reflect.DeepEqual(reg, want) {
		t.Fatalf("Lookup(%s) = %+v, want %+v", remappedKey, reg, want)
	}
	if _, ok := r.Lookup(bootstrapKey); ok {
		t.Fatalf("Lookup(%s) returned true after Remap freed it", bootstrapKey)
	}
}

func TestRemapUpdatesLookupID(t *testing.T) {
	r := newRemapRegistry(t)
	if err := r.Remap("org.qmux.bootstrap", remappedKey); err != nil {
		t.Fatalf("Remap = %v, want nil", err)
	}
	reg, ok := r.LookupID("org.qmux.bootstrap")
	if !ok || reg.Key != remappedKey {
		t.Fatalf("LookupID = %+v, %v after Remap, want key %s", reg, ok, remappedKey)
	}
}

func TestRemapFreesTheOldKeyForAnotherProtocolID(t *testing.T) {
	r := newRemapRegistry(t)
	if err := r.Remap("org.qmux.bootstrap", remappedKey); err != nil {
		t.Fatalf("Remap = %v, want nil", err)
	}
	info := protocol.Info{ID: "org.qmux.new", Versions: []uint16{1}}
	if err := r.Register(bootstrapKey, info, okHandler); err != nil {
		t.Fatalf("Register(%s, %q) after Remap = %v, want nil", bootstrapKey, info.ID, err)
	}
}

func TestRemapKeepsTheProtocolIDAtOneKey(t *testing.T) {
	r := newRemapRegistry(t)
	if err := r.Remap("org.qmux.bootstrap", remappedKey); err != nil {
		t.Fatalf("Remap = %v, want nil", err)
	}
	info := protocol.Info{ID: "org.qmux.bootstrap", Versions: []uint16{1}}
	if err := r.Register(bootstrapKey, info, okHandler); !errors.Is(err, registry.ErrIDConflict) {
		t.Fatalf("Register(%s, %q) after Remap = %v, want ErrIDConflict", bootstrapKey, info.ID, err)
	}
}

func TestRemapTwiceMovesTheRegistrationEachTime(t *testing.T) {
	r := newRemapRegistry(t)
	third := protocol.ProtocolKey{Class: 65284, Type: 65281}
	for _, key := range []protocol.ProtocolKey{remappedKey, third} {
		if err := r.Remap("org.qmux.bootstrap", key); err != nil {
			t.Fatalf("Remap(%s) = %v, want nil", key, err)
		}
	}
	reg, ok := r.Lookup(third)
	if !ok || reg.Info.ID != "org.qmux.bootstrap" || reg.Handler != namedHandler("org.qmux.bootstrap") {
		t.Fatalf("Lookup(%s) = %+v, %v after the second Remap, want org.qmux.bootstrap", third, reg, ok)
	}
	if _, ok := r.Lookup(remappedKey); ok {
		t.Fatalf("Lookup(%s) returned true after the second Remap", remappedKey)
	}
}

func TestRemapToTheSameKeyChangesNothing(t *testing.T) {
	r := newRemapRegistry(t)
	before := r.List()
	if err := r.Remap("org.qmux.bootstrap", bootstrapKey); err != nil {
		t.Fatalf("Remap to the same key = %v, want nil", err)
	}
	if after := r.List(); !reflect.DeepEqual(after, before) {
		t.Fatalf("List = %+v after Remap to the same key, want %+v", after, before)
	}
}

func TestRemapFailureReturnsTheSentinelAndLeavesTheRegistryUnchanged(t *testing.T) {
	cases := []struct {
		name string
		id   string
		key  protocol.ProtocolKey
		want error
	}{
		{"an unknown protocol ID", "org.qmux.unknown", remappedKey, registry.ErrNotFound},
		{"a key of another protocol ID", "org.qmux.bootstrap", otherKey, registry.ErrKeyConflict},
		{"class 65535", "org.qmux.bootstrap", protocol.ProtocolKey{Class: 65535, Type: 65280}, registry.ErrInvalidKey},
		{"type 65535", "org.qmux.bootstrap", protocol.ProtocolKey{Class: 65280, Type: 65535}, registry.ErrInvalidKey},
		{"an unknown ID and an invalid key", "org.qmux.unknown", protocol.ProtocolKey{Class: 65535, Type: 65280}, registry.ErrNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRemapRegistry(t)
			before := r.List()
			err := r.Remap(tc.id, tc.key)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Remap(%q, %s) = %v, want %v", tc.id, tc.key, err, tc.want)
			}
			if msg := err.Error(); !strings.Contains(msg, tc.key.String()) || !strings.Contains(msg, tc.id) {
				t.Fatalf("error %q does not name the key %s and the ID %q", msg, tc.key, tc.id)
			}
			if after := r.List(); !reflect.DeepEqual(after, before) {
				t.Fatalf("List = %+v after the failed Remap, want %+v", after, before)
			}
			for _, reg := range before {
				if got, ok := r.LookupID(reg.Info.ID); !ok || got.Key != reg.Key {
					t.Fatalf("LookupID(%q) = %+v, %v after the failed Remap, want key %s", reg.Info.ID, got, ok, reg.Key)
				}
			}
		})
	}
}

// The keys and the protocol ID of goroutine i in the concurrency test. Each
// goroutine owns its own keys, so the final state of the registry is known.
func workerKeyA(i int) protocol.ProtocolKey {
	return protocol.ProtocolKey{Class: 65280, Type: 65280 + uint16(i)}
}
func workerKeyB(i int) protocol.ProtocolKey {
	return protocol.ProtocolKey{Class: 65281, Type: 65280 + uint16(i)}
}
func workerID(i int) string { return fmt.Sprintf("org.qmux.worker%d", i) }

// sharedKey is the one protocol key that every goroutine contends for, so the
// write lock has real contention.
var sharedKey = protocol.ProtocolKey{Class: 65534, Type: 65534}

// runWorker calls every registry method for goroutine i. It registers at
// workerKeyA(i), then remaps to workerKeyB(i). If i is even, it then removes
// the registration.
func runWorker(t *testing.T, r *registry.Registry, i int) {
	keyA, keyB, id := workerKeyA(i), workerKeyB(i), workerID(i)
	if err := r.Register(keyA, protocol.Info{ID: id, Versions: []uint16{1}}, namedHandler(id)); err != nil {
		t.Errorf("Register(%s) = %v, want nil", keyA, err)
		return
	}
	if reg, ok := r.Lookup(keyA); !ok || reg.Info.ID != id {
		t.Errorf("Lookup(%s) = %+v, %v, want %q", keyA, reg, ok, id)
	}
	_ = r.List()
	if err := r.Remap(id, keyB); err != nil {
		t.Errorf("Remap(%q, %s) = %v, want nil", id, keyB, err)
	}
	if reg, ok := r.LookupID(id); !ok || reg.Key != keyB {
		t.Errorf("LookupID(%q) = %+v, %v, want key %s", id, reg, ok, keyB)
	}
	if _, ok := r.Lookup(keyA); ok {
		t.Errorf("Lookup(%s) returned true after Remap", keyA)
	}
	// The holder of the shared key frees it at once, so the final state does
	// not hold it.
	err := r.Register(sharedKey, protocol.Info{ID: id + ".shared", Versions: []uint16{1}}, okHandler)
	switch {
	case err == nil:
		if err := r.Unregister(sharedKey); err != nil {
			t.Errorf("Unregister(%s) = %v, want nil", sharedKey, err)
		}
	case !errors.Is(err, registry.ErrKeyConflict):
		t.Errorf("Register(%s) = %v, want nil or ErrKeyConflict", sharedKey, err)
	}
	if i%2 == 0 {
		if err := r.Unregister(keyB); err != nil {
			t.Errorf("Unregister(%s) = %v, want nil", keyB, err)
		}
	}
}

func TestEveryMethodIsSafeForConcurrentUse(t *testing.T) {
	const workers = 100
	r := registry.New(registry.Options{})
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() { runWorker(t, r, i) })
	}
	wg.Wait()

	var want []registry.Registration
	for i := 1; i < workers; i += 2 {
		want = append(want, registry.Registration{
			Key:     workerKeyB(i),
			Info:    protocol.Info{ID: workerID(i), Versions: []uint16{1}},
			Handler: namedHandler(workerID(i)),
		})
	}
	if got := r.List(); !reflect.DeepEqual(got, want) {
		t.Fatalf("final List has %d registrations, want %d: got %+v", len(got), len(want), got)
	}
	for i := range workers {
		if _, ok := r.LookupID(workerID(i)); ok != (i%2 == 1) {
			t.Fatalf("LookupID(%q) = %v, want %v", workerID(i), ok, i%2 == 1)
		}
	}
}
