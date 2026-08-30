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
)

// Array is one monitored system. Not every field applies to every vendor —
// Pure arrays use Host/Scheme/TokenEnv (scraped directly through our proxy);
// NetApp systems use ManagementLIF/Username/PasswordEnv (handed to a Harvest
// poller, which does its own authenticated collection).
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

	// NetApp ONTAP / StorageGRID (collected via a Harvest poller)
	ManagementLIF  string `yaml:"management_lif,omitempty" json:"management_lif,omitempty"`
	Username       string `yaml:"username,omitempty" json:"username,omitempty"`
	PasswordEnv    string `yaml:"password_env,omitempty" json:"password_env,omitempty"`
	Datacenter     string `yaml:"datacenter,omitempty" json:"datacenter,omitempty"`
	UseInsecureTLS bool   `yaml:"use_insecure_tls,omitempty" json:"use_insecure_tls,omitempty"`
}

func (a Array) IsNetApp() bool {
	return a.Vendor == VendorNetAppONTAP || a.Vendor == VendorNetAppStorageGRID
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
