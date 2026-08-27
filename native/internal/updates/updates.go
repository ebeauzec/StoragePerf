// Package updates is check-and-notify only: it looks up newer releases of
// the bundled sidecars and reports what it finds. It never downloads or
// installs anything — this box sits next to production storage traffic, so
// nothing here changes what's running without a human deciding to.
package updates

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
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

var targets = []target{
	{"prometheus", "Prometheus", "prometheus/prometheus", "v3.14.0"},
	{"victoriametrics", "VictoriaMetrics", "VictoriaMetrics/VictoriaMetrics", "v1.150.0"},
	{"harvest", "NetApp Harvest", "NetApp/harvest", "v26.08.0"},
	{"pure_exporter_reference", "Pure metric-schema reference", "PureStorage-OpenConnect/pure-fa-openmetrics-exporter", ""},
}

const thresholdsCheckedAgainst = "2026-08-27"

type Checker struct {
	mu      sync.RWMutex
	last    []Check
	enabled bool
	client  *http.Client
}

func NewChecker(enabled bool) *Checker {
	c := &Checker{enabled: enabled, client: &http.Client{Timeout: 10 * time.Second}}
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

func (c *Checker) checkOnce() {
	results := make([]Check, len(targets))
	for i, t := range targets {
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
				avail := latest != t.current
				chk.UpdateAvailable = &avail
			}
		}
		results[i] = chk
	}
	if len(results) > 0 {
		results[len(results)-1].Note = fmt.Sprintf(
			"Informational only — config/thresholds/*.yml were checked against published vendor metric names on %s. A newer tag here doesn't mean your thresholds are wrong, just that it may be worth re-checking.",
			thresholdsCheckedAgainst,
		)
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
