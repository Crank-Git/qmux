package server

import (
	"context"
	"strconv"
	"testing"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/transport"
)

// benchProtocols is the number of protocols in each dispatch benchmark (FR-bench-35,
// FR-bench-36).
const benchProtocols = 8

// benchType is the QTYPE of every benchmark query. Only the class or the first QNAME
// label changes between the protocols.
const benchType = 65280

// benchZone is the zone of every benchmark query.
const benchZone = "fleet.example."

// discardWriter accepts each answer and keeps nothing, so that the benchmark loop
// does not grow a slice.
type discardWriter struct{}

func (discardWriter) Write(a transport.Answer) (transport.Sent, error) {
	return transport.Sent{RCode: a.RCode}, nil
}

// benchQuery returns a valid query for name and key.
func benchQuery(name string, key protocol.ProtocolKey) *transport.Query {
	q := validQuery()
	q.Name = name
	q.Key = key
	return q
}

// benchServer returns a server for benchZone with no signer and a log that discards
// every record. Signing would hide the dispatch cost in the signature cost.
func benchServer() *Server {
	return NewServer(ServerConfig{Zones: []string{benchZone}, Logger: quietLogger()})
}

// runDispatch checks that each query in qs gets NOERROR, and then measures
// s.serveQuery over qs in turn.
func runDispatch(b *testing.B, s *Server, qs []*transport.Query) {
	b.Helper()
	for _, q := range qs {
		var w fakeWriter
		s.serveQuery(context.Background(), q, &w)
		if len(w.answers) != 1 || w.answers[0].RCode != transport.RCodeNoError {
			b.Fatalf("query %s %v: answers %+v, want one NOERROR answer", q.Name, q.Key, w.answers)
		}
	}
	ctx := context.Background()
	var w discardWriter
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		s.serveQuery(ctx, qs[i%len(qs)], w)
		i++
	}
}

// BenchmarkDispatchByClass measures dispatch across 8 registrations that differ only
// by the private class (FR-bench-35). The registry selects the handler by the
// protocol key.
func BenchmarkDispatchByClass(b *testing.B) {
	s := benchServer()
	qs := make([]*transport.Query, benchProtocols)
	for i := range benchProtocols {
		key := protocol.ProtocolKey{Class: uint16(65280 + i), Type: benchType}
		info := protocol.Info{ID: "bench" + strconv.Itoa(i) + ".qmux", Versions: []uint16{1}}
		if err := s.RegisterProtocol(key, info, payloadHandler([]byte{byte(i)})); err != nil {
			b.Fatalf("RegisterProtocol %v: %v", key, err)
		}
		qs[i] = benchQuery("device42."+benchZone, key)
	}
	runDispatch(b, s, qs)
}

// BenchmarkDispatchByLabel measures dispatch across 8 protocols under one IN-class
// protocol key (FR-bench-36). One registration has the key, and its handler selects
// the protocol by the first QNAME label, as the IN-class baseline does with "_fw".
func BenchmarkDispatchByLabel(b *testing.B) {
	s := benchServer()
	byLabel := make(map[string]protocol.Handler, benchProtocols)
	qs := make([]*transport.Query, benchProtocols)
	key := protocol.ProtocolKey{Class: 1, Type: benchType}
	for i := range benchProtocols {
		// A label that starts with "_v" is a version label, so the labels use "_p".
		label := "_p" + strconv.Itoa(i)
		byLabel[label] = payloadHandler([]byte{byte(i)})
		qs[i] = benchQuery(label+".device42."+benchZone, key)
	}
	// Every query has a known first label. A missing handler panics, the server
	// answers SERVFAIL, and the check in runDispatch fails the benchmark.
	mux := protocol.HandlerFunc(func(ctx context.Context, r *protocol.Request) (*protocol.Response, error) {
		return byLabel[r.Labels[0]].Handle(ctx, r)
	})
	info := protocol.Info{ID: "bench.qmux", Versions: []uint16{1}}
	if err := s.RegisterProtocol(key, info, mux); err != nil {
		b.Fatalf("RegisterProtocol %v: %v", key, err)
	}
	runDispatch(b, s, qs)
}
