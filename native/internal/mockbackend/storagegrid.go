package mockbackend

import (
	"fmt"
	"hash/fnv"
	"net/http"
	"time"

	"plumb/internal/mockdata"
)

// storagegridMux builds the two Grid Management API endpoints
// internal/netappnative/storagegrid.go calls: the login exchange, and the
// metric-query passthrough it uses instead of any REST-to-metric
// translation (StorageGRID already runs its own Prometheus). Since the
// real collector sends a fixed, known set of PromQL strings (see
// storagegrid.go's WriteMetrics), this matches those exact strings rather
// than implementing a general PromQL evaluator.
func storagegridMux(arr mockdata.Array) *http.ServeMux {
	mux := http.NewServeMux()
	counters := newCounterAccumulator()
	const token = "mock-token"

	mux.HandleFunc("POST /api/v3/authorize", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"responseTime": time.Now().Format(time.RFC3339), "status": "success", "apiVersion": "3.2", "data": token})
	})

	mux.HandleFunc("GET /api/v3/grid/metric-query", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		now := time.Now()
		query := r.URL.Query().Get("query")

		var value float64
		var ok bool
		var result []map[string]any
		switch query {
		case "avg(storagegrid_metadata_queries_average_latency_milliseconds)":
			value, ok = arr.CurrentValue("metadata_query_latency", arr.ID+"|metadata_query_latency", "frontend", 50, 150, now), true
		case "avg(storagegrid_node_cpu_utilization_percentage)":
			value, ok = arr.CurrentValue("node_cpu", arr.ID+"|node_cpu", "backend", 70, 85, now), true
		case "sum(storagegrid_ilm_awaiting_total_objects)":
			value, ok = arr.CurrentValue("ilm_backlog", arr.ID+"|ilm_backlog", "backend", 100000, 1000000, now), true
		case "sum(storagegrid_storage_utilization_total_space_bytes)":
			value, ok = 1_000_000, true
		case "sum(storagegrid_storage_utilization_usable_space_bytes)":
			pct := arr.CurrentValue("storage_capacity", arr.ID+"|storage_capacity", "backend", 80, 90, now)
			value, ok = 1_000_000*(1-pct/100), true
		case "sum(storagegrid_s3_operations_failed)":
			perMin := arr.CurrentValue("s3_error_rate", arr.ID+"|s3_error_rate", "frontend", 5, 30, now)
			value, ok = counters.accumulate(arr.ID+"|s3_failed", perMin/60.0), true
		case "sum(node_network_receive_errs_total)":
			// Band args (0.01, 5) mirror severity_watch/severity_critical in
			// netapp_storagegrid.yml: any sustained nonzero rate is a fault
			// by industry practice, so "healthy" targets genuine near-zero.
			perMin := arr.CurrentValue("network_errors", arr.ID+"|network_errors", "frontend", 0.01, 5, now)
			value, ok = counters.accumulate(arr.ID+"|net_rx_errs", perMin/60.0/2), true
		case "sum(node_network_transmit_errs_total)":
			perMin := arr.CurrentValue("network_errors", arr.ID+"|network_errors", "frontend", 0.01, 5, now)
			value, ok = counters.accumulate(arr.ID+"|net_tx_errs", perMin/60.0/2), true

		// Per-node breakdown queries (internal/netappnative/storagegrid.go's
		// writeNodeBreakdowns) — raw/rated per-node values, no sum()/avg(),
		// so each node in the mock topology gets its own entry rather than
		// one collapsed number. See nodeVector's doc comment for how one
		// node is picked to actually carry a non-healthy severity, so the
		// breakdown tells a real "which node" story instead of every node
		// uniformly showing the grid-wide number.
		case "storagegrid_metadata_queries_average_latency_milliseconds":
			result = nodeVector(arr, "metadata_query_latency", "frontend", 50, 150, now)
		case "storagegrid_node_cpu_utilization_percentage":
			result = nodeVector(arr, "node_cpu", "backend", 70, 85, now)
		case "storagegrid_ilm_awaiting_total_objects":
			result = nodeVector(arr, "ilm_backlog", "backend", 100000, 1000000, now)
		case "rate(storagegrid_s3_operations_failed[5m]) * 60":
			result = nodeVector(arr, "s3_error_rate", "frontend", 5, 30, now)
		case "(rate(node_network_receive_errs_total[5m]) + rate(node_network_transmit_errs_total[5m])) * 60":
			result = nodeVector(arr, "network_errors", "frontend", 0.01, 5, now)
		case "100 * (1 - (storagegrid_storage_utilization_usable_space_bytes / storagegrid_storage_utilization_total_space_bytes))":
			result = nodeVector(arr, "storage_capacity", "backend", 80, 90, now)
		}

		if result == nil {
			result = []map[string]any{}
			if ok {
				result = []map[string]any{
					{"metric": map[string]string{}, "value": []any{now.Unix(), fmt.Sprintf("%g", value)}},
				}
			}
		}
		writeJSON(w, map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": result}})
	})

	return mux
}

// gridNodes returns a realistic, fixed 4-node StorageGRID topology for one
// mock grid: an Admin Node, two Storage Nodes, and a Gateway Node — the
// same node types a real deployment's per-node breakdown would show.
func gridNodes(arr mockdata.Array) []string {
	return []string{
		arr.ID + "-dc1-adm1",
		arr.ID + "-dc1-s1",
		arr.ID + "-dc1-s2",
		arr.ID + "-dc1-g1",
	}
}

// culpritIndex deterministically picks which node in the topology carries
// the array's real severity for one metric, varying by metric so a demo
// doesn't always blame the same node for every problem. Not
// cryptographically anything — just enough spread to look plausible.
func culpritIndex(arr mockdata.Array, metricID string, n int) int {
	h := fnv.New32a()
	h.Write([]byte(arr.ID + "|" + metricID))
	return int(h.Sum32() % uint32(n))
}

// nodeVector builds one series per node in the mock grid's topology for a
// metric with a per-node breakdown. When the array's overall severity for
// this metric is healthy, every node reports a healthy value. Otherwise,
// exactly one node (see culpritIndex) reports the array's actual severity
// while the rest stay healthy — mirroring the real, common shape of a
// StorageGRID problem (one hot node, not the whole grid uniformly
// degraded), and giving the per-node breakdown feature something
// meaningful to show rather than N identical numbers.
func nodeVector(arr mockdata.Array, metricID, category string, watch, critical float64, now time.Time) []map[string]any {
	nodes := gridNodes(arr)
	severity := arr.SeverityFor(metricID, category)
	culprit := culpritIndex(arr, metricID, len(nodes))

	result := make([]map[string]any, 0, len(nodes))
	for i, node := range nodes {
		nodeSeverity := "healthy"
		if severity != "healthy" && i == culprit {
			nodeSeverity = severity
		}
		v := arr.ValueForSeverity(arr.ID+"|"+metricID+"|"+node, nodeSeverity, watch, critical, now)
		result = append(result, map[string]any{
			"metric": map[string]string{"instance": node},
			"value":  []any{now.Unix(), fmt.Sprintf("%g", v)},
		})
	}
	return result
}
