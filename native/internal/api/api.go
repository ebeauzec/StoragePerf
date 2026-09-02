// Package api wires every HTTP-facing piece of Plumb together: the Pure
// scrape proxy, the fleet/array/config/export/report endpoints the frontend
// calls, and the embedded static frontend itself.
package api

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"plumb/internal/config"
	"plumb/internal/eventstore"
	"plumb/internal/export"
	"plumb/internal/findingstore"
	"plumb/internal/mockbackend"
	"plumb/internal/netappnative"
	"plumb/internal/report"
	"plumb/internal/rules"
	"plumb/internal/scrapeproxy"
	"plumb/internal/selfupdate"
	"plumb/internal/targets"
	"plumb/internal/updates"
	"plumb/internal/vm"
)

type App struct {
	Version     string // set via -ldflags at build time — see cmd/plumb/main.go
	Root        string // directory containing the running executable (paths.Layout.Root) — where a self-update installs its sibling directory
	ConfigDir   string
	DataDir     string // base data/ dir — used to size up the VictoriaMetrics storage subdir for the Config tab
	TargetsPath string
	SelfAddr    string
	VM          *vm.Client
	Updates     *updates.Checker
	Frontend    fs.FS
	RestartVM   func(retentionPeriod string) // notifies main to restart VictoriaMetrics with a new -retentionPeriod
	Shutdown    func()                       // triggers main's graceful shutdown (server + sidecars) — see handleSelfUpdate
	ONTAP       *netappnative.ONTAPCollector
	StorageGrid *netappnative.StorageGridCollector
	ESeries     *netappnative.ESeriesCollector
	MockBackend *mockbackend.Backend
	Findings    *findingstore.Store // nil disables findings history/webhooks entirely (e.g. a test harness)
	Events      *eventstore.Store   // nil disables the Events tab entirely (e.g. a test harness) — see internal/netappnative/ems.go

	settingsMu              sync.RWMutex
	mockData                bool
	retention               string
	notifyEnabled           bool
	notifyWebhookURL        string
	notifyMinSeverity       string
	scheduledReportsEnabled bool
	scheduledReportInterval string
	scheduledReportHours    float64
}

// LoadSettings reads the persisted mock-data toggle and retention period at
// startup.
func (a *App) LoadSettings() error {
	s, err := config.LoadSettings(a.ConfigDir)
	if err != nil {
		return err
	}
	a.settingsMu.Lock()
	a.mockData = s.MockData
	a.retention = s.EffectiveRetentionPeriod()
	a.notifyEnabled = s.NotifyEnabled
	a.notifyWebhookURL = s.NotifyWebhookURL
	a.notifyMinSeverity = s.NotifyMinSeverity
	a.scheduledReportsEnabled = s.ScheduledReportsEnabled
	a.scheduledReportInterval = s.ScheduledReportInterval
	a.scheduledReportHours = s.ScheduledReportHours
	a.settingsMu.Unlock()
	return nil
}

func (a *App) mockEnabled() bool {
	a.settingsMu.RLock()
	defer a.settingsMu.RUnlock()
	return a.mockData
}

func (a *App) retentionPeriod() string {
	a.settingsMu.RLock()
	defer a.settingsMu.RUnlock()
	if a.retention == "" {
		return config.DefaultRetentionPeriod
	}
	return a.retention
}

// saveSettings persists both settings together (they live in the same
// settings.yml). If the retention period changed, it tells main to restart
// VictoriaMetrics with the new -retentionPeriod (VictoriaMetrics only
// reads that flag at startup). If the mock-data toggle changed, it starts
// or stops the mock backend (see internal/mockbackend) and regenerates
// Prometheus's scrape targets to point at the mock fleet or real
// config/arrays.yml accordingly.
func (a *App) saveSettings(s config.Settings) error {
	if err := config.SaveSettings(a.ConfigDir, s); err != nil {
		return err
	}
	a.settingsMu.Lock()
	retentionChanged := s.RetentionPeriod != a.retention
	mockChanged := s.MockData != a.mockData
	a.mockData = s.MockData
	a.retention = s.RetentionPeriod
	a.notifyEnabled = s.NotifyEnabled
	a.notifyWebhookURL = s.NotifyWebhookURL
	a.notifyMinSeverity = s.NotifyMinSeverity
	a.scheduledReportsEnabled = s.ScheduledReportsEnabled
	a.scheduledReportInterval = s.ScheduledReportInterval
	a.scheduledReportHours = s.ScheduledReportHours
	a.settingsMu.Unlock()

	if retentionChanged && a.RestartVM != nil {
		a.RestartVM(s.RetentionPeriod)
	}
	if mockChanged && a.MockBackend != nil {
		if s.MockData {
			if err := a.MockBackend.Start(); err != nil {
				return err
			}
		} else {
			a.MockBackend.Stop()
		}
	}
	if mockChanged {
		return a.regenerate()
	}
	return nil
}

