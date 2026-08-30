package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"plumb/internal/maintenance"
	"plumb/internal/notify"
	"plumb/internal/report"
	"plumb/internal/rules"
)

// --- Findings history & acknowledgment ---

func (a *App) handleFindings(w http.ResponseWriter, r *http.Request) {
	if a.Findings == nil {
		writeJSON(w, []any{})
		return
	}
	writeJSON(w, a.Findings.List())
}

func (a *App) handleFindingsHistory(w http.ResponseWriter, r *http.Request) {
	if a.Findings == nil {
		writeJSON(w, []any{})
		return
	}
	limit := 100
	if s := r.URL.Query().Get("limit"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			limit = v
		}
	}
	entries, err := a.Findings.History(limit)
	if err != nil {
		httpError(w, 500, err)
		return
	}
	if entries == nil {
		writeJSON(w, []any{})
		return
	}
	writeJSON(w, entries)
}

func (a *App) handleAckFinding(w http.ResponseWriter, r *http.Request) {
	if a.Findings == nil {
		httpError(w, 400, fmt.Errorf("findings history is not enabled"))
		return
	}
	var payload struct {
		ArrayID  string `json:"array_id"`
		MetricID string `json:"metric_id"`
		Note     string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		httpError(w, 400, err)
		return
	}
	if payload.ArrayID == "" || payload.MetricID == "" {
		httpError(w, 400, fmt.Errorf("array_id and metric_id are required"))
		return
	}
	ok, err := a.Findings.Acknowledge(payload.ArrayID, payload.MetricID, payload.Note, time.Now())
	if err != nil {
		httpError(w, 500, err)
		return
	}
	if !ok {
		httpError(w, 404, fmt.Errorf("no open finding for %s/%s", payload.ArrayID, payload.MetricID))
		return
	}
	writeJSON(w, map[string]any{"acked": true})
}

// --- Maintenance windows ---

func (a *App) handleGetMaintenance(w http.ResponseWriter, r *http.Request) {
	windows, err := maintenance.Load(a.ConfigDir)
	if err != nil {
		httpError(w, 500, err)
		return
	}
	if windows == nil {
		windows = []maintenance.Window{}
	}
	writeJSON(w, windows)
}

