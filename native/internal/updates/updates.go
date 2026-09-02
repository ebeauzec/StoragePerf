// Package updates looks up newer releases of the bundled sidecars, vendor
// metric-schema references, and Plumb itself, and reports what it finds.
// It is check-and-notify only for the sidecars and vendor references —
// this box sits next to production storage traffic, so nothing about them
// changes what's running without a human deciding to. Plumb's own entry is
// the one exception: internal/selfupdate can act on it, but only when a
// user explicitly clicks "Update now" — this package itself still only
// ever checks and reports.
package updates

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"plumb/internal/selfupdate"
)

type Check struct {
	ID              string  `json:"id"`
	Label           string  `json:"label"`
	Current         string  `json:"current"`
	Latest          *string `json:"latest"`
	UpdateAvailable *bool   `json:"update_available"`
	Status          string  `json:"status"`
	URL             string  `json:"url"`
	CheckedAt       *int64  `json:"checked_at"`
	Note            string  `json:"note,omitempty"`
}

type target struct {
	id, label, repo, current string
}

// current == "" marks an informational-only reference (no bundled binary,
// no version comparison) — see the note attached to these in checkOnce.
// "plumb"'s current field is left blank here and filled in per-Checker by
// NewChecker with the actual running version, since that's a runtime
// value, not something this static list can hold.
var baseTargets = []target{
	{"plumb", "Plumb", selfupdate.Repo, ""},
	{"prometheus", "Prometheus", "prometheus/prometheus", "v3.14.0"},
	{"victoriametrics", "VictoriaMetrics", "VictoriaMetrics/VictoriaMetrics", "v1.150.0"},
	{"pure_exporter_reference", "Pure metric-schema reference", "PureStorage-OpenConnect/pure-fa-openmetrics-exporter", ""},
	{"harvest_reference", "NetApp Harvest metric-schema reference", "NetApp/harvest", ""},
}

// PlumbTargetID is targets[0].id above — used by the API layer to find
// Plumb's own entry in a Snapshot() without hardcoding the string twice.
const PlumbTargetID = "plumb"

const thresholdsCheckedAgainst = "2026-08-27"

type Checker struct {
	mu      sync.RWMutex
	last    []Check
	enabled bool
	client  *http.Client
	targets []target
}

// NewChecker builds a checker for the sidecars, vendor references, and
// Plumb itself — plumbVersion is the running binary's own version
// (cmd/plumb's -ldflags-injected main.version), used as Plumb's "current"
// for comparison against the latest GitHub release, the same way the
// sidecars compare against their own hardcoded current versions.
func NewChecker(enabled bool, plumbVersion string) *Checker {
	targets := make([]target, len(baseTargets))
	copy(targets, baseTargets)
	for i := range targets {
		if targets[i].id == PlumbTargetID {
			targets[i].current = plumbVersion
		}
	}

	c := &Checker{enabled: enabled, client: &http.Client{Timeout: 10 * time.Second}, targets: targets}
	c.last = make([]Check, len(targets))
	for i, t := range targets {
		status := "not checked yet"
		if !enabled {
			status = "disabled (check-for-updates turned off)"
		}
		c.last[i] = Check{ID: t.id, Label: t.label, Current: t.current, Status: status, URL: fmt.Sprintf("https://github.com/%s/releases", t.repo)}
	}
	return c
}

// PlumbCheck returns Plumb's own entry from the last check, and whether
// checking is enabled at all — the API layer re-validates against this
// before acting on a self-update request rather than trusting the
// client's word that an update is available.
func (c *Checker) PlumbCheck() (Check, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, chk := range c.last {
		if chk.ID == PlumbTargetID {
			return chk, c.enabled
		}
	}
	return Check{}, c.enabled
}

func (c *Checker) Snapshot() (bool, []Check) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Check, len(c.last))
	copy(out, c.last)
	return c.enabled, out
}

func (c *Checker) fetchLatestTag(repo string) (string, error) {
	req, _ := http.NewRequest("GET", fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo), nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		var body struct {
			TagName string `json:"tag_name"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err == nil && body.TagName != "" {
			return body.TagName, nil
		}
	}

	req2, _ := http.NewRequest("GET", fmt.Sprintf("https://api.github.com/repos/%s/tags?per_page=1", repo), nil)
	resp2, err := c.client.Do(req2)
	if err != nil {
		return "", err
	}
	defer resp2.Body.Close()
	var tags []struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&tags); err != nil || len(tags) == 0 {
		return "", fmt.Errorf("no releases or tags found")
	}
	return tags[0].Name, nil
}

func normalizeVersion(v string) string {
	return strings.TrimPrefix(v, "v")
}

func (c *Checker) checkOnce() {
	results := make([]Check, len(c.targets))
	for i, t := range c.targets {
		chk := Check{ID: t.id, Label: t.label, Current: t.current, URL: fmt.Sprintf("https://github.com/%s/releases", t.repo)}
		now := time.Now().Unix()
		chk.CheckedAt = &now

		latest, err := c.fetchLatestTag(t.repo)
		if err != nil {
			chk.Status = "unreachable"
		} else {
			chk.Latest = &latest
			chk.Status = "ok"
			if t.current != "" {
				// Plain != would always report "update available" for
				// Plumb specifically: GitHub tags this repo "v0.8.6", but
				// the embedded binary version (main.version, from the bare
				// VERSION file) is "0.8.6" with no "v". The sidecars'
				// hardcoded "current" strings already include their own
				// "v" prefix matching their upstream tags, so normalizing
				// both sides is a no-op for them and the fix for Plumb.
				avail := normalizeVersion(latest) != normalizeVersion(t.current)
				chk.UpdateAvailable = &avail
			}
		}
		if t.current == "" {
			chk.Note = fmt.Sprintf(
				"Informational only — config/thresholds/*.yml and the native NetApp collector (internal/netappnative) were checked against this project's published metric names and source on %s. A newer tag here doesn't mean they're wrong, just that it may be worth re-checking.",
				thresholdsCheckedAgainst,
			)
		}
		results[i] = chk
	}

	c.mu.Lock()
	c.last = results
	c.mu.Unlock()
}

// Run checks immediately, then on the given interval, until stopCh closes.
// Does nothing at all if the checker was constructed with enabled=false —
// no network access, no background goroutine work, safe for a fully
// air-gapped deployment.
func (c *Checker) Run(interval time.Duration, stopCh <-chan struct{}) {
	if !c.enabled {
		return
	}
	c.checkOnce()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			c.checkOnce()
		}
	}
}
