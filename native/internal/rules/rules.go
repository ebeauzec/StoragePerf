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

// severityRank orders severities worst-first for comparison — used to tell
// whether a node/disk's own severity is actually worse than what its
// panel's own fleet-wide aggregate already shows (see the node-level
// finding block in BuildFindings).
func severityRank(s Severity) int {
	switch s {
	case Critical:
		return 3
	case Watch:
		return 2
	case Good:
		return 1
	default:
		return 0
	}
}

// blastRadiusNote states, in the finding itself rather than only in a
// config comment someone has to go find, what a node/disk-level problem
// actually means for the rest of the system — the two possible answers
// track exactly config.MetricDef.EscalateToNodeSeverity's own reasoning,
// so this can never drift out of sync with the escalation logic itself.
func blastRadiusNote(m config.MetricDef) string {
	if m.EscalateToNodeSeverity {
		return "This platform has no automatic way to route around a bad node for this resource, so the impact is real and current for whatever this node is hosting right now — not just a statistic."
	}
	return "This platform's redundancy (replication/erasure coding, quorum reads, or load-balanced traffic, depending on the metric) typically contains the impact to whatever happens to touch this specific node — check whether other nodes show the same symptom before treating it as grid-wide."
}

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

// classifyPanel is Classify plus the Informational downgrade: a metric
// where higher isn't inherently worse (see config.MetricDef.Informational)
// never reports Watch/Critical, no matter how far above its illustrative
// reference line the value sits — Unknown still passes through unchanged,
// since "no data" is worth showing regardless.
func classifyPanel(value float64, ok bool, watch, critical float64, informational bool) Severity {
	sev := Classify(value, ok, watch, critical)
	if informational && sev != Unknown {
		return Good
	}
	return sev
}

type Panel struct {
	ID             string   `json:"id"`
	Label          string   `json:"label"`
	Unit           string   `json:"unit"`
	Category       string   `json:"category"`
	Value          *float64 `json:"value"`
	Severity       Severity `json:"severity"`
	ThresholdLabel string   `json:"threshold_label"`
	Watch          float64  `json:"watch"`
	Critical       float64  `json:"critical"`
	// Informational mirrors config.MetricDef.Informational — the frontend
	// uses it to skip the alarming badge styling and the chart's threshold
	// line for a metric where higher isn't inherently worse (see that
	// field's own doc comment for why Watch/Critical still exist here
	// anyway).
	Informational bool         `json:"informational,omitempty"`
	Series        [][2]float64 `json:"series"`
	// Nodes is only present for a metric with NodeBreakdownQuery set (see
	// config.MetricDef) — a multi-node system's per-node values for this
	// same metric, worst (highest) first, so a grid/cluster-wide finding
	// can be traced back to the specific node driving it.
	Nodes []NodeValue `json:"nodes,omitempty"`
}

type NodeValue struct {
	Node     string   `json:"node"`
	Value    float64  `json:"value"`
	Severity Severity `json:"severity"`
}

