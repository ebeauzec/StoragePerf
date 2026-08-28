// Package mockdata defines the synthetic seven-system, four-vendor fleet
// behind the Config tab's "Show mock data" toggle, and the wave-function
// math that gives each of its metrics a believable, moving value. It does
// not evaluate panels or findings itself — internal/mockbackend serves
// these values over real HTTP endpoints (Pure OpenMetrics text, ONTAP's
// REST API, StorageGRID's Grid Management API), and Plumb's real
// collection pipeline (Prometheus, VictoriaMetrics, rules.EvaluateArray)
// takes it from there exactly as it would for a real array. That's
// deliberate: mock mode is a stand-in for real systems, not a separate,
// possibly-diverging code path for the enrichment logic.
package mockdata

import (
	"fmt"
	"math"
	"math/rand"
	"time"

	"plumb/internal/config"
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

func (a Array) AsConfigArray() config.Array {
	return config.Array{ID: a.ID, Name: a.Name, Model: a.Model, Vendor: a.Vendor}
}

func (a Array) IsNetApp() bool {
	return a.Vendor == config.VendorNetAppONTAP || a.Vendor == config.VendorNetAppStorageGRID
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

// valueAt computes one point's value purely as a function of (seed,
// absolute timestamp) — deliberately not of the point's position within
// whatever loop is currently generating a series. An earlier version
// walked a single seeded RNG once per generated point, which meant the
// value at "index 5" was always the same regardless of what real time it
// represented — so a 1H chart and a 24H chart, both built from ~60 points,
// produced literally identical curves, just with different axis labels.
// Keying everything off wall-clock time instead means:
//   - the same timestamp always maps to the same value, however many
//     different time-range requests happen to include it (the "current"
//     reading is stable across 1H/24H/7D views taken moments apart)
//   - different windows actually look different, because a 7D view
//     samples a slow multi-day wave a 1H view never gets far enough into
func valueAt(seed string, t time.Time, center, noise float64) float64 {
	ts := float64(t.Unix())
	phaseFast := float64(hashSeed(seed+"|fast")%1000) / 1000 * math.Pi * 2
	phaseSlow := float64(hashSeed(seed+"|slow")%1000) / 1000 * math.Pi * 2

	fast := math.Sin(ts/1200+phaseFast) * noise * 0.45   // ~20 min period — visible motion on 1H/24H views
	slow := math.Sin(ts/129600+phaseSlow) * noise * 0.35 // ~36 hour period — gives 7D/30D/1Y views their own shape

	jitterSeed := hashSeed(fmt.Sprintf("%s|%d", seed, int64(ts)))
	jitter := (rand.New(rand.NewSource(jitterSeed)).Float64()*2 - 1) * noise * 0.4

	v := center + fast + slow + jitter
	if v < 0 {
		v = 0
	}
	return v
}

// CurrentValue returns the synthetic value for one (array, metric) at time
// t. internal/mockbackend calls this on every request to its mock
// endpoints — VictoriaMetrics builds up the historical series over real
// scrape cycles from these live values, exactly like a real array, rather
// than this package pre-computing a series itself.
func CurrentValue(seed, profile, category string, watch, critical float64, t time.Time) float64 {
	center, noise := profileFactor(profile, category, watch, critical)
	return valueAt(seed, t, center, noise)
}
