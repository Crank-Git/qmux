package client_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/transport"
	"github.com/Crank-Git/qmux/pkg/transport/transporttest"
)

// truncateUDP makes the recorder send each UDP response with the TC bit and no answer
// RR, so that the client falls back to TCP.
func truncateUDP(network string, m *transporttest.Message) {
	if network == transport.UDP {
		m.Truncated = true
		m.Answer = nil
	}
}

func TestResultPayloadEqualsTheHandlerPayload(t *testing.T) { // FR-client-26
	var r recorder
	c := client.NewClient(client.ClientConfig{Resolver: r.start(t)})

	res := query(t, c, testQuery)

	if !bytes.Equal(res.Payload, testPayload) {
		t.Errorf("Payload = %x, want %x", res.Payload, testPayload)
	}
}

func TestResultHoldsTheNameKeyNoErrorAndTheTTL(t *testing.T) { // FR-client-27
	const ttl = 3600
	r := recorder{edit: func(_ string, m *transporttest.Message) { m.Answer[0].TTL = ttl }}
	c := client.NewClient(client.ClientConfig{Resolver: r.start(t)})

	res := query(t, c, testQuery)

	if res.Name != testName || res.Key != testKey {
		t.Errorf("Name, Key = %q, %v, want %q, %v", res.Name, res.Key, testName, testKey)
	}
	if res.RCode != int(transport.RCodeNoError) {
		t.Errorf("RCode = %d, want %d (NOERROR)", res.RCode, transport.RCodeNoError)
	}
	if res.TTL != ttl {
		t.Errorf("TTL = %d, want %d", res.TTL, ttl)
	}
}

func TestResultTransportIsTCPAfterAFallbackAndUDPOtherwise(t *testing.T) { // FR-client-28
	for _, tc := range []struct {
		name    string
		network string
		edit    func(string, *transporttest.Message)
		want    string
	}{
		{name: "udp answer", want: transport.UDP},
		{name: "fallback", edit: truncateUDP, want: transport.TCP},
		{name: "Network tcp", network: transport.TCP, want: transport.TCP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := recorder{edit: tc.edit}
			c := client.NewClient(client.ClientConfig{Resolver: r.start(t), Network: tc.network})

			if got := query(t, c, testQuery).Transport; got != tc.want {
				t.Errorf("Transport = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResultRTTIsTheTimeOfTheFinalAttemptOnly(t *testing.T) { // FR-client-30
	const timeout = 50 * time.Millisecond
	// The two dropped attempts take one attempt timeout each. An RTT that counts them is
	// longer than the attempt timeout.
	r := recorder{drop: func(n int) bool { return n <= 2 }}
	c := client.NewClient(client.ClientConfig{Resolver: r.start(t), AttemptTimeout: timeout})

	res := query(t, c, testQuery)

	if res.RTT <= 0 || res.RTT >= timeout {
		t.Errorf("RTT = %v, want more than 0 and less than the attempt timeout %v", res.RTT, timeout)
	}
}

func TestResultBytesEqualTheWireSizesOfTheFinalAttempt(t *testing.T) { // FR-client-31
	for _, tc := range []struct {
		name string
		edit func(string, *transporttest.Message)
	}{
		{name: "udp answer"},
		{name: "fallback", edit: truncateUDP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := recorder{edit: tc.edit}
			c := client.NewClient(client.ClientConfig{Resolver: r.start(t)})

			res := query(t, c, testQuery)

			sizes := r.answered()
			want := sizes[len(sizes)-1]
			if res.QueryBytes != want.query || res.ResponseBytes != want.response {
				t.Errorf("QueryBytes, ResponseBytes = %d, %d, want %d, %d from the server (all exchanges: %+v)",
					res.QueryBytes, res.ResponseBytes, want.query, want.response, sizes)
			}
		})
	}
}

func TestResultIsNotVerifiedWithoutTrustAnchors(t *testing.T) { // FR-client-32, FR-security-38
	var r recorder
	c := client.NewClient(client.ClientConfig{Resolver: r.start(t)})

	if query(t, c, testQuery).Verified {
		t.Error("Verified = true, want false for a client with no trust anchors")
	}
}
