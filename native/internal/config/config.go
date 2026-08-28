// Package config loads the two things a user actually edits: the array
// inventory (config/arrays.yml) and the per-vendor best-practice thresholds
// (config/thresholds/<vendor>.yml). Everything else in Plumb is derived
// from these two files.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	VendorPureFlashArray   = "pure_flasharray"
	VendorPureFlashBlade   = "pure_flashblade"
	VendorNetAppONTAP      = "netapp_ontap"
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
	MockData bool `yaml:"mock_data"`
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