// activeArrays returns whichever array list Prometheus should currently be
// scraping: the mock fleet (pointed at internal/mockbackend's own
// endpoints) while mock mode is on, or the real config/arrays.yml
// otherwise. Every handler that lists or looks up "the current arrays" —
// the fleet view, array detail, exports, reports — goes through this, so
// mock and real arrays are handled by identical code from here on; only
// which arrays exist differs, never how their data is fetched.
func (a *App) activeArrays() ([]config.Array, error) {
	if a.mockEnabled() && a.MockBackend != nil {
		return a.MockBackend.Arrays(a.SelfAddr), nil
	}
	return config.LoadArrays(a.ConfigDir)
}

func (a *App) activeArray(id string) (config.Array, bool, error) {
	arrays, err := a.activeArrays()
	if err != nil {
		return config.Array{}, false, err
	}
	for _, arr := range arrays {
		if arr.ID == id {
			return arr, true, nil
		}
	}
	return config.Array{}, false, nil
}

// EnsureMockBackend starts the mock backend if settings.yml already had
// mock_data:true when this process booted — called once at startup, after
// LoadSettings, since collection needs to be wired up before the first
// scrape happens.
func (a *App) EnsureMockBackend() error {
	if a.mockEnabled() && a.MockBackend != nil {
		return a.MockBackend.Start()
	}
	return nil
}

func (a *App) regenerate() error {
	arrays, err := a.activeArrays()
	if err != nil {
		return err
	}
	if _, err := targets.Generate(a.TargetsPath, arrays, a.SelfAddr); err != nil {
		return err
	}
	return nil
}

