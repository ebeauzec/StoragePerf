// Package notify sends a single generic webhook POST for a monitoring
// event. Deliberately just a webhook, not a bundled Slack/PagerDuty/email
// SDK: a plain HTTP POST with a JSON body (including a "text" field) is
// what Slack's own incoming-webhooks, PagerDuty's Events API v2 (via a
// thin proxy), Microsoft Teams, and generic automation tools (Zapier, n8n,
// a custom endpoint) all already accept — one integration point covers all
// of them without Plumb taking on credential storage for any of them.
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Config struct {
	Enabled     bool   `yaml:"enabled" json:"enabled"`
	WebhookURL  string `yaml:"webhook_url,omitempty" json:"webhook_url,omitempty"`
	MinSeverity string `yaml:"min_severity,omitempty" json:"min_severity,omitempty"` // "watch" | "critical"
}

func (c Config) EffectiveMinSeverity() string {
	if c.MinSeverity == "" {
		return "critical"
	}
	return c.MinSeverity
}

func severityRank(s string) int {
	switch s {
	case "critical":
		return 2
	case "watch":
		return 1
	default:
		return 0
	}
}

// Meets reports whether sev is at or above the configured minimum severity
// to notify on — the default (critical-only) is deliberately conservative:
// a fleet with several arrays sitting in watch would otherwise page someone
// on every poll cycle for conditions that are, by definition, not yet an
// emergency.
func (c Config) Meets(sev string) bool {
	return severityRank(sev) >= severityRank(c.EffectiveMinSeverity())
}

// Event is one notification-worthy occurrence: a finding newly opened or
// escalated, one resolved, a test ping from the Config tab, or a scheduled
// report becoming available.
type Event struct {
	Kind      string // "new" | "escalated" | "resolved" | "ems" | "test" | "report"
	ArrayID   string
	ArrayName string
	Vendor    string
	MetricID  string
	Label     string
	Severity  string
	Body      string
	Value     float64
	Unit      string
	Timestamp time.Time
}

func (e Event) summary() string {
	switch e.Kind {
	case "test":
		return "Plumb test notification — if you can see this, your webhook is configured correctly."
	case "resolved":
		return fmt.Sprintf("[RESOLVED] %s — %s is back to normal", e.ArrayName, e.Label)
	case "report":
		return e.Body
	default:
		return fmt.Sprintf("[%s] %s — %s: %s", strings.ToUpper(e.Severity), e.ArrayName, e.Label, e.Body)
	}
}

// Send POSTs ev as JSON to cfg.WebhookURL. The payload's top-level "text"
// field is what Slack's incoming-webhook integration renders directly, so
// pointing MinSeverity's URL at a Slack webhook needs no translation layer;
// every other field is there for a receiver that wants structured data
// instead (a generic automation tool, a custom endpoint).
func Send(client *http.Client, cfg Config, ev Event) error {
	if cfg.WebhookURL == "" {
		return fmt.Errorf("no webhook URL configured")
	}
	payload := map[string]any{
		"text":       ev.summary(),
		"event":      ev.Kind,
		"array_id":   ev.ArrayID,
		"array_name": ev.ArrayName,
		"vendor":     ev.Vendor,
		"metric_id":  ev.MetricID,
		"label":      ev.Label,
		"severity":   ev.Severity,
		"body":       ev.Body,
		"value":      ev.Value,
		"unit":       ev.Unit,
		"timestamp":  ev.Timestamp.Format(time.RFC3339),
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, cfg.WebhookURL, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook endpoint returned %s", resp.Status)
	}
	return nil
}
