// Package report generates the per-array and fleet-wide analysis documents.
// Every number and every sentence of narrative here is derived directly
// from the same VictoriaMetrics history and config/thresholds/*.yml the
// live dashboard uses — nothing is templated with placeholder text, and
// nothing requires a separate findings-history store: the analysis is
// computed by re-evaluating the stored time series over the report period.
package report

import (
	"fmt"
	"html/template"
	"io"
	"sort"
	"time"

	"plumb/internal/config"
	"plumb/internal/rules"
)

type MetricNarrative struct {
	Stats    rules.Stats
	Severity string // good | watch | critical, worst seen during the period
	Analysis string
}

type ArrayReport struct {
	Array       config.Array
	GeneratedAt time.Time
	PeriodStart time.Time
	PeriodEnd   time.Time
	Frontend    []MetricNarrative
	Backend     []MetricNarrative
	IssueCount  int
	Health      string
}

func narrate(s rules.Stats) MetricNarrative {
	sev := "good"
	switch {
	case s.CriticalPct > 0:
		sev = "critical"
	case s.WatchPct > 0:
		sev = "watch"
	}

	var text string
	switch {
	case s.SampleCount == 0:
		text = "No data was collected for this metric during the report period."
	case s.CriticalPct >= 10:
		text = fmt.Sprintf(
			"%s spent %.0f%% of this period at or above the critical threshold (%s), peaking at %.2f %s. "+
				"This is a sustained condition, not a brief spike, and is the clearest candidate for troubleshooting on this system.",
			s.Label, s.CriticalPct, s.ThresholdLabel, s.Max, s.Unit)
	case s.CriticalPct > 0:
		text = fmt.Sprintf(
			"%s briefly reached critical levels (%.0f%% of samples, peak %.2f %s), against a period average of %.2f %s. "+
				"Worth a quick check for what coincided with the spike, even though it wasn't sustained.",
			s.Label, s.CriticalPct, s.Max, s.Unit, s.Avg, s.Unit)
	case s.WatchPct >= 20:
		text = fmt.Sprintf(
			"%s ran above the illustrative watch threshold for %.0f%% of this period (avg %.2f %s, p95 %.2f %s). "+
				"Consistently elevated rather than spiky — a good candidate to re-baseline this threshold or investigate the sustained load.",
			s.Label, s.WatchPct, s.Avg, s.Unit, s.P95, s.Unit)
	case s.WatchPct > 0:
		text = fmt.Sprintf(
			"%s crossed into watch range occasionally (%.0f%% of samples), averaging %.2f %s overall.",
			s.Label, s.WatchPct, s.Avg, s.Unit)
	default:
		text = fmt.Sprintf("%s stayed within range throughout the period — avg %.2f %s, peak %.2f %s.",
			s.Label, s.Avg, s.Unit, s.Max, s.Unit)
	}

	if s.SampleCount > 0 {
		if s.TrendPct >= 15 {
			text += fmt.Sprintf(" It also trended up %.0f%% from the start of the period to the end — worth watching even if still in range.", s.TrendPct)
		} else if s.TrendPct <= -15 {
			text += fmt.Sprintf(" It trended down %.0f%% over the period, an improving direction.", -s.TrendPct)
		}
	}

	return MetricNarrative{Stats: s, Severity: sev, Analysis: text}
}

func BuildArrayReport(arr config.Array, allStats []rules.Stats, start, end time.Time) ArrayReport {
	rep := ArrayReport{Array: arr, GeneratedAt: time.Now(), PeriodStart: start, PeriodEnd: end, Health: "good"}
	for _, s := range allStats {
		n := narrate(s)
		if n.Severity == "critical" {
			rep.Health = "critical"
		} else if n.Severity == "watch" && rep.Health != "critical" {
			rep.Health = "watch"
		}
		if n.Severity != "good" {
			rep.IssueCount++
		}
		if s.Category == "frontend" {
			rep.Frontend = append(rep.Frontend, n)
		} else {
			rep.Backend = append(rep.Backend, n)
		}
	}
	return rep
}

type FleetArraySummary struct {
	Array      config.Array
	Health     string
	IssueCount int
}

type FleetReport struct {
	GeneratedAt time.Time
	PeriodStart time.Time
	PeriodEnd   time.Time
	Arrays      []FleetArraySummary
	Narrative   string
}

