// Package api wires every HTTP-facing piece of Plumb together: the Pure
// scrape proxy, the fleet/array/config/export/report endpoints the frontend
// calls, and the embedded static frontend itself.
package api

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"plumb/internal/config"
	"plumb/internal/export"
	"plumb/internal/harvest"
	"plumb/internal/mockdata"
	"plumb/internal/report"
	"plumb/internal/rules"
	"plumb/internal/scrapeproxy"
	"plumb/internal/targets"
	"plumb/internal/updates"
	"plumb/internal/vm"
)

type App struct {
	Version                  string // set via -ldflags at build time — see cmd/plumb/main.go
	ConfigDir                string
	TargetsPath              string
	HarvestPath              string
	SelfAddr                 string
	VM                       *vm.Client
	Updates                  *updates.Checker
	Frontend                 fs.FS
	RegenerateHarvestPollers func([]harvest.Poller) // notifies main to restart Harvest pollers after config changes

	mockMu   sync.RWMutex
	mockData bool
}

// LoadSettings reads the persisted mock-data toggle at startup.
func (a *App) LoadSettings() error {
	s, err := config.LoadSettings(a.ConfigDir)
	if err != nil {
		return err
	}
	a.mockMu.Lock()
	a.mockData = s.MockData
	a.mockMu.Unlock()
	return nil
}

func (a *App) mockEnabled() bool {
	a.mockMu.RLock()
	defer a.mockMu.RUnlock()
	return a.mockData
}

func (a *App) setMockEnabled(v bool) error {
	if err := config.SaveSettings(a.ConfigDir, config.Settings{MockData: v}); err != nil {
		return err
	}
	a.mockMu.Lock()
	a.mockData = v
	a.mockMu.Unlock()
	return nil
}

func (a *App) regenerate() error {
	arrays, err := config.LoadArrays(a.ConfigDir)
	if err != nil {
		return err
	}
	pollers, err := harvest.Generate(a.HarvestPath, arrays)
	if err != nil {
		return err
	}
	if _, err := targets.Generate(a.TargetsPath, arrays, pollers, a.SelfAddr); err != nil {
		return err
	}
	if a.RegenerateHarvestPollers != nil {
		a.RegenerateHarvestPollers(pollers)
	}
	return nil
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

// Different vendors name their headline latency/queue metric differently
// (host_latency vs volume_avg_latency, host_queue_depth vs volume_total_ops)
// — the fleet card just wants "the" latency/queue figure, so it looks for
// the category+role rather than a hardcoded cross-vendor ID.
func isLatencyPanel(id string) bool {
	return id == "host_latency" || id == "volume_avg_latency" || id == "metadata_query_latency" || id == "bucket_latency"
}
func isQueuePanel(id string) bool {
	return id == "host_queue_depth" || id == "volume_total_ops" || id == "s3_operations" || id == "bucket_throughput"
}

type fleetEntry struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Model      string         `json:"model"`
	Vendor     string         `json:"vendor"`
	Health     rules.Severity `json:"health"`
	QueueDepth *float64       `json:"queue_depth"`
	Latency    *float64       `json:"latency"`
	Sparkline  [][2]float64   `json:"sparkline"`
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
		if isQueuePanel(p.ID) {
			entry.QueueDepth = p.Value
		}
	}
	return entry
}

func (a *App) handleFleet(w http.ResponseWriter, r *http.Request) {
	out := []fleetEntry{}

	if a.mockEnabled() {
		for _, arr := range mockdata.Fleet {
			metrics, err := a.thresholdsFor(arr.Vendor)
			if err != nil {
				out = append(out, fleetEntry{ID: arr.ID, Name: arr.Name, Model: arr.Model, Vendor: arr.Vendor, Health: rules.Unknown, Sparkline: [][2]float64{}})
				continue
			}
			res := mockdata.EvaluateArray(arr, metrics, time.Hour)
			out = append(out, toFleetEntry(arr.ID, arr.Name, arr.Model, arr.Vendor, res))
		}
		writeJSON(w, out)
		return
	}

	arrays, err := config.LoadArrays(a.ConfigDir)
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

	if a.mockEnabled() {
		arr, ok := mockdata.GetArray(id)
		if !ok {
			httpError(w, 404, fmt.Errorf("unknown mock array %q", id))
			return
		}
		metrics, err := a.thresholdsFor(arr.Vendor)
		if err != nil {
			httpError(w, 500, err)
			return
		}
		writeJSON(w, mockdata.EvaluateArray(arr, metrics, window))
		return
	}

	arr, ok, err := config.GetArray(a.ConfigDir, id)
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

func (a *App) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"mock_data": a.mockEnabled()})
}

