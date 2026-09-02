package netappnative

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"plumb/internal/config"
)

// DiscoverONTAPCounters enumerates every performance counter ONTAP's REST
// API can report for this cluster — not just the handful
// config/thresholds/netapp_ontap.yml currently evaluates — as raw research
// material for extending that file later. Two-step and read-only, mirroring
// exactly what ontap_countertables.go's collectAggrDiskBusy/
// collectNICUtilization already do for the two tables Plumb actually uses:
//
//  1. GET /api/cluster/counter/tables lists every table name ONTAP exposes
//     on this cluster (a real, documented REST collection endpoint — some
//     ONTAP versions/configurations don't have it at all, per NetApp's own
//     KB CONTAP-100531, in which case this whole function degrades to a
//     single clear error rather than a partial, confusing result).
//  2. For each table, GET /api/cluster/counter/tables/{name} fetches its
//     schema (the counter names, types, and units within that table) —
//     schema only, never /rows, since fetching every row of every table
//     (potentially every volume, LUN, and disk on the cluster, dozens of
//     times over) is exactly the kind of unbounded load a discovery tool
//     must not put on a production cluster just to answer "what's
//     available." A table's schema already answers that.
func (c *ONTAPCollector) DiscoverONTAPCounters(arr config.Array) (string, error) {
	client := c.client(arr)
	body, err := c.get(client, arr, "/api/cluster/counter/tables")
	if err != nil {
		return "", fmt.Errorf("listing counter tables: %w (this REST endpoint doesn't exist on every ONTAP version — see NetApp KB CONTAP-100531)", err)
	}
	var listResp struct {
		Records []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"records"`
	}
	if err := json.Unmarshal(body, &listResp); err != nil {
		return "", fmt.Errorf("parsing counter table list: %w", err)
	}
	sort.Slice(listResp.Records, func(i, j int) bool { return listResp.Records[i].Name < listResp.Records[j].Name })

	var sb strings.Builder
	fmt.Fprintf(&sb, "# ONTAP counter table discovery for %s\n", arr.ID)
	fmt.Fprintf(&sb, "# %d counter table(s) found. Schema (not row data) for each — see each table's own\n", len(listResp.Records))
	fmt.Fprintf(&sb, "# counters for what config/thresholds/netapp_ontap.yml could additionally query.\n\n")

	for _, t := range listResp.Records {
		fmt.Fprintf(&sb, "## table: %s\n", t.Name)
		if t.Description != "" {
			fmt.Fprintf(&sb, "## %s\n", t.Description)
		}
		schemaBody, err := c.get(client, arr, "/api/cluster/counter/tables/"+t.Name)
		if err != nil {
			fmt.Fprintf(&sb, "(schema unavailable: %v)\n\n", err)
			continue
		}
		var schemaResp struct {
			CounterSchemas []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Type        string `json:"type"`
				Unit        string `json:"unit"`
				Denominator struct {
					Name string `json:"name"`
				} `json:"denominator"`
			} `json:"counter_schemas"`
		}
		if err := json.Unmarshal(schemaBody, &schemaResp); err != nil {
			fmt.Fprintf(&sb, "(schema unparseable: %v)\n\n", err)
			continue
		}
		sort.Slice(schemaResp.CounterSchemas, func(i, j int) bool {
			return schemaResp.CounterSchemas[i].Name < schemaResp.CounterSchemas[j].Name
		})
		for _, cs := range schemaResp.CounterSchemas {
			denom := ""
			if cs.Denominator.Name != "" {
				denom = " denominator=" + cs.Denominator.Name
			}
			fmt.Fprintf(&sb, "  %-40s type=%-10s unit=%-10s%s", cs.Name, cs.Type, cs.Unit, denom)
			if cs.Description != "" {
				fmt.Fprintf(&sb, "  # %s", cs.Description)
			}
			sb.WriteByte('\n')
		}
		sb.WriteByte('\n')
	}
	return sb.String(), nil
}

// DiscoverStorageGRIDMetrics enumerates every currently-exported time
// series on this grid's own embedded Prometheus — not just the metrics
// config/thresholds/netapp_storagegrid.yml already evaluates — as raw
// research material for extending that file later. `{__name__=~".+"}` is
// a standard, read-only Prometheus query matching every series, run
// through the exact same metric-query passthrough (and Metrics query
// permission) every other query in this file already uses — nothing new
// is granted or exposed beyond what normal operation already requires.
func (c *StorageGridCollector) DiscoverStorageGRIDMetrics(arr config.Array) (string, error) {
	client := c.client(arr)
	pr, err := c.queryRaw(client, arr, `{__name__=~".+"}`)
	if err != nil {
		return "", fmt.Errorf("querying all series: %w", err)
	}

	type series struct {
		name   string
		labels map[string]string
	}
	byName := map[string][]series{}
	for _, r := range pr.Data.Result {
		name := r.Metric["__name__"]
		if name == "" {
			continue
		}
		labels := make(map[string]string, len(r.Metric))
		for k, v := range r.Metric {
			if k != "__name__" {
				labels[k] = v
			}
		}
		byName[name] = append(byName[name], series{name: name, labels: labels})
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)

	var sb strings.Builder
	fmt.Fprintf(&sb, "# StorageGRID metric discovery for %s\n", arr.ID)
	fmt.Fprintf(&sb, "# %d distinct metric name(s), %d total series, found on this grid's own Prometheus.\n", len(names), len(pr.Data.Result))
	fmt.Fprintf(&sb, "# One example label set per metric shown — see config/thresholds/netapp_storagegrid.yml\n")
	fmt.Fprintf(&sb, "# for what's already evaluated. Names starting storagegrid_private_ are NetApp's own\n")
	fmt.Fprintf(&sb, "# unsupported/undocumented convention (see that file's own comments on this).\n\n")

	for _, name := range names {
		group := byName[name]
		fmt.Fprintf(&sb, "%s  (%d series)\n", name, len(group))
		labelKeys := make([]string, 0, len(group[0].labels))
		for k := range group[0].labels {
			labelKeys = append(labelKeys, k)
		}
		sort.Strings(labelKeys)
		if len(labelKeys) > 0 {
			parts := make([]string, 0, len(labelKeys))
			for _, k := range labelKeys {
				parts = append(parts, k+"="+group[0].labels[k])
			}
			fmt.Fprintf(&sb, "  e.g. %s\n", strings.Join(parts, ", "))
		}
	}
	return sb.String(), nil
}

// DiscoverESeriesCounters dumps the raw, pretty-printed response of the two
// analysed-statistics resources eseries.go actually reads — SANtricity's
// REST API has no wildcard "list every metric" endpoint the way ONTAP's
// counter-tables or StorageGRID's PromQL passthrough do, so the resource's
// own full field set (most of which config/thresholds/netapp_eseries.yml
// doesn't currently evaluate) is the most useful "what else is available"
// answer available without guessing at undocumented endpoints.
func (c *ESeriesCollector) DiscoverESeriesCounters(arr config.Array) (string, error) {
	client := c.client(arr)
	var sb strings.Builder
	fmt.Fprintf(&sb, "# E-Series (SANtricity) discovery for %s\n", arr.ID)
	fmt.Fprintf(&sb, "# Raw, full field set of the two per-resource statistics endpoints this collector\n")
	fmt.Fprintf(&sb, "# reads from — see config/thresholds/netapp_eseries.yml for what's already evaluated.\n\n")

	for _, path := range []string{"/storage-systems/1/analysed-volume-statistics", "/storage-systems/1/analysed-drive-statistics"} {
		fmt.Fprintf(&sb, "## %s\n", path)
		body, err := c.get(client, arr, path)
		if err != nil {
			fmt.Fprintf(&sb, "(unavailable: %v)\n\n", err)
			continue
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, body, "", "  "); err != nil {
			sb.Write(body)
		} else {
			sb.Write(pretty.Bytes())
		}
		sb.WriteString("\n\n")
	}
	return sb.String(), nil
}
