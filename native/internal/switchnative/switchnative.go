// Package switchnative collects uplink port health directly from a
// customer's own network switches (Cisco NX-API, Arista eAPI) and exposes
// it under the SAME array label a monitored array's own metrics use — not
// under the switch's own identity. That's deliberate: a switch isn't a
// fleet member Plumb tracks on its own, it's evidence for whichever
// array(s) config/switches.yml says its ports carry traffic for (see
// config.Switch's own doc comment for why that mapping is the actual
// design center of this feature, not the collector API).
//
// Implemented from scratch against each vendor's own public API reference
// (Cisco's NX-API CLI guide, Arista's eAPI command reference) — neither
// platform is in NetApp Harvest's scope, so there's nothing to cross-check
// against there either.
package switchnative

import (
	"fmt"
	"io"
	"sort"

	"plumb/internal/config"
)

// PortStats is one interface's current reading, platform-agnostic — nexus.go
// and arista.go each produce these from their own very different raw JSON
// shapes, so everything downstream of this point (WriteMetrics) never
// needs to know which platform a given switch is.
type PortStats struct {
	Port               string
	UtilizationPercent *float64 // nil if the platform's response didn't include a usable load percentage this poll
	ErrorsCumulative   float64  // raw counter — WriteMetrics emits it as a Prometheus counter, threshold query wraps it in rate(...), same idiom as every error-counter metric elsewhere in this project
}

// CollectPortStats dispatches to the right platform collector for one
// switch, returning stats for only the ports actually asked for (a switch
// can have far more ports than the ones linked to any given array).
func CollectPortStats(sw config.Switch, ports []string) ([]PortStats, error) {
	switch sw.Platform {
	case config.SwitchPlatformCiscoNXOS:
		return collectNexusPortStats(sw, ports)
	case config.SwitchPlatformAristaEOS:
		return collectAristaPortStats(sw, ports)
	default:
		return nil, fmt.Errorf("unknown switch platform %q", sw.Platform)
	}
}

// WriteMetrics writes one switch_port_utilization and one switch_port_errors
// sample for the array — worst-port utilization (one saturated uplink is
// the problem even if a second link on the same array is idle) and summed
// errors (any linked port's errors are this array's problem), drawn from
// every switch/port config/switches.yml links to it (config.LinksForArray).
// Deliberately a single value per array, not one series per port — this
// project's per-node/per-disk breakdown mechanism (config.MetricDef.
// NodeBreakdownQuery) is the right tool if a future version wants a
// per-port panel; a second, differently-labeled series under the same
// metric name here would just make the plain array-level threshold query
// ambiguous about which series it's averaging.
//
// Best-effort per link, same principle as every other collector in this
// project — one switch being unreachable never blocks metrics from
// another link on the same array.
func WriteMetrics(w io.Writer, arrayID string, links []config.ArrayLink) error {
	var stats []PortStats
	var errs []string
	for _, link := range links {
		s, err := CollectPortStats(link.Switch, link.Ports)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", link.Switch.ID, err))
			continue
		}
		stats = append(stats, s...)
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].Port < stats[j].Port })

	var maxUtil *float64
	var totalErrors float64
	for _, s := range stats {
		if s.UtilizationPercent != nil && (maxUtil == nil || *s.UtilizationPercent > *maxUtil) {
			maxUtil = s.UtilizationPercent
		}
		totalErrors += s.ErrorsCumulative
	}

	fmt.Fprintln(w, "# HELP switch_port_utilization Worst linked uplink port's utilization, percent of link speed")
	fmt.Fprintln(w, "# TYPE switch_port_utilization gauge")
	if maxUtil != nil {
		fmt.Fprintf(w, "switch_port_utilization{array=%q} %v\n", arrayID, *maxUtil)
	} else {
		fmt.Fprintf(w, "# switch_port_utilization unavailable for %s: no port reported a usable utilization figure\n", arrayID)
	}
	fmt.Fprintln(w, "# HELP switch_port_errors Cumulative error count summed across every linked uplink port")
	fmt.Fprintln(w, "# TYPE switch_port_errors counter")
	fmt.Fprintf(w, "switch_port_errors{array=%q} %v\n", arrayID, totalErrors)

	if len(errs) > 0 {
		fmt.Fprintf(w, "# some links unavailable for %s: %v\n", arrayID, errs)
	}
	return nil
}
