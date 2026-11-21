package registry_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/registry"
)

func mustRegister(t *testing.T, r *registry.Registry, key protocol.ProtocolKey, id string) {
	t.Helper()
	if err := r.Register(key, protocol.Info{ID: id, Versions: []uint16{1, 2}}, okHandler); err != nil {
		t.Fatalf("Register(%s, %q) = %v, want nil", key, id, err)
	}
}

func TestLookupReturnsTheRegistrationAfterAValidRegister(t *testing.T) {
	r := registry.New(registry.Options{})
	mustRegister(t, r, privateKey, "org.qmux.bootstrap")
	reg, ok := r.Lookup(privateKey)
	if !ok {
		t.Fatal("Lookup returned false after a valid Register")
	}
	if reg.Key != privateKey || reg.Info.ID != "org.qmux.bootstrap" || reg.Handler == nil {
		t.Fatalf("Lookup = %+v, want key %s, ID org.qmux.bootstrap and the handler", reg, privateKey)
	}
}

func TestRegisterReturnsErrKeyConflictForARegisteredKey(t *testing.T) {
	r := registry.New(registry.Options{})
	mustRegister(t, r, privateKey, "org.qmux.bootstrap")
	info := protocol.Info{ID: "org.qmux.other", Versions: []uint16{1}}
	if err := r.Register(privateKey, info, okHandler); !errors.Is(err, registry.ErrKeyConflict) {
		t.Fatalf("second Register(%s) = %v, want ErrKeyConflict", privateKey, err)
	}
	if reg, _ := r.Lookup(privateKey); reg.Info.ID != "org.qmux.bootstrap" {
		t.Fatalf("Lookup ID = %q after the conflict, want org.qmux.bootstrap", reg.Info.ID)
	}
}

func TestRegisterReturnsErrKeyConflictForTheSameIDAtTheSameKey(t *testing.T) {
	r := registry.New(registry.Options{})
	mustRegister(t, r, privateKey, "org.qmux.bootstrap")
	if err := r.Register(privateKey, validInfo, okHandler); !errors.Is(err, registry.ErrKeyConflict) {
		t.Fatalf("second Register(%s, same ID) = %v, want ErrKeyConflict", privateKey, err)
	}
}

func TestRegisterReturnsErrIDConflictForAnIDAtAnotherKey(t *testing.T) {
	r := registry.New(registry.Options{})
	mustRegister(t, r, privateKey, "org.qmux.bootstrap")
	other := protocol.ProtocolKey{Class: 65281, Type: 65280}
	if err := r.Register(other, validInfo, okHandler); !errors.Is(err, registry.ErrIDConflict) {
		t.Fatalf("Register(%s, %q) = %v, want ErrIDConflict", other, validInfo.ID, err)
	}
	if _, ok := r.Lookup(other); ok {
		t.Fatalf("Lookup(%s) returned true after ErrIDConflict", other)
	}
}

func TestRegisterAcceptsTwoTypesInTheSameClass(t *testing.T) {
	r := registry.New(registry.Options{})
	mustRegister(t, r, protocol.ProtocolKey{Class: 65280, Type: 65280}, "org.qmux.a")
	mustRegister(t, r, protocol.ProtocolKey{Class: 65280, Type: 65534}, "org.qmux.b")
}

func TestUnregisterRemovesTheRegistration(t *testing.T) {
	r := registry.New(registry.Options{})
	mustRegister(t, r, privateKey, "org.qmux.bootstrap")
	if err := r.Unregister(privateKey); err != nil {
		t.Fatalf("Unregister(%s) = %v, want nil", privateKey, err)
	}
	if _, ok := r.Lookup(privateKey); ok {
		t.Fatal("Lookup returned true after Unregister")
	}
	if _, ok := r.LookupID("org.qmux.bootstrap"); ok {
		t.Fatal("LookupID returned true after Unregister")
	}
}

func TestUnregisterReleasesTheProtocolIDForAnotherKey(t *testing.T) {
	r := registry.New(registry.Options{})
	mustRegister(t, r, privateKey, "org.qmux.bootstrap")
	if err := r.Unregister(privateKey); err != nil {
		t.Fatalf("Unregister(%s) = %v, want nil", privateKey, err)
	}
	mustRegister(t, r, protocol.ProtocolKey{Class: 65281, Type: 65280}, "org.qmux.bootstrap")
}

func TestUnregisterReturnsErrNotFoundForAnUnknownKey(t *testing.T) {
	r := registry.New(registry.Options{})
	if err := r.Unregister(privateKey); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("Unregister(%s) = %v, want ErrNotFound", privateKey, err)
	}
}