func (a *App) handleSetMaintenance(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		ArrayID string  `json:"array_id"`
		Hours   float64 `json:"hours"`
		Note    string  `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		httpError(w, 400, err)
		return
	}
	if payload.ArrayID == "" {
		httpError(w, 400, fmt.Errorf("array_id is required (use \"*\" for the whole fleet)"))
		return
	}
	if payload.Hours <= 0 {
		httpError(w, 400, fmt.Errorf("hours must be positive"))
		return
	}
	now := time.Now()
	windows, err := maintenance.Set(a.ConfigDir, payload.ArrayID, now.Add(time.Duration(payload.Hours*float64(time.Hour))), payload.Note, now)
	if err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, windows)
}

func (a *App) handleClearMaintenance(w http.ResponseWriter, r *http.Request) {
	arrayID := r.PathValue("id")
	windows, err := maintenance.Clear(a.ConfigDir, arrayID, time.Now())
	if err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, windows)
}

// --- Webhook notifications ---

// handleNotifyTest sends a real webhook using the settings currently
// persisted (not whatever unsaved values might be sitting in the Config
// tab's form) — save first, then test, mirrors how every other Config tab
// setting already behaves.
func (a *App) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	cfg := a.notifyConfig()
	if cfg.WebhookURL == "" {
		httpError(w, 400, fmt.Errorf("no webhook URL saved yet — save one first"))
		return
	}
	err := notify.Send(a.notifyClient(), cfg, notify.Event{Kind: "test", Timestamp: time.Now()})
	if err != nil {
		httpError(w, 502, fmt.Errorf("webhook test failed: %w", err))
		return
	}
	writeJSON(w, map[string]any{"sent": true})
}

// --- Scheduled report history ---

type reportHistoryEntry struct {
	Name        string    `json:"name"`
	GeneratedAt time.Time `json:"generated_at"`
	SizeBytes   int64     `json:"size_bytes"`
}

func (a *App) handleReportHistory(w http.ResponseWriter, r *http.Request) {
	dir := filepath.Join(a.DataDir, "reports")
	entries, err := os.ReadDir(dir)
	if err != nil {
		writeJSON(w, []reportHistoryEntry{})
		return
	}
	out := []reportHistoryEntry{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, reportHistoryEntry{Name: e.Name(), GeneratedAt: info.ModTime(), SizeBytes: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GeneratedAt.After(out[j].GeneratedAt) })
	writeJSON(w, out)
}

// handleReportHistoryFile serves one archived report by name — restricted
// to the exact basenames handleReportHistory itself just listed, since
// r.PathValue comes straight from the URL and this must never let a path
// like "../../config/arrays.yml" escape data/reports/.
func (a *App) handleReportHistoryFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		httpError(w, 400, fmt.Errorf("invalid report name"))
		return
	}
	dir := filepath.Join(a.DataDir, "reports")
	full := filepath.Join(dir, name)
	if filepath.Dir(full) != filepath.Clean(dir) {
		httpError(w, 400, fmt.Errorf("invalid report name"))
		return
	}
	f, err := os.Open(full)
	if err != nil {
		httpError(w, 404, fmt.Errorf("report not found"))
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/html")
	io.Copy(w, f)
}

// --- Suggested thresholds (baseline) ---

func (a *App) handleSuggestedThresholds(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	window := parseHours(r, 24*30) // a month is a more meaningful baseline than a day

	arr, ok, err := a.activeArray(id)
	if err != nil {
		httpError(w, 500, err)
		return
	}
	if !ok {
		httpError(w, 404, fmt.Errorf("unknown array %q", id))
		return
	}
	metrics, err := a.thresholdsFor(arr.Vendor)
	if err != nil {
		httpError(w, 500, err)
		return
	}
	end := time.Now()
	start := end.Add(-window)
	step := window / 300
	if step < 15*time.Second {
		step = 15 * time.Second
	}
	var allStats []rules.Stats
	for _, m := range metrics {
		promql := strings.ReplaceAll(m.Query, "{array}", arr.ID)
		pts, err := a.VM.RangeQuery(promql, start, end, step)
		if err != nil {
			httpError(w, 502, err)
			return
		}
		allStats = append(allStats, rules.Summarize(m, pts))
	}
	rep := report.BuildBaselineReport(arr, allStats, start, end)
	w.Header().Set("Content-Type", "text/html")
	report.WriteBaselineReport(w, rep)
}

// --- PDF exports ---

func (a *App) handleArrayReportPDF(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	window := parseHours(r, 24*7)
	rep, err := a.buildArrayReport(id, window)
	if err != nil {
		httpError(w, 502, err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-report-%s.pdf"`, id, rep.GeneratedAt.Format("20060102-1504")))
	if err := report.WriteArrayReportPDF(w, rep); err != nil {
		httpError(w, 500, err)
	}
}

func (a *App) handleFleetReportPDF(w http.ResponseWriter, r *http.Request) {
	window := parseHours(r, 24*7)
	end := time.Now()
	start := end.Add(-window)

	arrays, err := a.activeArrays()
	if err != nil {
		httpError(w, 500, err)
		return
	}
	var summaries []report.FleetArraySummary
	for _, arr := range arrays {
		rep, err := a.buildArrayReport(arr.ID, window)
		if err != nil {
			continue
		}
		summaries = append(summaries, report.FleetArraySummary{Array: rep.Array, Health: rep.Health, IssueCount: rep.IssueCount, TrendPct: rep.TrendPct, TrendLabel: rep.TrendLabel})
	}
	fleetRep := report.BuildFleetReport(summaries, start, end)
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="plumb-fleet-report-%s.pdf"`, fleetRep.GeneratedAt.Format("20060102-1504")))
	if err := report.WriteFleetReportPDF(w, fleetRep); err != nil {
		httpError(w, 500, err)
	}
}
