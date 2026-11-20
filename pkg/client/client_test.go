package client

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/miekg/dns"
)

// testServerUDP starts a UDP server that answers with the given handler.
// Returns the server address and a cleanup function.
func testServerUDP(t *testing.T, handler dns.HandlerFunc) (string, func()) {
	t.Helper()
	ln, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	server := &dns.Server{
		Net:       "udp",
		Handler:   handler,
		UDPSize:   defaultUDPSize,
		PacketConn: ln,
	}
	go server.ActivateAndServe()
	addr := ln.LocalAddr().String()
	return addr, func() {
		server.Shutdown()
		ln.Close()
	}
}

// testServerTCP starts a TCP server that answers with the given handler.
func testServerTCP(t *testing.T, handler dns.HandlerFunc) (string, func()) {
	t.Helper()
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenTCP: %v", err)
	}
	server := &dns.Server{
		Net:      "tcp",
		Handler:  handler,
		Listener: ln,
	}
	go server.ActivateAndServe()
	addr := ln.Addr().String()
	return addr, func() {
		server.Shutdown()
		ln.Close()
	}
}

// makeQueryMsg creates a DNS query message.
func makeQueryMsg(name string, qclass, qtype uint16) *dns.Msg {
	m := new(dns.Msg)
	m.SetQuestion(name, qtype)
	m.Question[0].Qclass = qclass
	m.SetEdns0(defaultUDPSize, false)
	return m
}

// makeResponseMsg creates a DNS response message with opaque RDATA.
func makeResponseMsg(req *dns.Msg, payload []byte, ttl uint32) *dns.Msg {
	resp := new(dns.Msg)
	resp.SetReply(req)
	resp.Authoritative = true

	rr := &dns.RFC3597{
		Hdr: dns.RR_Header{
			Name:   req.Question[0].Name,
			Rrtype: req.Question[0].Qtype,
			Class:  req.Question[0].Qclass,
			Ttl:    ttl,
		},
		Rdata: hex.EncodeToString(payload),
	}
	resp.Answer = append(resp.Answer, rr)
	return resp
}

// --- NewClient tests ---

func TestNewClientDefaults(t *testing.T) {
	c := NewClient(ClientConfig{Resolver: "127.0.0.1:5353"})
	if c.cfg.AttemptTimeout != defaultAttemptTimeout {
		t.Errorf("AttemptTimeout = %v, want %v", c.cfg.AttemptTimeout, defaultAttemptTimeout)
	}
	if c.cfg.Attempts != defaultAttempts {
		t.Errorf("Attempts = %d, want %d", c.cfg.Attempts, defaultAttempts)
	}
	if c.cfg.UDPSize != defaultUDPSize {
		t.Errorf("UDPSize = %d, want %d", c.cfg.UDPSize, defaultUDPSize)
	}
	if c.cfg.Network != "udp" {
		t.Errorf("Network = %q, want %q", c.cfg.Network, "udp")
	}
}

func TestNewClientCustomValues(t *testing.T) {
	c := NewClient(ClientConfig{
		Resolver:         "127.0.0.1:5353",
		Network:          "tcp",
		AttemptTimeout:   5 * time.Second,
		Attempts:         5,
		UDPSize:          4096,
		DisableTCPFallback: true,
		Recursion:        true,
	})
	if c.cfg.Network != "tcp" {
		t.Errorf("Network = %q, want %q", c.cfg.Network, "tcp")
	}
	if c.cfg.AttemptTimeout != 5*time.Second {
		t.Errorf("AttemptTimeout = %v, want 5s", c.cfg.AttemptTimeout)
	}
	if c.cfg.Attempts != 5 {
		t.Errorf("Attempts = %d, want 5", c.cfg.Attempts)
	}
	if c.cfg.UDPSize != 4096 {
		t.Errorf("UDPSize = %d, want 4096", c.cfg.UDPSize)
	}
	if !c.cfg.DisableTCPFallback {
		t.Error("DisableTCPFallback should be true")
	}
	if !c.cfg.Recursion {
		t.Error("Recursion should be true")
	}
}

func TestNewClientNilLogger(t *testing.T) {
	c := NewClient(ClientConfig{Resolver: "127.0.0.1:5353"})
	if c.cfg.Logger == nil {
		t.Fatal("Logger should not be nil")
	}
}