// handleScrapeNetApp is Prometheus's actual scrape target for any NetApp
// array configured with credentials (ManagementLIF set) — see
// internal/targets. It collects directly from the array on every scrape
// (no separate poller process, no cached state file) and formats the
// result as a Prometheus exposition-format response, exactly like
// scrapeproxy.Handler does for Pure arrays.
func (a *App) handleScrapeNetApp(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	arr, ok, err := a.activeArray(id)
	if err != nil {
		httpError(w, 500, err)
		return
	}
	if !ok {
		httpError(w, 404, fmt.Errorf("unknown array %q", id))
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	switch arr.Vendor {
	case config.VendorNetAppONTAP:
		if err := a.ONTAP.WriteMetrics(w, arr); err != nil {
			httpError(w, 502, err)
		}
	case config.VendorNetAppStorageGRID:
		if err := a.StorageGrid.WriteMetrics(w, arr); err != nil {
			httpError(w, 502, err)
		}
	case config.VendorNetAppESeries:
		if err := a.ESeries.WriteMetrics(w, arr); err != nil {
			httpError(w, 502, err)
		}
	default:
		httpError(w, 400, fmt.Errorf("array %q is not a NetApp vendor", id))
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, status int, err error) {
	http.Error(w, err.Error(), status)
}

func (a *App) thresholdsFor(vendor string) ([]config.MetricDef, error) {
	return config.LoadThresholds(a.ConfigDir, vendor)
}

func parseHours(r *http.Request, def float64) time.Duration {
	h := def
	if s := r.URL.Query().Get("hours"); s != "" {
		if v, err := strconv.ParseFloat(s, 64); err == nil {
			h = v
		}
	}
	return time.Duration(h * float64(time.Hour))
}

// isLatencyPanel identifies the fleet card's headline latency figure —
// shared with the fleet report's trend column via report.IsLatencyMetric so
// both mean the same metric by "the" latency figure for a vendor.
func isLatencyPanel(id string) bool {
	return report.IsLatencyMetric(id)
}

// secondaryStat picks each vendor's own best available "at a glance" second
// stat for the fleet tile. This deliberately does NOT assume every vendor
// publishes a comparable ops/queue-depth metric — ONTAP and StorageGRID's
// thresholds files have no such metric (see config/thresholds/*.yml), so
// mapping their fleet tile to node CPU busy instead is a real, correctly
// labeled stat rather than a permanently blank "Queue" field from looking
// up a metric ID (volume_total_ops / s3_operations) that was never defined.
func secondaryStat(id string) (ok bool, label, unit string) {
	switch id {
	case "host_queue_depth", "bucket_throughput":
		return true, "Queue", ""
	case "node_cpu_busy", "node_cpu":
		return true, "CPU", "%"
	case "eseries_capacity_used_percent":
		return true, "Cap", "%"
	}
	return false, "", ""
}

type fleetEntry struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Model          string         `json:"model"`
	Vendor         string         `json:"vendor"`
	Health         rules.Severity `json:"health"`
	Secondary      *float64       `json:"secondary_value"`
	SecondaryLabel string         `json:"secondary_label"`
	SecondaryUnit  string         `json:"secondary_unit"`
	Latency        *float64       `json:"latency"`
	Sparkline      [][2]float64   `json:"sparkline"`
}

func toFleetEntry(id, name, model, vendor string, res rules.Result) fleetEntry {
	// Sparkline defaults non-nil ([]T{} rather than the zero-value nil) so a
	// vendor whose panels don't match isLatencyPanel still serializes as
	// JSON "[]", not "null" — the same class of bug as rules.Panel.Series.
	entry := fleetEntry{ID: id, Name: name, Model: model, Vendor: vendor, Health: res.Health, Sparkline: [][2]float64{}}
	for _, p := range res.Panels {
		if isLatencyPanel(p.ID) {
			entry.Latency = p.Value
			entry.Sparkline = p.Series
		}
		if ok, label, unit := secondaryStat(p.ID); ok {
			entry.Secondary = p.Value
			entry.SecondaryLabel = label
			entry.SecondaryUnit = unit
		}
	}
	return entry
}

func (a *App) handleFleet(w http.ResponseWriter, r *http.Request) {
	out := []fleetEntry{}

	arrays, err := a.activeArrays()
	if err != nil {
		httpError(w, 500, err)
		return
	}
	for _, arr := range arrays {
		metrics, err := a.thresholdsFor(arr.Vendor)
		if err != nil {
			out = append(out, fleetEntry{ID: arr.ID, Name: arr.Name, Model: arr.Model, Vendor: arr.Vendor, Health: rules.Unknown, Sparkline: [][2]float64{}})
			continue
		}
		res, err := rules.EvaluateArray(a.VM, arr, metrics, time.Hour)
		if err != nil {
			out = append(out, fleetEntry{ID: arr.ID, Name: arr.Name, Model: arr.Model, Vendor: arr.Vendor, Health: rules.Unknown, Sparkline: [][2]float64{}})
			continue
		}
		out = append(out, toFleetEntry(arr.ID, arr.Name, arr.Model, arr.Vendor, res))
	}
	writeJSON(w, out)
}

func (a *App) handleArrayDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	window := parseHours(r, 24)

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
	res, err := rules.EvaluateArray(a.VM, arr, metrics, window)
	if err != nil {
		httpError(w, 502, err)
		return
	}
	writeJSON(w, res)
}

func (a *App) handleGetArraysConfig(w http.ResponseWriter, r *http.Request) {
	arrays, err := config.LoadArrays(a.ConfigDir)
	if err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"arrays": arrays})
}

