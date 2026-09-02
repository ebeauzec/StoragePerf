package netappnative

import (
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"time"

	"plumb/internal/config"
)

// ESeriesCollector polls a storage system's embedded SANtricity REST API
// directly (basic auth, same auth model as ONTAPCollector) and exposes the
// three metrics config/thresholds/netapp_eseries.yml expects:
//
//   - eseries_host_latency  — GET /storage-systems/1/analysed-volume-
//     statistics, combinedResponseTime averaged across volumes (µs→ms).
//     SANtricity's own REST API reference documents this field on the
//     per-volume analysed-statistics resource.
//   - eseries_drive_latency (+ per-drive breakdown) — GET /storage-systems/1/
//     analysed-drive-statistics, combinedResponseTime per drive. Unlike
//     ONTAP's aggr_disk_busy, this is a latency figure, not a busy-percent
//     one — SANtricity's drive statistics resource doesn't publish a direct
//     utilization percentage the way ONTAP's counter-tables API does, so
//     latency is the more defensible back-end pressure signal to build on
//     here without guessing at a derived percentage.
//   - eseries_capacity_used_percent — GET /storage-systems/1, summed
//     usedSpace/totalSpace across the array's storage pools.
//
// "1" as the storage-system ID throughout assumes the embedded REST API
// reached directly on the controller (the common case a single arr.ManagementLIF
// points at) rather than a separate multi-array Web Services Proxy, which
// addresses systems by their own real WWN-derived ID instead. If a live
// system uses the proxy, that ID needs to replace the literal "1" below —
// there's no way to discover it generically without one to test against.
type ESeriesCollector struct{}

func NewESeriesCollector() *ESeriesCollector { return &ESeriesCollector{} }

func (c *ESeriesCollector) client(arr config.Array) *http.Client {
	// DisableKeepAlives: same reasoning as ONTAPCollector.client — a fresh
	// client per scrape, discarded right after, so nothing benefits from
	// keep-alive and leaving it on just leaks one idle connection per poll.
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: arr.UseInsecureTLS}, DisableKeepAlives: true},
	}
}

func (c *ESeriesCollector) get(client *http.Client, arr config.Array, path string) ([]byte, error) {
	req, err := http.NewRequest("GET", "https://"+arr.ManagementLIF+"/devmgr/v2"+path, nil)
	if err != nil {
		return nil, err
	}
	password := ""
	if arr.PasswordEnv != "" {
		password = os.Getenv(arr.PasswordEnv)
	}
	req.SetBasicAuth(arr.Username, password)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, &httpStatusError{path: path, status: resp.Status, body: string(body)}
	}
	return body, nil
}

// httpStatusError mirrors ontap.go's inline fmt.Errorf for a non-200
// response — a named type here only because eseries.go doesn't otherwise
// import fmt, and this keeps the error message shape identical to the
// other collectors' without pulling in an extra import for one call site.
type httpStatusError struct {
	path, status, body string
}

func (e *httpStatusError) Error() string {
	return e.path + ": " + e.status + ": " + e.body
}

// WriteMetrics writes a Prometheus exposition-format scrape for one
// E-Series system. Each sub-collection is independent and best-effort,
// same principle as every other collector in this package — one failing
// never prevents the others from being collected and exposed.
func (c *ESeriesCollector) WriteMetrics(w io.Writer, arr config.Array) error {
	pw := &promWriter{}
	client := c.client(arr)

	c.collectHostLatency(pw, client, arr)
	c.collectDriveLatency(pw, client, arr)
	c.collectCapacity(pw, client, arr)

	return pw.Emit(w)
}

type analysedVolumeStatsResp []struct {
	VolumeID             string  `json:"volumeId"`
	CombinedResponseTime float64 `json:"combinedResponseTime"` // microseconds, per SANtricity's REST API reference
}

