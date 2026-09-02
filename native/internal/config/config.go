// Package config loads the two things a user actually edits: the array
// inventory (config/arrays.yml) and the per-vendor best-practice thresholds
// (config/thresholds/<vendor>.yml). Everything else in Plumb is derived
// from these two files.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	VendorPureFlashArray    = "pure_flasharray"
	VendorPureFlashBlade    = "pure_flashblade"
	VendorNetAppONTAP       = "netapp_ontap"
	VendorNetAppStorageGRID = "netapp_storagegrid"
	VendorNetAppESeries     = "netapp_eseries"
)

// Array is one monitored system. Not every field applies to every vendor —
// Pure arrays use Host/Scheme/TokenEnv (scraped directly through our proxy);
// NetApp systems use ManagementLIF/Username/PasswordEnv, collected
// in-process by internal/netappnative directly against the cluster's own
// REST API — no separate poller process.
type Array struct {
	ID     string `yaml:"id" json:"id"`
	Name   string `yaml:"name" json:"name"`
	Model  string `yaml:"model" json:"model"`
	Vendor string `yaml:"vendor" json:"vendor"`

	// Pure FlashArray / FlashBlade
	Host        string `yaml:"host,omitempty" json:"host,omitempty"`
	Scheme      string `yaml:"scheme,omitempty" json:"scheme,omitempty"`
	MetricsPath string `yaml:"metrics_path,omitempty" json:"metrics_path,omitempty"`
	TokenEnv    string `yaml:"token_env,omitempty" json:"token_env,omitempty"`
	VerifyTLS   bool   `yaml:"verify_tls,omitempty" json:"verify_tls,omitempty"`

	// NetApp ONTAP / StorageGRID / E-Series (collected in-process — see internal/netappnative).
	// E-Series' embedded SANtricity REST API uses the same basic-auth-over-HTTPS
	// shape as ONTAP's, so it reuses these same fields rather than needing its own.
	ManagementLIF  string `yaml:"management_lif,omitempty" json:"management_lif,omitempty"`
	Username       string `yaml:"username,omitempty" json:"username,omitempty"`
	PasswordEnv    string `yaml:"password_env,omitempty" json:"password_env,omitempty"`
	Datacenter     string `yaml:"datacenter,omitempty" json:"datacenter,omitempty"`
	UseInsecureTLS bool   `yaml:"use_insecure_tls,omitempty" json:"use_insecure_tls,omitempty"`
}

func (a Array) IsNetApp() bool {
	return a.Vendor == VendorNetAppONTAP || a.Vendor == VendorNetAppStorageGRID || a.Vendor == VendorNetAppESeries
}

type arraysFile struct {
	Arrays []Array `yaml:"arrays"`
}

func arraysPath(configDir string) string { return filepath.Join(configDir, "arrays.yml") }

