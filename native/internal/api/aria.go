package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"plumb/internal/ariaexport"
	"plumb/internal/config"
	"plumb/internal/report"
	"plumb/internal/rules"
)

// ARIA integration — see internal/ariaexport for what the snapshot is and why.
// Two delivery paths share one builder: GET /api/aria/export (an MSP's ARIA
// pulls it directly) and the same JSON as a file (downloaded by hand, or
// written on a schedule to data/aria-exports/) for import where Plumb isn't
// reachable from the MSP.

const (
	ariaDefaultHours = 24 * 7
	ariaMaxHours     = 24 * 90
	ariaMaxEvents    = 200
	ariaSparkPoints  = 48
	maxAriaExports   = 60

	ariaIdentityTTL   = time.Hour
	ariaIdentityRetry = 10 * time.Minute
)

type ariaCachedIdentity struct {
	id *ariaexport.Identity // nil when the last attempt failed
	at time.Time
}

func (a *App) ariaConfig() (token string, scheduled bool, site string) {
	a.settingsMu.RLock()
	defer a.settingsMu.RUnlock()
	return a.ariaExportToken, a.ariaExportEnabled, a.ariaSiteLabel
}

func (a *App) ariaExportsDir() string { return filepath.Join(a.DataDir, "aria-exports") }

