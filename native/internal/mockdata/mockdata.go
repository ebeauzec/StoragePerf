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
// relative to each metric's own watch/critical thresholds by default — see
// bandFactor — so this works for any vendor's thresholds file without
// per-metric hardcoded values. Overrides pins one specific metric ID to an
// explicit severity band regardless of Profile, for scenario systems that
// need a precise, realistic pattern (e.g. "only the controller is
// overloaded, everything else is fine") rather than an entire front-end or
// back-end category moving together.
type Array struct {
	ID        string
	Name      string
	Model     string
	Vendor    string
	Profile   string            // "healthy" | "watch" | "critical" — the default for any metric without an override
	Overrides map[string]string // metric ID -> explicit severity, takes precedence over Profile
}

// SeverityFor resolves the target band for one metric: its explicit
// override if set, else the array's overall Profile — with the same
// critical+backend->healthy flip profileFactor always applied, since that
// flip is what makes an unmodified "critical" Profile demonstrate the
// upstream-bottleneck correlation finding. An override always means
// exactly what it says, with no implicit category flip, since scenario
// systems below use overrides specifically to defeat that generalization
// and model something more specific.
func (a Array) SeverityFor(metricID, category string) string {
	if s, ok := a.Overrides[metricID]; ok {
		return s
	}
	severity := a.Profile
	if severity == "critical" && category == "backend" {
		severity = "healthy"
	}
	return severity
}

var Fleet = []Array{
	{ID: "mock-fa-prod-east-01", Name: "fa-prod-east-01", Model: "FA-X90R2", Vendor: config.VendorPureFlashArray, Profile: "critical"},
	{ID: "mock-fa-prod-west-02", Name: "fa-prod-west-02", Model: "FA-X70R3", Vendor: config.VendorPureFlashArray, Profile: "watch"},
	{ID: "mock-fa-erp-cluster", Name: "fa-erp-cluster", Model: "FA-X70R3", Vendor: config.VendorPureFlashArray, Profile: "healthy"},
	{ID: "mock-fb-media-01", Name: "fb-media-01", Model: "FlashBlade//S", Vendor: config.VendorPureFlashBlade, Profile: "watch"},
	{ID: "mock-ontap-cluster-01", Name: "ontap-cluster-01", Model: "AFF A400", Vendor: config.VendorNetAppONTAP, Profile: "critical"},
	{ID: "mock-ontap-cluster-02", Name: "ontap-cluster-02", Model: "AFF A250", Vendor: config.VendorNetAppONTAP, Profile: "healthy"},
	{ID: "mock-sg-grid-01", Name: "sg-grid-01", Model: "StorageGRID", Vendor: config.VendorNetAppStorageGRID, Profile: "watch"},

	// --- Three additional, deliberately distinct failure patterns ---
	//
	// The seven systems above only ever show two shapes: uniformly healthy/
	// watch, or the flagship "front-end bad, back-end spotless" pattern that
	// triggers the upstream-bottleneck finding. Real fleets also produce
	// patterns that DON'T fit that shape — where the array genuinely is the
	// problem, or where a real issue is real but narrow enough that a
	// latency-only dashboard would miss it. These three exist to show that
	// Plumb's correlation logic isn't just pattern-matching "front-end
	// worse than back-end" — it only fires when the back-end is genuinely
	// clean, and stays silent (correctly) for these three, leaving the
	// per-metric findings to tell the real story instead.

	// 1. Back-end saturation with front-end fallout — the mirror image of
	// the flagship pattern. Both node_cpu_busy and aggr_disk_busy critical
	// (the controller and the disks behind it are both genuinely maxed
	// out), which drags volume_avg_latency into watch as a real downstream
	// symptom — not critical on its own, just elevated, which is exactly
	// what makes this the common misdiagnosis: the on-call engineer sees
	// "latency is up" and reflexively blames the network, when the array
	// itself is the actual bottleneck. No correlation finding fires here,
	// because the back-end is not clean — that absence is itself the
	// diagnostic signal.
	{
		ID: "mock-ontap-cluster-03", Name: "ontap-cluster-03", Model: "AFF A400", Vendor: config.VendorNetAppONTAP,
		Profile: "healthy",
		Overrides: map[string]string{
			"node_cpu_busy":            "critical",
			"aggr_disk_busy":           "critical",
			"volume_avg_latency":       "watch",
			"volume_avg_latency_read":  "watch",
			"volume_avg_latency_write": "watch",
		},
	},

	// 2. Capacity-driven degradation — pool_saturation critical (the array
	// is genuinely almost full), with host_latency and replication_lag
	// both nudged into watch as real, correlated side effects: garbage
	// collection/space reclamation competing for the same resources slows
	// both host I/O and the replication pipeline. Queue depth and network
	// errors stay clean, since neither is causally related to capacity
	// pressure. This is a commonly under-diagnosed pattern because teams
	// watch performance dashboards and capacity dashboards separately, and
	// rarely correlate a slowly climbing capacity trend with the latency
	// graph drifting upward alongside it.
	{
		ID: "mock-fa-capacity-01", Name: "fa-capacity-01", Model: "FA-X70R3", Vendor: config.VendorPureFlashArray,
		Profile: "healthy",
		Overrides: map[string]string{
			"pool_saturation": "critical",
			"host_latency":    "watch",
			"replication_lag": "watch",
		},
	},

	// 3. Isolated ILM backlog — every client-facing and node-level metric
	// stays clean; only ilm_backlog is critical. StorageGRID's lifecycle-
	// management backlog is the one metric on any vendor Plumb supports
	// with no equivalent elsewhere (see docs/METRICS-REFERENCE.md §5), and
	// it's specifically documented to rise before client-facing latency
	// does — this system demonstrates exactly that: a real, worsening
	// problem that's invisible on every metric except this one, making it
	// a genuine early-warning case rather than an already-obvious outage.
	{
		ID: "mock-sg-grid-02", Name: "sg-grid-02", Model: "StorageGRID", Vendor: config.VendorNetAppStorageGRID,
		Profile: "healthy",
		Overrides: map[string]string{
			"ilm_backlog": "critical",
		},
	},

	// 4. Replication lag with a clean capacity signal — demonstrates the
	// rules engine's second cross-panel correlation finding (the one
	// beyond front-end/back-end): host_latency critical (which, via
	// pure.go's shared metricID design, cascades to host_latency_write
	// too) and replication_lag critical, while pool_saturation stays
	// healthy. That combination should read as local write-path
	// contention — something competing for the write path, not the
	// replication link or a capacity-driven side effect — since capacity
	// is explicitly ruled out here, unlike mock-fa-capacity-01 above where
	// it's the actual cause.
	{
		ID: "mock-fa-writecontention-01", Name: "fa-writecontention-01", Model: "FA-X70R3", Vendor: config.VendorPureFlashArray,
		Profile: "healthy",
		Overrides: map[string]string{
			"host_latency":    "critical",
			"replication_lag": "critical",
			"pool_saturation": "healthy",
		},
	},
}

