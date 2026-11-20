package transport

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/miekg/dns"
)

// SendUDP sends a DNS message over UDP to the given address and returns the
// response. It uses the configured UDPSize for the send buffer. The function
// respects ctx deadlines and returns a timeout error when the context expires
// before a response arrives.
//
// Returns the unpacked message, round-trip time, query wire size, response
// wire size, and any error that occurred.
func SendUDP(ctx context.Context, addr string, m *dns.Msg, udpSize int) (*dns.Msg, time.Duration, int, int, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, 0, 0, 0, fmt.Errorf("transport: resolve udp address: %w", err)
	}

	conn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		return nil, 0, 0, 0, fmt.Errorf("transport: dial udp: %w", err)
	}
	defer conn.Close()

	wire, err := m.Pack()
	if err != nil {
		return nil, 0, 0, 0, fmt.Errorf("transport: pack query: %w", err)
	}
	queryBytes := len(wire)

	start := time.Now()

	// Write with deadline from context.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(deadline)
	}
	if _, err := conn.Write(wire); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, time.Since(start), queryBytes, 0, cerr
		}
		return nil, time.Since(start), queryBytes, 0, fmt.Errorf("transport: write udp: %w", err)
	}

	// Read with deadline from context.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetReadDeadline(deadline)
	}

	buf := make([]byte, udpSize)
	n, err := conn.Read(buf)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, time.Since(start), queryBytes, 0, cerr
		}
		return nil, time.Since(start), queryBytes, 0, fmt.Errorf("transport: read udp: %w", err)
	}

	rtt := time.Since(start)

	resp := new(dns.Msg)
	if err := resp.Unpack(buf[:n]); err != nil {
		return nil, rtt, queryBytes, n, fmt.Errorf("transport: unpack response: %w", err)
	}

	return resp, rtt, queryBytes, n, nil
}

// SendTCP sends a DNS message over TCP to the given address and returns the
// response. It respects ctx deadlines.
//
// Returns the unpacked message, round-trip time, query wire size, response
// wire size, and any error that occurred.
func SendTCP(ctx context.Context, addr string, m *dns.Msg) (*dns.Msg, time.Duration, int, int, error) {
	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		return nil, 0, 0, 0, fmt.Errorf("transport: resolve tcp address: %w", err)
	}

	conn, err := net.DialTCP("tcp", nil, tcpAddr)
	if err != nil {
		return nil, 0, 0, 0, fmt.Errorf("transport: dial tcp: %w", err)
	}
	defer conn.Close()

	wire, err := m.Pack()
	if err != nil {
		return nil, 0, 0, 0, fmt.Errorf("transport: pack query: %w", err)
	}
	queryBytes := len(wire)

	start := time.Now()

	// Write length-prefixed query.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(deadline)
	}
	length := make([]byte, 2)
	length[0] = byte(len(wire) >> 8)
	length[1] = byte(len(wire))
	if _, err := conn.Write(append(length, wire...)); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, time.Since(start), queryBytes, 0, cerr
		}
		return nil, time.Since(start), queryBytes, 0, fmt.Errorf("transport: write tcp: %w", err)
	}

	// Read length-prefixed response.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetReadDeadline(deadline)
	}

	lengthBuf := make([]byte, 2)
	if _, err := io.ReadFull(conn, lengthBuf); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, time.Since(start), queryBytes, 0, cerr
		}
		return nil, time.Since(start), queryBytes, 0, fmt.Errorf("transport: read tcp length: %w", err)
	}

	respLen := int(binary.BigEndian.Uint16(lengthBuf))
	if respLen == 0 {
		return nil, time.Since(start), queryBytes, 0, fmt.Errorf("transport: zero-length tcp response")
	}

	respBuf := make([]byte, respLen)
	if _, err := io.ReadFull(conn, respBuf); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, time.Since(start), queryBytes, 0, cerr
		}
		return nil, time.Since(start), queryBytes, 0, fmt.Errorf("transport: read tcp body: %w", err)
	}

	rtt := time.Since(start)

	resp := new(dns.Msg)
	if err := resp.Unpack(respBuf); err != nil {
		return nil, rtt, queryBytes, respLen, fmt.Errorf("transport: unpack response: %w", err)
	}

	return resp, rtt, queryBytes, respLen, nil
}
