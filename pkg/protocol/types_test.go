package protocol_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/protocol"
)

// stubSchema, stubHandler and stubDescriber exist only to prove the interface
// method sets at compile time.
type stubSchema struct{}

func (stubSchema) Validate(uint16, []byte) error { return nil }

type stubHandler struct{}

func (stubHandler) Handle(context.Context, *protocol.Request) (*protocol.Response, error) {
	return nil, nil
}

type stubDescriber struct{}

func (stubDescriber) Info() protocol.Info { return protocol.Info{} }

// The compiler rejects these lines when a method set differs from the spec.
var (
	_ protocol.Schema    = stubSchema{}
	_ protocol.Handler   = stubHandler{}
	_ protocol.Handler   = protocol.HandlerFunc(nil)
	_ protocol.Describer = stubDescriber{}
)

func TestProtocolKeyHasClassAndTypeFields(t *testing.T) {
	k := protocol.ProtocolKey{Class: 65280, Type: 65280}
	if k.Class != 65280 || k.Type != 65280 {
		t.Fatalf("ProtocolKey{Class: 65280, Type: 65280} = %+v", k)
	}
}

func TestProtocolKeyStringIsDecimalClassSlashType(t *testing.T) {
	tests := []struct {
		key  protocol.ProtocolKey
		want string
	}{
		{protocol.ProtocolKey{65280, 65281}, "65280/65281"},
		{protocol.ProtocolKey{65280, 65280}, "65280/65280"},
		{protocol.ProtocolKey{1, 16}, "1/16"},
		{protocol.ProtocolKey{0, 0}, "0/0"},
		{protocol.ProtocolKey{65535, 65535}, "65535/65535"},
	}
	for _, tt := range tests {
		if got := tt.key.String(); got != tt.want {
			t.Errorf("ProtocolKey%+v.String() = %q, want %q", tt.key, got, tt.want)
		}
	}
}

func TestInfoLiteralHoldsIDVersionsAndSchema(t *testing.T) {
	info := protocol.Info{
		ID:       "org.qmux.bootstrap",
		Versions: []uint16{1, 2},
		Schema:   stubSchema{},
	}
	if info.ID != "org.qmux.bootstrap" || len(info.Versions) != 2 || info.Schema == nil {
		t.Fatalf("Info literal = %+v", info)
	}
}

func TestDescriberReturnsInfo(t *testing.T) {
	var d protocol.Describer = stubDescriber{}
	if got := d.Info(); got.ID != "" {
		t.Fatalf("stubDescriber.Info().ID = %q, want empty", got.ID)
	}
}

func TestHandlerFuncCallsTheWrappedFunctionWithItsArguments(t *testing.T) {
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "marker")
	req := &protocol.Request{
		QName:      "node1.fleet.example.",
		Key:        protocol.ProtocolKey{Class: 65280, Type: 65280},
		Version:    1,
		Labels:     []string{"node1"},
		Zone:       "fleet.example.",
		Source:     netip.MustParseAddrPort("127.0.0.1:5353"),
		Transport:  "udp",
		UDPSize:    1232,
		ID:         7,
		ReceivedAt: time.Unix(1, 0),
	}
	want := &protocol.Response{Payload: []byte{1}, Status: protocol.StatusOK}
	wantErr := errors.New("handler error")

	calls := 0
	var gotCtx context.Context
	var gotReq *protocol.Request
	h := protocol.HandlerFunc(func(c context.Context, r *protocol.Request) (*protocol.Response, error) {
		calls++
		gotCtx, gotReq = c, r
		return want, wantErr
	})

	resp, err := h.Handle(ctx, req)

	if calls != 1 {
		t.Fatalf("wrapped function called %d times, want 1", calls)
	}
	if gotCtx.Value(ctxKey{}) != "marker" {
		t.Errorf("wrapped function got a different context")
	}
	if gotReq != req {
		t.Errorf("wrapped function got request %p, want %p", gotReq, req)
	}
	if resp != want {
		t.Errorf("Handle returned response %p, want %p", resp, want)
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("Handle returned error %v, want %v", err, wantErr)
	}
}

func TestStatusConstantsMatchTheApplicationStatusTable(t *testing.T) {
	tests := []struct {
		name string
		got  protocol.Status
		want uint16
	}{
		{"StatusOK", protocol.StatusOK, 0},
		{"StatusNotFound", protocol.StatusNotFound, 1},
		{"StatusBadRequest", protocol.StatusBadRequest, 2},
		{"StatusVersionUnsupported", protocol.StatusVersionUnsupported, 3},
		{"StatusUnavailable", protocol.StatusUnavailable, 4},
	}
	for _, tt := range tests {
		if uint16(tt.got) != tt.want {
			t.Errorf("%s = %d, want %d", tt.name, tt.got, tt.want)
		}
	}
}

func TestStatusStringNamesEachFrameworkStatusAndPrintsOthersInDecimal(t *testing.T) { // FR-codec-42
	for status, want := range map[protocol.Status]string{
		0: "ok", 1: "not-found", 2: "bad-request", 3: "version-unsupported", 4: "unavailable",
		5: "5", 255: "255", 256: "256", 65535: "65535",
	} {
		if got := status.String(); got != want {
			t.Errorf("Status(%d).String() = %q, want %q", uint16(status), got, want)
		}
	}
}

func TestMessageTypeConstantsAreRecordOneAndManifestTwo(t *testing.T) {
	if protocol.MessageRecord != 1 {
		t.Errorf("MessageRecord = %d, want 1", protocol.MessageRecord)
	}
	if protocol.MessageManifest != 2 {
		t.Errorf("MessageManifest = %d, want 2", protocol.MessageManifest)
	}
}

// privateRangeCases holds the boundaries of the range 65280–65534.
var privateRangeCases = []struct {
	n    uint16
	want bool
}{
	{0, false},
	{1, false},
	{65279, false},
	{65280, true},
	{65281, true},
	{65534, true},
	{65535, false},
}

func TestIsPrivateClassIsTrueOnlyFrom65280To65534(t *testing.T) {
	for _, tt := range privateRangeCases {
		if got := protocol.IsPrivateClass(tt.n); got != tt.want {
			t.Errorf("IsPrivateClass(%d) = %v, want %v", tt.n, got, tt.want)
		}
	}
}

func TestIsPrivateTypeIsTrueOnlyFrom65280To65534(t *testing.T) {
	for _, tt := range privateRangeCases {
		if got := protocol.IsPrivateType(tt.n); got != tt.want {
			t.Errorf("IsPrivateType(%d) = %v, want %v", tt.n, got, tt.want)
		}
	}
}
