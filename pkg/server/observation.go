package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/transport"
)

// observationErrorInterval is the minimum time between two error records for failed
// writes to the observation log. A full disk fails every write, and one record for each
// query would fill the log of the server too.
const observationErrorInterval = time.Minute

// tsLayout is RFC 3339 with exactly nine fraction digits. time.RFC3339Nano removes
// trailing zeros, so the width of the field would change from line to line.
const tsLayout = "2006-01-02T15:04:05.000000000Z07:00"

// observation is one line of the observation log, with the fields of FR-qmuxd-33.
type observation struct {
	TS        string `json:"ts"`
	Transport string `json:"transport"`
	Source    string `json:"source"`
	QName     string `json:"qname"`
	QClass    uint16 `json:"qclass"`
	QType     uint16 `json:"qtype"`
	RCode     string `json:"rcode"`
	// Status is nil when the sent response holds no envelope. JSON then holds null.
	Status     *string `json:"status"`
	BytesIn    int     `json:"bytes_in"`
	BytesOut   int     `json:"bytes_out"`
	DurationUS int64   `json:"duration_us"`
}

// observationLog writes one JSON line for each answered query to w.
type observationLog struct {
	logger *slog.Logger

	// mu serializes the writes to w, so that two lines never mix (FR-qmuxd-35).
	mu      sync.Mutex
	w       io.Writer
	lastErr time.Time
}

// record writes the line for q and the response sent. status is nil when sent holds no
// envelope. A write error does not stop the server. record logs it at most once in each
// observationErrorInterval.
func (o *observationLog) record(ctx context.Context, q *transport.Query, sent transport.Sent, status *protocol.Status) {
	line := observation{
		TS:         q.ReceivedAt.UTC().Format(tsLayout),
		Transport:  q.Transport,
		Source:     q.Source.String(),
		QName:      q.Name,
		QClass:     q.Key.Class,
		QType:      q.Key.Type,
		RCode:      sent.RCode.String(),
		BytesIn:    q.WireSize,
		BytesOut:   sent.WireSize,
		DurationUS: time.Since(q.ReceivedAt).Microseconds(),
	}
	if status != nil {
		name := statusName(*status)
		line.Status = &name
	}
	// The fields are strings and integers only, so Marshal cannot fail.
	b, _ := json.Marshal(line)
	b = append(b, '\n')

	o.mu.Lock()
	defer o.mu.Unlock()
	if _, err := o.w.Write(b); err != nil {
		if now := time.Now(); now.Sub(o.lastErr) >= observationErrorInterval {
			o.lastErr = now
			o.logger.ErrorContext(ctx, "server: write observation log", "error", err)
		}
	}
}

// statusName returns the name of a framework application status from the status table
// of the spec, or the decimal number for any other status.
func statusName(s protocol.Status) string {
	switch s {
	case protocol.StatusOK:
		return "ok"
	case protocol.StatusNotFound:
		return "not-found"
	case protocol.StatusBadRequest:
		return "bad-request"
	case protocol.StatusVersionUnsupported:
		return "version-unsupported"
	case protocol.StatusUnavailable:
		return "unavailable"
	}
	return strconv.FormatUint(uint64(s), 10)
}