// collectHostLatency also emits a per-volume breakdown — volumeId is
// already present on every row this call returns, so unlike ONTAP's
// equivalent this needed no second endpoint, just using data already being
// fetched. Same masking reasoning as every other breakdown in this
// codebase: an array-wide average across every volume can hide one
// genuinely slow one.
func (c *ESeriesCollector) collectHostLatency(pw *promWriter, client *http.Client, arr config.Array) {
	body, err := c.get(client, arr, "/storage-systems/1/analysed-volume-statistics")
	if err != nil {
		pw.note("eseries_host_latency", "eseries_host_latency unavailable for %s: %v", arr.ID, err)
		return
	}
	var r analysedVolumeStatsResp
	if err := json.Unmarshal(body, &r); err != nil || len(r) == 0 {
		pw.note("eseries_host_latency", "eseries_host_latency unavailable for %s: no records in analysed-volume-statistics response", arr.ID)
		return
	}
	var sum float64
	for _, v := range r {
		ms := v.CombinedResponseTime / 1000.0
		sum += ms
		if v.VolumeID != "" {
			pw.gaugeNode("eseries_host_latency_by_volume", "Host-side latency by volume, milliseconds", arr.ID, v.VolumeID, ms)
		}
	}
	avgMs := sum / float64(len(r))
	pw.gauge("eseries_host_latency", "Average host-side (volume) latency in milliseconds", arr.ID, avgMs)
}

type analysedDriveStatsResp []struct {
	DiskID               string  `json:"diskId"`
	CombinedResponseTime float64 `json:"combinedResponseTime"` // microseconds
}

func (c *ESeriesCollector) collectDriveLatency(pw *promWriter, client *http.Client, arr config.Array) {
	body, err := c.get(client, arr, "/storage-systems/1/analysed-drive-statistics")
	if err != nil {
		pw.note("eseries_drive_latency", "eseries_drive_latency unavailable for %s: %v", arr.ID, err)
		return
	}
	var r analysedDriveStatsResp
	if err := json.Unmarshal(body, &r); err != nil || len(r) == 0 {
		pw.note("eseries_drive_latency", "eseries_drive_latency unavailable for %s: no records in analysed-drive-statistics response", arr.ID)
		return
	}
	var sum float64
	for _, d := range r {
		ms := d.CombinedResponseTime / 1000.0
		sum += ms
		name := d.DiskID
		if name == "" {
			continue
		}
		pw.gaugeNode("eseries_drive_latency_by_drive", "Drive latency by drive, milliseconds", arr.ID, name, ms)
	}
	pw.gauge("eseries_drive_latency", "Average drive latency in milliseconds", arr.ID, sum/float64(len(r)))
}

type storageSystemResp struct {
	StoragePoolSpaceInfo []struct {
		TotalRaidedSpace string `json:"totalRaidedSpace"` // bytes, as a decimal string per SANtricity's REST convention for large integers
		UsedSpace        string `json:"usedSpace"`
	} `json:"storagePoolSpaceInfo"`
}

func (c *ESeriesCollector) collectCapacity(pw *promWriter, client *http.Client, arr config.Array) {
	body, err := c.get(client, arr, "/storage-systems/1")
	if err != nil {
		pw.note("eseries_capacity_used_percent", "eseries_capacity_used_percent unavailable for %s: %v", arr.ID, err)
		return
	}
	var r storageSystemResp
	if err := json.Unmarshal(body, &r); err != nil || len(r.StoragePoolSpaceInfo) == 0 {
		pw.note("eseries_capacity_used_percent", "eseries_capacity_used_percent unavailable for %s: no storagePoolSpaceInfo in /storage-systems/1 response", arr.ID)
		return
	}
	var total, used float64
	for _, p := range r.StoragePoolSpaceInfo {
		total += parseBytes(p.TotalRaidedSpace)
		used += parseBytes(p.UsedSpace)
	}
	if total <= 0 {
		pw.note("eseries_capacity_used_percent", "eseries_capacity_used_percent unavailable for %s: total space reported as zero", arr.ID)
		return
	}
	pw.gauge("eseries_capacity_used_percent", "Storage pool capacity used, percent", arr.ID, used/total*100)
}

func parseBytes(s string) float64 {
	var v float64
	for _, r := range s {
		if r < '0' || r > '9' {
			return v
		}
		v = v*10 + float64(r-'0')
	}
	return v
}
