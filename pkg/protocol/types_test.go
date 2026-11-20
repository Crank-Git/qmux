package protocol

import (
	"context"
	"testing"
)

// FR-registry-1
func TestProtocolKeyFields(t *testing.T) {
	k := ProtocolKey{Class: 65280, Type: 65280}
	if k.Class != 65280 || k.Type != 65280 {
		t.Fatalf("expected 65280/65280, got %d/%d", k.Class, k.Type)
	}
}

// FR-registry-2
func TestProtocolKeyString(t *testing.T) {
	got := ProtocolKey{65280, 65281}.String()
	if got != "65280/65281" {
		t.Fatalf("expected 65280/65281, got %s", got)
	}
}

// FR-registry-3
func TestInfoFields(t *testing.T) {
	_ = Info{ID: "test", Versions: []uint16{1}, Schema: nil}
}

// FR-registry-4
func TestSchemaInterface(t *testing.T) {
	var s Schema = testSchema{}
	if s.Validate(1, nil) != nil {
		t.Fatal("unexpected error")
	}
}

type testSchema struct{}

func (testSchema) Validate(uint16, []byte) error { return nil }

// FR-registry-5
func TestHandlerInterface(t *testing.T) {
	var h Handler = testHandler{}
	ctx := context.Background()
	_, err := h.Handle(ctx, &Request{})
	if err != nil {
		t.Fatal("unexpected error")
	}
}

type testHandler struct{}

func (testHandler) Handle(context.Context, *Request) (*Response, error) {
	return &Response{}, nil
}

// FR-registry-6
func TestHandlerFunc(t *testing.T) {
	var f HandlerFunc = func(ctx context.Context, req *Request) (*Response, error) {
		return &Response{Status: StatusOK}, nil
	}
	ctx := context.Background()
	resp, err := f.Handle(ctx, &Request{})
	if err != nil {
		t.Fatal("unexpected error")
	}
	if resp.Status != StatusOK {
		t.Fatalf("expected StatusOK, got %d", resp.Status)
	}
}

// FR-registry-7
func TestDescriberInterface(t *testing.T) {
	var d Describer = testDescriber{}
	info := d.Info()
	if info.ID != "test" {
		t.Fatalf("expected test, got %s", info.ID)
	}
}

type testDescriber struct{}

func (testDescriber) Info() Info { return Info{ID: "test"} }

// FR-registry-8
func TestStatusConstants(t *testing.T) {
	if StatusOK != 0 || StatusNotFound != 1 || StatusBadRequest != 2 ||
		StatusVersionUnsupported != 3 || StatusUnavailable != 4 {
		t.Fatal("status constants mismatch")
	}
}

// FR-registry-9
func TestMessageTypeConstants(t *testing.T) {
	if MessageRecord != 1 || MessageManifest != 2 {
		t.Fatal("message type constants mismatch")
	}
}

// FR-registry-10
func TestIsPrivateClass(t *testing.T) {
	tests := []struct {
		c    uint16
		want bool
	}{
		{65280, true}, {65534, true}, {65279, false}, {65535, false},
		{1, false}, {3, false},
	}
	for _, tt := range tests {
		if got := IsPrivateClass(tt.c); got != tt.want {
			t.Errorf("IsPrivateClass(%d) = %v, want %v", tt.c, got, tt.want)
		}
	}
}

// FR-registry-11
func TestIsPrivateType(t *testing.T) {
	tests := []struct {
		t    uint16
		want bool
	}{
		{65280, true}, {65534, true}, {65279, false}, {65535, false},
		{1, false}, {16, false},
	}
	for _, tt := range tests {
		if got := IsPrivateType(tt.t); got != tt.want {
			t.Errorf("IsPrivateType(%d) = %v, want %v", tt.t, got, tt.want)
		}
	}
}