func (a *App) handlePutArraysConfig(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Arrays []config.Array `json:"arrays"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		httpError(w, 400, err)
		return
	}
	if err := config.SaveArrays(a.ConfigDir, payload.Arrays); err != nil {
		httpError(w, 500, err)
		return
	}
	if err := a.regenerate(); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"saved": len(payload.Arrays)})
}

// dirSize sums file sizes under path. Errors on individual entries are
// swallowed rather than aborting the walk — VictoriaMetrics's background
// merge/compaction can remove or replace part files while this runs, and a
// disk-usage figure that's a moment stale beats a Config tab that fails to
// load because of it.
func dirSize(path string) int64 {
	var size int64
	filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			size += info.Size()
		}
		return nil
	})
	return size
}

func (a *App) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	options := make([]map[string]string, len(config.RetentionOptions))
	for i, o := range config.RetentionOptions {
		options[i] = map[string]string{"value": o.Value, "label": o.Label}
	}
	scheduleOptions := make([]map[string]string, len(config.ScheduleOptions))
	for i, o := range config.ScheduleOptions {
		scheduleOptions[i] = map[string]string{"value": o.Value, "label": o.Label}
	}
	a.settingsMu.RLock()
	notifyEnabled, webhookURL, minSev := a.notifyEnabled, a.notifyWebhookURL, a.notifyMinSeverity
	schedEnabled, schedInterval := a.scheduledReportsEnabled, a.scheduledReportInterval
	a.settingsMu.RUnlock()
	if minSev == "" {
		minSev = "critical"
	}
	if schedInterval == "" {
		schedInterval = "daily"
	}
	writeJSON(w, map[string]any{
		"mock_data":                 a.mockEnabled(),
		"retention_period":          a.retentionPeriod(),
		"retention_options":         options,
		"db_size_bytes":             dirSize(filepath.Join(a.DataDir, "victoriametrics")),
		"notify_enabled":            notifyEnabled,
		"notify_webhook_url":        webhookURL,
		"notify_min_severity":       minSev,
		"scheduled_reports_enabled": schedEnabled,
		"scheduled_report_interval": schedInterval,
		"schedule_options":          scheduleOptions,
	})
}

func (a *App) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		MockData                bool   `json:"mock_data"`
		RetentionPeriod         string `json:"retention_period"`
		NotifyEnabled           bool   `json:"notify_enabled"`
		NotifyWebhookURL        string `json:"notify_webhook_url"`
		NotifyMinSeverity       string `json:"notify_min_severity"`
		ScheduledReportsEnabled bool   `json:"scheduled_reports_enabled"`
		ScheduledReportInterval string `json:"scheduled_report_interval"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		httpError(w, 400, err)
		return
	}
	if !config.ValidRetentionPeriod(payload.RetentionPeriod) {
		httpError(w, 400, fmt.Errorf("invalid retention_period %q", payload.RetentionPeriod))
		return
	}
	if payload.NotifyMinSeverity == "" {
		payload.NotifyMinSeverity = "critical"
	}
	if !config.ValidNotifySeverity(payload.NotifyMinSeverity) {
		httpError(w, 400, fmt.Errorf("invalid notify_min_severity %q", payload.NotifyMinSeverity))
		return
	}
	if payload.NotifyEnabled && payload.NotifyWebhookURL == "" {
		httpError(w, 400, fmt.Errorf("notify_webhook_url is required to enable notifications"))
		return
	}
	if payload.ScheduledReportInterval == "" {
		payload.ScheduledReportInterval = "daily"
	}
	if !config.ValidScheduleInterval(payload.ScheduledReportInterval) {
		httpError(w, 400, fmt.Errorf("invalid scheduled_report_interval %q", payload.ScheduledReportInterval))
		return
	}
	s := config.Settings{
		MockData: payload.MockData, RetentionPeriod: payload.RetentionPeriod,
		NotifyEnabled: payload.NotifyEnabled, NotifyWebhookURL: payload.NotifyWebhookURL, NotifyMinSeverity: payload.NotifyMinSeverity,
		ScheduledReportsEnabled: payload.ScheduledReportsEnabled, ScheduledReportInterval: payload.ScheduledReportInterval,
		ScheduledReportHours: config.ScheduleReportHours(payload.ScheduledReportInterval),
	}
	if err := a.saveSettings(s); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"saved": true})
}

func (a *App) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"version": a.Version})
}

func (a *App) handleUpdates(w http.ResponseWriter, r *http.Request) {
	enabled, checks := a.Updates.Snapshot()
	writeJSON(w, map[string]any{"enabled": enabled, "checks": checks})
}

