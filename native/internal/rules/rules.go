// Package rules is the enrichment layer: it evaluates a vendor's
// config/thresholds/*.yml against live and historical VictoriaMetrics data
// and turns that into the panels and findings the UI and reports render.
// Adding a metric to a vendor's thresholds file adds a panel and a
// potential finding here automatically — nothing in this file is
// vendor-specific.
package rules

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"plumb/internal/config"
	"plumb/internal/vm"
)

type Severity string

const (
	Good     Severity = "good"
	Watch    Severity = "watch"
	Critical Severity = "critical"
	Unknown  Severity = "unknown"
)

func Classify(value float64, ok bool, watch, critical float64) Severity {
	if !ok {
		return Unknown
	}
	if value >= critical {
		return Critical
	}
	if value >= watch {
		return Watch
	}
	return Good
}

type Panel struct {
	ID             string      `json:"id"`
	Label          string      `json:"label"`
	Unit           string      `json:"unit"`
	Category       string      `json:"category"`
	Value          *float64    `json:"value"`
	Severity       Severity    `json:"severity"`
	ThresholdLabel string      `json:"threshold_label"`
	Watch          float64     `json:"watch"`
	Critical       float64     `json:"critical"`
	Series         [][2]float64 `json:"series"`
}

type Finding struct {
	Severity Severity `json:"severity"`
	Tag      string   `json:"tag"`
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	Ref      string   `json:"ref"`
}

type Result struct {
	Panels   []Panel   `json:"panels"`
	Findings []Finding `json:"findings"`
	Health   Severity  `json:"health"`
}

func substitute(query, arrayID string) string {
	return strings.ReplaceAll(query, "{array}", arrayID)
}