// ariaAuthorized writes the 401 itself and reports whether to continue.
func (a *App) ariaAuthorized(w http.ResponseWriter, r *http.Request) bool {
	token, _, _ := a.ariaConfig()
	if ariaexport.Authorized(r, token) {
		return true
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="plumb-aria"`)
	http.Error(w, "a valid ARIA export token is required (Authorization: Bearer <token>, X-Plumb-Token, or ?token=)", http.StatusUnauthorized)
	return false
}

// ariaIdentityFor returns an ONTAP cluster's name/UUID/serials, cached: the
// export is pulled on a timer and identity essentially never changes, so it
// isn't worth two extra REST calls to the array on every pull. A failure is
// cached briefly too, so an unreachable array doesn't add a 10-second timeout
// to every export.
func (a *App) ariaIdentityFor(arr config.Array) *ariaexport.Identity {
	if arr.Vendor != config.VendorNetAppONTAP || a.ONTAP == nil {
		return nil
	}
	a.ariaMu.Lock()
	if a.ariaIdentity == nil {
		a.ariaIdentity = map[string]ariaCachedIdentity{}
	}
	c, ok := a.ariaIdentity[arr.ID]
	a.ariaMu.Unlock()
	if ok {
		ttl := ariaIdentityTTL
		if c.id == nil {
			ttl = ariaIdentityRetry
		}
		if time.Since(c.at) < ttl {
			return c.id
		}
	}

	var out *ariaexport.Identity
	if id, err := a.ONTAP.Identity(arr); err != nil {
		log.Printf("[aria] identity for %s unavailable (matching falls back to name): %v", arr.ID, err)
	} else {
		out = &ariaexport.Identity{ClusterName: id.ClusterName, ClusterUUID: id.ClusterUUID, Version: id.Version}
		for _, n := range id.Nodes {
			out.Nodes = append(out.Nodes, ariaexport.NodeIdentity{Name: n.Name, Serial: n.Serial, Model: n.Model})
		}
	}
	a.ariaMu.Lock()
	a.ariaIdentity[arr.ID] = ariaCachedIdentity{id: out, at: time.Now()}
	a.ariaMu.Unlock()
	return out
}

// buildAriaArray evaluates one array over [start,end]. It mirrors
// buildArrayReport (same queries, same narration) but also keeps a thinned
// series per metric and the numeric capacity projection, which the HTML
// report has no use for.
func (a *App) buildAriaArray(arr config.Array, start, end time.Time, window time.Duration) (ae ariaexport.ArrayExport, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[aria] PANIC exporting %s (recovered): %v\n%s", arr.ID, r, debug.Stack())
			err = fmt.Errorf("internal error while evaluating %s", arr.ID)
		}
	}()

	ae = ariaexport.ArrayExport{ID: arr.ID, Name: arr.Name, Model: arr.Model, Vendor: arr.Vendor, Datacenter: arr.Datacenter, Metrics: []ariaexport.MetricExport{}}

	metrics, err := a.thresholdsFor(arr)
	if err != nil {
		return ae, err
	}
	step := window / 300
	if step < 15*time.Second {
		step = 15 * time.Second
	}

	var allStats []rules.Stats
	spark := map[string][][2]float64{}
	for _, m := range metrics {
		pts, qerr := a.VM.RangeQuery(strings.ReplaceAll(m.Query, "{array}", arr.ID), start, end, step)
		if qerr != nil {
			return ae, qerr
		}
		allStats = append(allStats, rules.Summarize(m, pts))
		series := make([][2]float64, len(pts))
		for i, p := range pts {
			series[i] = [2]float64{p.Time, p.Value}
		}
		spark[m.ID] = ariaexport.Downsample(series, ariaSparkPoints)
	}
	rep := report.BuildArrayReport(arr, allStats, start, end, int(window/step))
	ae.Health, ae.IssueCount, ae.CoverageNote = rep.Health, rep.IssueCount, rep.CoverageNote

	stats := map[string]rules.Stats{}
	for _, s := range allStats {
		stats[s.MetricID] = s
	}
	for _, n := range append(append([]report.MetricNarrative{}, rep.Frontend...), rep.Backend...) {
		s := n.Stats
		ae.Metrics = append(ae.Metrics, ariaexport.MetricExport{
			ID: s.MetricID, Label: s.Label, Unit: s.Unit, Category: s.Category, Severity: n.Severity, Samples: s.SampleCount,
			Min: s.Min, Avg: s.Avg, Max: s.Max, P90: s.P90, P95: s.P95, P99: s.P99,
			WatchPct: s.WatchPct, CriticalPct: s.CriticalPct, Episodes: s.Episodes, TrendPct: s.TrendPct,
			ThresholdLabel: s.ThresholdLabel, Watch: s.SeverityWatch, Critical: s.SeverityCritical,
			Analysis: n.Analysis, Sparkline: spark[s.MetricID],
		})
		if report.IsLatencyMetric(s.MetricID) && s.SampleCount > 0 && ae.Latency == nil {
			ae.Latency = &ariaexport.Latency{MetricID: s.MetricID, Label: s.Label, Unit: s.Unit, Avg: s.Avg, P95: s.P95, Max: s.Max, TrendPct: s.TrendPct}
		}
		if days, ok := report.ProjectDaysToCritical(s); ok {
			ae.Capacity = append(ae.Capacity, ariaexport.CapacityProjection{
				MetricID: s.MetricID, Label: s.Label, Unit: s.Unit, Current: s.LastQuarterAvg, Critical: s.SeverityCritical, DaysToCritical: days})
		}
	}

	// Findings are evaluated over the same period so they describe what the
	// metrics above describe, not just the last hour.
	if res, ferr := rules.EvaluateArray(a.VM, arr, metrics, window); ferr == nil {
		for _, f := range res.Findings {
			ae.Findings = append(ae.Findings, ariaexport.FindingExport{
				Severity: string(f.Severity), Tag: f.Tag, Title: f.Title, Body: f.Body, MetricID: f.MetricID,
				Investigate: f.Investigate, Remediate: f.Remediate})
			if f.MetricID == "" {
				ae.UpstreamSuspected = true
			}
		}
	}
	ae.Identity = a.ariaIdentityFor(arr)
	return ae, nil
}

func (a *App) buildAriaExport(window time.Duration) (ariaexport.Export, error) {
	end := time.Now()
	start := end.Add(-window)
	arrays, err := a.activeArrays()
	if err != nil {
		return ariaexport.Export{}, err
	}
	_, _, site := a.ariaConfig()
	exp := ariaexport.Export{
		Schema: ariaexport.Schema, GeneratedAt: end.UTC(), PlumbVersion: a.Version, Site: site,
		PeriodHours: window.Hours(), PeriodStart: start.UTC(), PeriodEnd: end.UTC(),
		MockData: a.mockEnabled(), Arrays: []ariaexport.ArrayExport{},
	}
	for _, arr := range arrays {
		ae, aerr := a.buildAriaArray(arr, start, end, window)
		if aerr != nil {
			// One unreachable/odd system must not withhold everyone else's data.
			ae.Health = "unknown"
			ae.CoverageNote = "Plumb could not evaluate this system for the export: " + aerr.Error()
		}
		exp.Arrays = append(exp.Arrays, ae)
	}
	if a.Events != nil {
		if evs, eerr := a.Events.List("", ariaMaxEvents); eerr == nil {
			for _, e := range evs {
				if t, perr := time.Parse(time.RFC3339, e.Time); perr == nil && t.Before(start) {
					continue
				}
				exp.Events = append(exp.Events, ariaexport.EventExport{ArrayID: e.ArrayID, ArrayName: e.ArrayName, Time: e.Time,
					Severity: e.Severity, Name: e.Name, Node: e.Node, Message: e.Message})
			}
		}
	}
	return exp, nil
}

func ariaWindow(r *http.Request) time.Duration {
	d := parseHours(r, ariaDefaultHours)
	if d < time.Hour {
		d = time.Hour
	}
	if d > ariaMaxHours*time.Hour {
		d = ariaMaxHours * time.Hour
	}
	return d
}

func (a *App) handleAriaInfo(w http.ResponseWriter, r *http.Request) {
	if !a.ariaAuthorized(w, r) {
		return
	}
	arrays, err := a.activeArrays()
	if err != nil {
		httpError(w, 500, err)
		return
	}
	token, scheduled, site := a.ariaConfig()
	writeJSON(w, map[string]any{
		"schema": ariaexport.Schema, "plumb_version": a.Version, "site": site, "array_count": len(arrays),
		"mock_data": a.mockEnabled(), "token_required": token != "", "scheduled_export": scheduled,
	})
}

func (a *App) handleAriaExport(w http.ResponseWriter, r *http.Request) {
	if !a.ariaAuthorized(w, r) {
		return
	}
	exp, err := a.buildAriaExport(ariaWindow(r))
	if err != nil {
		httpError(w, 502, err)
		return
	}
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="plumb-aria-%s.json"`, exp.GeneratedAt.Format("20060102-1504")))
	}
	writeJSON(w, exp)
}

