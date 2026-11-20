package registry

import (
	"context"
	"errors"
	"testing"

	"github.com/Crank-Git/qmux/pkg/protocol"
)

// noopHandler is a no-op handler for tests.
var noopHandler protocol.HandlerFunc = func(_ context.Context, _ *protocol.Request) (*protocol.Response, error) {
	return &protocol.Response{Status: protocol.StatusOK}, nil
}

// TestValidateIDValid checks that valid protocol IDs pass validation.
func TestValidateIDValid(t *testing.T) {
	valid := []string{"org.qmux.bootstrap", "a.b", "x.y.z"}
	for _, id := range valid {
		if err := validateID(id); err != nil {
			t.Errorf("validateID(%q) = %v, want nil", id, err)
		}
	}
}

// TestValidateIDInvalid checks that invalid protocol IDs return ErrInvalidID.
func TestValidateIDInvalid(t *testing.T) {
	invalid := []string{"qmux", "Org.Qmux.x", "org..qmux", "-org.qmux", ""}
	for _, id := range invalid {
		if err := validateID(id); err == nil {
			t.Errorf("validateID(%q) = nil, want error", id)
		}
	}
}

// TestValidateIDTooLong checks that a 129-byte ID returns ErrInvalidID.
func TestValidateIDTooLong(t *testing.T) {
	long := make([]byte, 129)
	for i := range long {
		long[i] = 'a'
	}
	if err := validateID(string(long)); err == nil {
		t.Error("validateID(129 bytes) = nil, want error")
	}
}

// TestValidateVersionsEmpty checks that an empty versions list returns ErrInvalidVersions.
func TestValidateVersionsEmpty(t *testing.T) {
	r := New(Options{})
	err := r.validateVersions(nil)
	if err == nil {
		t.Fatal("expected error for empty versions")
	}
}

// TestValidateVersionsZero checks that version 0 returns ErrInvalidVersions.
func TestValidateVersionsZero(t *testing.T) {
	r := New(Options{})
	err := r.validateVersions([]uint16{0})
	if err == nil {
		t.Fatal("expected error for version 0")
	}
}

// TestValidateVersionsDuplicate checks that duplicate versions return ErrInvalidVersions.
func TestValidateVersionsDuplicate(t *testing.T) {
	r := New(Options{})
	err := r.validateVersions([]uint16{1, 1})
	if err == nil {
		t.Fatal("expected error for duplicate versions")
	}
}

// TestValidateVersionsUnsorted checks that unsorted versions pass validation.
func TestValidateVersionsUnsorted(t *testing.T) {
	r := New(Options{})
	err := r.validateVersions([]uint16{3, 1, 2})
	if err != nil {
		t.Fatalf("validateVersions([3,1,2]) = %v, want nil", err)
	}
}

// TestRegisterSortsVersions checks that Register stores versions in ascending order.
func TestRegisterSortsVersions(t *testing.T) {
	r := New(Options{})
	err := r.Register(protocol.ProtocolKey{Class: 65280, Type: 65280}, protocol.Info{
		ID: "org.qmux.test", Versions: []uint16{3, 1, 2},
	}, noopHandler)
	if err != nil {
		t.Fatalf("Register = %v, want nil", err)
	}
	reg, ok := r.Lookup(protocol.ProtocolKey{Class: 65280, Type: 65280})
	if !ok {
		t.Fatal("Lookup: not found")
	}
	expected := []uint16{1, 2, 3}
	if len(reg.Info.Versions) != 3 || reg.Info.Versions[0] != 1 || reg.Info.Versions[1] != 2 || reg.Info.Versions[2] != 3 {
		t.Errorf("versions = %v, want %v", reg.Info.Versions, expected)
	}
}

// TestValidClasses checks that classes 1, 65280, and 65534 register successfully.
func TestValidClasses(t *testing.T) {
	for _, class := range []uint16{1, 65280, 65534} {
		r := New(Options{})
		err := r.Register(protocol.ProtocolKey{Class: class, Type: 65280}, protocol.Info{
			ID: "org.qmux.test", Versions: []uint16{1},
		}, noopHandler)
		if err != nil {
			t.Errorf("class %d: %v", class, err)
		}
	}
}