// commaInt formats a whole number with thousands separators — {value:.0f}
// templates are used for count-style metrics (ops, objects, errors/min)
// that are often in the thousands or more, where a bare Sprintf("%.0f")
// like "141642" is hard to read at a glance.
func commaInt(v float64) string {
	s := fmt.Sprintf("%.0f", v)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

func formatTemplate(tmpl string, value, threshold float64) string {
	r := strings.NewReplacer(
		"{value:.1f}", fmt.Sprintf("%.1f", value),
		"{value:.0f}", commaInt(value),
		"{value:.2f}", fmt.Sprintf("%.2f", value),
		"{threshold:.1f}", fmt.Sprintf("%.1f", threshold),
		"{threshold:.0f}", commaInt(threshold),
		"{threshold:.2f}", fmt.Sprintf("%.2f", threshold),
	)
	return r.Replace(tmpl)
}

// EvaluateArray runs every metric defined for arr's vendor and returns the
// current panels, findings, and overall health.
func EvaluateArray(client *vm.Client, arr config.Array, metrics []config.MetricDef, window time.Duration) (Result, error) {
	now := time.Now()
	start := now.Add(-window)
	step := window / 120
	if step < 15*time.Second {
		step = 15 * time.Second
	}

	var panels []Panel
	for _, m := range metrics {
		query := substitute(m.Query, arr.ID)

		value, ok, err := client.InstantQuery(query)
		if err != nil {
			return Result{}, fmt.Errorf("querying %s: %w", m.ID, err)
		}
		sev := Classify(value, ok, m.SeverityWatch, m.SeverityCritical)

		pts, err := client.RangeQuery(query, start, now, step)
		if err != nil {
			return Result{}, fmt.Errorf("range querying %s: %w", m.ID, err)
		}
		series := make([][2]float64, len(pts))
		for i, p := range pts {
			series[i] = [2]float64{p.Time, p.Value}
		}

		var valPtr *float64
		if ok {
			v := value
			valPtr = &v
		}

		panels = append(panels, Panel{
			ID: m.ID, Label: m.Label, Unit: m.Unit, Category: m.Category,
			Value: valPtr, Severity: sev, ThresholdLabel: m.ThresholdLabel,
			Watch: m.SeverityWatch, Critical: m.SeverityCritical, Series: series,
		})
	}

	findings, health := BuildFindings(arr.ID, metrics, panels)
	return Result{Panels: panels, Findings: findings, Health: health}, nil
}

// BuildFindings turns already-evaluated panels (severity + value already
// computed, however that happened — a real VM query or synthetic mock data)
// into the per-metric findings, the cross-panel correlation finding, and the
// overall health. Factored out of EvaluateArray so the mock-data path
// (internal/mockdata) produces findings with identical logic to real data,
// not a separate reimplementation.
func BuildFindings(arrayID string, metrics []config.MetricDef, panels []Panel) ([]Finding, Severity) {
	metricByID := make(map[string]config.MetricDef, len(metrics))
	for _, m := range metrics {
		metricByID[m.ID] = m
	}

	var findings []Finding
	var severities []Severity
	for _, p := range panels {
		severities = append(severities, p.Severity)
		if p.Value == nil || (p.Severity != Watch && p.Severity != Critical) {
			continue
		}
		m, ok := metricByID[p.ID]
		if !ok {
			continue
		}
		tmpl, exists := m.Finding[string(p.Severity)]
		if !exists {
			continue
		}
		threshold := m.SeverityWatch
		if p.Severity == Critical {
			threshold = m.SeverityCritical
		}
		findings = append(findings, Finding{
			Severity: p.Severity, Tag: m.Category, Title: m.Label,
			Body: formatTemplate(tmpl, *p.Value, threshold),
			Ref:  fmt.Sprintf("%s · %s", arrayID, m.ID),
		})
	}

	// Cross-panel correlation: front-end degraded while every back-end
	// signal we have is clean points at an upstream (SAN/network) cause
	// rather than the array itself. Deliberately vendor-agnostic — it
	// just looks at category, not specific metric IDs.
	var frontendBad, haveBackend, backendClean bool = false, false, true
	for _, p := range panels {
		if p.Category == "frontend" && (p.Severity == Watch || p.Severity == Critical) {
			frontendBad = true
		}
		if p.Category == "backend" {
			haveBackend = true
			if p.Severity != Good {
				backendClean = false
			}
		}
	}
	if frontendBad && haveBackend && backendClean {
		worst := Watch
		for _, p := range panels {
			if p.Category == "frontend" && p.Severity == Critical {
				worst = Critical
			}
		}
		findings = append([]Finding{{
			Severity: worst,
			Tag:      "fleet",
			Title:    "Bottleneck is likely upstream of the array",
			Body: "Front-end metrics are degraded while every back-end signal this vendor publishes is within range. " +
				"That points to the network/fabric path as the likely cause rather than the array's internal components.",
			Ref: fmt.Sprintf("%s · derived from front-end + back-end panels", arrayID),
		}}, findings...)
	}

	health := Good
	for _, s := range severities {
		if s == Critical {
			health = Critical
			break
		}
		if s == Watch {
			health = Watch
		}
	}

	return findings, health
}

// Stats summarizes one metric's series over a report period — the basis
// for the "comprehensive analysis at every level" report requirement.
type Stats struct {
	MetricID        string
	Label           string
	Unit            string
	Category        string
	Min, Avg, Max   float64
	P95             float64
	WatchPct        float64 // fraction of samples at/above watch
	CriticalPct     float64
	TrendPct        float64 // % change, first quarter avg -> last quarter avg
	ThresholdLabel  string
	SampleCount     int
}

func Percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)-1))
	return sorted[idx]
}

func Summarize(m config.MetricDef, pts []vm.Point) Stats {
	s := Stats{MetricID: m.ID, Label: m.Label, Unit: m.Unit, Category: m.Category, ThresholdLabel: m.ThresholdLabel, SampleCount: len(pts)}
	if len(pts) == 0 {
		return s
	}
	values := make([]float64, len(pts))
	sum := 0.0
	watchCount, critCount := 0, 0
	s.Min, s.Max = pts[0].Value, pts[0].Value
	for i, p := range pts {
		values[i] = p.Value
		sum += p.Value
		if p.Value < s.Min {
			s.Min = p.Value
		}
		if p.Value > s.Max {
			s.Max = p.Value
		}
		if p.Value >= m.SeverityCritical {
			critCount++
		} else if p.Value >= m.SeverityWatch {
			watchCount++
		}
	}
	s.Avg = sum / float64(len(pts))
	s.WatchPct = 100 * float64(watchCount) / float64(len(pts))
	s.CriticalPct = 100 * float64(critCount) / float64(len(pts))

	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	s.P95 = Percentile(sorted, 0.95)

	quarter := len(pts) / 4
	if quarter > 0 {
		firstSum, lastSum := 0.0, 0.0
		for i := 0; i < quarter; i++ {
			firstSum += pts[i].Value
		}
		for i := len(pts) - quarter; i < len(pts); i++ {
			lastSum += pts[i].Value
		}
		firstAvg := firstSum / float64(quarter)
		lastAvg := lastSum / float64(quarter)
		if firstAvg != 0 {
			s.TrendPct = 100 * (lastAvg - firstAvg) / firstAvg
		}
	}
	return s
}
