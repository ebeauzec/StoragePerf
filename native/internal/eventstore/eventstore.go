// Package eventstore holds discrete, already-timestamped occurrences —
// currently ONTAP EMS events (see internal/netappnative/ems.go) — as
// opposed to internal/findingstore, which tracks the open/resolved state of
// a metric currently over its own watch/critical threshold. An event has no
// "currently open" state to reconcile against: it happened at a point in
// time and stays in the log. That's the whole reason this is a separate,
// simpler package rather than a fork of findingstore — an append-only file,
// deduplicated on the source system's own event index, read back
// newest-first and capped at whatever the caller asks for.
package eventstore

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Event is one stored occurrence. Field names intentionally mirror
// internal/netappnative.EMSEvent's JSON shape so a caller can pass one
// straight through — this package doesn't import netappnative (nothing
// storage-generic should depend on one specific collector), so the
// conversion happens at the call site instead (see internal/api/monitor.go).
type Event struct {
	ArrayID   string `json:"array_id"`
	ArrayName string `json:"array_name"`
	Source    string `json:"source"` // e.g. "ems" — which collector produced this, for a future second event source
	Key       string `json:"key"`    // dedup key, e.g. netappnative.EMSEvent.DedupKey() — never re-appended once seen
	Time      string `json:"time"`   // RFC3339 — kept as a string so this package never needs a time.Time import/format opinion beyond what's already on the wire
	Severity  string `json:"severity"`
	Name      string `json:"name"`
	Node      string `json:"node"`
	Message   string `json:"message"`
}

// Store is safe for concurrent use — the monitor loop's periodic Append
// calls and the API's on-demand List calls go through the same instance.
type Store struct {
	mu   sync.Mutex
	path string
	seen map[string]bool
}

// Open loads just enough of the existing log to populate the dedup set —
// events themselves are re-read from disk on every List call rather than
// held in memory, since the log is expected to stay small (a curated
// allowlist, capped fetch size per poll) but there's no reason to hold two
// copies of it resident for the life of the process either.
func Open(dataDir string) (*Store, error) {
	s := &Store{path: filepath.Join(dataDir, "events.jsonl"), seen: map[string]bool{}}
	existing, err := s.readAll()
	if err != nil {
		return nil, err
	}
	for _, e := range existing {
		s.seen[e.Key] = true
	}
	return s, nil
}

func (s *Store) readAll() ([]Event, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var events []Event
	dec := json.NewDecoder(bytes.NewReader(b))
	for {
		var e Event
		if err := dec.Decode(&e); err != nil {
			break
		}
		events = append(events, e)
	}
	return events, nil
}

// Append writes any events not already present (by Key) to the log. Safe to
// call repeatedly with overlapping results from consecutive polls — this is
// the only place new rows are ever added, and it's the dedup gate.
func (s *Store) Append(events []Event) error {
	if len(events) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var toWrite []Event
	for _, e := range events {
		if e.Key == "" || s.seen[e.Key] {
			continue
		}
		s.seen[e.Key] = true
		toWrite = append(toWrite, e)
	}
	if len(toWrite) == 0 {
		return nil
	}

	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, e := range toWrite {
		b, err := json.Marshal(e)
		if err != nil {
			return err
		}
		if _, err := f.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	return nil
}

// List returns the most recent events, newest first, optionally filtered to
// one array, capped at limit (0 means no cap).
func (s *Store) List(arrayID string, limit int) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	events, err := s.readAll()
	if err != nil {
		return nil, err
	}
	// newest first
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	if arrayID != "" {
		filtered := events[:0]
		for _, e := range events {
			if e.ArrayID == arrayID {
				filtered = append(filtered, e)
			}
		}
		events = filtered
	}
	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}
