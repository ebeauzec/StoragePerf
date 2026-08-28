// Package netappnative replaces NetApp's official Harvest collector with a
// small, purpose-built collector living in Plumb's own process. It exists
// because Harvest publishes linux/amd64 binaries only — no native rewrite
// bundled Harvest as a sidecar the way Plumb bundles Prometheus and
// VictoriaMetrics, but that meant NetApp ONTAP/StorageGRID collection
// simply didn't work at all on Windows or macOS.
//
// The output contract deliberately mimics what a bundled Harvest poller
// used to expose: the same metric names, the same `array` label, the same
// units — so config/thresholds/netapp_ontap.yml and
// config/thresholds/netapp_storagegrid.yml, and everything downstream of
// them (panels, findings, reports), needed zero changes. Every metric
// Harvest used to publish for these two vendors is reproduced here,
// including the two ONTAP metrics (aggr_disk_busy, nic_util_percent) that
// need ONTAP's raw performance counter-tables API rather than its simpler
// REST resource endpoints — see ontap.go and ontap_countertables.go for
// the full per-metric source trail.
package netappnative

import (
	"fmt"
	"io"
	"sort"
)

// promWriter accumulates Prometheus exposition-format metric blocks for one
// scrape. Kept minimal on purpose — this only ever emits gauges/counters
// with a single `array` label, never the richer per-object label sets
// Harvest itself produces, since config/thresholds/netapp_*.yml only ever
// queries at that granularity today. Each call appends one self-contained
// HELP+TYPE+sample block, keyed by metric name so output order is stable
// (sorted by name) without splitting a metric's own three lines apart.
type promWriter struct {
	blocks map[string]string
}

func (p *promWriter) ensure() {
	if p.blocks == nil {
		p.blocks = map[string]string{}
	}
}

func (p *promWriter) gauge(name, help, arrayID string, value float64) {
	p.ensure()
	p.blocks[name] = fmt.Sprintf("# HELP %s %s\n# TYPE %s gauge\n%s{array=%q} %g\n", name, help, name, name, arrayID, value)
}

func (p *promWriter) counter(name, help, arrayID string, value float64) {
	p.ensure()
	p.blocks[name] = fmt.Sprintf("# HELP %s %s\n# TYPE %s counter\n%s{array=%q} %g\n", name, help, name, name, arrayID, value)
}

// note records a metric that could not be collected this poll (endpoint
// unreachable, auth failure, field missing) as a comment rather than
// silently emitting nothing — visible in data/logs and in a raw scrape,
// not just an absence a dashboard quietly shows as "—". Keyed the same way
// so a later successful collection of the same metric overwrites the note.
func (p *promWriter) note(key, format string, args ...any) {
	p.ensure()
	p.blocks[key] = "# " + fmt.Sprintf(format, args...) + "\n"
}

func (p *promWriter) Emit(w io.Writer) error {
	names := make([]string, 0, len(p.blocks))
	for n := range p.blocks {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if _, err := io.WriteString(w, p.blocks[n]); err != nil {
			return err
		}
	}
	return nil
}