func TestLookupReturnsFalseForAnUnknownKey(t *testing.T) {
	r := registry.New(registry.Options{})
	reg, ok := r.Lookup(privateKey)
	if ok {
		t.Fatal("Lookup of an unknown key returned true")
	}
	if reg.Handler != nil || reg.Info.ID != "" || reg.Info.Versions != nil || reg.Key != (protocol.ProtocolKey{}) {
		t.Fatalf("Lookup of an unknown key = %+v, want the zero value", reg)
	}
}

func TestLookupIDReturnsTheRegistrationOfTheProtocolID(t *testing.T) {
	r := registry.New(registry.Options{})
	mustRegister(t, r, privateKey, "org.qmux.bootstrap")
	reg, ok := r.LookupID("org.qmux.bootstrap")
	if !ok {
		t.Fatal(`LookupID("org.qmux.bootstrap") returned false`)
	}
	if reg.Key != privateKey || reg.Info.ID != "org.qmux.bootstrap" {
		t.Fatalf("LookupID = %+v, want key %s", reg, privateKey)
	}
}

func TestLookupIDReturnsFalseForAnUnknownID(t *testing.T) {
	r := registry.New(registry.Options{})
	reg, ok := r.LookupID("org.qmux.bootstrap")
	if ok {
		t.Fatal("LookupID of an unknown ID returned true")
	}
	if reg.Handler != nil || reg.Info.ID != "" || reg.Key != (protocol.ProtocolKey{}) {
		t.Fatalf("LookupID of an unknown ID = %+v, want the zero value", reg)
	}
}

func TestListOrdersTheRegistrationsByClassThenByType(t *testing.T) {
	r := registry.New(registry.Options{})
	mustRegister(t, r, protocol.ProtocolKey{Class: 65281, Type: 65280}, "org.qmux.c")
	mustRegister(t, r, protocol.ProtocolKey{Class: 65280, Type: 65534}, "org.qmux.b")
	mustRegister(t, r, protocol.ProtocolKey{Class: 65280, Type: 65280}, "org.qmux.a")
	want := []protocol.ProtocolKey{{Class: 65280, Type: 65280}, {Class: 65280, Type: 65534}, {Class: 65281, Type: 65280}}
	var got []protocol.ProtocolKey
	for _, reg := range r.List() {
		got = append(got, reg.Key)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("List keys = %v, want %v", got, want)
	}
}

func TestListOfAnEmptyRegistryIsEmpty(t *testing.T) {
	if got := registry.New(registry.Options{}).List(); len(got) != 0 {
		t.Fatalf("List = %v, want no registrations", got)
	}
}

func TestListReturnsACopyOfTheRegistrations(t *testing.T) {
	r := registry.New(registry.Options{})
	mustRegister(t, r, privateKey, "org.qmux.bootstrap")
	first := r.List()
	first[0] = registry.Registration{Key: protocol.ProtocolKey{Class: 1, Type: 1}, Info: protocol.Info{ID: "org.qmux.changed"}}
	second := r.List()
	if second[0].Key != privateKey || second[0].Info.ID != "org.qmux.bootstrap" {
		t.Fatalf("List()[0] = %+v after the caller overwrote it, want the original", second[0])
	}
}

func TestLookupReturnsACopyOfTheVersions(t *testing.T) {
	r := registry.New(registry.Options{})
	mustRegister(t, r, privateKey, "org.qmux.bootstrap")
	reg, _ := r.Lookup(privateKey)
	reg.Info.Versions[0] = 9
	again, _ := r.Lookup(privateKey)
	if want := []uint16{1, 2}; !slices.Equal(again.Info.Versions, want) {
		t.Fatalf("Lookup versions = %v after the caller changed them, want %v", again.Info.Versions, want)
	}
}

func TestLookupIDReturnsACopyOfTheVersions(t *testing.T) {
	r := registry.New(registry.Options{})
	mustRegister(t, r, privateKey, "org.qmux.bootstrap")
	reg, _ := r.LookupID("org.qmux.bootstrap")
	reg.Info.Versions[0] = 9
	again, _ := r.LookupID("org.qmux.bootstrap")
	if want := []uint16{1, 2}; !slices.Equal(again.Info.Versions, want) {
		t.Fatalf("LookupID versions = %v after the caller changed them, want %v", again.Info.Versions, want)
	}
}

func TestListReturnsACopyOfTheVersions(t *testing.T) {
	r := registry.New(registry.Options{})
	mustRegister(t, r, privateKey, "org.qmux.bootstrap")
	r.List()[0].Info.Versions[0] = 9
	if want, got := []uint16{1, 2}, r.List()[0].Info.Versions; !slices.Equal(got, want) {
		t.Fatalf("List versions = %v after the caller changed them, want %v", got, want)
	}
}