// --- Query tests ---

func TestQuerySendsToResolver(t *testing.T) {
	addr, cleanup := testServerUDP(t, func(w dns.ResponseWriter, req *dns.Msg) {
		resp := makeResponseMsg(req, []byte("hello"), 60)
		_ = w.WriteMsg(resp)
	})
	defer cleanup()

	c := NewClient(ClientConfig{Resolver: addr})
	result, err := c.Query(context.Background(), Query{
		Name:  "device42.fleet.example.",
		Class: 65280,
		Type:  65280,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if string(result.Payload) != "hello" {
		t.Errorf("Payload = %q, want %q", result.Payload, "hello")
	}
	if result.Transport != "udp" {
		t.Errorf("Transport = %q, want %q", result.Transport, "udp")
	}
}

func TestQuerySendsCorrectQuestion(t *testing.T) {
	var receivedName string
	var receivedClass, receivedType uint16
	addr, cleanup := testServerUDP(t, func(w dns.ResponseWriter, req *dns.Msg) {
		receivedName = req.Question[0].Name
		receivedClass = req.Question[0].Qclass
		receivedType = req.Question[0].Qtype
		resp := makeResponseMsg(req, []byte("ok"), 60)
		_ = w.WriteMsg(resp)
	})
	defer cleanup()

	c := NewClient(ClientConfig{Resolver: addr})
	_, err := c.Query(context.Background(), Query{
		Name:  "device42.fleet.example.",
		Class: 65280,
		Type:  65280,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if receivedName != "device42.fleet.example." {
		t.Errorf("QNAME = %q, want %q", receivedName, "device42.fleet.example.")
	}
	if receivedClass != 65280 {
		t.Errorf("Qclass = %d, want 65280", receivedClass)
	}
	if receivedType != 65280 {
		t.Errorf("Qtype = %d, want 65280", receivedType)
	}
}

func TestQueryVersionLabel(t *testing.T) {
	var receivedName string
	addr, cleanup := testServerUDP(t, func(w dns.ResponseWriter, req *dns.Msg) {
		receivedName = req.Question[0].Name
		resp := makeResponseMsg(req, []byte("ok"), 60)
		_ = w.WriteMsg(resp)
	})
	defer cleanup()

	c := NewClient(ClientConfig{Resolver: addr})
	_, err := c.Query(context.Background(), Query{
		Name:    "device42.fleet.example.",
		Class:   65280,
		Type:    65280,
		Version: 2,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if receivedName != "_v2.device42.fleet.example." {
		t.Errorf("QNAME = %q, want %q", receivedName, "_v2.device42.fleet.example.")
	}
}

func TestQueryOPTRR(t *testing.T) {
	var hasOPT bool
	addr, cleanup := testServerUDP(t, func(w dns.ResponseWriter, req *dns.Msg) {
		hasOPT = req.IsEdns0() != nil
		resp := makeResponseMsg(req, []byte("ok"), 60)
		_ = w.WriteMsg(resp)
	})
	defer cleanup()

	c := NewClient(ClientConfig{Resolver: addr, UDPSize: 4096})
	_, err := c.Query(context.Background(), Query{
		Name:  "device42.fleet.example.",
		Class: 65280,
		Type:  65280,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if !hasOPT {
		t.Error("Expected OPT RR in query")
	}
}

func TestQueryDifferentIDs(t *testing.T) {
	var ids []uint16
	addr, cleanup := testServerUDP(t, func(w dns.ResponseWriter, req *dns.Msg) {
		ids = append(ids, req.Id)
		resp := new(dns.Msg)
		resp.SetReply(req)
		resp.Rcode = dns.RcodeServerFailure
		_ = w.WriteMsg(resp)
	})
	defer cleanup()

	c := NewClient(ClientConfig{Resolver: addr, Attempts: 3})
	_, err := c.Query(context.Background(), Query{
		Name:  "device42.fleet.example.",
		Class: 65280,
		Type:  65280,
	})
	// We expect an error because the server returns SERVFAIL, not NOERROR.
	// But we should have seen 3 different IDs if retries happened.
	_ = err

	if len(ids) < 2 {
		t.Errorf("Expected at least 2 attempts, got %d", len(ids))
	}
	// Check that at least two IDs differ.
	if len(ids) >= 2 && ids[0] == ids[1] {
		t.Error("First two query IDs should differ")
	}
}

func TestQueryTCPNetwork(t *testing.T) {
	addr, cleanup := testServerTCP(t, func(w dns.ResponseWriter, req *dns.Msg) {
		resp := makeResponseMsg(req, []byte("tcp-ok"), 60)
		_ = w.WriteMsg(resp)
	})
	defer cleanup()

	c := NewClient(ClientConfig{Resolver: addr, Network: "tcp"})
	result, err := c.Query(context.Background(), Query{
		Name:  "device42.fleet.example.",
		Class: 65280,
		Type:  65280,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if result.Transport != "tcp" {
		t.Errorf("Transport = %q, want %q", result.Transport, "tcp")
	}
	if string(result.Payload) != "tcp-ok" {
		t.Errorf("Payload = %q, want %q", result.Payload, "tcp-ok")
	}
}

func TestQueryTCPRoundTrip(t *testing.T) {
	// Start a UDP server that also listens on TCP (same port).
	udpAddr, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}

	tcpAddr, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: udpAddr.LocalAddr().(*net.UDPAddr).Port})
	if err != nil {
		t.Fatalf("ListenTCP: %v", err)
	}

	addr := udpAddr.LocalAddr().String()

	handler := func(w dns.ResponseWriter, req *dns.Msg) {
		resp := makeResponseMsg(req, []byte("roundtrip"), 60)
		_ = w.WriteMsg(resp)
	}

	udpServer := &dns.Server{Net: "udp", Handler: dns.HandlerFunc(handler), UDPSize: 1232, PacketConn: udpAddr}
	tcpServer := &dns.Server{Net: "tcp", Handler: dns.HandlerFunc(handler), Listener: tcpAddr}

	go udpServer.ActivateAndServe()
	go tcpServer.ActivateAndServe()

	defer func() {
		udpServer.Shutdown()
		tcpServer.Shutdown()
		udpAddr.Close()
		tcpAddr.Close()
	}()

	// UDP query.
	c := NewClient(ClientConfig{Resolver: addr})
	result, err := c.Query(context.Background(), Query{
		Name:  "device42.fleet.example.",
		Class: 65280,
		Type:  65280,
	})
	if err != nil {
		t.Fatalf("UDP Query: %v", err)
	}
	if string(result.Payload) != "roundtrip" {
		t.Errorf("UDP Payload = %q, want %q", result.Payload, "roundtrip")
	}

	// TCP query.
	cTCP := NewClient(ClientConfig{Resolver: addr, Network: "tcp"})
	resultTCP, err := cTCP.Query(context.Background(), Query{
		Name:  "device42.fleet.example.",
		Class: 65280,
		Type:  65280,
	})
	if err != nil {
		t.Fatalf("TCP Query: %v", err)
	}
	if string(resultTCP.Payload) != "roundtrip" {
		t.Errorf("TCP Payload = %q, want %q", resultTCP.Payload, "roundtrip")
	}
}

// --- TCP fallback tests ---

func TestQueryTCPFallback(t *testing.T) {
	addr, cleanup := testServerUDP(t, func(w dns.ResponseWriter, req *dns.Msg) {
		// First response: truncated (TC bit set).
		resp := new(dns.Msg)
		resp.SetReply(req)
		resp.Truncated = true
		resp.RecursionAvailable = true
		resp.SetEdns0(4096, false)
		_ = w.WriteMsg(resp)

		// The client should retry over TCP. We can't intercept the TCP call
		// here, so this test only verifies that the UDP path triggers TC.
	})
	defer cleanup()

	c := NewClient(ClientConfig{Resolver: addr})
	_, err := c.Query(context.Background(), Query{
		Name:  "device42.fleet.example.",
		Class: 65280,
		Type:  65280,
	})
	// We expect a network error because there's no TCP server on the same port.
	if err == nil {
		t.Fatal("expected error for TC without TCP server, got nil")
	}
}

func TestQueryDisableTCPFallback(t *testing.T) {
	addr, cleanup := testServerUDP(t, func(w dns.ResponseWriter, req *dns.Msg) {
		resp := new(dns.Msg)
		resp.SetReply(req)
		resp.Truncated = true
		resp.SetEdns0(4096, false)
		_ = w.WriteMsg(resp)
	})
	defer cleanup()

	c := NewClient(ClientConfig{Resolver: addr, DisableTCPFallback: true})
	_, err := c.Query(context.Background(), Query{
		Name:  "device42.fleet.example.",
		Class: 65280,
		Type:  65280,
	})
	if err == nil {
		t.Fatal("expected truncation error, got nil")
	}
	dnsErr, ok := err.(*DNSError)
	if !ok {
		t.Fatalf("error = %T, want *DNSError", err)
	}
	if dnsErr.Kind != KindTruncation {
		t.Errorf("Kind = %v, want %v", dnsErr.Kind, KindTruncation)
	}
}

// --- Response matching tests ---

func TestQueryMismatchedQuestionName(t *testing.T) {
	addr, cleanup := testServerUDP(t, func(w dns.ResponseWriter, req *dns.Msg) {
		// Answer with a different name.
		resp := new(dns.Msg)
		resp.SetReply(req)
		resp.Authoritative = true
		rr := &dns.RFC3597{
			Hdr: dns.RR_Header{
				Name:   "wrong.name.",
				Rrtype: req.Question[0].Qtype,
				Class:  req.Question[0].Qclass,
				Ttl:    60,
			},
			Rdata: hex.EncodeToString([]byte("wrong")),
		}
		resp.Answer = append(resp.Answer, rr)
		_ = w.WriteMsg(resp)
	})
	defer cleanup()

	c := NewClient(ClientConfig{Resolver: addr})
	_, err := c.Query(context.Background(), Query{
		Name:  "device42.fleet.example.",
		Class: 65280,
		Type:  65280,
	})
	if err == nil {
		t.Fatal("expected mismatch error, got nil")
	}
	dnsErr, ok := err.(*DNSError)
	if !ok {
		t.Fatalf("error = %T, want *DNSError", err)
	}
	if dnsErr.Kind != KindMismatch {
		t.Errorf("Kind = %v, want %v", dnsErr.Kind, KindMismatch)
	}
}

func TestQueryMismatchedQuestionType(t *testing.T) {
	addr, cleanup := testServerUDP(t, func(w dns.ResponseWriter, req *dns.Msg) {
		resp := new(dns.Msg)
		resp.SetReply(req)
		resp.Authoritative = true
		rr := &dns.RFC3597{
			Hdr: dns.RR_Header{
				Name:   req.Question[0].Name,
				Rrtype: 65281, // Different type
				Class:  req.Question[0].Qclass,
				Ttl:    60,
			},
			Rdata: hex.EncodeToString([]byte("wrong-type")),
		}
		resp.Answer = append(resp.Answer, rr)
		_ = w.WriteMsg(resp)
	})
	defer cleanup()

	c := NewClient(ClientConfig{Resolver: addr})
	_, err := c.Query(context.Background(), Query{
		Name:  "device42.fleet.example.",
		Class: 65280,
		Type:  65280,
	})
	if err == nil {
		t.Fatal("expected mismatch error, got nil")
	}
	dnsErr, ok := err.(*DNSError)
	if !ok {
		t.Fatalf("error = %T, want *DNSError", err)
	}
	if dnsErr.Kind != KindMismatch {
		t.Errorf("Kind = %v, want %v", dnsErr.Kind, KindMismatch)
	}
}

func TestQueryZeroAnswers(t *testing.T) {
	addr, cleanup := testServerUDP(t, func(w dns.ResponseWriter, req *dns.Msg) {
		resp := new(dns.Msg)
		resp.SetReply(req)
		// No answer RRs.
		_ = w.WriteMsg(resp)
	})
	defer cleanup()

	c := NewClient(ClientConfig{Resolver: addr})
	_, err := c.Query(context.Background(), Query{
		Name:  "device42.fleet.example.",
		Class: 65280,
		Type:  65280,
	})
	if err == nil {
		t.Fatal("expected mismatch error for zero answers, got nil")
	}
	dnsErr, ok := err.(*DNSError)
	if !ok {
		t.Fatalf("error = %T, want *DNSError", err)
	}
	if dnsErr.Kind != KindMismatch {
		t.Errorf("Kind = %v, want %v", dnsErr.Kind, KindMismatch)
	}
}

func TestQueryTwoAnswers(t *testing.T) {
	addr, cleanup := testServerUDP(t, func(w dns.ResponseWriter, req *dns.Msg) {
		resp := new(dns.Msg)
		resp.SetReply(req)
		resp.Authoritative = true
		for i := 0; i < 2; i++ {
			rr := &dns.RFC3597{
				Hdr: dns.RR_Header{
					Name:   req.Question[0].Name,
					Rrtype: req.Question[0].Qtype,
					Class:  req.Question[0].Qclass,
					Ttl:    60,
				},
				Rdata: hex.EncodeToString([]byte("extra")),
			}
			resp.Answer = append(resp.Answer, rr)
		}
		_ = w.WriteMsg(resp)
	})
	defer cleanup()

	c := NewClient(ClientConfig{Resolver: addr})
	_, err := c.Query(context.Background(), Query{
		Name:  "device42.fleet.example.",
		Class: 65280,
		Type:  65280,
	})
	if err == nil {
		t.Fatal("expected mismatch error for two answers, got nil")
	}
	dnsErr, ok := err.(*DNSError)
	if !ok {
		t.Fatalf("error = %T, want *DNSError", err)
	}
	if dnsErr.Kind != KindMismatch {
		t.Errorf("Kind = %v, want %v", dnsErr.Kind, KindMismatch)
	}
}

// --- RCODE tests ---

func TestQuerySERVFAIL(t *testing.T) {
	addr, cleanup := testServerUDP(t, func(w dns.ResponseWriter, req *dns.Msg) {
		resp := new(dns.Msg)
		resp.SetReply(req)
		resp.Rcode = dns.RcodeServerFailure
		_ = w.WriteMsg(resp)
	})
	defer cleanup()

	c := NewClient(ClientConfig{Resolver: addr})
	_, err := c.Query(context.Background(), Query{
		Name:  "device42.fleet.example.",
		Class: 65280,
		Type:  65280,
	})
	if err == nil {
		t.Fatal("expected rcode error, got nil")
	}
	dnsErr, ok := err.(*DNSError)
	if !ok {
		t.Fatalf("error = %T, want *DNSError", err)
	}
	if dnsErr.Kind != KindRCode {
		t.Errorf("Kind = %v, want %v", dnsErr.Kind, KindRCode)
	}
	if dnsErr.RCode != dns.RcodeServerFailure {
		t.Errorf("RCode = %d, want %d", dnsErr.RCode, dns.RcodeServerFailure)
	}
}

func TestQueryREFUSED(t *testing.T) {
	addr, cleanup := testServerUDP(t, func(w dns.ResponseWriter, req *dns.Msg) {
		resp := new(dns.Msg)
		resp.SetReply(req)
		resp.Rcode = dns.RcodeRefused
		_ = w.WriteMsg(resp)
	})
	defer cleanup()

	c := NewClient(ClientConfig{Resolver: addr})
	_, err := c.Query(context.Background(), Query{
		Name:  "device42.fleet.example.",
		Class: 65280,
		Type:  65280,
	})
	if err == nil {
		t.Fatal("expected rcode error, got nil")
	}
	dnsErr, ok := err.(*DNSError)
	if !ok {
		t.Fatalf("error = %T, want *DNSError", err)
	}
	if dnsErr.Kind != KindRCode {
		t.Errorf("Kind = %v, want %v", dnsErr.Kind, KindRCode)
	}
	if dnsErr.RCode != dns.RcodeRefused {
		t.Errorf("RCode = %d, want %d", dnsErr.RCode, dns.RcodeRefused)
	}
}

// --- Result tests ---

func TestResultFields(t *testing.T) {
	addr, cleanup := testServerUDP(t, func(w dns.ResponseWriter, req *dns.Msg) {
		resp := makeResponseMsg(req, []byte{0xa1, 0xb2, 0xc3}, 120)
		_ = w.WriteMsg(resp)
	})
	defer cleanup()

	c := NewClient(ClientConfig{Resolver: addr})
	result, err := c.Query(context.Background(), Query{
		Name:  "device42.fleet.example.",
		Class: 65280,
		Type:  65280,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if result.Name != "device42.fleet.example." {
		t.Errorf("Name = %q, want %q", result.Name, "device42.fleet.example.")
	}
	if result.Key != (protocol.ProtocolKey{Class: 65280, Type: 65280}) {
		t.Errorf("Key = %v, want %v", result.Key, protocol.ProtocolKey{Class: 65280, Type: 65280})
	}
	if result.RCode != dns.RcodeSuccess {
		t.Errorf("RCode = %d, want %d", result.RCode, dns.RcodeSuccess)
	}
	if len(result.Payload) != 3 || result.Payload[0] != 0xa1 || result.Payload[1] != 0xb2 || result.Payload[2] != 0xc3 {
		t.Errorf("Payload = %v, want [161 178 195]", result.Payload)
	}
	if result.TTL != 120 {
		t.Errorf("TTL = %d, want 120", result.TTL)
	}
	if result.Transport != "udp" {
		t.Errorf("Transport = %q, want %q", result.Transport, "udp")
	}
	if result.FellBack {
		t.Error("FellBack should be false")
	}
	if result.RTT <= 0 {
		t.Errorf("RTT = %v, want > 0", result.RTT)
	}
	if result.Verified {
		t.Error("Verified should be false")
	}
}

func TestResultTCPFields(t *testing.T) {
	// Start a UDP server that also listens on TCP (same port).
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}

	tcpListener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: udpConn.LocalAddr().(*net.UDPAddr).Port})
	if err != nil {
		t.Fatalf("ListenTCP: %v", err)
	}

	addr := udpConn.LocalAddr().String()

	handler := func(w dns.ResponseWriter, req *dns.Msg) {
		resp := makeResponseMsg(req, []byte{0xde, 0xad}, 60)
		_ = w.WriteMsg(resp)
	}

	udpServer := &dns.Server{Net: "udp", Handler: dns.HandlerFunc(handler), UDPSize: 1232, PacketConn: udpConn}
	tcpServer := &dns.Server{Net: "tcp", Handler: dns.HandlerFunc(handler), Listener: tcpListener}

	go udpServer.ActivateAndServe()
	go tcpServer.ActivateAndServe()

	defer func() {
		udpServer.Shutdown()
		tcpServer.Shutdown()
		udpConn.Close()
		tcpListener.Close()
	}()

	c := NewClient(ClientConfig{Resolver: addr, Network: "tcp"})
	result, err := c.Query(context.Background(), Query{
		Name:  "device42.fleet.example.",
		Class: 65280,
		Type:  65280,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if result.Transport != "tcp" {
		t.Errorf("Transport = %q, want %q", result.Transport, "tcp")
	}
	if result.FellBack {
		t.Error("FellBack should be false for direct TCP")
	}
}

// --- Error type tests ---

func TestDNSErrorRCodeString(t *testing.T) {
	err := &DNSError{
		Kind:  KindRCode,
		RCode: dns.RcodeRefused,
		Name:  "test.",
		Key:   protocol.ProtocolKey{Class: 65280, Type: 65280},
	}
	msg := err.Error()
	if msg == "" {
		t.Error("Error() should not be empty")
	}
}

func TestDNSErrorTimeoutString(t *testing.T) {
	err := &DNSError{
		Kind: KindTimeout,
		Name: "test.",
		Key:  protocol.ProtocolKey{Class: 65280, Type: 65280},
	}
	msg := err.Error()
	if msg == "" {
		t.Error("Error() should not be empty")
	}
}

func TestDNSErrorWraps(t *testing.T) {
	cause := context.Canceled
	err := &DNSError{
		Kind: KindTimeout,
		Name: "test.",
		Key:  protocol.ProtocolKey{Class: 65280, Type: 65280},
		Err:  cause,
	}
	if !errors.Is(err, cause) {
		t.Error("DNSError should wrap the cause")
	}
}

func TestAppErrorString(t *testing.T) {
	err := &AppError{
		Status:     protocol.StatusNotFound,
		ProtocolID: "org.qmux.firmware",
		Version:    1,
	}
	msg := err.Error()
	if msg == "" {
		t.Error("Error() should not be empty")
	}
}

func TestVerifyErrorString(t *testing.T) {
	err := &VerifyError{Reason: "expired"}
	msg := err.Error()
	if msg == "" {
		t.Error("Error() should not be empty")
	}
}

func TestVerifyErrorWithCause(t *testing.T) {
	cause := fmt.Errorf("underlying issue")
	err := &VerifyError{Reason: "validation", Err: cause}
	if !errors.Is(err, cause) {
		t.Error("VerifyError should wrap the cause")
	}
}

// --- Context cancellation tests ---

func TestQueryContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	c := NewClient(ClientConfig{Resolver: "127.0.0.1:1"}) // Invalid address.
	_, err := c.Query(ctx, Query{
		Name:  "device42.fleet.example.",
		Class: 65280,
		Type:  65280,
	})
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

func TestQueryContextTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	c := NewClient(ClientConfig{Resolver: "127.0.0.1:1", AttemptTimeout: 500 * time.Millisecond})
	_, err := c.Query(ctx, Query{
		Name:  "device42.fleet.example.",
		Class: 65280,
		Type:  65280,
	})
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	dnsErr, ok := err.(*DNSError)
	if !ok {
		t.Fatalf("error = %T, want *DNSError", err)
	}
	if dnsErr.Kind != KindTimeout {
		t.Errorf("Kind = %v, want %v", dnsErr.Kind, KindTimeout)
	}
}

// --- QNAME normalization ---

func TestQueryNameTrailingDot(t *testing.T) {
	var receivedName string
	addr, cleanup := testServerUDP(t, func(w dns.ResponseWriter, req *dns.Msg) {
		receivedName = req.Question[0].Name
		resp := makeResponseMsg(req, []byte("ok"), 60)
		_ = w.WriteMsg(resp)
	})
	defer cleanup()

	c := NewClient(ClientConfig{Resolver: addr})
	// Name without trailing dot.
	_, err := c.Query(context.Background(), Query{
		Name:  "device42.fleet.example",
		Class: 65280,
		Type:  65280,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if receivedName != "device42.fleet.example." {
		t.Errorf("QNAME = %q, want %q", receivedName, "device42.fleet.example.")
	}
}

func TestQueryNameLowercase(t *testing.T) {
	var receivedName string
	addr, cleanup := testServerUDP(t, func(w dns.ResponseWriter, req *dns.Msg) {
		receivedName = req.Question[0].Name
		resp := makeResponseMsg(req, []byte("ok"), 60)
		_ = w.WriteMsg(resp)
	})
	defer cleanup()

	c := NewClient(ClientConfig{Resolver: addr})
	// Name with uppercase.
	_, err := c.Query(context.Background(), Query{
		Name:  "DEVICE42.FLEET.EXAMPLE.",
		Class: 65280,
		Type:  65280,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if receivedName != "device42.fleet.example." {
		t.Errorf("QNAME = %q, want %q", receivedName, "device42.fleet.example.")
	}
}

// --- Case-insensitive response matching ---

func TestQueryCaseInsensitiveResponse(t *testing.T) {
	addr, cleanup := testServerUDP(t, func(w dns.ResponseWriter, req *dns.Msg) {
		// Answer with uppercase name.
		resp := new(dns.Msg)
		resp.SetReply(req)
		resp.Authoritative = true
		rr := &dns.RFC3597{
			Hdr: dns.RR_Header{
				Name:   "DEVICE42.FLEET.EXAMPLE.", // Uppercase
				Rrtype: req.Question[0].Qtype,
				Class:  req.Question[0].Qclass,
				Ttl:    60,
			},
			Rdata: hex.EncodeToString([]byte("ok")),
		}
		resp.Answer = append(resp.Answer, rr)
		_ = w.WriteMsg(resp)
	})
	defer cleanup()

	c := NewClient(ClientConfig{Resolver: addr})
	result, err := c.Query(context.Background(), Query{
		Name:  "device42.fleet.example.",
		Class: 65280,
		Type:  65280,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if string(result.Payload) != "ok" {
		t.Errorf("Payload = %q, want %q", result.Payload, "ok")
	}
}
