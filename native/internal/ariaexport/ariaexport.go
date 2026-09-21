// Package ariaexport defines the machine-readable performance snapshot Plumb
// hands to ARIA (the NetApp Active IQ reporting tool an MSP/TAM runs).
//
// Active IQ knows what a system IS and what NetApp saw in its AutoSupport
// (configuration, firmware, risks, capacity as of the last upload). It does not
// know how the system is PERFORMING right now, or whether a complaint is the
// array or the network in front of it. Plumb measures exactly that on the
// customer's site, so this snapshot carries it back: per-metric statistics for
// the period, the derived findings (including the "bottleneck is likely
// upstream" call), capacity-runway projections, recent EMS events, and — for
// ONTAP — the cluster name and node serial numbers ARIA needs to match an
// array to its Active IQ record exactly.
//
// The same JSON is served two ways: pulled directly over HTTP
// (GET /api/aria/export) and downloaded as a file for import where the
// customer's Plumb isn't reachable from the MSP (dark sites, no VPN).
//
// Schema changes must stay backward compatible within a major version:
// consumers ignore unknown fields, and Schema is bumped only on a breaking change.
package ariaexport

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"
)

const Schema = "plumb.aria-export/1"

// Export is the whole snapshot.
type Export struct {
	Schema       string        `json:"schema"`
	GeneratedAt  time.Time     `json:"generated_at"`
	PlumbVersion string        `json:"plumb_version"`
	Site         string        `json:"site,omitempty"` // free-text label of this Plumb installation, when set
	PeriodHours  float64       `json:"period_hours"`
	PeriodStart  time.Time     `json:"period_start"`
	PeriodEnd    time.Time     `json:"period_end"`
	MockData     bool          `json:"mock_data,omitempty"` // true when Plumb is running its built-in demo fleet — ARIA refuses to treat that as customer data
	Arrays       []ArrayExport `json:"arrays"`
	Events       []EventExport `json:"events,omitempty"`
}

// ArrayExport is one monitored system.
type ArrayExport struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Model      string `json:"model"`
	Vendor     string `json:"vendor"`
	Datacenter string `json:"datacenter,omitempty"`

	// Identity is present for ONTAP when the cluster answered; absent otherwise.
	Identity *Identity `json:"identity,omitempty"`

	Health       string `json:"health"` // good | watch | critical
	IssueCount   int    `json:"issue_count"`
	CoverageNote string `json:"coverage_note,omitempty"`
	// UpstreamSuspected is true when Plumb's own cross-panel rule concluded the
	// array looks healthy while the path in front of it does not.
	UpstreamSuspected bool `json:"upstream_suspected,omitempty"`

	Latency  *Latency             `json:"latency,omitempty"`
	Metrics  []MetricExport       `json:"metrics"`
	Findings []FindingExport      `json:"findings,omitempty"`
	Capacity []CapacityProjection `json:"capacity,omitempty"`
}

// Identity lets a consumer match the array to its own inventory record.
type Identity struct {
	ClusterName string         `json:"cluster_name,omitempty"`
	ClusterUUID string         `json:"cluster_uuid,omitempty"`
	Version     string         `json:"version,omitempty"`
	Nodes       []NodeIdentity `json:"nodes,omitempty"`
}

type NodeIdentity struct {
	Name   string `json:"name"`
	Serial string `json:"serial_number"`
	Model  string `json:"model,omitempty"`
}

// Latency is the vendor's headline latency metric — the one comparable
// "is this getting slower" signal across every vendor.
type Latency struct {
	MetricID string  `json:"metric_id"`
	Label    string  `json:"label"`
	Unit     string  `json:"unit"`
	Avg      float64 `json:"avg"`
	P95      float64 `json:"p95"`
	Max      float64 `json:"max"`
	TrendPct float64 `json:"trend_pct"`
}

// MetricExport is one metric's statistics for the period.
type MetricExport struct {
	ID             string       `json:"id"`
	Label          string       `json:"label"`
	Unit           string       `json:"unit"`
	Category       string       `json:"category"` // frontend | backend
	Severity       string       `json:"severity"` // good | watch | critical — worst seen in the period
	Samples        int          `json:"samples"`
	Min            float64      `json:"min"`
	Avg            float64      `json:"avg"`
	Max            float64      `json:"max"`
	P90            float64      `json:"p90"`
	P95            float64      `json:"p95"`
	P99            float64      `json:"p99"`
	WatchPct       float64      `json:"watch_pct"`
	CriticalPct    float64      `json:"critical_pct"`
	Episodes       int          `json:"episodes"`
	TrendPct       float64      `json:"trend_pct"`
	ThresholdLabel string       `json:"threshold_label,omitempty"`
	Watch          float64      `json:"watch"`
	Critical       float64      `json:"critical"`
	Analysis       string       `json:"analysis,omitempty"` // Plumb's own plain-language reading of the numbers
	Sparkline      [][2]float64 `json:"sparkline,omitempty"`
}

// FindingExport mirrors rules.Finding.
type FindingExport struct {
	Severity    string   `json:"severity"`
	Tag         string   `json:"tag"`
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	MetricID    string   `json:"metric_id,omitempty"`
	Investigate []string `json:"investigate,omitempty"`
	Remediate   []string `json:"remediate,omitempty"`
}

// CapacityProjection is a linear days-until-critical estimate for a capacity
// metric that is already above its watch line and still growing.
type CapacityProjection struct {
	MetricID       string  `json:"metric_id"`
	Label          string  `json:"label"`
	Unit           string  `json:"unit"`
	Current        float64 `json:"current"`
	Critical       float64 `json:"critical"`
	DaysToCritical float64 `json:"days_to_critical"`
}

// EventExport is one recent ONTAP EMS event.
type EventExport struct {
	ArrayID   string `json:"array_id"`
	ArrayName string `json:"array_name"`
	Time      string `json:"time"`
	Severity  string `json:"severity"`
	Name      string `json:"name"`
	Node      string `json:"node,omitempty"`
	Message   string `json:"message"`
}

// Downsample thins a series to at most max points by keeping the last point
// of each equal-width bucket, so the shape and the most recent value survive.
func Downsample(pts [][2]float64, max int) [][2]float64 {
	if max <= 0 || len(pts) <= max {
		return pts
	}
	out := make([][2]float64, 0, max)
	step := float64(len(pts)) / float64(max)
	for i := 1; i <= max; i++ {
		idx := int(float64(i)*step) - 1
		if idx >= len(pts) {
			idx = len(pts) - 1
		}
		out = append(out, pts[idx])
	}
	return out
}

// Authorized decides whether a request may read the export. With no token
// configured it allows everyone — the rest of Plumb's API is equally open and
// is meant to sit on a trusted network — and once a token is set it must be
// presented as a Bearer header, an X-Plumb-Token header, or ?token=.
func Authorized(r *http.Request, token string) bool {
	if token == "" {
		return true
	}
	got := ""
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		got = strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	if got == "" {
		got = r.Header.Get("X-Plumb-Token")
	}
	if got == "" {
		got = r.URL.Query().Get("token")
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}