type Finding struct {
	Severity Severity `json:"severity"`
	Tag      string   `json:"tag"`
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	Ref      string   `json:"ref"`
	// MetricID is empty for the derived cross-panel "Bottleneck is likely
	// upstream" finding (it isn't any one metric) and set for every
	// per-metric finding — the acknowledge API needs an exact ID rather
	// than a caller re-parsing Ref's "<arrayID> · <metricID>" display format.
	MetricID    string   `json:"metric_id,omitempty"`
	Investigate []string `json:"investigate"`
	Remediate   []string `json:"remediate"`
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

	// initialized non-nil, not just declared, so an array with zero metrics
	// (shouldn't happen, but not impossible) serializes as JSON "[]" rather
	// than "null" — a nil slice crashes the frontend's array.length checks
	panels := []Panel{}
	for _, m := range metrics {
		query := substitute(m.Query, arr.ID)

		value, ok, err := client.InstantQuery(query)
		if err != nil {
			return Result{}, fmt.Errorf("querying %s: %w", m.ID, err)
		}
		sev := classifyPanel(value, ok, m.SeverityWatch, m.SeverityCritical, m.Informational)

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

		var nodes []NodeValue
		if m.NodeBreakdownQuery != "" {
			pts, err := client.InstantQueryVector(substitute(m.NodeBreakdownQuery, arr.ID))
			if err != nil {
				return Result{}, fmt.Errorf("node breakdown querying %s: %w", m.ID, err)
			}
			nodes = make([]NodeValue, 0, len(pts))
			for _, p := range pts {
				node := p.Labels["node"]
				if node == "" {
					continue
				}
				nodes = append(nodes, NodeValue{Node: node, Value: p.Value, Severity: classifyPanel(p.Value, true, m.SeverityWatch, m.SeverityCritical, m.Informational)})
			}
			sort.Slice(nodes, func(i, j int) bool { return nodes[i].Value > nodes[j].Value })
			// Deliberately NOT escalating sev here: BuildFindings below
			// needs the fleet-wide value's own, unescalated classification
			// to correctly (a) word the general finding against the value
			// it actually quotes, and (b) decide whether a node's own
			// finding is "new" information worth its own entry. Escalating
			// sev before that call would make node-1 at 90% (fleet-wide
			// average, e.g. 56%) skip its own finding entirely — the fleet
			// number would already "look" as bad, even though the finding
			// text would then be quoting 56% under critical framing, which
			// is simply wrong. See the post-BuildFindings pass below for
			// where the panel's displayed badge actually gets escalated,
			// once using the correct data on both sides.
		}

		panels = append(panels, Panel{
			ID: m.ID, Label: m.Label, Unit: m.Unit, Category: m.Category,
			Value: valPtr, Severity: sev, ThresholdLabel: m.ThresholdLabel,
			Watch: m.SeverityWatch, Critical: m.SeverityCritical, Series: series,
			Informational: m.Informational, Nodes: nodes,
		})
	}

	findings, health := BuildFindings(arr.ID, metrics, panels)

	// Escalate each panel's own displayed badge to its worst node/disk,
	// but only now — after Health and the findings above were already
	// computed from the true, unescalated fleet-wide classification (see
	// the long comment where nodes are built above for why doing this any
	// earlier corrupts both). And only for a metric flagged
	// EscalateToNodeSeverity: showing the panel's badge as worse than its
	// own displayed fleet-wide value is exactly the kind of thing that
	// should be architecture-justified per metric, not a blanket default
	// for every vendor that happens to have a breakdown.
	metricByID := make(map[string]config.MetricDef, len(metrics))
	for _, m := range metrics {
		metricByID[m.ID] = m
	}
	for i := range panels {
		m, ok := metricByID[panels[i].ID]
		if !ok || !m.EscalateToNodeSeverity {
			continue
		}
		for _, n := range panels[i].Nodes {
			if severityRank(n.Severity) > severityRank(panels[i].Severity) {
				panels[i].Severity = n.Severity
			}
		}
	}

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

	// non-nil for the same reason as panels above — a fully healthy array
	// has zero findings, and a nil slice would serialize as JSON "null"
	// instead of "[]", crashing the frontend's findings.length check
	findings := []Finding{}
	var severities []Severity
	for _, p := range panels {
		severities = append(severities, p.Severity)
		// A node/disk worse than its own panel's fleet-wide average counts
		// toward overall Health too, but only for a metric where a bad node
		// genuinely has nowhere else for its workload to go — see
		// config.MetricDef.EscalateToNodeSeverity's own doc comment for the
		// per-vendor architecture reasoning. A StorageGRID grid's own
		// redundancy (erasure coding/replication, quorum reads, load
		// balancing away from busy nodes) means one hot node out of
		// possibly dozens shouldn't paint the whole grid's health the same
		// as the exact same finding would on a 2-node ONTAP HA pair, where
		// nothing else absorbs it. The node-level finding below still
		// fires and still names the node either way — this only gates
		// whether it also inflates the system-wide badge.
		if m, ok := metricByID[p.ID]; ok && m.EscalateToNodeSeverity {
			for _, n := range p.Nodes {
				severities = append(severities, n.Severity)
			}
		}
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
			Body:        formatTemplate(tmpl, *p.Value, threshold),
			Ref:         fmt.Sprintf("%s · %s", arrayID, m.ID),
			MetricID:    m.ID,
			Investigate: m.Investigate,
			Remediate:   m.Remediate,
		})
	}

	// Node/disk-level findings: a metric with a NodeBreakdownQuery can have
	// one node genuinely at watch/critical while the fleet-wide average
	// (the loop above) reads good — the average masks it, same as the
	// masking problem node_cpu_busy/aggr_disk_busy had before they got
	// this breakdown. Without this block, that hot node would show up in
	// the panel's own breakdown chart but never as an actual finding —
	// meaning it also wouldn't reach the findings history or a webhook
	// (internal/api/monitor.go builds both from panel-level severity).
	// Generic across every vendor's NodeBreakdownQuery metrics, not just
	// ONTAP's — StorageGRID's existing per-node metrics get the same
	// protection for free.
	for _, p := range panels {
		if len(p.Nodes) == 0 {
			continue
		}
		m, ok := metricByID[p.ID]
		if !ok {
			continue
		}
		for _, n := range p.Nodes {
			if severityRank(n.Severity) <= severityRank(p.Severity) {
				continue // the fleet-wide number already reflects this node, or worse
			}
			tmpl, exists := m.Finding[string(n.Severity)]
			if !exists {
				continue
			}
			threshold := m.SeverityWatch
			if n.Severity == Critical {
				threshold = m.SeverityCritical
			}
			findings = append(findings, Finding{
				Severity: n.Severity, Tag: m.Category, Title: fmt.Sprintf("%s — %s", m.Label, n.Node),
				Body: fmt.Sprintf("The fleet-wide average for this metric reads %s, but %s is masking that: %s %s",
					strings.ToLower(string(p.Severity)), n.Node, formatTemplate(tmpl, n.Value, threshold), blastRadiusNote(m)),
				Ref:         fmt.Sprintf("%s · %s · %s", arrayID, m.ID, n.Node),
				MetricID:    m.ID + "/" + n.Node,
				Investigate: m.Investigate,
				Remediate:   m.Remediate,
			})
		}
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
			Investigate: []string{
				"Check switch/fabric port counters (CRC errors, discards, link resets) on the paths between hosts and this array over the same window.",
				"Confirm host HBA/NIC driver and firmware versions, and multipath (MPIO) failover state — a path flapping in and out looks like latency at the array.",
				"Compare front-end latency against SAN/network device queue depth, buffer-credit exhaustion (FC), or NIC ring-buffer drops (iSCSI/NFS), if your fabric monitoring exposes them.",
				"Correlate the timing of the front-end degradation against any recent zoning, switch firmware, or network maintenance changes.",
			},
			Remediate: []string{
				"Engage the network/SAN team first — this is very likely their path, not an array-side fix.",
				"If a specific fabric link or interface is implicated, reseat/replace the SFP or cable and re-test.",
				"Re-balance or fail back multipath sessions once the network layer is confirmed stable.",
				"If no path issue is found, remember this vendor's public back-end metrics may not cover every internal component — don't rule the array out on back-end panels alone before engaging vendor support.",
			},
		}}, findings...)
	}

	// Second cross-panel correlation: replication lag alone can't say
	// whether the cause is local (write path contending for the same
	// resources as replication) or remote (the link or target). Every
	// per-metric finding's own investigate text already tells the reader
	// to go check this manually; this derives the answer automatically
	// from panels already evaluated, the same way the front-end/back-end
	// correlation above does. Deliberately looks at a small fixed set of
	// metric IDs (present on Pure and ONTAP, absent on StorageGRID, which
	// has no replication concept) rather than a category, since "lag" and
	// "write latency" aren't categories the way front-end/back-end are.
	panelByID := make(map[string]Panel, len(panels))
	for _, p := range panels {
		panelByID[p.ID] = p
	}
	firstBad := func(ids ...string) (Panel, bool) {
		for _, id := range ids {
			if p, ok := panelByID[id]; ok && (p.Severity == Watch || p.Severity == Critical) {
				return p, true
			}
		}
		return Panel{}, false
	}
	firstPresent := func(ids ...string) (Panel, bool) {
		for _, id := range ids {
			if p, ok := panelByID[id]; ok {
				return p, true
			}
		}
		return Panel{}, false
	}
	if lag, ok := firstBad("replication_lag", "snapmirror_lag"); ok {
		writeLatency, haveWrite := firstPresent("host_latency_write", "volume_avg_latency_write")
		capacity, haveCapacity := firstPresent("pool_saturation", "aggr_capacity", "storage_capacity")
		switch {
		case haveWrite && (writeLatency.Severity == Watch || writeLatency.Severity == Critical) && (!haveCapacity || capacity.Severity == Good):
			findings = append([]Finding{{
				Severity: lag.Severity, Tag: "fleet",
				Title: "Replication lag looks like local write-path contention, not the link",
				Body:  fmt.Sprintf("%s is elevated at the same time as %s, while capacity is within range. That combination points at something competing for the same local write path — background space reclamation, dedup/compression scans, or a genuine write burst — rather than the replication link or target.", lag.Label, writeLatency.Label),
				Ref:   fmt.Sprintf("%s · derived from %s + %s", arrayID, lag.ID, writeLatency.ID),
				Investigate: []string{
					"Review recent snapshot schedules, dedup/compression scans, or other background jobs that share the write path with replication.",
					"Check the source volume's write change rate for a genuine burst coinciding with the lag increase.",
					"Confirm this is broad (most volumes affected) rather than isolated to whatever's being replicated, which would point elsewhere.",
				},
				Remediate: []string{
					"Reschedule the competing background job to a lower-impact window rather than tuning replication directly.",
					"If a genuine write burst, this may resolve on its own once it passes — confirm lag recovers afterward.",
				},
			}}, findings...)
		case haveWrite && writeLatency.Severity == Good:
			findings = append([]Finding{{
				Severity: lag.Severity, Tag: "fleet",
				Title: "Replication lag with clean local write latency points at the link or target",
				Body:  fmt.Sprintf("%s is elevated while %s is within range — the local write path isn't the bottleneck, which points at the replication link itself or the destination system.", lag.Label, writeLatency.Label),
				Ref:   fmt.Sprintf("%s · derived from %s + %s", arrayID, lag.ID, writeLatency.ID),
				Investigate: []string{
					"Check the replication network path's throughput and utilization between source and destination for the same window.",
					"Confirm the destination system's own health and capacity — a slow or full target causes lag on every incoming relationship regardless of source load.",
				},
				Remediate: []string{
					"Engage the network team if the link itself is saturated.",
					"If the destination is the bottleneck, address its performance/capacity directly — treat it as its own system in this dashboard.",
				},
			}}, findings...)
		}
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
	MetricID      string
	Label         string
	Unit          string
	Category      string
	Min, Avg, Max float64
	P90, P95, P99 float64
	WatchPct      float64 // fraction of samples at/above watch
	CriticalPct   float64
	// Episodes counts distinct excursions at/above watch during the period
	// (a rising-edge crossing of the watch line, not a running tally of
	// samples) — the same WatchPct/CriticalPct reads identically whether a
	// metric spent 10% of the period in one sustained stretch or scattered
	// across ten short spikes, and those are different problems: one
	// steady condition vs. something recurring (a scheduled job, an
	// intermittent link fault) worth finding the trigger for.
	Episodes int
	TrendPct float64 // % change, first quarter avg -> last quarter avg
	// LastQuarterAvg and TrendSpanSeconds are the raw inputs behind TrendPct
	// (last quarter's average value, and the real elapsed time between the
	// first and last quarter's midpoints) — kept separately so a caller can
	// project an absolute rate (value/day), not just a relative percentage,
	// for metrics where "days until threshold" is meaningful (see
	// report.capacityProjection).
	FirstQuarterAvg  float64
	LastQuarterAvg   float64
	TrendSpanSeconds float64
	ThresholdLabel   string
	SeverityWatch    float64
	SeverityCritical float64
	SampleCount      int
}

func Percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)-1))
	return sorted[idx]
}

func Summarize(m config.MetricDef, pts []vm.Point) Stats {
	s := Stats{MetricID: m.ID, Label: m.Label, Unit: m.Unit, Category: m.Category, ThresholdLabel: m.ThresholdLabel, SampleCount: len(pts),
		SeverityWatch: m.SeverityWatch, SeverityCritical: m.SeverityCritical}
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

	wasBad := false
	for _, p := range pts {
		isBad := p.Value >= m.SeverityWatch
		if isBad && !wasBad {
			s.Episodes++
		}
		wasBad = isBad
	}

	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	s.P90 = Percentile(sorted, 0.90)
	s.P95 = Percentile(sorted, 0.95)
	s.P99 = Percentile(sorted, 0.99)

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
		s.FirstQuarterAvg = firstAvg
		s.LastQuarterAvg = lastAvg
		if firstAvg != 0 {
			s.TrendPct = 100 * (lastAvg - firstAvg) / firstAvg
		}
		firstMid := pts[quarter/2].Time
		lastMid := pts[len(pts)-quarter+quarter/2].Time
		s.TrendSpanSeconds = lastMid - firstMid
	}
	return s
}
