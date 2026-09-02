package api

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"plumb/internal/config"
	"plumb/internal/eventstore"
	"plumb/internal/findingstore"
	"plumb/internal/maintenance"
	"plumb/internal/notify"
	"plumb/internal/report"
	"plumb/internal/rules"
)

// notifyConfig reads the current webhook settings — a small read-locked
// accessor mirroring mockEnabled/retentionPeriod, so RunMonitor always sees
// the latest Config-tab value without needing its own restart.
func (a *App) notifyConfig() notify.Config {
	a.settingsMu.RLock()
	defer a.settingsMu.RUnlock()
	return notify.Config{Enabled: a.notifyEnabled, WebhookURL: a.notifyWebhookURL, MinSeverity: a.notifyMinSeverity}
}

func (a *App) scheduleConfig() (enabled bool, interval string, hours float64) {
	a.settingsMu.RLock()
	defer a.settingsMu.RUnlock()
	return a.scheduledReportsEnabled, a.scheduledReportInterval, a.scheduledReportHours
}

// RunMonitor is Plumb's background eyes: on each tick it re-evaluates every
// active array exactly like the dashboard does, reconciles the persistent
// findings store (internal/findingstore) so history/acknowledgment survive
// restarts, and fires a webhook for anything newly bad — muted for an array
// currently in a maintenance window (internal/maintenance). The same loop
// also checks, once per tick, whether it's time to generate and archive the
// next scheduled fleet report — one background goroutine instead of three
// independently drifting tickers.
func (a *App) RunMonitor(interval time.Duration, stopCh <-chan struct{}) {
	a.monitorOnce()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			a.monitorOnce()
			a.maybeRunScheduledReport()
		}
	}
}

func (a *App) monitorOnce() {
	if a.Findings == nil {
		return
	}
	arrays, err := a.activeArrays()
	if err != nil {
		log.Printf("[monitor] listing arrays: %v", err)
		return
	}
	windows, err := maintenance.Load(a.ConfigDir)
	if err != nil {
		log.Printf("[monitor] loading maintenance windows: %v", err)
	}
	cfg := a.notifyConfig()
	now := time.Now()

	for _, arr := range arrays {
		metrics, err := a.thresholdsFor(arr.Vendor)
		if err != nil {
			continue
		}
		res, err := rules.EvaluateArray(a.VM, arr, metrics, time.Hour)
		if err != nil {
			log.Printf("[monitor] evaluating %s: %v", arr.ID, err)
			continue
		}

		// EMS events are collected straight from the array, never through
		// Prometheus/VictoriaMetrics — see internal/netappnative/ems.go's
		// doc comment for why. ONTAP only; StorageGRID and Pure have no EMS
		// equivalent Plumb collects today.
		if a.Events != nil && arr.Vendor == config.VendorNetAppONTAP && a.ONTAP != nil {
			if emsEvents, err := a.ONTAP.CollectEMSEvents(arr); err != nil {
				log.Printf("[monitor] collecting EMS events for %s: %v", arr.ID, err)
			} else if len(emsEvents) > 0 {
				converted := make([]eventstore.Event, len(emsEvents))
				for i, e := range emsEvents {
					converted[i] = eventstore.Event{
						ArrayID: e.ArrayID, ArrayName: e.ArrayName, Source: "ems", Key: e.DedupKey(),
						Time: e.Time.Format(time.RFC3339), Severity: e.Severity, Name: e.Name, Node: e.Node, Message: e.Message,
					}
				}
				if err := a.Events.Append(converted); err != nil {
					log.Printf("[monitor] saving EMS events for %s: %v", arr.ID, err)
				}
				if cfg.Enabled && cfg.WebhookURL != "" {
					if muted, _ := maintenance.Active(windows, arr.ID, now); !muted {
						for _, e := range emsEvents {
							if !cfg.Meets(e.Severity) {
								continue
							}
							ev := notify.Event{Kind: "ems", ArrayID: e.ArrayID, ArrayName: e.ArrayName, Vendor: arr.Vendor, Label: e.Name, Severity: e.Severity, Timestamp: e.Time, Body: e.Message}
							if err := notify.Send(a.notifyClient(), cfg, ev); err != nil {
								log.Printf("[monitor] webhook send failed for EMS event %s/%s: %v", e.ArrayID, e.Name, err)
							}
						}
					}
				}
			}
		}
		// Sourced from res.Findings, not res.Panels directly — a panel with
		// a NodeBreakdownQuery can have a node genuinely worse than its own
		// fleet-wide average (rules.BuildFindings' node-level finding
		// block), and that node-level finding's MetricID/Severity are what
		// need to reach the findings store and webhook, not just the
		// panel's own (masked) severity.
		var current []findingstore.CurrentFinding
		for _, f := range res.Findings {
			if f.MetricID == "" || (f.Severity != rules.Watch && f.Severity != rules.Critical) {
				continue // the cross-panel correlation findings have no MetricID and aren't per-metric state to track
			}
			current = append(current, findingstore.CurrentFinding{MetricID: f.MetricID, Label: f.Title, Severity: string(f.Severity)})
		}
		newOrEscalated, resolved, err := a.Findings.Reconcile(arr.ID, arr.Name, arr.Vendor, current, now)
		if err != nil {
			log.Printf("[monitor] saving findings for %s: %v", arr.ID, err)
		}
		if !cfg.Enabled || cfg.WebhookURL == "" {
			continue
		}
		muted, _ := maintenance.Active(windows, arr.ID, now)
		if muted {
			continue
		}
		for _, r := range newOrEscalated {
			if !cfg.Meets(r.Severity) {
				continue
			}
			ev := notify.Event{Kind: "new", ArrayID: r.ArrayID, ArrayName: r.ArrayName, Vendor: r.Vendor, MetricID: r.MetricID, Label: r.Label, Severity: r.Severity, Timestamp: now}
			if body := findingBody(res, r.MetricID); body != "" {
				ev.Body = body
			}
			if err := notify.Send(a.notifyClient(), cfg, ev); err != nil {
				log.Printf("[monitor] webhook send failed for %s/%s: %v", r.ArrayID, r.MetricID, err)
			}
		}
		for _, r := range resolved {
			if !cfg.Meets(r.Severity) {
				continue
			}
			ev := notify.Event{Kind: "resolved", ArrayID: r.ArrayID, ArrayName: r.ArrayName, Vendor: r.Vendor, MetricID: r.MetricID, Label: r.Label, Severity: r.Severity, Timestamp: now}
			if err := notify.Send(a.notifyClient(), cfg, ev); err != nil {
				log.Printf("[monitor] webhook send failed for %s/%s: %v", r.ArrayID, r.MetricID, err)
			}
		}
	}
}