func BuildFleetReport(summaries []FleetArraySummary, start, end time.Time) FleetReport {
	sort.Slice(summaries, func(i, j int) bool {
		rank := map[string]int{"critical": 0, "watch": 1, "good": 2, "unknown": 3}
		if rank[summaries[i].Health] != rank[summaries[j].Health] {
			return rank[summaries[i].Health] < rank[summaries[j].Health]
		}
		return summaries[i].IssueCount > summaries[j].IssueCount
	})

	critical, watch := 0, 0
	for _, s := range summaries {
		switch s.Health {
		case "critical":
			critical++
		case "watch":
			watch++
		}
	}
	var narrative string
	switch {
	case critical == 0 && watch == 0:
		narrative = fmt.Sprintf("All %d monitored systems stayed within their illustrative thresholds for this period. No fleet-wide action indicated.", len(summaries))
	case critical > 0:
		narrative = fmt.Sprintf("%d of %d systems reached critical levels on at least one metric during this period, and %d more reached watch. "+
			"Start with the systems at the top of the table below — they're ranked worst-first.", critical, len(summaries), watch)
	default:
		narrative = fmt.Sprintf("%d of %d systems reached watch-level thresholds during this period; none reached critical. Worth a routine look, not an urgent one.", watch, len(summaries))
	}

	return FleetReport{GeneratedAt: time.Now(), PeriodStart: start, PeriodEnd: end, Arrays: summaries, Narrative: narrative}
}

const styleBlock = `
<style>
  :root{ --ink:#161b22; --muted:#5b6472; --line:#e2e5ea; --good:#1a8f4c; --watch:#a9660a; --critical:#c1263d; --paper:#ffffff; --accent:#0f6f66; }
  *{box-sizing:border-box;}
  body{ font-family:"IBM Plex Sans","Segoe UI",Arial,sans-serif; color:var(--ink); background:#f4f5f7; margin:0; padding:40px 24px; }
  .sheet{ max-width:900px; margin:0 auto; background:var(--paper); border:1px solid var(--line); border-radius:10px; padding:40px 48px; }
  h1{ font-size:22px; margin:0 0 4px; }
  h2{ font-size:15px; text-transform:uppercase; letter-spacing:0.06em; color:var(--muted); border-bottom:1px solid var(--line); padding-bottom:8px; margin:32px 0 16px; }
  .meta{ color:var(--muted); font-size:13px; margin-bottom:24px; }
  table{ width:100%; border-collapse:collapse; font-size:13px; margin-bottom:8px; }
  th,td{ text-align:left; padding:8px 10px; border-bottom:1px solid var(--line); vertical-align:top; }
  th{ color:var(--muted); font-weight:600; font-size:11px; text-transform:uppercase; letter-spacing:0.04em; }
  .pill{ display:inline-block; font-size:11px; font-weight:700; text-transform:uppercase; letter-spacing:0.03em; padding:2px 8px; border-radius:20px; }
  .pill-good{ color:var(--good); background:rgba(26,143,76,0.12); }
  .pill-watch{ color:var(--watch); background:rgba(169,102,10,0.12); }
  .pill-critical{ color:var(--critical); background:rgba(193,38,61,0.12); }
  .analysis{ font-size:13.5px; line-height:1.6; margin:6px 0 18px; }
  .summary-line{ font-size:14.5px; line-height:1.7; background:#f4f5f7; border-radius:8px; padding:14px 16px; margin-bottom:8px;}
  .footer{ margin-top:36px; padding-top:16px; border-top:1px solid var(--line); font-size:11.5px; color:var(--muted); }
</style>`

func pillClass(sev string) string {
	switch sev {
	case "critical":
		return "pill-critical"
	case "watch":
		return "pill-watch"
	default:
		return "pill-good"
	}
}

