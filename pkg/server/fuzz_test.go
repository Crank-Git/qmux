package server

import (
	"context"
	"net/netip"
	"testing"

	"github.com/Crank-Git/qmux/pkg/transport"
	"github.com/Crank-Git/qmux/pkg/transport/transporttest"
)

// FuzzServeMessage sends arbitrary bytes through the request path of the server over
// UDP and TCP. The server must not panic, and each response must parse. (FR-server-44)
func FuzzServeMessage(f *testing.F) {
	question := []transporttest.RR{{Name: "device42.fleet.example.", Key: testKey}}
	// The seeds reach each check of the request path, and the handler.
	seeds := []transporttest.Message{
		{ID: 1, Question: question},
		{ID: 2, Question: []transporttest.RR{{Name: "_v1.Device42.FLEET.example.", Key: testKey}}},
		{ID: 3, Response: true, Question: question},
		{ID: 4, Opcode: opcodeNotify, Question: question},
		{ID: 5},
		{ID: 6, Question: []transporttest.RR{question[0], question[0]}},
		{ID: 7, Question: question, EDNS: true, UDPSize: 1232},
		{ID: 8, Question: question, EDNS: true, EDNSVersion: 1, UDPSize: 4096},
		{ID: 9, Question: []transporttest.RR{{Name: "a.other.example.", Key: testKey}}},
		{ID: 10, Question: question, Answer: []transporttest.RR{{Name: "x.", Key: testKey, Data: []byte{1, 2, 3}}}},
		padded(transporttest.Message{ID: 11, Question: question}, 600),
	}
	for _, m := range seeds {
		wire, err := transporttest.Pack(m)
		if err != nil {
			f.Fatalf("Pack seed %d: %v", m.ID, err)
		}
		f.Add(wire)
	}
	// A short message, a bare header, and a header with a truncated question.
	f.Add([]byte{0})
	f.Add(make([]byte, 12))
	f.Add([]byte("\x00\x01\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\xc0"))

	s := NewServer(ServerConfig{Zones: []string{"fleet.example."}, Logger: quietLogger()})
	if err := s.RegisterProtocol(testKey, testInfo, payloadHandler([]byte("ok"))); err != nil {
		f.Fatalf("RegisterProtocol: %v", err)
	}
	src := netip.MustParseAddrPort("127.0.0.1:5300")
	f.Fuzz(func(t *testing.T, wire []byte) {
		for _, network := range []string{transport.UDP, transport.TCP} {
			resp, err := transport.ServeMessage(context.Background(), wire, src, network,
				transport.HandlerFunc(s.serveQuery), transport.ServeConfig{UDPSize: s.cfg.UDPSize})
			// ServeMessage rejects a message shorter than the header, and the server
			// ignores a message with QR set. Neither has a response to check.
			if err != nil || resp == nil {
				continue
			}
			if _, err := transporttest.Unpack(resp); err != nil {
				t.Fatalf("%s: the response does not parse: %v", network, err)
			}
		}
	})
}
