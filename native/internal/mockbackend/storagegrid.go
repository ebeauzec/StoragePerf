package mockbackend

import (
	"fmt"
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
		switch query {
		case "avg(storagegrid_metadata_queries_average_latency_milliseconds)":
			value, ok = mockdata.CurrentValue(arr.ID+"|metadata_query_latency", arr.Profile, "frontend", 50, 150, now), true
		case "avg(storagegrid_node_cpu_utilization_percentage)":
			value, ok = mockdata.CurrentValue(arr.ID+"|node_cpu", arr.Profile, "backend", 70, 85, now), true
		case "sum(storagegrid_ilm_awaiting_total_objects)":
			value, ok = mockdata.CurrentValue(arr.ID+"|ilm_backlog", arr.Profile, "backend", 100000, 1000000, now), true
		case "sum(storagegrid_storage_utilization_total_space_bytes)":
			value, ok = 1_000_000, true
		case "sum(storagegrid_storage_utilization_usable_space_bytes)":
			pct := mockdata.CurrentValue(arr.ID+"|storage_capacity", arr.Profile, "backend", 80, 90, now)
			value, ok = 1_000_000*(1-pct/100), true
		case "sum(storagegrid_s3_operations_failed)":
			perMin := mockdata.CurrentValue(arr.ID+"|s3_error_rate", arr.Profile, "frontend", 5, 30, now)
			value, ok = counters.accumulate(arr.ID+"|s3_failed", perMin/60.0), true
		case "sum(node_network_receive_errs_total)":
			perMin := mockdata.CurrentValue(arr.ID+"|network_errors", arr.Profile, "frontend", 5, 15, now)
			value, ok = counters.accumulate(arr.ID+"|net_rx_errs", perMin/60.0/2), true
		case "sum(node_network_transmit_errs_total)":
			perMin := mockdata.CurrentValue(arr.ID+"|network_errors", arr.Profile, "frontend", 5, 15, now)
			value, ok = counters.accumulate(arr.ID+"|net_tx_errs", perMin/60.0/2), true
		}

		result := []map[string]any{}
		if ok {
			result = []map[string]any{
				{"metric": map[string]string{}, "value": []any{now.Unix(), fmt.Sprintf("%g", value)}},
			}
		}
		writeJSON(w, map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": result}})
	})

	return mux
}
