// Package mockdata generates a plausible-looking, entirely synthetic fleet
// in-process, for the Config tab's "Show mock data" toggle. It exists so a
// fresh install shows the full multi-vendor experience (fleet, findings,
// reports, export) with one click, without running the separate demo
// exporters or having any real arrays configured yet.
//
// It deliberately reuses rules.BuildFindings — the same findings/health
// logic real data goes through — so mock mode is a stand-in for the data
// source only, not a separate, possibly-diverging code path for the
// enrichment logic itself.
package mockdata

import (
	"math"
	"math/rand"
	"time"

	"plumb/internal/config"
	"plumb/internal/rules"
	"plumb/internal/vm"
)

// Array is one synthetic fleet member. Profile drives where its metrics sit
// relative to each metric's own watch/critical thresholds — see
// profileFactor — so this works for any vendor's thresholds file without
// per-metric hardcoded values.
type Array struct {
	ID      string
	Name    string
	Model   string
	Vendor  string
	Profile string // "healthy" | "watch" | "critical"
}

var Fleet = []Array{
	{"mock-fa-prod-east-01", "fa-prod-east-01", "FA-X90R2", config.VendorPureFlashArray, "critical"},
	{"mock-fa-prod-west-02", "fa-prod-west-02", "FA-X70R3", config.VendorPureFlashArray, "watch"},
	{"mock-fa-erp-cluster", "fa-erp-cluster", "FA-X70R3", config.VendorPureFlashArray, "healthy"},
	{"mock-fb-media-01", "fb-media-01", "FlashBlade//S", config.VendorPureFlashBlade, "watch"},
	{"mock-ontap-cluster-01", "ontap-cluster-01", "AFF A400", config.VendorNetAppONTAP, "critical"},
	{"mock-ontap-cluster-02", "ontap-cluster-02", "AFF A250", config.VendorNetAppONTAP, "healthy"},
	{"mock-sg-grid-01", "sg-grid-01", "StorageGRID", config.VendorNetAppStorageGRID, "watch"},
}

func GetArray(id string) (Array, bool) {
	for _, a := range Fleet {
		if a.ID == id {
			return a, true
		}
	}
	return Array{}, false
}

func (a Array) AsConfigArray() config.Array {
	return config.Array{ID: a.ID, Name: a.Name, Model: a.Model, Vendor: a.Vendor}
}

// profileFactor picks a center value and noise amplitude relative to a
// metric's own thresholds, so "critical" always renders meaningfully above
// that metric's critical line and "healthy" comfortably below its watch
// line, regardless of the metric's actual unit or scale.
//
// A "critical" array is deliberately front-end-critical but back-end-clean
// — not every metric pushed to critical uniformly — because that's the one
// pattern that demonstrates Plumb's flagship correlation finding ("bottleneck
// is likely upstream"). Making every metric on a bad array uniformly bad
// would be an easier implementation but a worse demo: it would never show
// the one piece of inferred intelligence the whole front-end/back-end split
// exists to produce. See rules.BuildFindings for the actual correlation
// logic this is set up to trigger.
func profileFactor(profile, category string, watch, critical float64) (center, noise float64) {
	effective := profile
	if profile == "critical" && category == "backend" {
		effective = "healthy"
	}
	spread := critical - watch
	if spread <= 0 {
		spread = math.Abs(critical)*0.2 + 1
	}
	switch effective {
	case "critical":
		return critical + spread*0.3, spread * 0.15
	case "watch":
		return (watch + critical) / 2, spread * 0.12
	default: // healthy
		return watch * 0.35, watch*0.12 + 0.01
	}
}

func hashSeed(s string) int64 {
	// FNV-1a, computed in uint64 (its natural width) then narrowed —
	// doing the arithmetic directly in int64 overflows the untyped
	// constant at compile time.
	var h uint64 = 14695981039346656037
	for _, c := range s {
		h ^= uint64(c)
		h *= 1099511628211
	}
	return int64(h & 0x7fffffffffffffff)
}

// GenerateSeries produces a synthetic time series for one (array, metric)
// pair: a stable center value (from profileFactor) plus a slow sine drift
// and random jitter, so charts look alive rather than flat, while staying
// seeded per array+metric so repeated requests within the same run don't
// jump around incoherently. category is "frontend" or "backend" — see
// profileFactor for why it matters.
func GenerateSeries(seed, profile, category string, watch, critical float64, start, end time.Time, step time.Duration) []vm.Point {
	center, noise := profileFactor(profile, category, watch, critical)
	r := rand.New(rand.NewSource(hashSeed(seed)))
	phase := r.Float64() * math.Pi * 2

	var pts []vm.Point
	i := 0.0
	for t := start; !t.After(end); t = t.Add(step) {
		wave := math.Sin(i/18+phase) * noise * 0.6
		jitter := (r.Float64()*2 - 1) * noise * 0.5
		value := center + wave + jitter
		if value < 0 {
			value = 0
		}
		pts = append(pts, vm.Point{Time: float64(t.Unix()), Value: value})
		i++
	}
	return pts
}

// EvaluateArray builds a full rules.Result for one mock array — same shape
// (panels, findings, health) a real evaluation produces, via the same
// rules.BuildFindings logic, over synthetic series instead of a
// VictoriaMetrics query.
func EvaluateArray(arr Array, metrics []config.MetricDef, window time.Duration) rules.Result {
	now := time.Now()
	start := now.Add(-window)
	step := window / 60
	if step < 15*time.Second {
		step = 15 * time.Second
	}

	panels := []rules.Panel{} // non-nil — see rules.EvaluateArray's identical comment
	for _, m := range metrics {
		series := GenerateSeries(arr.ID+"|"+m.ID, arr.Profile, m.Category, m.SeverityWatch, m.SeverityCritical, start, now, step)

		var valPtr *float64
		pairs := [][2]float64{}
		sev := rules.Unknown
		if len(series) > 0 {
			v := series[len(series)-1].Value
			valPtr = &v
			sev = rules.Classify(v, true, m.SeverityWatch, m.SeverityCritical)
			pairs = make([][2]float64, len(series))
			for i, p := range series {
				pairs[i] = [2]float64{p.Time, p.Value}
			}
		}

		panels = append(panels, rules.Panel{
			ID: m.ID, Label: m.Label, Unit: m.Unit, Category: m.Category,
			Value: valPtr, Severity: sev, ThresholdLabel: m.ThresholdLabel,
			Watch: m.SeverityWatch, Critical: m.SeverityCritical, Series: pairs,
		})
	}

	findings, health := rules.BuildFindings(arr.ID, metrics, panels)
	return rules.Result{Panels: panels, Findings: findings, Health: health}
}