var arrayTmpl = template.Must(template.New("array").Funcs(template.FuncMap{"pillClass": pillClass}).Parse(`<!doctype html><html><head><meta charset="utf-8"><title>Plumb Report — {{.Array.Name}}</title>` + styleBlock + `</head><body>
<div class="sheet">
  <h1>{{.Array.Name}} — Performance Report</h1>
  <div class="meta">{{.Array.Model}} · {{.Array.Vendor}} · Period: {{.PeriodStart.Format "2006-01-02 15:04"}} – {{.PeriodEnd.Format "2006-01-02 15:04"}} UTC · Generated {{.GeneratedAt.Format "2006-01-02 15:04"}} UTC</div>

  <div class="summary-line">
    Overall status this period: <span class="pill {{pillClass .Health}}">{{.Health}}</span>
    {{if gt .IssueCount 0}} — {{.IssueCount}} metric(s) crossed a threshold at least once.{{else}} — no metric crossed its illustrative threshold.{{end}}
  </div>

  <h2>Front-End — SAN &amp; Host Path</h2>
  {{range .Frontend}}
  <div><span class="pill {{pillClass .Severity}}">{{.Severity}}</span> <strong>{{.Stats.Label}}</strong></div>
  <div class="analysis">{{.Analysis}}</div>
  {{else}}<p class="analysis">No front-end metrics defined for this vendor.</p>{{end}}
  <table>
    <tr><th>Metric</th><th>Min</th><th>Avg</th><th>P95</th><th>Max</th><th>Unit</th><th>Samples</th></tr>
    {{range .Frontend}}<tr><td>{{.Stats.Label}}</td><td>{{printf "%.2f" .Stats.Min}}</td><td>{{printf "%.2f" .Stats.Avg}}</td><td>{{printf "%.2f" .Stats.P95}}</td><td>{{printf "%.2f" .Stats.Max}}</td><td>{{.Stats.Unit}}</td><td>{{.Stats.SampleCount}}</td></tr>{{end}}
  </table>

  <h2>Back-End — Array Internal</h2>
  {{range .Backend}}
  <div><span class="pill {{pillClass .Severity}}">{{.Severity}}</span> <strong>{{.Stats.Label}}</strong></div>
  <div class="analysis">{{.Analysis}}</div>
  {{else}}<p class="analysis">No back-end metrics defined for this vendor.</p>{{end}}
  <table>
    <tr><th>Metric</th><th>Min</th><th>Avg</th><th>P95</th><th>Max</th><th>Unit</th><th>Samples</th></tr>
    {{range .Backend}}<tr><td>{{.Stats.Label}}</td><td>{{printf "%.2f" .Stats.Min}}</td><td>{{printf "%.2f" .Stats.Avg}}</td><td>{{printf "%.2f" .Stats.P95}}</td><td>{{printf "%.2f" .Stats.Max}}</td><td>{{.Stats.Unit}}</td><td>{{.Stats.SampleCount}}</td></tr>{{end}}
  </table>

  <div class="footer">Generated by Plumb from locally stored VictoriaMetrics history. Thresholds are illustrative defaults from config/thresholds/{{.Array.Vendor}}.yml, not vendor-published SLAs — tune them to this system's observed baseline.</div>
</div>
</body></html>`))

var fleetTmpl = template.Must(template.New("fleet").Funcs(template.FuncMap{"pillClass": pillClass}).Parse(`<!doctype html><html><head><meta charset="utf-8"><title>Plumb Fleet Report</title>` + styleBlock + `</head><body>
<div class="sheet">
  <h1>Fleet Performance Report</h1>
  <div class="meta">{{len .Arrays}} systems · Period: {{.PeriodStart.Format "2006-01-02 15:04"}} – {{.PeriodEnd.Format "2006-01-02 15:04"}} UTC · Generated {{.GeneratedAt.Format "2006-01-02 15:04"}} UTC</div>

  <div class="summary-line">{{.Narrative}}</div>

  <h2>Systems, worst first</h2>
  <table>
    <tr><th>System</th><th>Vendor</th><th>Model</th><th>Status</th><th>Issues this period</th></tr>
    {{range .Arrays}}<tr>
      <td>{{.Array.Name}}</td><td>{{.Array.Vendor}}</td><td>{{.Array.Model}}</td>
      <td><span class="pill {{pillClass .Health}}">{{.Health}}</span></td>
      <td>{{.IssueCount}}</td>
    </tr>{{end}}
  </table>

  <div class="footer">Generated by Plumb. See each system's individual report for full metric-level analysis.</div>
</div>
</body></html>`))

func WriteArrayReport(w io.Writer, rep ArrayReport) error {
	return arrayTmpl.Execute(w, rep)
}

func WriteFleetReport(w io.Writer, rep FleetReport) error {
	return fleetTmpl.Execute(w, rep)
}