type ariaExportFile struct {
	Name        string    `json:"name"`
	GeneratedAt time.Time `json:"generated_at"`
	SizeBytes   int64     `json:"size_bytes"`
}

func (a *App) handleAriaExports(w http.ResponseWriter, r *http.Request) {
	if !a.ariaAuthorized(w, r) {
		return
	}
	out := []ariaExportFile{}
	if entries, err := os.ReadDir(a.ariaExportsDir()); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			if info, ierr := e.Info(); ierr == nil {
				out = append(out, ariaExportFile{Name: e.Name(), GeneratedAt: info.ModTime(), SizeBytes: info.Size()})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GeneratedAt.After(out[j].GeneratedAt) })
	writeJSON(w, out)
}

// handleAriaExportFile serves one archived export by basename only — the name
// comes straight from the URL, so it must never be able to escape the folder.
func (a *App) handleAriaExportFile(w http.ResponseWriter, r *http.Request) {
	if !a.ariaAuthorized(w, r) {
		return
	}
	name := r.PathValue("name")
	if name == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") || !strings.HasSuffix(name, ".json") {
		httpError(w, 400, fmt.Errorf("invalid export name"))
		return
	}
	b, err := os.ReadFile(filepath.Join(a.ariaExportsDir(), name))
	if err != nil {
		httpError(w, 404, fmt.Errorf("export not found"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	w.Write(b)
}

// maybeRunScheduledAriaExport writes the snapshot to data/aria-exports/ on the
// same daily/weekly cadence as scheduled reports, when the ARIA file export is
// switched on. "Due" is read from the newest file's modification time, like
// maybeRunScheduledReport, so a restart never causes a duplicate.
func (a *App) maybeRunScheduledAriaExport() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[aria] PANIC in scheduled export (recovered): %v\n%s", r, debug.Stack())
		}
	}()
	_, enabled, _ := a.ariaConfig()
	if !enabled {
		return
	}
	_, interval, hours := a.scheduleConfig()
	if !config.ValidScheduleInterval(interval) {
		interval = "daily"
	}
	if hours <= 0 {
		hours = config.ScheduleReportHours(interval)
	}
	dir := a.ariaExportsDir()
	if last := latestScheduledReportTime(dir); !last.IsZero() && time.Since(last) < config.ScheduleIntervalDuration(interval) {
		return
	}
	exp, err := a.buildAriaExport(time.Duration(hours * float64(time.Hour)))
	if err != nil {
		log.Printf("[aria] scheduled export: %v", err)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[aria] scheduled export: %v", err)
		return
	}
	b, err := json.MarshalIndent(exp, "", "  ")
	if err != nil {
		log.Printf("[aria] scheduled export: %v", err)
		return
	}
	name := "aria-" + exp.GeneratedAt.Format("20060102-150405") + ".json"
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		log.Printf("[aria] scheduled export: %v", err)
		return
	}
	log.Printf("[aria] scheduled export written: %s", name)
	pruneScheduledReports(dir, maxAriaExports)
}
