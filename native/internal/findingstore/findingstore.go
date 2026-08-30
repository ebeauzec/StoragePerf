// Package findingstore is Plumb's memory of what's been wrong and for how
// long — without it, every finding looks equally "new" on every evaluation,
// which is fine for a live dashboard (it just re-derives current state) but
// wrong for two things a snapshot-only view can't do: telling a webhook
// "this JUST became critical" instead of re-firing every poll, and letting
// an operator acknowledge a known issue so it stops demanding attention
// without hiding it from the dashboard.
//
// Deliberately a flat JSON file, not a database — this only ever tracks the
// small set of metrics currently at watch/critical across the whole fleet
// (rarely more than a few dozen rows), matching the project's existing
// file-based config/data philosophy. VictoriaMetrics already retains the
// real historical values; this only needs to remember the shape of "when
// did this specific problem start and has anyone looked at it," not the
// numbers themselves.
package findingstore

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Record is one currently-open (watch or critical) finding for one array's
// metric. Removed from the store the moment that metric returns to
// good/unknown — see HistoryEntry for what happens to it then.
type Record struct {
	ArrayID   string     `json:"array_id"`
	ArrayName string     `json:"array_name"`
	Vendor    string     `json:"vendor"`
	MetricID  string     `json:"metric_id"`
	Label     string     `json:"label"`
	Severity  string     `json:"severity"` // "watch" | "critical"
	FirstSeen time.Time  `json:"first_seen"`
	LastSeen  time.Time  `json:"last_seen"`
	Acked     bool       `json:"acked"`
	AckedAt   *time.Time `json:"acked_at,omitempty"`
	AckNote   string     `json:"ack_note,omitempty"`
}

func (r Record) key() string { return r.ArrayID + "|" + r.MetricID }

// HistoryEntry is what a Record becomes once its metric returns to
// good/unknown — an append-only log, since "how long did past problems
// last" is exactly the kind of thing a live snapshot can't answer.
type HistoryEntry struct {
	ArrayID    string    `json:"array_id"`
	ArrayName  string    `json:"array_name"`
	MetricID   string    `json:"metric_id"`
	Label      string    `json:"label"`
	Severity   string    `json:"severity"`
	FirstSeen  time.Time `json:"first_seen"`
	ResolvedAt time.Time `json:"resolved_at"`
	WasAcked   bool      `json:"was_acked"`
}

// CurrentFinding is what the caller (the monitor loop, evaluating live
// panels exactly like the dashboard does) hands in for one array on each
// cycle — the whole current set of that array's non-good metrics.
type CurrentFinding struct {
	MetricID string
	Label    string
	Severity string // "watch" | "critical"
}

func severityRank(s string) int {
	switch s {
	case "critical":
		return 2
	case "watch":
		return 1
	default:
		return 0
	}
}

// Store is safe for concurrent use — the monitor loop's periodic Reconcile
// calls and the API's on-demand Acknowledge/List calls all go through the
// same instance.
type Store struct {
	mu          sync.Mutex
	path        string
	historyPath string
	records     map[string]Record
}

// Open loads the persisted findings, if any — a missing file is a fresh
// install or an upgrade from before this feature existed, not an error.
func Open(dataDir string) (*Store, error) {
	s := &Store{
		path:        filepath.Join(dataDir, "findings.json"),
		historyPath: filepath.Join(dataDir, "findings-history.jsonl"),
		records:     map[string]Record{},
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	var list []Record
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, err
	}
	for _, r := range list {
		s.records[r.key()] = r
	}
	return s, nil
}

// saveLocked persists the current record set — called with mu already held.
func (s *Store) saveLocked() error {
	list := make([]Record, 0, len(s.records))
	for _, r := range s.records {
		list = append(list, r)
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, b, 0o644)
}

func (s *Store) appendHistoryLocked(e HistoryEntry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.historyPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// Reconcile updates the store with one array's complete current set of
// non-good findings, returning what changed this cycle for notification
// purposes: newly-opened records, records that got worse (watch->critical),
// and records that resolved (were open, aren't in `current` anymore).
// De-escalation (critical->watch, still bad but improving) updates the
// stored severity without being reported as either — it's not new bad news,
// and it's not resolved.
//
// arrayID scopes this call to one array's rows only; other arrays' records
// are untouched, so this can be called once per array per monitor cycle
// without needing the whole fleet's state available at once.
func (s *Store) Reconcile(arrayID, arrayName, vendor string, current []CurrentFinding, now time.Time) (newOrEscalated, resolved []Record, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	seen := make(map[string]bool, len(current))
	for _, c := range current {
		key := arrayID + "|" + c.MetricID
		seen[key] = true
		existing, had := s.records[key]
		if !had {
			r := Record{ArrayID: arrayID, ArrayName: arrayName, Vendor: vendor, MetricID: c.MetricID, Label: c.Label, Severity: c.Severity, FirstSeen: now, LastSeen: now}
			s.records[key] = r
			newOrEscalated = append(newOrEscalated, r)
			continue
		}
		existing.LastSeen = now
		existing.ArrayName, existing.Vendor, existing.Label = arrayName, vendor, c.Label
		if severityRank(c.Severity) > severityRank(existing.Severity) {
			existing.Severity = c.Severity
			existing.Acked = false // an escalation deserves fresh attention even if the earlier, milder state was acknowledged
			existing.AckedAt = nil
			s.records[key] = existing
			newOrEscalated = append(newOrEscalated, existing)
			continue
		}
		existing.Severity = c.Severity
		s.records[key] = existing
	}

	for key, r := range s.records {
		if r.ArrayID != arrayID || seen[key] {
			continue
		}
		delete(s.records, key)
		resolved = append(resolved, r)
		s.appendHistoryLocked(HistoryEntry{ArrayID: r.ArrayID, ArrayName: r.ArrayName, MetricID: r.MetricID, Label: r.Label, Severity: r.Severity, FirstSeen: r.FirstSeen, ResolvedAt: now, WasAcked: r.Acked})
	}

	return newOrEscalated, resolved, s.saveLocked()
}

// Acknowledge marks one open finding as seen — returns false if no such
// open record exists (already resolved, or never existed).
func (s *Store) Acknowledge(arrayID, metricID, note string, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := arrayID + "|" + metricID
	r, ok := s.records[key]
	if !ok {
		return false, nil
	}
	r.Acked = true
	r.AckedAt = &now
	r.AckNote = note
	s.records[key] = r
	return true, s.saveLocked()
}

// List returns every open record, worst-severity and oldest first — the
// natural "what needs attention, and what's been open longest" order.
func (s *Store) List() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := make([]Record, 0, len(s.records))
	for _, r := range s.records {
		list = append(list, r)
	}
	sortRecords(list)
	return list
}

func sortRecords(list []Record) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0; j-- {
			a, b := list[j], list[j-1]
			less := severityRank(a.Severity) > severityRank(b.Severity) ||
				(severityRank(a.Severity) == severityRank(b.Severity) && a.FirstSeen.Before(b.FirstSeen))
			if !less {
				break
			}
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

// History returns the most recent resolved findings, newest first, capped
// at limit (0 means no cap) — read by streaming the append-only log rather
// than holding it all in memory permanently.
func (s *Store) History(limit int) ([]HistoryEntry, error) {
	b, err := os.ReadFile(s.historyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var entries []HistoryEntry
	dec := json.NewDecoder(bytes.NewReader(b))
	for {
		var e HistoryEntry
		if err := dec.Decode(&e); err != nil {
			break
		}
		entries = append(entries, e)
	}
	// newest first
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}