func LoadArrays(configDir string) ([]Array, error) {
	b, err := os.ReadFile(arraysPath(configDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f arraysFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parsing arrays.yml: %w", err)
	}
	return f.Arrays, nil
}

func SaveArrays(configDir string, arrays []Array) error {
	b, err := yaml.Marshal(arraysFile{Arrays: arrays})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(arraysPath(configDir), b, 0o644)
}

// Switch is a network switch whose uplink ports carry traffic for one or
// more monitored arrays' front-end (host/SAN) path. This is what lets
// Plumb's front-end/back-end correlation finding (internal/rules.go) cite
// real port evidence instead of only inferring "probably the network" by
// elimination — see SwitchLink's own doc comment for why the port mapping,
// not the collector API itself, is the actual design center of this.
//
// Platform selects which management API this switch's own credentials are
// handed to: "cisco_nxos" (NX-API, JSON-RPC over HTTPS) or "arista_eos"
// (eAPI, same JSON-RPC shape) — see internal/switchnative. Both are local
// management-plane calls to hardware the customer already owns, same trust
// model as every other collector in this project.
type Switch struct {
	ID                string       `yaml:"id" json:"id"`
	Name              string       `yaml:"name" json:"name"`
	Platform          string       `yaml:"platform" json:"platform"` // "cisco_nxos" | "arista_eos"
	ManagementAddress string       `yaml:"management_address" json:"management_address"`
	Username          string       `yaml:"username" json:"username"`
	PasswordEnv       string       `yaml:"password_env,omitempty" json:"password_env,omitempty"`
	UseInsecureTLS    bool         `yaml:"use_insecure_tls,omitempty" json:"use_insecure_tls,omitempty"`
	Links             []SwitchLink `yaml:"links" json:"links"`
}

const (
	SwitchPlatformCiscoNXOS = "cisco_nxos"
	SwitchPlatformAristaEOS = "arista_eos"
)

// SwitchLink says which of this switch's ports carry traffic for one
// specific array. A switch can link multiple arrays (different port
// groups), and — less commonly, e.g. a dual-homed host path — an array
// could in principle be linked from more than one switch; this is why the
// mapping lives on the switch (one YAML file, one place an operator who
// knows the physical wiring edits it) rather than duplicated per-array.
// Without this mapping a switch's telemetry has no way to be attributed to
// a specific array's own correlation finding, which is the actual reason
// this type exists at all, not just to model the collector API.
type SwitchLink struct {
	ArrayID string   `yaml:"array_id" json:"array_id"`
	Ports   []string `yaml:"ports" json:"ports"`
}

type switchesFile struct {
	Switches []Switch `yaml:"switches"`
}

func switchesPath(configDir string) string { return filepath.Join(configDir, "switches.yml") }

func LoadSwitches(configDir string) ([]Switch, error) {
	b, err := os.ReadFile(switchesPath(configDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f switchesFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parsing switches.yml: %w", err)
	}
	return f.Switches, nil
}

func SaveSwitches(configDir string, switches []Switch) error {
	b, err := yaml.Marshal(switchesFile{Switches: switches})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(switchesPath(configDir), b, 0o644)
}

// ArrayLink pairs a Switch with just the ports of it that are linked to one
// particular array — what LinksForArray returns, and all a caller scraping
// one array's switch metrics needs (it doesn't care about that switch's
// other links to other arrays).
type ArrayLink struct {
	Switch Switch
	Ports  []string
}

// LinksForArray returns every port, across every configured switch, linked
// to the given array — almost always zero or one switch's worth, but a
// dual-homed array's ports could span two.
func LinksForArray(switches []Switch, arrayID string) []ArrayLink {
	var out []ArrayLink
	for _, sw := range switches {
		for _, link := range sw.Links {
			if link.ArrayID == arrayID && len(link.Ports) > 0 {
				out = append(out, ArrayLink{Switch: sw, Ports: link.Ports})
			}
		}
	}
	return out
}

func GetArray(configDir, id string) (Array, bool, error) {
	arrays, err := LoadArrays(configDir)
	if err != nil {
		return Array{}, false, err
	}
	for _, a := range arrays {
		if a.ID == id {
			return a, true, nil
		}
	}
	return Array{}, false, nil
}

// MetricDef is one row of a vendor's thresholds file — see
// config/thresholds/*.yml for the authoritative field documentation.
type MetricDef struct {
	ID               string            `yaml:"id"`
	Category         string            `yaml:"category"`
	Label            string            `yaml:"label"`
	Unit             string            `yaml:"unit"`
	Query            string            `yaml:"query"`
	Comparison       string            `yaml:"comparison"`
	SeverityWatch    float64           `yaml:"severity_watch"`
	SeverityCritical float64           `yaml:"severity_critical"`
	ThresholdLabel   string            `yaml:"threshold_label"`
	Finding          map[string]string `yaml:"finding"`
	Investigate      []string          `yaml:"investigate"`
	Remediate        []string          `yaml:"remediate"`

	// NodeBreakdownQuery is optional: a PromQL template (same {array}
	// substitution as Query) that returns one series per node, each
	// labeled `node`, for a metric whose Query collapses a multi-node
	// system into one grid/cluster-wide number. When set, the panel this
	// metric produces also carries a per-node breakdown — see
	// config/thresholds/netapp_storagegrid.yml's use of it and
	// internal/netappnative/storagegrid.go's `_by_node` metrics for where
	// the underlying per-node series come from.
	NodeBreakdownQuery string `yaml:"node_breakdown_query,omitempty"`

	// Informational marks a metric where higher is not inherently worse —
	// IOPS, bandwidth, ops/sec panels shipped as "workload characterization"
	// (their own threshold_label says "illustrative only — set from your
	// own baseline", an admission the number means nothing on its own).
	// SeverityWatch/Critical still exist for these (the chart still needs
	// a reference line, and Suggested Thresholds still wants a P90/P99 to
	// compare against), but rules.Classify never returns Watch/Critical for
	// one — a "critical" badge on "throughput is high" is actively
	// misleading, not a fact worth alarming on.
	Informational bool `yaml:"informational,omitempty"`

	// EscalateToNodeSeverity is optional and only meaningful alongside
	// NodeBreakdownQuery: when true, a node/disk in the breakdown reporting
	// worse than the fleet-wide value escalates this metric's own panel
	// badge, and the array's overall health, to match — not just a
	// separate node-level finding (that finding fires either way).
	//
	// This is NOT simply "does the metric have a breakdown" — it's "does
	// this platform have any redundancy or automatic compensation that
	// absorbs one bad node without the rest of the system inheriting the
	// problem." That answer is architecture-specific, confirmed per metric
	// against each vendor's own documentation rather than assumed:
	//
	//   - ONTAP (node_cpu_busy, aggr_disk_busy): true. An aggregate, and
	//     the disks/CPU behind it, is owned by exactly one node in the HA
	//     pair (docs.netapp.com/us-en/ontap/disks-aggregates) with no
	//     automatic rebalancing — moving a workload off a hot node takes a
	//     deliberate vol move or aggregate relocate. In a small (typically
	//     2-node) cluster, one hot node is a large fraction of total
	//     capacity with nothing else picking up the slack, so it's
	//     correct for the whole cluster's badge to read the node's actual
	//     severity, not the average that dilutes it away.
	//   - StorageGRID (all nine of its NodeBreakdownQuery metrics): false.
	//     Object data itself is deliberately spread across nodes with
	//     redundancy (erasure coding tolerates losing multiple fragments;
	//     replication keeps 2+ copies), and metadata reads need only a
	//     QUORUM of an object's own replicas, not every node
	//     (docs.netapp.com/us-en/storagegrid/s3/consistency.html). S3
	//     traffic is actively load-balanced away from busy nodes by CPU
	//     weighting (docs.netapp.com/us-en/storagegrid/admin/
	//     managing-load-balancing.html) — the platform is explicitly
	//     engineered to absorb exactly this kind of single-node
	//     degradation. Escalating the whole grid to Critical because one
	//     of possibly dozens of nodes is hot would misstate the actual
	//     blast radius and risk alert fatigue that masks a real
	//     grid-wide problem (most/all nodes elevated at once) later. The
	//     node-level finding still fires and still names the specific
	//     node — that's the right amount of alarm for a redundant system.
	//   - ONTAP (volume_avg_latency, volume_space_used_percent,
	//     volume_snapshot_used_percent, lun_space_used_percent,
	//     qtree_quota_used_percent): false, for a different reason than
	//     StorageGRID above — not redundancy, but cardinality. A cluster
	//     typically has 2-8 nodes/aggregates, so one bad entry really is a
	//     large fraction of the system; it can have hundreds to thousands
	//     of volumes/LUNs/qtrees, so one hot entry out of that many
	//     crossing critical is a real problem for that entry's own
	//     workload (and still gets its own finding below) but isn't
	//     representative of the array as a whole the way a hot node is.
	EscalateToNodeSeverity bool `yaml:"escalate_to_node_severity,omitempty"`
}

type thresholdsFile struct {
	Metrics []MetricDef `yaml:"metrics"`
}

// thresholdsFileName maps a vendor id to its YAML file under config/thresholds/.
func thresholdsFileName(vendor string) string {
	return vendor + ".yml"
}

// Settings holds small UI-toggleable app settings, persisted separately
// from arrays.yml since they're operational preferences, not inventory.
type Settings struct {
	MockData        bool   `yaml:"mock_data"`
	RetentionPeriod string `yaml:"retention_period,omitempty"`

	NotifyEnabled     bool   `yaml:"notify_enabled,omitempty"`
	NotifyWebhookURL  string `yaml:"notify_webhook_url,omitempty"`
	NotifyMinSeverity string `yaml:"notify_min_severity,omitempty"` // "watch" | "critical"

	ScheduledReportsEnabled bool    `yaml:"scheduled_reports_enabled,omitempty"`
	ScheduledReportInterval string  `yaml:"scheduled_report_interval,omitempty"` // "daily" | "weekly"
	ScheduledReportHours    float64 `yaml:"scheduled_report_hours,omitempty"`    // period each generated report covers
}

// ScheduleOptions is the whitelist the Config tab's schedule-frequency
// dropdown offers — each pairs how often a report is generated with how
// much history it covers, since a daily report covering 30 days would be
// almost entirely a repeat of the previous day's.
var ScheduleOptions = []struct {
	Value, Label string
	Interval     time.Duration
	ReportHours  float64
}{
	{"daily", "Daily (last 24h)", 24 * time.Hour, 24},
	{"weekly", "Weekly (last 7d)", 7 * 24 * time.Hour, 24 * 7},
}

func ValidScheduleInterval(v string) bool {
	for _, o := range ScheduleOptions {
		if o.Value == v {
			return true
		}
	}
	return false
}

func ScheduleIntervalDuration(v string) time.Duration {
	for _, o := range ScheduleOptions {
		if o.Value == v {
			return o.Interval
		}
	}
	return 24 * time.Hour
}

func ScheduleReportHours(v string) float64 {
	for _, o := range ScheduleOptions {
		if o.Value == v {
			return o.ReportHours
		}
	}
	return 24
}

func ValidNotifySeverity(v string) bool {
	return v == "watch" || v == "critical"
}

// DefaultRetentionPeriod matches VictoriaMetrics's own default of keeping
// everything — a fresh settings.yml (or one predating this field) should
// behave exactly as Plumb always has, not suddenly start purging data.
const DefaultRetentionPeriod = "100y"

// RetentionOptions is the whitelist of values the Config tab's retention
// dropdown offers and the API accepts — VictoriaMetrics's -retentionPeriod
// flag feeds a child-process argv, so this is validated against an exact
// set rather than a free-form duration parser.
var RetentionOptions = []struct{ Value, Label string }{
	{"1w", "1 week"},
	{"1M", "1 month"},
	{"3M", "3 months"},
	{"6M", "6 months"},
	{"1y", "1 year"},
	{"2y", "2 years"},
	{"5y", "5 years"},
	{DefaultRetentionPeriod, "Unlimited"},
}

func ValidRetentionPeriod(v string) bool {
	for _, o := range RetentionOptions {
		if o.Value == v {
			return true
		}
	}
	return false
}

// EffectiveRetentionPeriod returns the configured period, or the default if
// unset (a zero-value Settings{} from a missing settings.yml).
func (s Settings) EffectiveRetentionPeriod() string {
	if s.RetentionPeriod == "" {
		return DefaultRetentionPeriod
	}
	return s.RetentionPeriod
}

func settingsPath(configDir string) string { return filepath.Join(configDir, "settings.yml") }

func LoadSettings(configDir string) (Settings, error) {
	b, err := os.ReadFile(settingsPath(configDir))
	if err != nil {
		if os.IsNotExist(err) {
			return Settings{}, nil
		}
		return Settings{}, err
	}
	var s Settings
	if err := yaml.Unmarshal(b, &s); err != nil {
		return Settings{}, fmt.Errorf("parsing settings.yml: %w", err)
	}
	return s, nil
}

func SaveSettings(configDir string, s Settings) error {
	b, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(settingsPath(configDir), b, 0o644)
}

func LoadThresholds(configDir, vendor string) ([]MetricDef, error) {
	p := filepath.Join(configDir, "thresholds", thresholdsFileName(vendor))
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("no thresholds file for vendor %q (expected %s): %w", vendor, p, err)
	}
	var f thresholdsFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", p, err)
	}
	return f.Metrics, nil
}
