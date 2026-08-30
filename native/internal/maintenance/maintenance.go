// Package maintenance implements a deliberately narrow kind of "planned
// work" awareness: an operator can mute webhook notifications for one array
// (or the whole fleet) until a set time, for a known, expected disruption —
// a firmware upgrade, a migration, planned maintenance. It never hides or
// alters anything on the live dashboard or in reports: panels, findings,
// and report statistics stay completely honest always. Only whether a
// webhook fires is affected — the same distinction real monitoring tools
// (Nagios, PagerDuty) draw between "muted" and "resolved."
package maintenance

import (
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Window mutes notifications for ArrayID (or "*" for every array) until
// Until. Stored, not computed from a duration, so a restart doesn't reset
// the clock.
type Window struct {
	ArrayID string    `yaml:"array_id" json:"array_id"`
	Until   time.Time `yaml:"until" json:"until"`
	Note    string    `yaml:"note,omitempty" json:"note,omitempty"`
}

type file struct {
	Windows []Window `yaml:"windows"`
}

func path(configDir string) string { return filepath.Join(configDir, "maintenance.yml") }

func Load(configDir string) ([]Window, error) {
	b, err := os.ReadFile(path(configDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f file
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Windows, nil
}

func save(configDir string, windows []Window) error {
	b, err := yaml.Marshal(file{Windows: windows})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(path(configDir), b, 0o644)
}

// pruneExpired drops windows that have already lapsed, so the file doesn't
// accumulate stale entries across months of use.
func pruneExpired(windows []Window, now time.Time) []Window {
	kept := make([]Window, 0, len(windows))
	for _, w := range windows {
		if w.Until.After(now) {
			kept = append(kept, w)
		}
	}
	return kept
}

// Set opens (or replaces) the active window for arrayID. Only one window
// per array is tracked at a time — setting a new one for an array that
// already has one simply extends/changes it, rather than stacking windows
// that would need their own resolution logic for "which one wins."
func Set(configDir, arrayID string, until time.Time, note string, now time.Time) ([]Window, error) {
	existing, err := Load(configDir)
	if err != nil {
		return nil, err
	}
	kept := make([]Window, 0, len(existing)+1)
	for _, w := range pruneExpired(existing, now) {
		if w.ArrayID != arrayID {
			kept = append(kept, w)
		}
	}
	kept = append(kept, Window{ArrayID: arrayID, Until: until, Note: note})
	if err := save(configDir, kept); err != nil {
		return nil, err
	}
	return kept, nil
}

// Clear ends arrayID's maintenance window early, if it has one.
func Clear(configDir, arrayID string, now time.Time) ([]Window, error) {
	existing, err := Load(configDir)
	if err != nil {
		return nil, err
	}
	kept := make([]Window, 0, len(existing))
	for _, w := range pruneExpired(existing, now) {
		if w.ArrayID != arrayID {
			kept = append(kept, w)
		}
	}
	if err := save(configDir, kept); err != nil {
		return nil, err
	}
	return kept, nil
}

// Active reports whether arrayID is currently muted, either by a window
// naming it specifically or a fleet-wide ("*") one, and which window is
// responsible (the more specific one wins if both exist).
func Active(windows []Window, arrayID string, now time.Time) (bool, Window) {
	var fleetWide *Window
	for i := range windows {
		w := windows[i]
		if !w.Until.After(now) {
			continue
		}
		if w.ArrayID == arrayID {
			return true, w
		}
		if w.ArrayID == "*" {
			fleetWide = &windows[i]
		}
	}
	if fleetWide != nil {
		return true, *fleetWide
	}
	return false, Window{}
}
