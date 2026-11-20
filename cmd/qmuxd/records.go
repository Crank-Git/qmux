package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/Crank-Git/qmux/pkg/codec"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/security"
)

// hexPrefix starts a payload string that converts to a CBOR byte string.
const hexPrefix = "hex:"

// recordHandler returns the static-record handler of records. records maps each
// lowercase record QNAME to its record.
//
// The handler answers the record of the request name with the record payload and
// status. It answers not-found when no record has the name, or when the record holds
// another version.
func recordHandler(records map[string]record) protocol.Handler {
	return protocol.HandlerFunc(func(_ context.Context, req *protocol.Request) (*protocol.Response, error) {
		// The server gives the name in lowercase, so the lookup ignores the case of
		// the query.
		r, ok := records[req.Name]
		if !ok || (r.Version != 0 && r.Version != req.Version) {
			return &protocol.Response{Status: protocol.StatusNotFound, Version: req.Version}, nil
		}
		return &protocol.Response{Payload: r.Payload, Status: r.Status, Version: req.Version}, nil
	})
}

// recordPayload returns the payload bytes of the record fr at the JSON path path. The
// record must hold exactly one of payload and payload_hex.
func recordPayload(fr fileRecord, path string) ([]byte, error) {
	switch {
	case fr.Payload != nil && fr.PayloadHex != nil:
		return nil, fmt.Errorf("%s: the record holds payload and payload_hex; use exactly one", path)
	case fr.PayloadHex != nil:
		b, err := hex.DecodeString(*fr.PayloadHex)
		if err != nil {
			return nil, fmt.Errorf("%s.payload_hex: %w", path, err)
		}
		return b, nil
	case fr.Payload != nil:
		return payloadCBOR(fr.Payload, path+".payload")
	}
	return nil, fmt.Errorf("%s: the record holds no payload or payload_hex; use exactly one", path)
}

// payloadCBOR returns the deterministic CBOR encoding of the JSON value raw at the JSON
// path path, with the JSON-to-CBOR rule of FR-qmuxd-21. The error names the JSON path
// of the first value that has no CBOR kind.
func payloadCBOR(raw json.RawMessage, path string) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	// UseNumber keeps the JSON text of each number, so that an integer above 2^53
	// does not lose precision and an integer stays apart from a float.
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c, err := cborValue(v, path)
	if err != nil {
		return nil, err
	}
	b, err := codec.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return b, nil
}

// cborValue returns the value that codec.Marshal encodes as the CBOR kind of the
// decoded JSON value v at the JSON path path.
func cborValue(v any, path string) (any, error) {
	switch v := v.(type) {
	case nil:
		return nil, fmt.Errorf("%s: null is not a valid payload value", path)
	case bool:
		return v, nil
	case json.Number:
		return cborNumber(v, path)
	case string:
		h, ok := strings.CutPrefix(v, hexPrefix)
		if !ok {
			return v, nil
		}
		b, err := hex.DecodeString(h)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return b, nil
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			c, err := cborValue(e, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
			out[i] = c
		}
		return out, nil
	default:
		// encoding/json decodes an object into any as map[string]any, and no other
		// kind remains.
		return cborMap(v.(map[string]any), path)
	}
}

// cborNumber returns the number n at the JSON path path as a uint64, an int64 or a
// float64. A number with a fraction or an exponent is a float.
func cborNumber(n json.Number, path string) (any, error) {
	s := n.String()
	if strings.ContainsAny(s, ".eE") {
		f, err := n.Float64()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return f, nil
	}
	if strings.HasPrefix(s, "-") {
		i, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return i, nil
	}
	u, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return u, nil
}

// cborMap returns the object m at the JSON path path as a map with unsigned integer
// keys when every key is a decimal integer, and as a map with text keys otherwise.
func cborMap(m map[string]any, path string) (any, error) {
	uintKeys := make(map[uint64]any, len(m))
	textKeys := make(map[string]any, len(m))
	for k, e := range m {
		c, err := cborValue(e, fmt.Sprintf("%s[%q]", path, k))
		if err != nil {
			return nil, err
		}
		textKeys[k] = c
		// Only the canonical form is a decimal integer key, so "01" and "1" never
		// become one key.
		if u, err := strconv.ParseUint(k, 10, 64); err == nil && strconv.FormatUint(u, 10) == k {
			uintKeys[u] = c
		}
	}
	if len(uintKeys) == len(textKeys) {
		return uintKeys, nil
	}
	return textKeys, nil
}

// checkEnvelopeSizes returns an error for the first record of p whose envelope, signed
// by signer, is larger than the envelope limit. path is the JSON path of p. The server
// answers such a record with SERVFAIL, so qmuxd rejects it at load time.
func checkEnvelopeSizes(p protocolConfig, path string, signer *security.Signer) error {
	for _, name := range slices.Sorted(maps.Keys(p.Records)) {
		r := p.Records[name]
		version := r.Version
		if version == 0 {
			version = p.Info.Versions[len(p.Info.Versions)-1]
		}
		e := codec.Envelope{
			Format:      1,
			ProtocolID:  p.Info.ID,
			Version:     uint64(version),
			MessageType: uint64(protocol.MessageRecord),
			Status:      uint64(r.Status),
			Payload:     r.Payload,
		}
		err := signer.Sign(&e, name, p.Key)
		if err == nil {
			_, err = codec.Encode(e, codec.Limits{})
		}
		if err != nil {
			return fmt.Errorf("%s.records[%q]: %w", path, name, err)
		}
	}
	return nil
}
