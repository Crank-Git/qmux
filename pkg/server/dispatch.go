package server

import (
	"context"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/registry"
	"github.com/Crank-Git/qmux/pkg/transport"
)

// dispatch answers q, which passed every check, with the handler of reg. zone is the
// zone that matches q.Name.
func (s *Server) dispatch(ctx context.Context, q *transport.Query, zone string, reg registry.Registration, w transport.Writer) {
	name := strings.ToLower(q.Name)
	version, labels := splitQName(name, zone)
	resp, ok := s.callHandler(ctx, reg.Handler, &protocol.Request{
		QName:      name,
		Key:        q.Key,
		Version:    version,
		Labels:     labels,
		Zone:       zone,
		Source:     q.Source,
		Transport:  q.Transport,
		UDPSize:    q.UDPSize,
		ID:         q.ID,
		ReceivedAt: q.ReceivedAt,
	})
	if !ok {
		s.write(w, transport.Answer{RCode: transport.RCodeServFail})
		return
	}
	if schema := reg.Info.Schema; schema != nil {
		v := resp.Version
		if v == 0 {
			// The registry keeps the versions in ascending order.
			v = reg.Info.Versions[len(reg.Info.Versions)-1]
		}
		if err := schema.Validate(v, resp.Payload); err != nil {
			s.write(w, transport.Answer{RCode: transport.RCodeServFail})
			return
		}
	}
	ttl := resp.TTL
	if ttl == 0 {
		ttl = s.cfg.TTL
	}
	s.write(w, transport.Answer{RCode: transport.RCodeNoError, TTL: ttl, Payload: resp.Payload})
}

// handlerResult is the result of one handler call.
type handlerResult struct {
	resp     *protocol.Response
	err      error
	panicked bool
}

// callHandler returns the response of h to req, and true. It returns false when h
// returns an error or a nil response, panics, or runs past cfg.HandlerTimeout.
//
// callHandler returns at the timeout also when h ignores its context. h then runs on
// in its own goroutine, and callHandler discards its late response.
func (s *Server) callHandler(ctx context.Context, h protocol.Handler, req *protocol.Request) (*protocol.Response, bool) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.HandlerTimeout)
	defer cancel()
	// The buffer lets a late handler goroutine send its result and end.
	done := make(chan handlerResult, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				s.cfg.Logger.Error("server: handler panic", "key", req.Key.String(),
					"panic", p, "stack", string(debug.Stack()))
				done <- handlerResult{panicked: true}
			}
		}()
		resp, err := h.Handle(ctx, req)
		done <- handlerResult{resp: resp, err: err}
	}()

	select {
	case r := <-done:
		if r.panicked || r.err != nil {
			return nil, false
		}
		if r.resp == nil {
			s.cfg.Logger.Error("server: handler returned a nil response and a nil error", "key", req.Key.String())
			return nil, false
		}
		return r.resp, true
	case <-ctx.Done():
		return nil, false
	}
}

// splitQName returns the version and the labels of name below zone. Both name and
// zone are lowercase and fully qualified, and name is inside zone.
//
// The version comes from a leftmost version label, which is not in the labels. It is
// 0 when the leftmost label is not a version label.
func splitQName(name, zone string) (uint16, []string) {
	below := name
	if zone != rootZone {
		below = name[:len(name)-len(zone)]
	} else if name == rootZone {
		below = ""
	}
	labels := splitLabels(below)
	if len(labels) > 0 {
		if v, ok := parseVersionLabel(labels[0]); ok {
			return v, labels[1:]
		}
	}
	return 0, labels
}

// splitLabels returns the labels of name, which ends with a dot or is empty. A dot
// after a backslash is part of its label, as in the presentation format of a name.
func splitLabels(name string) []string {
	var labels []string
	start := 0
	for i := 0; i < len(name); i++ {
		switch name[i] {
		case '\\':
			i++
		case '.':
			labels = append(labels, name[start:i])
			start = i + 1
		}
	}
	return labels
}

// parseVersionLabel returns the version that label names, and true. A version label
// is "_v" and a decimal number from 1 to 65535 with no leading zero.
func parseVersionLabel(label string) (uint16, bool) {
	digits, ok := strings.CutPrefix(label, "_v")
	if !ok || digits == "" || digits[0] == '0' {
		return 0, false
	}
	// ParseUint rejects a sign, a non-digit and a value above 65535.
	v, err := strconv.ParseUint(digits, 10, 16)
	if err != nil {
		return 0, false
	}
	return uint16(v), true
}
