package mockbackend

import (
	"fmt"
	"net/http"
	"time"

	"plumb/internal/mockdata"
)

// eseriesMux builds the fixed set of SANtricity REST API paths
// internal/netappnative/eseries.go actually calls, serving synthetic
// values instead of a real storage system's — same "exact inverse of the
// collector's own math" principle as ontapMux, see that file's own doc
// comment.
func eseriesMux(arr mockdata.Array) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /devmgr/v2/storage-systems/1/analysed-volume-statistics", func(w http.ResponseWriter, r *http.Request) {
		// eseries_host_latency: display = avg(combinedResponseTime)/1000.
		// A single synthetic volume is enough — the collector averages
		// across whatever the array reports, one or many.
		ms := arr.CurrentValue("eseries_host_latency", arr.ID+"|eseries_host_latency", "frontend", 8, 15, time.Now())
		writeJSON(w, []map[string]any{
			{"volumeId": "vol-1", "combinedResponseTime": ms * 1000},
		})
	})

	mux.HandleFunc("GET /devmgr/v2/storage-systems/1/analysed-drive-statistics", func(w http.ResponseWriter, r *http.Request) {
		// eseries_drive_latency: display = avg(combinedResponseTime)/1000
		// across drives. drive-1 carries the array's real severity,
		// drive-2/3 stay healthy — same masking demo as ONTAP's
		// aggr_disk_busy: one bad drive averaged against healthy ones can
		// hide below the array-wide threshold without the breakdown.
		now := time.Now()
		ms := map[string]float64{
			"drive-1": arr.CurrentValue("eseries_drive_latency", arr.ID+"|eseries_drive_latency", "backend", 10, 20, now),
			"drive-2": arr.ValueForSeverity(arr.ID+"|eseries_drive_latency|drive-2", "healthy", 10, 20, now),
			"drive-3": arr.ValueForSeverity(arr.ID+"|eseries_drive_latency|drive-3", "healthy", 10, 20, now),
		}
		records := make([]map[string]any, 0, len(ms))
		for _, disk := range []string{"drive-1", "drive-2", "drive-3"} {
			records = append(records, map[string]any{"diskId": disk, "combinedResponseTime": ms[disk] * 1000})
		}
		writeJSON(w, records)
	})

	mux.HandleFunc("GET /devmgr/v2/storage-systems/1", func(w http.ResponseWriter, r *http.Request) {
		// eseries_capacity_used_percent: display = sum(used)/sum(total)*100
		// across storage pools — a direct snapshot like ONTAP's aggregate
		// capacity, so fix total and derive used from the target percent.
		pct := arr.CurrentValue("eseries_capacity_used_percent", arr.ID+"|eseries_capacity_used_percent", "backend", 75, 85, time.Now())
		const total = 1_000_000_000_000.0 // 1TB, as a round number for a readable mock response
		used := total * pct / 100
		writeJSON(w, map[string]any{
			"storagePoolSpaceInfo": []map[string]any{
				{"totalRaidedSpace": fmt.Sprintf("%.0f", total), "usedSpace": fmt.Sprintf("%.0f", used)},
			},
		})
	})

	return mux
}
