package eventstore

import "testing"

func TestAppendDedupsAndPersists(t *testing.T) {
	dir := t.TempDir()

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	events := []Event{
		{ArrayID: "a1", Key: "a1|1", Severity: "critical", Name: "disk.failed", Time: "2026-01-01T00:00:00Z"},
		{ArrayID: "a1", Key: "a1|2", Severity: "watch", Name: "wafl.vol.full", Time: "2026-01-01T00:01:00Z"},
	}
	if err := s.Append(events); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// Re-append the same events (simulating overlapping poll windows) plus
	// one genuinely new one — only the new one should land.
	if err := s.Append(append(events, Event{ArrayID: "a1", Key: "a1|3", Severity: "critical", Name: "ha.takeover.start", Time: "2026-01-01T00:02:00Z"})); err != nil {
		t.Fatalf("Append (overlap): %v", err)
	}

	list, err := s.List("", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 deduped events, got %d: %+v", len(list), list)
	}
	// newest first
	if list[0].Key != "a1|3" {
		t.Fatalf("expected newest-first order, got %+v", list)
	}

	// A fresh Open against the same dir should see the dedup set already
	// populated from disk — re-appending must not duplicate.
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("Open (reload): %v", err)
	}
	if err := s2.Append(events); err != nil {
		t.Fatalf("Append after reload: %v", err)
	}
	list2, err := s2.List("", 0)
	if err != nil {
		t.Fatalf("List after reload: %v", err)
	}
	if len(list2) != 3 {
		t.Fatalf("expected dedup to survive reload, got %d events", len(list2))
	}
}

func TestListFiltersByArrayAndLimit(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	events := []Event{
		{ArrayID: "a1", Key: "a1|1", Time: "2026-01-01T00:00:00Z"},
		{ArrayID: "a2", Key: "a2|1", Time: "2026-01-01T00:01:00Z"},
		{ArrayID: "a1", Key: "a1|2", Time: "2026-01-01T00:02:00Z"},
	}
	if err := s.Append(events); err != nil {
		t.Fatalf("Append: %v", err)
	}

	a1, err := s.List("a1", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(a1) != 2 {
		t.Fatalf("expected 2 events for a1, got %d", len(a1))
	}

	limited, err := s.List("", 1)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("expected limit=1 to cap at 1 event, got %d", len(limited))
	}
}
