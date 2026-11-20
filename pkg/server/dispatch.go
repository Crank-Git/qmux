package server

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"

	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/registry"
	"github.com/Crank-Git/qmux/pkg/transport"
)

// dispatch answers q, which passed every check, with the handler of reg. zone is the
// zone that matches q.Name.
//
// A query with a bad version label, or with a version that reg does not have, gets
// an envelope with that application status. The server then does not call the handler.
func (s *Server) dispatch(ctx context.Context, q *transport.Query, zone string, reg registry.Registration, w transport.Writer) {
	qname := strings.ToLower(q.Name)
	v, ok := splitQName(qname, zone)
	if !ok {
		s.answerStatus(ctx, q, reg, protocol.StatusBadRequest, w)
		return
	}
	if v.version != 0 && !slices.Contains(reg.Info.Versions, v.version) {
		s.answerStatus(ctx, q, reg, protocol.StatusVersionUnsupported, w)
		return
	}
	version := v.version
	if version == 0 {
		version = highestVersion(reg)
	}
	resp, ok := s.callHandler(ctx, reg.Handler, &protocol.Request{
		QName:      qname,
		Name:       v.name,
		Key:        q.Key,
		Version:    version,
		Labels:     v.labels,
		Zone:       zone,
		Source:     q.Source,
		Transport:  q.Transport,
		UDPSize:    q.UDPSize,
		ID:         q.ID,
		ReceivedAt: q.ReceivedAt,
	})
	if !ok {
		s.write(ctx, q, w, transport.Answer{RCode: transport.RCodeServFail}, "")
		return
	}
	rdata, err := s.envelope(q, reg, resp)
	if err != nil {
		s.cfg.Logger.Error("server: build envelope", "key", q.Key.String(), "error", err)
		s.write(ctx, q, w, transport.Answer{RCode: transport.RCodeServFail}, "")
		return
	}
	s.answerEnvelope(ctx, q, w, rdata, resp.TTL, resp.Status)
}

// answerStatus answers q with an envelope that holds status, no payload and the
// highest version of reg.
//
// The schema of reg checks handler payloads only, so answerStatus does not call it.
// A schema that rejects an empty payload would otherwise hide the status.
func (s *Server) answerStatus(ctx context.Context, q *transport.Query, reg registry.Registration, status protocol.Status, w transport.Writer) {
	rdata, err := s.encodeEnvelope(q, reg, highestVersion(reg), &protocol.Response{Status: status})
	if err != nil {
		s.cfg.Logger.Error("server: build envelope", "key", q.Key.String(), "error", err)
		s.write(ctx, q, w, transport.Answer{RCode: transport.RCodeServFail}, "")
		return
	}
	s.answerEnvelope(ctx, q, w, rdata, 0, status)
}

// answerEnvelope answers q with NOERROR and one answer RR that holds rdata. A ttl of
// 0 selects cfg.TTL.
func (s *Server) answerEnvelope(ctx context.Context, q *transport.Query, w transport.Writer, rdata []byte, ttl uint32, status protocol.Status) {
	if ttl == 0 {
		ttl = s.cfg.TTL
	}
	s.write(ctx, q, w, transport.Answer{RCode: transport.RCodeNoError, TTL: ttl, Payload: rdata},
		strconv.FormatUint(uint64(status), 10))
}

// highestVersion returns the highest version of reg. The registry keeps the versions
// in ascending order.
func highestVersion(reg registry.Registration) uint16 {
	return reg.Info.Versions[len(reg.Info.Versions)-1]
}

// envelope returns the encoded envelope of resp to q for the registration reg.
//
// It returns errVersion when resp.Version is not a version of reg, the error of
// reg.Info.Schema when the schema rejects the payload, and the error of
// encodeEnvelope.
func (s *Server) envelope(q *transport.Query, reg registry.Registration, resp *protocol.Response) ([]byte, error) {
	version := resp.Version
	if version == 0 {
		version = highestVersion(reg)
	} else if !slices.Contains(reg.Info.Versions, version) {
		return nil, fmt.Errorf("%w: %d", errVersion, version)
	}
	if schema := reg.Info.Schema; schema != nil {
		if err := schema.Validate(version, resp.Payload); err != nil {
			return nil, fmt.Errorf("schema rejects the payload of version %d: %w", version, err)
		}
	}
	return s.encodeEnvelope(q, reg, version, resp)
}

// encodeEnvelope returns the encoded envelope of resp to q with the protocol ID of
// reg and version. When cfg.Signer is set, the envelope holds a signature block for
// q.Name and q.Key. It returns the error of the signer and the error of codec.Encode.
func (s *Server) encodeEnvelope(q *transport.Query, reg registry.Registration, version uint16, resp *protocol.Response) ([]byte, error) {
	messageType := resp.MessageType
	if messageType == 0 {
		messageType = protocol.MessageRecord
	}
	e := codec.Envelope{
		Format:      1,
		ProtocolID:  reg.Info.ID,
		Version:     uint64(version),
		MessageType: uint64(messageType),
		Status:      uint64(resp.Status),
		Payload:     resp.Payload,
	}
	if s.cfg.Signer != nil {
		// The signature covers the QNAME with the version label, so an envelope
		// for one version does not verify for another version (FR-security-16).
		if err := s.cfg.Signer.Sign(&e, q.Name, q.Key); err != nil {
			return nil, fmt.Errorf("signer: %w", err)
		}
	}
	b, err := codec.Encode(e, codec.Limits{})
	if err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}
	return b, nil
}

// errVersion means that the handler returned a protocol version that its
// registration does not have.
var errVersion = errors.New("response version is not a version of the registration")

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

// qnameParts is a QNAME split into the version label and the rest.
type qnameParts struct {
	// version is 0 when the QNAME has no version label.
	version uint16
	// name is the QNAME without the version label.
	name string
	// labels holds the labels below the zone, without the version label.
	labels []string
}

// splitQName returns the parts of name. Both name and zone are lowercase and fully
// qualified, and name is inside zone. Only a label below zone can be the version label.
//
// It returns false when the leftmost label below zone starts with "_v" but does not
// parse as a version label.
func splitQName(name, zone string) (qnameParts, bool) {
	labels := splitLabels(name)
	labels = labels[:len(labels)-len(splitLabels(zone))]
	if len(labels) == 0 || !strings.HasPrefix(labels[0], "_v") {
		return qnameParts{name: name, labels: labels}, true
	}
	v, ok := parseVersionLabel(labels[0])
	if !ok {
		return qnameParts{}, false
	}
	rest := name[len(labels[0])+1:]
	if rest == "" {
		rest = rootZone
	}
	return qnameParts{version: v, name: rest, labels: labels[1:]}, true
}

// splitLabels returns the labels of name, which is in the presentation format. The
// root name has no labels. A backslash escapes the next character, so an escaped dot
// (\.) is part of its label. The digits of a \DDD escape are never a dot, so the split
// keeps them in the label too.
func splitLabels(name string) []string {
	if name == rootZone {
		return nil
	}
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
	// A zone in the config can lack the final dot. Its last label still counts.
	if start < len(name) {
		labels = append(labels, name[start:])
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
