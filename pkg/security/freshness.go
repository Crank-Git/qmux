package security

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/Crank-Git/qmux/pkg/protocol"
)

// FreshnessStore holds the newest issued_at that a Verifier accepted for each
// protocol ID and QNAME. The zero value is an empty in-memory store. A
// FreshnessStore is safe for concurrent use.
type FreshnessStore struct {
	mu     sync.Mutex
	newest map[freshnessKey]uint64
}

// freshnessKey is the protocol ID and the QNAME without the version label, with
// ASCII letters in lowercase, so a version change does not reset freshness.
type freshnessKey struct {
	protocolID string
	qname      string
}

func newFreshnessKey(protocolID, qname string) freshnessKey {
	qname = protocol.FoldName(qname)
	if label, rest, ok := strings.Cut(qname, "."); ok && isVersionLabel(label) {
		qname = rest
	}
	return freshnessKey{protocolID: protocolID, qname: qname}
}

// isVersionLabel reports whether label is "_v" and a decimal number from 1 to
// 65535 with no leading zero. pkg/server applies the same rule.
func isVersionLabel(label string) bool {
	digits, ok := strings.CutPrefix(label, "_v")
	if !ok || digits == "" || digits[0] == '0' || len(digits) > 5 {
		return false
	}
	n := 0
	for _, c := range digits {
		if c < '0' || c > '9' {
			return false
		}
		n = n*10 + int(c-'0')
	}
	return n <= 65535
}

// admit records issuedAt for k and returns true when issuedAt is not older than
// the stored value. It returns false and changes nothing for an older value. The
// check and the update hold one lock, so two concurrent calls cannot both pass
// with a value older than the other.
func (s *FreshnessStore) admit(k freshnessKey, issuedAt uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.newest[k]; ok && issuedAt < prev {
		return false
	}
	if s.newest == nil {
		s.newest = make(map[freshnessKey]uint64)
	}
	s.newest[k] = issuedAt
	return true
}

// freshnessFile is the JSON form of a FreshnessStore.
type freshnessFile struct {
	Entries []freshnessEntry `json:"entries"`
}

type freshnessEntry struct {
	ProtocolID string `json:"protocol_id"`
	QName      string `json:"qname"`
	IssuedAt   uint64 `json:"issued_at"`
}

// Save writes the store to the file at path as JSON, so a device keeps it across
// restarts. It replaces an existing file in one rename, so a failed Save leaves
// the earlier file complete. The file has mode 0600.
func (s *FreshnessStore) Save(path string) error {
	s.mu.Lock()
	f := freshnessFile{Entries: make([]freshnessEntry, 0, len(s.newest))}
	for k, v := range s.newest {
		f.Entries = append(f.Entries, freshnessEntry{ProtocolID: k.protocolID, QName: k.qname, IssuedAt: v})
	}
	s.mu.Unlock()
	// A sorted list gives the same file for the same store.
	slices.SortFunc(f.Entries, func(a, b freshnessEntry) int {
		return cmp.Or(cmp.Compare(a.ProtocolID, b.ProtocolID), cmp.Compare(a.QName, b.QName))
	})
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("save freshness store %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("save freshness store: %w", err)
	}
	_, werr := tmp.Write(append(b, '\n'))
	cerr := tmp.Close()
	if err := cmp.Or(werr, cerr); err != nil {
		return fmt.Errorf("save freshness store: %w", errors.Join(err, os.Remove(tmp.Name())))
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("save freshness store: %w", errors.Join(err, os.Remove(tmp.Name())))
	}
	return nil
}

// LoadFreshnessStore returns the store in the JSON file at path, which Save wrote.
// It returns an error when the file is not that JSON form or holds an unknown
// field. When two entries have one key, the store keeps the newer issued_at.
func LoadFreshnessStore(path string) (*FreshnessStore, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load freshness store: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var f freshnessFile
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("load freshness store %s: %w", path, err)
	}
	s := &FreshnessStore{}
	for _, e := range f.Entries {
		s.admit(freshnessKey{protocolID: e.ProtocolID, qname: e.QName}, e.IssuedAt)
	}
	return s, nil
}
