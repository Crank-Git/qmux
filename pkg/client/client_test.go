package client_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/transport/transporttest"
)

const testName = "device42.fleet.example."

var (
	testKey     = protocol.ProtocolKey{Class: 65280, Type: 65280}
	testPayload = []byte{0xa1, 0x01, 0x02}
)

// recorder is a fake server that records each query it receives. It answers a query
// with testPayload, or drops it when drop returns true for the query number. The first
// query has number 1.
type recorder struct {
	drop func(n int) bool

	mu      sync.Mutex
	queries []transporttest.Message
	errs    []error
}

func (r *recorder) respond(_ string, wire []byte) [][]byte {
	q, err := transporttest.Unpack(wire)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.errs = append(r.errs, err)
		return nil
	}
	r.queries = append(r.queries, q)
	if r.drop != nil && r.drop(len(r.queries)) {
		return nil
	}
	resp, err := transporttest.Pack(transporttest.Message{
		ID:       q.ID,
		Response: true,
		Question: q.Question,
		Answer:   []transporttest.RR{{Name: q.Question[0].Name, Key: q.Question[0].Key, TTL: 60, Data: testPayload}},
	})
	if err != nil {
		r.errs = append(r.errs, err)
		return nil
	}
	return [][]byte{resp}
}

// received returns the queries that r received. It fails the test when r could not
// read or answer a query.
func (r *recorder) received(t *testing.T) []transporttest.Message {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, err := range r.errs {
		t.Errorf("fake server: %v", err)
	}
	return append([]transporttest.Message(nil), r.queries...)
}

// start starts a fake server that uses r and returns its address.
func (r *recorder) start(t *testing.T) string {
	t.Helper()
	return transporttest.StartServer(t, r.respond)
}

func query(t *testing.T, c *client.Client, q client.Query) *client.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := c.Query(ctx, q)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	return res
}

// onlyQuery returns the one query that r received. It fails the test for any other
// count.
func onlyQuery(t *testing.T, r *recorder) transporttest.Message {
	t.Helper()
	got := r.received(t)
	if len(got) != 1 {
		t.Fatalf("server received %d queries, want 1", len(got))
	}
	return got[0]
}

func TestQueryReachesTheResolverAddress(t *testing.T) { // FR-client-1
	var r recorder
	c := client.NewClient(client.ClientConfig{Resolver: r.start(t)})

	res := query(t, c, client.Query{Name: testName, Class: testKey.Class, Type: testKey.Type})

	onlyQuery(t, &r)
	if !bytes.Equal(res.Payload, testPayload) {
		t.Errorf("Payload = %x, want %x", res.Payload, testPayload)
	}
}

func TestQuerySendsOneQuestionWithTheNameClassAndType(t *testing.T) { // FR-client-3
	var r recorder
	c := client.NewClient(client.ClientConfig{Resolver: r.start(t)})

	query(t, c, client.Query{Name: testName, Class: 65281, Type: 65283})

	q := onlyQuery(t, &r)
	want := protocol.ProtocolKey{Class: 65281, Type: 65283}
	if len(q.Question) != 1 || q.Question[0].Name != testName || q.Question[0].Key != want {
		t.Errorf("question = %+v, want one question %s %v", q.Question, testName, want)
	}
}

func TestQueryAddsTheFinalDotToTheName(t *testing.T) {
	var r recorder
	c := client.NewClient(client.ClientConfig{Resolver: r.start(t)})

	res := query(t, c, client.Query{Name: "device42.fleet.example", Class: testKey.Class, Type: testKey.Type})

	if got := onlyQuery(t, &r).Question[0].Name; got != testName {
		t.Errorf("QNAME = %q, want %q", got, testName)
	}
	if res.Name != testName {
		t.Errorf("Result.Name = %q, want %q", res.Name, testName)
	}
}

