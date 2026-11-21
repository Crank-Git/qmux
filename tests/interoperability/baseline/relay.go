package baseline

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
)

// relay forwards the datagrams of one CoAP lookup between a loopback socket and the
// server, and counts the datagrams that it sends to the server. go-coap creates the
// client socket itself and gives no hook on its writes, so the count comes from here.
//
// The relay opens a new socket to the server for each lookup, so the server sees a
// new source port each time (FR-bench-23). It adds one loopback hop in each direction.
type relay struct {
	local    *net.UDPConn
	upstream *net.UDPConn
	sent     atomic.Int64
	// peer is the address of the go-coap socket, known after its first datagram.
	peer atomic.Pointer[net.UDPAddr]
	wg   sync.WaitGroup
}

// newRelay returns a started relay to the server at server. The error is a listen or
// dial error.
func newRelay(server string) (*relay, error) {
	raddr, err := net.ResolveUDPAddr("udp", server)
	if err != nil {
		return nil, fmt.Errorf("baseline: coap relay: resolve %s: %w", server, err)
	}
	upstream, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return nil, fmt.Errorf("baseline: coap relay: dial %s: %w", server, err)
	}
	local, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("baseline: coap relay: listen: %w", err), upstream.Close())
	}
	r := &relay{local: local, upstream: upstream}
	r.wg.Add(2)
	go r.toServer()
	go r.toClient()
	return r, nil
}

// addr returns the loopback address to which the go-coap client sends.
func (r *relay) addr() string {
	return r.local.LocalAddr().String()
}

// close stops the relay and returns the number of datagrams that it sent to the
// server. The close of the sockets ends both forward loops.
func (r *relay) close() int {
	// The lookup is complete, so a close error changes no result.
	_ = r.local.Close()
	_ = r.upstream.Close()
	r.wg.Wait()
	return int(r.sent.Load())
}

// toServer forwards each datagram from the go-coap socket to the server.
func (r *relay) toServer() {
	defer r.wg.Done()
	buf := make([]byte, 65535)
	for {
		n, from, err := r.local.ReadFromUDP(buf)
		if err != nil {
			return
		}
		r.peer.Store(from)
		if _, err := r.upstream.Write(buf[:n]); err == nil {
			r.sent.Add(1)
		}
	}
}

// toClient forwards each datagram from the server to the go-coap socket.
func (r *relay) toClient() {
	defer r.wg.Done()
	buf := make([]byte, 65535)
	for {
		n, err := r.upstream.Read(buf)
		if err != nil {
			// A connected UDP socket reports an ICMP error as a read error. The server
			// can come back, so only a closed socket ends the loop.
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		if peer := r.peer.Load(); peer != nil {
			// A failed write loses the datagram, as a lossy link does, and go-coap
			// then retransmits.
			_, _ = r.local.WriteToUDP(buf[:n], peer)
		}
	}
}