func (a *App) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		MockData bool `json:"mock_data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		httpError(w, 400, err)
		return
	}
	if err := a.setMockEnabled(payload.MockData); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"mock_data": payload.MockData})
}

func (a *App) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"version": a.Version})
}

func (a *App) handleUpdates(w http.ResponseWriter, r *http.Request) {
	enabled, checks := a.Updates.Snapshot()
	writeJSON(w, map[string]any{"enabled": enabled, "checks": checks})
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

	if a.mockEnabled() {
		arr, ok := mockdata.GetArray(id)
		if !ok {
			httpError(w, 404, fmt.Errorf("unknown mock array %q", id))
			return
		}
		metrics, err := a.thresholdsFor(arr.Vendor)
		if err != nil {
			httpError(w, 500, err)
			return
		}
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-mock-metrics-%s.csv"`, id, end.Format("20060102-1504")))
		writeMockCSV(w, arr, metrics, start, end, step)
		return
	}

	arr, ok, err := config.GetArray(a.ConfigDir, id)
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

func writeMockCSV(w http.ResponseWriter, arr mockdata.Array, metrics []config.MetricDef, start, end time.Time, step time.Duration) {
	cw := csv.NewWriter(w)
	defer cw.Flush()
	cw.Write([]string{"array_id", "metric_id", "metric_label", "category", "unit", "timestamp_unix", "timestamp_iso", "value"})
	for _, m := range metrics {
		pts := mockdata.GenerateSeries(arr.ID+"|"+m.ID, arr.Profile, m.Category, m.SeverityWatch, m.SeverityCritical, start, end, step)
		for _, p := range pts {
			ts := time.Unix(int64(p.Time), 0).UTC()
			cw.Write([]string{
				arr.ID, m.ID, m.Label, m.Category, m.Unit,
				strconv.FormatFloat(p.Time, 'f', 0, 64),
				ts.Format(time.RFC3339),
				strconv.FormatFloat(p.Value, 'f', -1, 64),
			})
		}
	}
}

func (a *App) buildArrayReport(id string, window time.Duration) (report.ArrayReport, error) {
	end := time.Now()
	start := end.Add(-window)
	step := window / 300
	if step < 15*time.Second {
		step = 15 * time.Second
	}

	if a.mockEnabled() {
		mArr, ok := mockdata.GetArray(id)
		if !ok {
			return report.ArrayReport{}, fmt.Errorf("unknown mock array %q", id)
		}
		metrics, err := a.thresholdsFor(mArr.Vendor)
		if err != nil {
			return report.ArrayReport{}, err
		}
		var allStats []rules.Stats
		for _, m := range metrics {
			pts := mockdata.GenerateSeries(mArr.ID+"|"+m.ID, mArr.Profile, m.Category, m.SeverityWatch, m.SeverityCritical, start, end, step)
			allStats = append(allStats, rules.Summarize(m, pts))
		}
		return report.BuildArrayReport(mArr.AsConfigArray(), allStats, start, end), nil
	}

	arr, ok, err := config.GetArray(a.ConfigDir, id)
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
	return report.BuildArrayReport(arr, allStats, start, end), nil
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

	var ids []string
	if a.mockEnabled() {
		for _, arr := range mockdata.Fleet {
			ids = append(ids, arr.ID)
		}
	} else {
		arrays, err := config.LoadArrays(a.ConfigDir)
		if err != nil {
			httpError(w, 500, err)
			return
		}
		for _, arr := range arrays {
			ids = append(ids, arr.ID)
		}
	}

	var summaries []report.FleetArraySummary
	for _, id := range ids {
		rep, err := a.buildArrayReport(id, window)
		if err != nil {
			continue
		}
		summaries = append(summaries, report.FleetArraySummary{Array: rep.Array, Health: rep.Health, IssueCount: rep.IssueCount})
	}
	fleetRep := report.BuildFleetReport(summaries, start, end)
	w.Header().Set("Content-Type", "text/html")
	report.WriteFleetReport(w, fleetRep)
}

func (a *App) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /scrape/{id}", scrapeproxy.Handler(a.ConfigDir))
	mux.HandleFunc("GET /api/fleet", a.handleFleet)
	mux.HandleFunc("GET /api/arrays/{id}", a.handleArrayDetail)
	mux.HandleFunc("GET /api/config/arrays", a.handleGetArraysConfig)
	mux.HandleFunc("PUT /api/config/arrays", a.handlePutArraysConfig)
	mux.HandleFunc("GET /api/config/settings", a.handleGetSettings)
	mux.HandleFunc("PUT /api/config/settings", a.handlePutSettings)
	mux.HandleFunc("GET /api/version", a.handleVersion)
	mux.HandleFunc("GET /api/updates", a.handleUpdates)
	mux.HandleFunc("GET /api/export/{id}", a.handleExport)
	mux.HandleFunc("GET /api/reports/array/{id}", a.handleArrayReport)
	mux.HandleFunc("GET /api/reports/fleet", a.handleFleetReport)
	mux.Handle("/", http.FileServerFS(a.Frontend))
	return mux
}

// InitialRegenerate runs the same regeneration the config-save endpoint
// triggers, used once at startup so targets/harvest config exist before
// Prometheus/Harvest start scraping.
func (a *App) InitialRegenerate() error {
	return a.regenerate()
}