// handleSelfUpdate is Plumb's one exception to "check-and-notify only" —
// see internal/selfupdate's doc comment for the full safety story. This
// handler only ever runs in response to an explicit click; nothing here
// fires on a timer or without a request.
func (a *App) handleSelfUpdate(w http.ResponseWriter, r *http.Request) {
	if a.Version == "dev" {
		httpError(w, 400, fmt.Errorf("self-update isn't meaningful for a dev build (no matching release to compare against)"))
		return
	}
	plumbCheck, enabled := a.Updates.PlumbCheck()
	if !enabled {
		httpError(w, 400, fmt.Errorf("update checking is disabled (PLUMB_CHECK_FOR_UPDATES=false)"))
		return
	}
	if plumbCheck.UpdateAvailable == nil || !*plumbCheck.UpdateAvailable {
		httpError(w, 400, fmt.Errorf("no update available — last checked version is %s", a.Version))
		return
	}
	if a.Shutdown == nil {
		httpError(w, 500, fmt.Errorf("self-update not wired up"))
		return
	}

	// A real download (tens of MB) needs a much longer timeout than the
	// 10s used for the lightweight version-check requests elsewhere —
	// deliberately a fresh client, not a.Updates' own.
	longClient := &http.Client{Timeout: 5 * time.Minute}

	rel, err := selfupdate.FetchLatest(longClient)
	if err != nil {
		httpError(w, 502, fmt.Errorf("checking latest release: %w", err))
		return
	}
	newRoot, err := selfupdate.Apply(longClient, a.Root, rel)
	if err != nil {
		httpError(w, 502, fmt.Errorf("applying update: %w", err))
		return
	}
	if err := selfupdate.StartAndHandoff(newRoot); err != nil {
		httpError(w, 502, fmt.Errorf("starting new version: %w", err))
		return
	}

	writeJSON(w, map[string]any{"status": "restarting", "new_version": rel.Tag})

	// The new process is already starting and will retry-bind this port
	// until it succeeds (see main.go) — give this response time to reach
	// the client before tearing this process down.
	go func() {
		time.Sleep(500 * time.Millisecond)
		a.Shutdown()
	}()
}

func (a *App) handleExport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	window := parseHours(r, 24)
	end := time.Now()
	start := end.Add(-window)
	step := window / 500
	if step < 15*time.Second {
		step = 15 * time.Second
	}

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

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-metrics-%s.csv"`, id, end.Format("20060102-1504")))
	if err := export.CSV(w, a.VM, arr, metrics, start, end, step); err != nil {
		// headers are already sent at this point in a partial-write failure;
		// nothing more we can do but stop.
		return
	}
}

