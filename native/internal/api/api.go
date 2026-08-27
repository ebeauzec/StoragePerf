// Package api wires every HTTP-facing piece of Plumb together: the Pure
// scrape proxy, the fleet/array/config/export/report endpoints the frontend
// calls, and the embedded static frontend itself.
package api

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"plumb/internal/config"
	"plumb/internal/export"
	"plumb/internal/harvest"
	"plumb/internal/report"
	"plumb/internal/rules"
	"plumb/internal/scrapeproxy"
	"plumb/internal/targets"
	"plumb/internal/updates"
	"plumb/internal/vm"
)

type App struct {
	ConfigDir      string
	TargetsPath    string
	HarvestPath    string
	SelfAddr       string
	VM             *vm.Client
	Updates        *updates.Checker
	Frontend       fs.FS
	RegenerateHarvestPollers func([]harvest.Poller) // notifies main to restart Harvest pollers after config changes
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

func (a *App) handleFleet(w http.ResponseWriter, r *http.Request) {
	arrays, err := config.LoadArrays(a.ConfigDir)
	if err != nil {
		httpError(w, 500, err)
		return
	}
	type fleetEntry struct {
		ID         string       `json:"id"`
		Name       string       `json:"name"`
		Model      string       `json:"model"`
		Vendor     string       `json:"vendor"`
		Health     rules.Severity `json:"health"`
		QueueDepth *float64     `json:"queue_depth"`
		Latency    *float64     `json:"latency"`
		Sparkline  [][2]float64 `json:"sparkline"`
	}
	out := []fleetEntry{}
	for _, arr := range arrays {
		metrics, err := a.thresholdsFor(arr.Vendor)
		if err != nil {
			out = append(out, fleetEntry{ID: arr.ID, Name: arr.Name, Model: arr.Model, Vendor: arr.Vendor, Health: rules.Unknown})
			continue
		}
		res, err := rules.EvaluateArray(a.VM, arr, metrics, time.Hour)
		if err != nil {
			out = append(out, fleetEntry{ID: arr.ID, Name: arr.Name, Model: arr.Model, Vendor: arr.Vendor, Health: rules.Unknown})
			continue
		}
		entry := fleetEntry{ID: arr.ID, Name: arr.Name, Model: arr.Model, Vendor: arr.Vendor, Health: res.Health}
		for _, p := range res.Panels {
			if isLatencyPanel(p.ID) {
				entry.Latency = p.Value
				entry.Sparkline = p.Series
			}
			if isQueuePanel(p.ID) {
				entry.QueueDepth = p.Value
			}
		}
		out = append(out, entry)
	}
	writeJSON(w, out)
}

// Different vendors name their headline latency/queue metric differently
// (host_latency vs volume_avg_latency, host_queue_depth vs volume_total_ops)
// — the fleet card just wants "the" latency/queue figure, so it looks for
// the category+role rather than a hardcoded cross-vendor ID.
func isLatencyPanel(id string) bool {
	return id == "host_latency" || id == "volume_avg_latency" || id == "metadata_query_latency"
}
func isQueuePanel(id string) bool {
	return id == "host_queue_depth" || id == "volume_total_ops" || id == "s3_operations"
}

func (a *App) handleArrayDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
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
	window := parseHours(r, 24)
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

func (a *App) handleUpdates(w http.ResponseWriter, r *http.Request) {
	enabled, checks := a.Updates.Snapshot()
	writeJSON(w, map[string]any{"enabled": enabled, "checks": checks})
}

func (a *App) handleExport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
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
	window := parseHours(r, 24)
	end := time.Now()
	start := end.Add(-window)
	step := window / 500
	if step < 15*time.Second {
		step = 15 * time.Second
	}

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-metrics-%s.csv"`, id, end.Format("20060102-1504")))
	if err := export.CSV(w, a.VM, arr, metrics, start, end, step); err != nil {
		// headers are already sent at this point in a partial-write failure;
		// nothing more we can do but stop.
		return
	}
}

func (a *App) buildArrayReport(id string, window time.Duration) (report.ArrayReport, error) {
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
	arrays, err := config.LoadArrays(a.ConfigDir)
	if err != nil {
		httpError(w, 500, err)
		return
	}
	window := parseHours(r, 24*7)
	end := time.Now()
	start := end.Add(-window)

	var summaries []report.FleetArraySummary
	for _, arr := range arrays {
		rep, err := a.buildArrayReport(arr.ID, window)
		if err != nil {
			summaries = append(summaries, report.FleetArraySummary{Array: arr, Health: "unknown"})
			continue
		}
		summaries = append(summaries, report.FleetArraySummary{Array: arr, Health: rep.Health, IssueCount: rep.IssueCount})
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