func (a Array) AsConfigArray() config.Array {
	return config.Array{ID: a.ID, Name: a.Name, Model: a.Model, Vendor: a.Vendor}
}

func (a Array) IsNetApp() bool {
	return a.Vendor == config.VendorNetAppONTAP || a.Vendor == config.VendorNetAppStorageGRID
}

// bandFactor picks a center value and noise amplitude that render
// meaningfully inside the given severity band relative to a metric's own
// thresholds — "critical" comfortably above the critical line, "watch"
// between watch and critical, "healthy" comfortably below watch — for any
// metric's own unit or scale. See Array.SeverityFor for how a metric's
// target band is chosen (Profile by default, or an explicit Overrides
// entry for scenario systems targeting one specific metric).
func bandFactor(severity string, watch, critical float64) (center, noise float64) {
	spread := critical - watch
	if spread <= 0 {
		spread = math.Abs(critical)*0.2 + 1
	}
	switch severity {
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
// t, using the array's per-metric severity (Overrides, or Profile with its
// category flip). internal/mockbackend calls this on every request to its
// mock endpoints — VictoriaMetrics builds up the historical series over
// real scrape cycles from these live values, exactly like a real array,
// rather than this package pre-computing a series itself.
//
// metricID identifies which metric this is for Overrides lookup; seed is
// the wave-function key and may differ from metricID (e.g. a metric with
// separate read/write dimensions uses one CurrentValue call per dimension,
// each with its own seed, but both resolve the same metricID's severity).
func (a Array) CurrentValue(metricID, seed, category string, watch, critical float64, t time.Time) float64 {
	center, noise := bandFactor(a.SeverityFor(metricID, category), watch, critical)
	return valueAt(seed, t, center, noise)
}

// ValueForSeverity is CurrentValue with an explicit severity band instead
// of one resolved from the array's Profile/Overrides — for simulating a
// per-node breakdown, where most nodes should look healthy regardless of
// the array's own severity, and (when the array isn't healthy) exactly one
// node should carry it, rather than every node uniformly showing whatever
// the grid-wide severity is.
func (a Array) ValueForSeverity(seed, severity string, watch, critical float64, t time.Time) float64 {
	center, noise := bandFactor(severity, watch, critical)
	return valueAt(seed, t, center, noise)
}