// handleBackup streams a full, unaggregated JSON-lines export of everything
// VictoriaMetrics has stored — not just the curated per-metric CSV export
// above. This is a real backup: every raw sample, for every metric, for
// every array, restorable into any Prometheus-compatible TSDB via its
// /api/v1/import endpoint (VictoriaMetrics's own "logical backup" mechanism,
// not something Plumb invents). Defaults to all recorded history; pass
// ?start=<unix>&end=<unix> for a bounded export instead, and ?match=<promql
// selector> to scope it to specific metrics rather than everything.
//
// This reads from the real VictoriaMetrics instance regardless of the
// Config tab's mock-data toggle — while mock mode is on, its synthetic
// systems are collected through the real pipeline (see
// internal/mockbackend) just like real arrays, so their data genuinely
// lives here too and this exports it the same way.
func (a *App) handleBackup(w http.ResponseWriter, r *http.Request) {
	match := r.URL.Query().Get("match")
	if match == "" {
		match = `{__name__!=""}` // "every time series" — the standard PromQL idiom for it
	}
	start := time.Unix(0, 0)
	end := time.Now()
	if s := r.URL.Query().Get("start"); s != "" {
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			start = time.Unix(v, 0)
		}
	}
	if e := r.URL.Query().Get("end"); e != "" {
		if v, err := strconv.ParseInt(e, 10, 64); err == nil {
			end = time.Unix(v, 0)
		}
	}

	body, err := a.VM.ExportRaw(match, start, end)
	if err != nil {
		httpError(w, 502, fmt.Errorf("backup export failed: %w", err))
		return
	}
	defer body.Close()

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="plumb-backup-%s.jsonl"`, end.Format("20060102-1504")))
	io.Copy(w, body)
}

func (a *App) buildArrayReport(id string, window time.Duration) (report.ArrayReport, error) {
	end := time.Now()
	start := end.Add(-window)
	step := window / 300
	if step < 15*time.Second {
		step = 15 * time.Second
	}

	arr, ok, err := a.activeArray(id)
	if err != nil {
		return report.ArrayReport{}, err
	}
	if !ok {
		return report.ArrayReport{}, fmt.Errorf("unknown array %q", id)
	}
	metrics, err := a.thresholdsFor(arr.Vendor)
	if err != nil {
		return report.ArrayReport{}, err
	}
	var allStats []rules.Stats
	for _, m := range metrics {
		promql := strings.ReplaceAll(m.Query, "{array}", arr.ID)
		pts, err := a.VM.RangeQuery(promql, start, end, step)
		if err != nil {
			return report.ArrayReport{}, err
		}
		allStats = append(allStats, rules.Summarize(m, pts))
	}
	expectedSamples := int(window / step)
	return report.BuildArrayReport(arr, allStats, start, end, expectedSamples), nil
}

func (a *App) handleArrayReport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	window := parseHours(r, 24*7)
	rep, err := a.buildArrayReport(id, window)
	if err != nil {
		httpError(w, 502, err)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	report.WriteArrayReport(w, rep)
}

func (a *App) handleFleetReport(w http.ResponseWriter, r *http.Request) {
	window := parseHours(r, 24*7)
	end := time.Now()
	start := end.Add(-window)

	arrays, err := a.activeArrays()
	if err != nil {
		httpError(w, 500, err)
		return
	}
	ids := make([]string, len(arrays))
	for i, arr := range arrays {
		ids[i] = arr.ID
	}

	var summaries []report.FleetArraySummary
	for _, id := range ids {
		rep, err := a.buildArrayReport(id, window)
		if err != nil {
			continue
		}
		summaries = append(summaries, report.FleetArraySummary{Array: rep.Array, Health: rep.Health, IssueCount: rep.IssueCount, TrendPct: rep.TrendPct, TrendLabel: rep.TrendLabel})
	}
	fleetRep := report.BuildFleetReport(summaries, start, end)
	w.Header().Set("Content-Type", "text/html")
	report.WriteFleetReport(w, fleetRep)
}

func (a *App) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /scrape/{id}", scrapeproxy.Handler(a.activeArray))
	mux.HandleFunc("GET /scrape/netapp/{id}", a.handleScrapeNetApp)
	mux.HandleFunc("GET /api/fleet", a.handleFleet)
	mux.HandleFunc("GET /api/arrays/{id}", a.handleArrayDetail)
	mux.HandleFunc("GET /api/config/arrays", a.handleGetArraysConfig)
	mux.HandleFunc("PUT /api/config/arrays", a.handlePutArraysConfig)
	mux.HandleFunc("GET /api/config/settings", a.handleGetSettings)
	mux.HandleFunc("PUT /api/config/settings", a.handlePutSettings)
	mux.HandleFunc("GET /api/version", a.handleVersion)
	mux.HandleFunc("GET /api/updates", a.handleUpdates)
	mux.HandleFunc("POST /api/self-update", a.handleSelfUpdate)
	mux.HandleFunc("GET /api/export/{id}", a.handleExport)
	mux.HandleFunc("GET /api/backup", a.handleBackup)
	mux.HandleFunc("GET /api/reports/array/{id}", a.handleArrayReport)
	mux.HandleFunc("GET /api/reports/fleet", a.handleFleetReport)
	mux.HandleFunc("GET /api/reports/array/{id}/pdf", a.handleArrayReportPDF)
	mux.HandleFunc("GET /api/reports/fleet/pdf", a.handleFleetReportPDF)
	mux.HandleFunc("GET /api/reports/array/{id}/suggested-thresholds", a.handleSuggestedThresholds)
	mux.HandleFunc("GET /api/arrays/{id}/discover", a.handleDiscoverMetrics)
	mux.HandleFunc("GET /api/reports/history", a.handleReportHistory)
	mux.HandleFunc("GET /api/reports/history/{name}", a.handleReportHistoryFile)
	mux.HandleFunc("GET /api/findings", a.handleFindings)
	mux.HandleFunc("GET /api/findings/history", a.handleFindingsHistory)
	mux.HandleFunc("GET /api/events", a.handleEvents)
	mux.HandleFunc("POST /api/findings/ack", a.handleAckFinding)
	mux.HandleFunc("GET /api/maintenance", a.handleGetMaintenance)
	mux.HandleFunc("POST /api/maintenance", a.handleSetMaintenance)
	mux.HandleFunc("DELETE /api/maintenance/{id}", a.handleClearMaintenance)
	mux.HandleFunc("POST /api/notify/test", a.handleNotifyTest)
	mockbackend.RegisterPureRoutes(mux) // inert unless targets.go points at them (mock mode on) — see internal/mockbackend
	mux.Handle("/", http.FileServerFS(a.Frontend))
	return mux
}

// InitialRegenerate runs the same regeneration the config-save endpoint
// triggers, used once at startup so targets/harvest config exist before
// Prometheus/Harvest start scraping.
func (a *App) InitialRegenerate() error {
	return a.regenerate()
}