func findingBody(res rules.Result, metricID string) string {
	for _, f := range res.Findings {
		if f.MetricID == metricID {
			return f.Body
		}
	}
	return ""
}

var notifyHTTPClient = &http.Client{Timeout: 10 * time.Second}

func (a *App) notifyClient() *http.Client { return notifyHTTPClient }

// maxScheduledReports caps how many archived fleet reports data/reports/
// accumulates before the oldest are pruned — enough for roughly a year of
// weekly reports (or three months of daily ones) without unbounded growth.
const maxScheduledReports = 60

// maybeRunScheduledReport generates and archives a fleet report when the
// configured schedule says it's due. "Due" is determined by inspecting the
// newest already-archived report's filename rather than tracking a
// separate in-memory timestamp, so a restart never causes a duplicate
// report just because in-memory state reset to zero.
func (a *App) maybeRunScheduledReport() {
	enabled, interval, hours := a.scheduleConfig()
	if !enabled {
		return
	}
	if !config.ValidScheduleInterval(interval) {
		interval = "daily"
		hours = 24
	}
	dir := filepath.Join(a.DataDir, "reports")
	last := latestScheduledReportTime(dir)
	due := config.ScheduleIntervalDuration(interval)
	if !last.IsZero() && time.Since(last) < due {
		return
	}

	end := time.Now()
	start := end.Add(-time.Duration(hours * float64(time.Hour)))
	arrays, err := a.activeArrays()
	if err != nil {
		log.Printf("[monitor] scheduled report: listing arrays: %v", err)
		return
	}
	var summaries []report.FleetArraySummary
	for _, arr := range arrays {
		rep, err := a.buildArrayReport(arr.ID, time.Duration(hours*float64(time.Hour)))
		if err != nil {
			continue
		}
		summaries = append(summaries, report.FleetArraySummary{Array: rep.Array, Health: rep.Health, IssueCount: rep.IssueCount, TrendPct: rep.TrendPct, TrendLabel: rep.TrendLabel})
	}
	fleetRep := report.BuildFleetReport(summaries, start, end)

	filename := filepath.Join(dir, "fleet-"+end.Format("20060102-150405")+".html")
	f, err := os.Create(filename)
	if err != nil {
		log.Printf("[monitor] scheduled report: creating file: %v", err)
		return
	}
	writeErr := report.WriteFleetReport(f, fleetRep)
	f.Close()
	if writeErr != nil {
		log.Printf("[monitor] scheduled report: writing: %v", writeErr)
		return
	}
	log.Printf("[monitor] scheduled report generated: %s", filepath.Base(filename))
	pruneScheduledReports(dir, maxScheduledReports)

	if cfg := a.notifyConfig(); cfg.Enabled && cfg.WebhookURL != "" {
		critical, watch := 0, 0
		for _, s := range summaries {
			switch s.Health {
			case "critical":
				critical++
			case "watch":
				watch++
			}
		}
		body := "All systems within range."
		if critical > 0 || watch > 0 {
			body = "systems: " + itoa(critical) + " critical, " + itoa(watch) + " watch, out of " + itoa(len(summaries)) + " total."
		}
		notify.Send(a.notifyClient(), cfg, notify.Event{Kind: "report", Body: "Scheduled fleet report generated — " + body, Timestamp: end})
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func latestScheduledReportTime(dir string) time.Time {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return time.Time{}
	}
	var latest time.Time
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
	}
	return latest
}

func pruneScheduledReports(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type fileInfo struct {
		name    string
		modTime time.Time
	}
	var files []fileInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileInfo{name: e.Name(), modTime: info.ModTime()})
	}
	if len(files) <= keep {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.After(files[j].modTime) })
	for _, f := range files[keep:] {
		os.Remove(filepath.Join(dir, f.name))
	}
}