func TestQueryAddsTheVersionLabelWhenVersionIsNotZero(t *testing.T) { // FR-client-4
	var r recorder
	c := client.NewClient(client.ClientConfig{Resolver: r.start(t)})

	query(t, c, client.Query{Name: testName, Class: testKey.Class, Type: testKey.Type, Version: 2})

	if got, want := onlyQuery(t, &r).Question[0].Name, "_v2."+testName; got != want {
		t.Errorf("QNAME = %q, want %q", got, want)
	}
}

func TestQueryAdvertisesTheConfiguredUDPSize(t *testing.T) { // FR-client-5
	for _, tc := range []struct {
		name string
		cfg  uint16
		want uint16
	}{
		{name: "default", cfg: 0, want: 1232},
		{name: "configured", cfg: 4096, want: 4096},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r recorder
			c := client.NewClient(client.ClientConfig{Resolver: r.start(t), UDPSize: tc.cfg})

			query(t, c, client.Query{Name: testName, Class: testKey.Class, Type: testKey.Type})

			q := onlyQuery(t, &r)
			if !q.EDNS || q.UDPSize != tc.want {
				t.Errorf("EDNS = %v, UDP size = %d, want an OPT RR with %d", q.EDNS, q.UDPSize, tc.want)
			}
		})
	}
}

func TestQuerySetsRDOnlyWithRecursion(t *testing.T) {
	for _, recursion := range []bool{false, true} {
		var r recorder
		c := client.NewClient(client.ClientConfig{Resolver: r.start(t), Recursion: recursion})

		query(t, c, client.Query{Name: testName, Class: testKey.Class, Type: testKey.Type})

		if got := onlyQuery(t, &r).RecursionDesired; got != recursion {
			t.Errorf("Recursion %v: RD = %v, want %v", recursion, got, recursion)
		}
	}
}

// This test replaces net.DefaultResolver, so it must not run in parallel with a test
// that resolves a name.
func TestQueryCompletesWithoutTheSystemResolver(t *testing.T) { // FR-client-6
	var lookups int
	var mu sync.Mutex
	saved := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			mu.Lock()
			lookups++
			mu.Unlock()
			return nil, errors.New("the system resolver is removed for this test")
		},
	}
	t.Cleanup(func() { net.DefaultResolver = saved })

	var r recorder
	c := client.NewClient(client.ClientConfig{Resolver: r.start(t)})
	query(t, c, client.Query{Name: testName, Class: testKey.Class, Type: testKey.Type})

	onlyQuery(t, &r)
	mu.Lock()
	defer mu.Unlock()
	if lookups != 0 {
		t.Errorf("the client sent %d queries to the system resolver, want 0", lookups)
	}
}

func TestQueryReturnsANetworkErrorForAHostNameResolver(t *testing.T) {
	c := client.NewClient(client.ClientConfig{Resolver: "localhost:53"})

	_, err := c.Query(context.Background(), client.Query{Name: testName, Class: testKey.Class, Type: testKey.Type})

	if err == nil {
		t.Fatal("Query with a host name resolver returned no error")
	}
	wantKind(t, err, client.KindNetwork)
}

func TestQueryUsesANewQueryIDForEachAttempt(t *testing.T) { // FR-client-7
	r := recorder{drop: func(n int) bool { return n <= 2 }}
	// A short attempt timeout keeps the two dropped attempts fast.
	c := client.NewClient(client.ClientConfig{Resolver: r.start(t), AttemptTimeout: 50 * time.Millisecond})

	query(t, c, client.Query{Name: testName, Class: testKey.Class, Type: testKey.Type})

	got := r.received(t)
	if len(got) != 3 {
		t.Fatalf("server received %d queries, want 3", len(got))
	}
	ids := map[uint16]bool{}
	for _, q := range got {
		ids[q.ID] = true
	}
	if len(ids) != 3 {
		t.Errorf("the 3 attempts used %d different IDs, want 3", len(ids))
	}
}