// TestReservedClass checks that class 3 rejects by default and accepts with AllowReservedClasses.
func TestReservedClass(t *testing.T) {
	r := New(Options{AllowReservedClasses: true})
	err := r.Register(protocol.ProtocolKey{Class: 3, Type: 65280}, protocol.Info{
		ID: "org.qmux.test", Versions: []uint16{1},
	}, noopHandler)
	if err != nil {
		t.Fatalf("class 3 with AllowReservedClasses: %v", err)
	}
	r2 := New(Options{})
	err = r2.Register(protocol.ProtocolKey{Class: 3, Type: 65280}, protocol.Info{
		ID: "org.qmux.test", Versions: []uint16{1},
	}, noopHandler)
	if err == nil {
		t.Fatal("class 3 without AllowReservedClasses should fail")
	}
}

// TestInvalidClasses checks that classes 0, 2, 254, 255, 65279, and 65535 return ErrInvalidKey.
func TestInvalidClasses(t *testing.T) {
	r := New(Options{})
	for _, class := range []uint16{0, 2, 254, 255, 65279, 65535} {
		err := r.Register(protocol.ProtocolKey{Class: class, Type: 65280}, protocol.Info{
			ID: "org.qmux.test", Versions: []uint16{1},
		}, noopHandler)
		if err == nil {
			t.Errorf("class %d: expected error, got nil", class)
		} else if !errors.Is(err, ErrInvalidKey) {
			t.Errorf("class %d: error = %v, want ErrInvalidKey", class, err)
		}
	}
}

// TestValidTypes checks that types 65280 and 65534 register successfully.
func TestValidTypes(t *testing.T) {
	for _, typ := range []uint16{65280, 65534} {
		r := New(Options{})
		err := r.Register(protocol.ProtocolKey{Class: 65280, Type: typ}, protocol.Info{
			ID: "org.qmux.test", Versions: []uint16{1},
		}, noopHandler)
		if err != nil {
			t.Errorf("type %d: %v", typ, err)
		}
	}
}

// TestNonPrivateType checks that type 16 rejects by default and accepts with AllowNonPrivateTypes.
func TestNonPrivateType(t *testing.T) {
	r := New(Options{AllowNonPrivateTypes: true})
	err := r.Register(protocol.ProtocolKey{Class: 65280, Type: 16}, protocol.Info{
		ID: "org.qmux.test", Versions: []uint16{1},
	}, noopHandler)
	if err != nil {
		t.Fatalf("type 16 with AllowNonPrivateTypes: %v", err)
	}
	r2 := New(Options{})
	err = r2.Register(protocol.ProtocolKey{Class: 65280, Type: 16}, protocol.Info{
		ID: "org.qmux.test", Versions: []uint16{1},
	}, noopHandler)
	if err == nil {
		t.Fatal("type 16 without AllowNonPrivateTypes should fail")
	}
}

// TestReservedTypes checks that reserved types 0, 41, 250, 251, 252, 255 always return ErrInvalidKey.
func TestReservedTypes(t *testing.T) {
	r := New(Options{AllowNonPrivateTypes: true, AllowReservedClasses: true})
	for _, typ := range []uint16{0, 41, 250, 251, 252, 255} {
		err := r.Register(protocol.ProtocolKey{Class: 3, Type: typ}, protocol.Info{
			ID: "org.qmux.test", Versions: []uint16{1},
		}, noopHandler)
		if err == nil {
			t.Errorf("type %d: expected error, got nil", typ)
		} else if !errors.Is(err, ErrInvalidKey) {
			t.Errorf("type %d: error = %v, want ErrInvalidKey", typ, err)
		}
	}
}

// TestNilHandler checks that a nil handler returns ErrInvalidHandler.
func TestNilHandler(t *testing.T) {
	r := New(Options{})
	err := r.Register(protocol.ProtocolKey{Class: 65280, Type: 65280}, protocol.Info{
		ID: "org.qmux.test", Versions: []uint16{1},
	}, nil)
	if err == nil {
		t.Fatal("expected error for nil handler")
	} else if !errors.Is(err, ErrInvalidHandler) {
		t.Errorf("error = %v, want ErrInvalidHandler", err)
	}
}
