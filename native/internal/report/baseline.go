package report

import (
	"fmt"
	"html/template"
	"io"
	"time"

	"plumb/internal/config"
	"plumb/internal/rules"
)

// ThresholdSuggestion compares one metric's configured watch/critical
// against what this specific array's own history actually looks like —
// every vendor thresholds file is explicit that its numbers are
// illustrative starting points, not published SLAs, but until now nothing
// computed what "this array's own observed normal range" (the advice every
// finding gives) actually is. P90/P99 are suggestions, not a prescription —
// this never writes config/thresholds/*.yml itself; a human decides whether
// and how to apply it.
type ThresholdSuggestion struct {
	MetricID          string
	Label             string
	Unit              string
	ThresholdLabel    string
	CurrentWatch      float64
	CurrentCritical   float64
	ObservedP90       float64
	ObservedP95       float64
	ObservedP99       float64
	ObservedMax       float64
	SuggestedWatch    float64
	SuggestedCritical float64
	SampleCount       int
	// LooksIllustrative is true when the currently configured thresholds are
	// far from what this array actually sees (P99 comfortably below watch,
	// or watch itself far below the observed P90) — the cases most worth an
	// operator's attention, surfaced first rather than left to spot in a
	// long table.
	LooksIllustrative bool
}

type BaselineReport struct {
	Array       config.Array
	GeneratedAt time.Time
	PeriodStart time.Time
	PeriodEnd   time.Time
	Suggestions []ThresholdSuggestion
	// LowDataNote fires once, fleet-report-header-style, when too little
	// history exists for these suggestions to be trustworthy — rather than
	// silently attaching the same caveat to every single row.
	LowDataNote string
}

// minSuggestionSamples is the floor below which a P90/P99 is more likely to
// reflect the sample size than the array's real behavior — 100 points is a
// deliberately low bar (roughly 8 hours at the default 5-minute report
// step), just enough to rule out "this is one data point wearing a
// percentile's clothing."
const minSuggestionSamples = 100

func BuildBaselineReport(arr config.Array, allStats []rules.Stats, start, end time.Time) BaselineReport {
	rep := BaselineReport{Array: arr, GeneratedAt: time.Now(), PeriodStart: start, PeriodEnd: end}
	minSamples := -1
	for _, s := range allStats {
		if s.SampleCount == 0 {
			continue
		}
		if minSamples == -1 || s.SampleCount < minSamples {
			minSamples = s.SampleCount
		}
		sugg := ThresholdSuggestion{
			MetricID: s.MetricID, Label: s.Label, Unit: s.Unit, ThresholdLabel: s.ThresholdLabel,
			CurrentWatch: s.SeverityWatch, CurrentCritical: s.SeverityCritical,
			ObservedP90: s.P90, ObservedP95: s.P95, ObservedP99: s.P99, ObservedMax: s.Max,
			SuggestedWatch: s.P90, SuggestedCritical: s.P99, SampleCount: s.SampleCount,
		}
		if sugg.SuggestedCritical <= sugg.SuggestedWatch {
			// A flat/near-flat metric can put P90 and P99 within rounding of
			// each other — widen so "watch" and "critical" stay meaningfully
			// distinct bands rather than collapsing to the same line.
			sugg.SuggestedCritical = sugg.SuggestedWatch * 1.25
		}
		if s.SeverityCritical > 0 && (s.P99 < s.SeverityWatch*0.5 || s.P90 > s.SeverityCritical) {
			sugg.LooksIllustrative = true
		}
		rep.Suggestions = append(rep.Suggestions, sugg)
	}
	if minSamples >= 0 && minSamples < minSuggestionSamples {
		rep.LowDataNote = fmt.Sprintf(
			"At least one metric below has only %d samples for this period — treat its suggested values as provisional until more history accumulates.",
			minSamples)
	}
	return rep
}

var baselineTmpl = template.Must(template.New("baseline").Parse(`<!doctype html><html><head><meta charset="utf-8"><title>Plumb Suggested Thresholds — {{.Array.Name}}</title>` + styleBlock + `</head><body>
<div class="sheet">
  <h1>{{.Array.Name}} — Suggested Thresholds</h1>
  <div class="meta">{{.Array.Model}} · {{.Array.Vendor}} · Based on: {{.PeriodStart.Format "2006-01-02 15:04"}} – {{.PeriodEnd.Format "2006-01-02 15:04"}} UTC · Generated {{.GeneratedAt.Format "2006-01-02 15:04"}} UTC</div>

  <div class="summary-line">
    Suggested Watch = this array's own observed P90; suggested Critical = its observed P99, over the period above.
    These are computed, not published — review before changing config/thresholds/{{.Array.Vendor}}.yml, since a brief real spike will
    still show up as P99 even on an otherwise healthy system.
  </div>

  {{if .LowDataNote}}
  <div class="summary-line" style="border:1px solid var(--watch); background:rgba(169,102,10,0.1);"><strong>Limited data:</strong> {{.LowDataNote}}</div>
  {{end}}

  <table>
    <tr><th>Metric</th><th>Current Watch / Critical</th><th>Observed P90 / P95 / P99</th><th>Observed Max</th><th>Suggested Watch / Critical</th><th>Samples</th></tr>
    {{range .Suggestions}}<tr{{if .LooksIllustrative}} style="background:rgba(169,102,10,0.06)"{{end}}>
      <td>{{.Label}}{{if .LooksIllustrative}}<br><span class="pill pill-watch">worth reviewing</span>{{end}}</td>
      <td>{{printf "%.2f" .CurrentWatch}} / {{printf "%.2f" .CurrentCritical}} {{.Unit}}</td>
      <td>{{printf "%.2f" .ObservedP90}} / {{printf "%.2f" .ObservedP95}} / {{printf "%.2f" .ObservedP99}} {{.Unit}}</td>
      <td>{{printf "%.2f" .ObservedMax}} {{.Unit}}</td>
      <td>{{printf "%.2f" .SuggestedWatch}} / {{printf "%.2f" .SuggestedCritical}} {{.Unit}}</td>
      <td>{{.SampleCount}}</td>
    </tr>{{else}}<tr><td colspan="6">No metrics had data for this period.</td></tr>{{end}}
  </table>

  <div class="footer">Generated by Plumb from this array's own VictoriaMetrics history. "Worth reviewing" flags a configured threshold that's far from what this array actually sees — either the current line is well above anything observed, or the array is already running past it without alerting.</div>
</div>
</body></html>`))

func WriteBaselineReport(w io.Writer, rep BaselineReport) error {
	return baselineTmpl.Execute(w, rep)
}
