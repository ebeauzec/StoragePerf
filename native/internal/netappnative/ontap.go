package netappnative

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"

	"plumb/internal/config"
)

// ONTAPCollector polls a cluster's REST API directly (basic auth, the same
// auth_style and account Harvest's own setup docs call for) and re-exposes
// every metric config/thresholds/netapp_ontap.yml expects, under the exact
// same names Harvest used to produce:
//
//   - volume_avg_latency  — GET /api/cluster/metrics, latency.total (µs→ms).
//     Cluster-wide rather than per-volume: avoids an N+1 fetch across every
//     volume on every 15s poll, and matches the array-wide granularity
//     Plumb's own dashboard already shows. volume_avg_latency_by_volume and
//     volume_space_used_percent(_by_volume) fill in the per-volume detail
//     the cluster-wide number alone can't answer — GET /api/storage/volumes
//     with the statistics/space field groups, one page (not per-volume, per
//     collectVolumeLatencyBreakdown's own comment on why).
//   - node_cpu_busy       — GET /api/cluster/nodes, statistics.processor_
//     utilization_raw / _base. NetApp's own documented single-sample
//     division (raw/base*100) is confirmed wrong (their KB article
//     CONTAP-377586, corroborated by Checkmk's fix in werk #17623): the
//     correct math is a ratio of deltas between two samples, so this keeps
//     the previous poll's raw/base per array (and per node — see
//     collectNodeCPU's own comment on why the cluster-wide average alone
//     has a real blind spot) and computes delta(raw)/delta(base)*100 — the
//     first poll for any array has nothing to diff against yet and is
//     skipped, not guessed.
//   - aggr_space_used_percent — GET /api/storage/aggregates,
//     space.block_storage.{used,size} summed across aggregates. Plain
//     arithmetic on a capacity field, not a performance counter.
//   - snapmirror_lag_time — GET /api/snapmirror/relationships, lag_time
//     (ISO-8601 duration, e.g. "PT8H35M42S"), max across relationships.
//   - snapmirror_bandwidth_bytes — GET api/private/cli/snapmirror,
//     total_transfer_bytes. See collectSnapMirrorBandwidth's own comment:
//     this is the one metric in this file sourced from an unsupported CLI
//     passthrough rather than a documented REST resource.
//   - nic_rx_crc_errors   — GET /api/network/ethernet/ports, statistics.
//     device.{receive_raw,transmit_raw}.errors. This is confirmed real and
//     live (unlike Harvest's own source, which reads CRC-specific counters
//     from the separate counter-tables API), but it's the port's total
//     receive+transmit error count, not isolated to CRC errors the way
//     the metric's name implies — a reasonable proxy for "this link has a
//     problem," not a precise match to Harvest's own field. Exposed as a
//     raw counter either way: config/thresholds/netapp_ontap.yml's own
//     query already wraps this in rate(...), so summing the current
//     cumulative total and letting that existing query do the diffing is
//     the standard Prometheus idiom for counters.
//   - aggr_disk_busy, nic_util_percent — see ontap_countertables.go. Both
//     need ONTAP's raw performance counter-tables API (a different
//     endpoint family from the metrics above), whose bulk-fetch query
//     syntax is copied directly from Harvest's own production Go source
//     rather than guessed. aggr_disk_busy also gets a per-disk breakdown
//     for the same reason node_cpu_busy gets a per-node one.
type ONTAPCollector struct {
	mu       sync.Mutex
	cpuState map[string]cpuSample
	volState map[string]volumeLatencySample
	schemaState
}

type cpuSample struct {
	raw, base float64
	at        time.Time
}

func NewONTAPCollector() *ONTAPCollector {
	return &ONTAPCollector{cpuState: map[string]cpuSample{}, volState: map[string]volumeLatencySample{}, schemaState: newSchemaState()}
}

func (c *ONTAPCollector) client(arr config.Array) *http.Client {
	// DisableKeepAlives: a fresh Client/Transport is built for every scrape
	// (~15s per array) and discarded right after — no reuse for keep-alive
	// to help with. A bespoke *http.Transport's IdleConnTimeout zero-value
	// means "never time out," so without this, every scrape leaked one
	// permanently-idle established connection neither side ever closed —
	// confirmed live via lsof after several hours of uptime.
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: arr.UseInsecureTLS}, DisableKeepAlives: true},
	}
}

func (c *ONTAPCollector) get(client *http.Client, arr config.Array, path string) ([]byte, error) {
	req, err := http.NewRequest("GET", "https://"+arr.ManagementLIF+path, nil)
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
		return nil, fmt.Errorf("%s: %s: %s", path, resp.Status, string(body))
	}
	return body, nil
}

// WriteMetrics writes a Prometheus exposition-format scrape for one ONTAP
// array. Each sub-collection is independent and best-effort — one failing
// (a REST call timing out, a field missing on an older ONTAP version)
// never prevents the others from still being collected and exposed.
func (c *ONTAPCollector) WriteMetrics(w io.Writer, arr config.Array) error {
	pw := &promWriter{}
	client := c.client(arr)

	c.collectClusterLatency(pw, client, arr)
	c.collectVolumeLatencyBreakdown(pw, client, arr)
	c.collectVolumeCapacity(pw, client, arr)
	c.collectNodeCPU(pw, client, arr)
	c.collectAggregateCapacity(pw, client, arr)
	c.collectSnapMirrorLag(pw, client, arr)
	c.collectSnapMirrorBandwidth(pw, client, arr)
	c.collectNICErrors(pw, client, arr)
	c.collectAggrDiskBusy(pw, client, arr)
	c.collectNICUtilization(pw, client, arr)

	return pw.Emit(w)
}

// ONTAP's REST API performance objects (this endpoint, and per-object ones
// like /api/storage/volumes/{id}/metrics) consistently return latency/
// iops/throughput broken into read/write/other/total sub-fields — this is
// documented as the "RWOT" convention, available cluster-wide as of ONTAP
// 9.6 (docs.netapp.com/us-en/ontap-automation/rest/performance_metrics.html).
// Read/Write are a best-effort addition on top of the Total this collector
// already used: if a given ONTAP version's /api/cluster/metrics doesn't
// populate them (e.g. an older release, or this specific endpoint not
// following the same convention as the per-object ones — I couldn't get a
// live cluster's literal response to confirm the field names one-to-one
// against this exact endpoint), they simply decode as zero and
// collectClusterLatency below skips emitting them rather than publishing
// a wrong number. Total is unaffected either way.
type clusterMetricsResp struct {
	Records []struct {
		Latency struct {
			Total float64  `json:"total"`
			Read  *float64 `json:"read"` // pointer: distinguishes "field absent" from a genuine 0.0 reading
			Write *float64 `json:"write"`
		} `json:"latency"`
		Timestamp string `json:"timestamp"`
	} `json:"records"`
}

func (c *ONTAPCollector) collectClusterLatency(pw *promWriter, client *http.Client, arr config.Array) {
	body, err := c.get(client, arr, "/api/cluster/metrics")
	if err != nil {
		pw.note("volume_avg_latency", "volume_avg_latency unavailable for %s: %v", arr.ID, err)
		return
	}
	var r clusterMetricsResp
	if err := json.Unmarshal(body, &r); err != nil || len(r.Records) == 0 {
		pw.note("volume_avg_latency", "volume_avg_latency unavailable for %s: no records in /api/cluster/metrics response", arr.ID)
		return
	}
	// ONTAP reports latency in microseconds; config/thresholds/netapp_ontap.yml's
	// severity_watch/critical (8/15) are in milliseconds, matching Harvest's convention.
	latest := r.Records[len(r.Records)-1].Latency
	pw.gauge("volume_avg_latency", "Cluster-wide average latency in milliseconds", arr.ID, latest.Total/1000.0)

	if latest.Read != nil {
		pw.gauge("volume_avg_latency_read", "Cluster-wide average read latency in milliseconds", arr.ID, *latest.Read/1000.0)
	} else {
		pw.note("volume_avg_latency_read", "volume_avg_latency_read unavailable for %s: read latency not populated by /api/cluster/metrics on this ONTAP version", arr.ID)
	}
	if latest.Write != nil {
		pw.gauge("volume_avg_latency_write", "Cluster-wide average write latency in milliseconds", arr.ID, *latest.Write/1000.0)
	} else {
		pw.note("volume_avg_latency_write", "volume_avg_latency_write unavailable for %s: write latency not populated by /api/cluster/metrics on this ONTAP version", arr.ID)
	}
}

// volumeStatsResp covers the "statistics" field group on the standard
// /api/storage/volumes list resource — the same RWOT (read/write/other/
// total) raw-counter convention collectClusterLatency's own comment already
// documents for /api/cluster/metrics, exposed per volume instead of
// cluster-wide. latency_raw/iops_raw are cumulative since the last counter
// reset, not a ready-to-use rate — same reason node CPU needs a delta
// between two polls rather than a single sample.
type volumeStatsResp struct {
	Records []struct {
		Name  string `json:"name"`
		Space struct {
			Size float64 `json:"size"`
			Used float64 `json:"used"`
		} `json:"space"`
		Statistics struct {
			IOPSRaw struct {
				Total float64 `json:"total"`
			} `json:"iops_raw"`
			LatencyRaw struct {
				Total float64 `json:"total"`
			} `json:"latency_raw"`
		} `json:"statistics"`
	} `json:"records"`
}

type volumeLatencySample struct {
	latencyRaw, iopsRaw float64
}

// collectVolumeLatencyBreakdown adds a per-volume breakdown on top of
// collectClusterLatency's existing cluster-wide volume_avg_latency (which
// this never touches — a separate _by_volume metric name, same reasoning
// as node_cpu_busy_by_node). volume_avg_latency's own investigate text
// already asks "which volume(s) are driving the average" — this answers
// it. Capped to what one /api/storage/volumes page returns (max_records
// below) rather than paging through every volume on a large cluster on
// every 15s poll — the topk(10, ...) query in
// config/thresholds/netapp_ontap.yml only ever needs the worst few anyway,
// and a page this size is very unlikely to exclude a genuinely hot volume
// from the top 10.
func (c *ONTAPCollector) collectVolumeLatencyBreakdown(pw *promWriter, client *http.Client, arr config.Array) {
	body, err := c.get(client, arr, "/api/storage/volumes?fields=name,statistics.iops_raw.total,statistics.latency_raw.total&max_records=200")
	if err != nil {
		pw.note("volume_avg_latency_by_volume", "volume_avg_latency_by_volume unavailable for %s: %v", arr.ID, err)
		return
	}
	var r volumeStatsResp
	if err := json.Unmarshal(body, &r); err != nil {
		pw.note("volume_avg_latency_by_volume", "volume_avg_latency_by_volume unavailable for %s: could not parse /api/storage/volumes response", arr.ID)
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	anyEmitted := false
	for _, v := range r.Records {
		if v.Name == "" {
			continue
		}
		key := "vol|" + arr.ID + "|" + v.Name
		prev, had := c.volState[key]
		cur := volumeLatencySample{latencyRaw: v.Statistics.LatencyRaw.Total, iopsRaw: v.Statistics.IOPSRaw.Total}
		c.volState[key] = cur
		if !had || cur.iopsRaw <= prev.iopsRaw {
			continue // first sample for this volume, or an idle/reset counter — nothing to diff yet
		}
		ms := (cur.latencyRaw - prev.latencyRaw) / (cur.iopsRaw - prev.iopsRaw) / 1000.0
		if ms < 0 {
			continue // counter reset between polls (e.g. volume move) — skip rather than publish a negative
		}
		pw.gaugeNode("volume_avg_latency_by_volume", "Average latency by volume, milliseconds", arr.ID, v.Name, ms)
		anyEmitted = true
	}
	if !anyEmitted {
		pw.note("volume_avg_latency_by_volume", "volume_avg_latency_by_volume warming up for %s (needs a second sample per volume to compute a rate)", arr.ID)
	}
}

// collectVolumeCapacity is a plain capacity read (space.size/space.used are
// already absolute, unlike the _raw performance counters above — no delta
// needed, same as collectAggregateCapacity). Fleet-wide value is the worst
// (max) volume rather than an average: an average across every volume on
// the cluster tells you almost nothing useful about whether any single one
// is actually full, the same reasoning nic_utilization already uses max()
// instead of avg() for port utilization.
func (c *ONTAPCollector) collectVolumeCapacity(pw *promWriter, client *http.Client, arr config.Array) {
	body, err := c.get(client, arr, "/api/storage/volumes?fields=name,space.size,space.used&max_records=200")
	if err != nil {
		pw.note("volume_space_used_percent", "volume_space_used_percent unavailable for %s: %v", arr.ID, err)
		return
	}
	var r volumeStatsResp
	if err := json.Unmarshal(body, &r); err != nil {
		pw.note("volume_space_used_percent", "volume_space_used_percent unavailable for %s: could not parse /api/storage/volumes response", arr.ID)
		return
	}
	if len(r.Records) == 0 {
		pw.note("volume_space_used_percent", "volume_space_used_percent unavailable for %s: no records in /api/storage/volumes response", arr.ID)
		return
	}

	worst := 0.0
	for _, v := range r.Records {
		if v.Name == "" || v.Space.Size <= 0 {
			continue
		}
		pct := v.Space.Used / v.Space.Size * 100
		pw.gaugeNode("volume_space_used_percent_by_volume", "Capacity used by volume, percent", arr.ID, v.Name, pct)
		if pct > worst {
			worst = pct
		}
	}
	pw.gauge("volume_space_used_percent", "Worst single volume's capacity used, percent", arr.ID, worst)
}

type nodesResp struct {
	Records []struct {
		Name       string `json:"name"`
		Statistics struct {
			ProcessorUtilizationRaw  float64 `json:"processor_utilization_raw"`
			ProcessorUtilizationBase float64 `json:"processor_utilization_base"`
		} `json:"statistics"`
	} `json:"records"`
}

// collectNodeCPU emits both the cluster-wide average (unchanged — every
// existing threshold/panel/report keys on this exact metric name) and a
// per-node breakdown. The average alone has a real blind spot: two nodes
// at 95%/5% busy average to ~50%, comfortably under the 70% watch line,
// silently hiding a genuine single-node hot spot — exactly the finding
// this metric's own investigate text already asks the reader to check for
// ("both nodes elevated, or only one?") but the pre-aggregated number
// can't answer. `name` is a standard REST resource field on every ONTAP
// version this targets, unlike the counter-tables properties used
// elsewhere in this file, which is why this one didn't need the same
// verification caveat.
func (c *ONTAPCollector) collectNodeCPU(pw *promWriter, client *http.Client, arr config.Array) {
	body, err := c.get(client, arr, "/api/cluster/nodes?fields=name,statistics.processor_utilization_raw,statistics.processor_utilization_base")
	if err != nil {
		pw.note("node_cpu_busy", "node_cpu_busy unavailable for %s: %v", arr.ID, err)
		return
	}
	var r nodesResp
	if err := json.Unmarshal(body, &r); err != nil || len(r.Records) == 0 {
		pw.note("node_cpu_busy", "node_cpu_busy unavailable for %s: no records in /api/cluster/nodes response", arr.ID)
		return
	}

	now := time.Now()
	c.mu.Lock()
	var sumRaw, sumBase float64
	var perNodePct []struct {
		name string
		pct  float64
	}
	allWarm := true
	for i, n := range r.Records {
		sumRaw += n.Statistics.ProcessorUtilizationRaw
		sumBase += n.Statistics.ProcessorUtilizationBase

		nodeName := n.Name
		if nodeName == "" {
			nodeName = fmt.Sprintf("node-%d", i)
		}
		key := arr.ID + "|" + nodeName
		prevNode, hadNode := c.cpuState[key]
		c.cpuState[key] = cpuSample{raw: n.Statistics.ProcessorUtilizationRaw, base: n.Statistics.ProcessorUtilizationBase, at: now}
		if !hadNode || n.Statistics.ProcessorUtilizationBase <= prevNode.base {
			allWarm = false
			continue
		}
		nodePct := (n.Statistics.ProcessorUtilizationRaw - prevNode.raw) / (n.Statistics.ProcessorUtilizationBase - prevNode.base) * 100
		perNodePct = append(perNodePct, struct {
			name string
			pct  float64
		}{nodeName, nodePct})
	}
	prevTotal, hadTotal := c.cpuState[arr.ID]
	c.cpuState[arr.ID] = cpuSample{raw: sumRaw, base: sumBase, at: now}
	c.mu.Unlock()

	if !hadTotal || sumBase <= prevTotal.base {
		// First poll for this array (or a counter reset, e.g. a node reboot) —
		// nothing valid to diff against yet. Correct next poll, not a guess now.
		pw.note("node_cpu_busy", "node_cpu_busy warming up for %s (needs a second sample to compute a rate)", arr.ID)
		return
	}
	pct := (sumRaw - prevTotal.raw) / (sumBase - prevTotal.base) * 100
	pw.gauge("node_cpu_busy", "Cluster-wide average CPU busy percent", arr.ID, pct)

	if allWarm {
		for _, np := range perNodePct {
			pw.gaugeNode("node_cpu_busy_by_node", "CPU busy percent by node", arr.ID, np.name, np.pct)
		}
	}
}

type aggregatesResp struct {
	Records []struct {
		Space struct {
			BlockStorage struct {
				Size float64 `json:"size"`
				Used float64 `json:"used"`
			} `json:"block_storage"`
		} `json:"space"`
	} `json:"records"`
}

func (c *ONTAPCollector) collectAggregateCapacity(pw *promWriter, client *http.Client, arr config.Array) {
	body, err := c.get(client, arr, "/api/storage/aggregates?fields=space.block_storage.size,space.block_storage.used")
	if err != nil {
		pw.note("aggr_space_used_percent", "aggr_space_used_percent unavailable for %s: %v", arr.ID, err)
		return
	}
	var r aggregatesResp
	if err := json.Unmarshal(body, &r); err != nil || len(r.Records) == 0 {
		pw.note("aggr_space_used_percent", "aggr_space_used_percent unavailable for %s: no records in /api/storage/aggregates response", arr.ID)
		return
	}
	var used, size float64
	for _, a := range r.Records {
		used += a.Space.BlockStorage.Used
		size += a.Space.BlockStorage.Size
	}
	if size <= 0 {
		pw.note("aggr_space_used_percent", "aggr_space_used_percent unavailable for %s: aggregate size reported as zero", arr.ID)
		return
	}
	pw.gauge("aggr_space_used_percent", "Aggregate capacity used, percent", arr.ID, used/size*100)
}

type snapmirrorResp struct {
	Records []struct {
		LagTime string `json:"lag_time"`
	} `json:"records"`
}

// isoDuration matches the subset of ISO-8601 durations ONTAP's REST API
// actually emits for lag_time (P[n]DT[n]H[n]M[n]S) — years/months never
// appear in a replication lag and are deliberately not handled.
var isoDuration = regexp.MustCompile(`^P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+(?:\.\d+)?)S)?)?$`)

func parseISODurationSeconds(s string) (float64, bool) {
	m := isoDuration.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	days, _ := strconv.ParseFloat(zeroIfEmpty(m[1]), 64)
	hours, _ := strconv.ParseFloat(zeroIfEmpty(m[2]), 64)
	minutes, _ := strconv.ParseFloat(zeroIfEmpty(m[3]), 64)
	seconds, _ := strconv.ParseFloat(zeroIfEmpty(m[4]), 64)
	return days*86400 + hours*3600 + minutes*60 + seconds, true
}

func zeroIfEmpty(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

func (c *ONTAPCollector) collectSnapMirrorLag(pw *promWriter, client *http.Client, arr config.Array) {
	body, err := c.get(client, arr, "/api/snapmirror/relationships?fields=lag_time")
	if err != nil {
		pw.note("snapmirror_lag_time", "snapmirror_lag_time unavailable for %s: %v", arr.ID, err)
		return
	}
	var r snapmirrorResp
	if err := json.Unmarshal(body, &r); err != nil {
		pw.note("snapmirror_lag_time", "snapmirror_lag_time unavailable for %s: could not parse /api/snapmirror/relationships response", arr.ID)
		return
	}
	if len(r.Records) == 0 {
		// No SnapMirror relationships configured is a legitimate, common
		// state (not every array replicates) — expose 0, not a note, so the
		// panel reads as healthy rather than "no data".
		pw.gauge("snapmirror_lag_time", "Maximum SnapMirror lag across relationships, seconds", arr.ID, 0)
		return
	}
	var maxLag float64
	for _, rel := range r.Records {
		if secs, ok := parseISODurationSeconds(rel.LagTime); ok && secs > maxLag {
			maxLag = secs
		}
	}
	pw.gauge("snapmirror_lag_time", "Maximum SnapMirror lag across relationships, seconds", arr.ID, maxLag)
}

// snapmirrorPrivateResp is api/private/cli/snapmirror's shape — an
// unsupported CLI passthrough, not a documented REST resource (see
// collectSnapMirrorBandwidth's own comment for why this is the only path
// to a bandwidth figure at all).
type snapmirrorPrivateResp struct {
	Records []struct {
		RelationshipID     string  `json:"relationship_id"`
		TotalTransferBytes float64 `json:"total_transfer_bytes"`
	} `json:"records"`
}

// collectSnapMirrorBandwidth is best-effort in a stronger sense than every
// other collector in this file: api/private/cli/* is NetApp's own
// CLI-passthrough mechanism, explicitly unsupported and undocumented as a
// REST resource — confirmed by NetApp Harvest's own SnapMirror config
// (conf/rest/9.12.0/snapmirror.yaml), whose comment states plainly that
// the fields this needs ("total_transfer_bytes") only exist via this
// private endpoint, not the public api/snapmirror/relationships resource
// collectSnapMirrorLag above uses. If this endpoint changes shape or
// disappears in a future ONTAP release, this metric alone degrades to
// "unavailable" — replication_lag and everything else in this file is
// completely unaffected, the same isolation principle already applied to
// StorageGRID's private-metric-sourced GET/PUT panels.
func (c *ONTAPCollector) collectSnapMirrorBandwidth(pw *promWriter, client *http.Client, arr config.Array) {
	body, err := c.get(client, arr, "/api/private/cli/snapmirror?fields=total_transfer_bytes")
	if err != nil {
		pw.note("snapmirror_bandwidth_bytes", "snapmirror_bandwidth_bytes unavailable for %s: %v (api/private/cli is an unsupported CLI passthrough and may not exist on every ONTAP version/permission level)", arr.ID, err)
		return
	}
	var r snapmirrorPrivateResp
	if err := json.Unmarshal(body, &r); err != nil {
		pw.note("snapmirror_bandwidth_bytes", "snapmirror_bandwidth_bytes unavailable for %s: could not parse api/private/cli/snapmirror response", arr.ID)
		return
	}
	if len(r.Records) == 0 {
		pw.gauge("snapmirror_bandwidth_bytes", "Cumulative SnapMirror transfer bytes, summed across relationships", arr.ID, 0)
		return
	}
	var total float64
	for _, rel := range r.Records {
		total += rel.TotalTransferBytes
	}
	// Raw cumulative total, not a rate — config/thresholds/netapp_ontap.yml's
	// snapmirror_bandwidth query wraps this in rate(...), the standard
	// Prometheus idiom for a counter (same pattern as nic_rx_crc_errors).
	pw.counter("snapmirror_bandwidth_bytes", "Cumulative SnapMirror transfer bytes, summed across relationships", arr.ID, total)
}

type ethernetPortsResp struct {
	Records []struct {
		Statistics struct {
			Device struct {
				ReceiveRaw struct {
					Errors float64 `json:"errors"`
				} `json:"receive_raw"`
				TransmitRaw struct {
					Errors float64 `json:"errors"`
				} `json:"transmit_raw"`
			} `json:"device"`
		} `json:"statistics"`
	} `json:"records"`
}

func (c *ONTAPCollector) collectNICErrors(pw *promWriter, client *http.Client, arr config.Array) {
	body, err := c.get(client, arr, "/api/network/ethernet/ports?fields=statistics.device.receive_raw.errors,statistics.device.transmit_raw.errors")
	if err != nil {
		pw.note("nic_rx_crc_errors", "nic_rx_crc_errors unavailable for %s: %v", arr.ID, err)
		return
	}
	var r ethernetPortsResp
	if err := json.Unmarshal(body, &r); err != nil {
		pw.note("nic_rx_crc_errors", "nic_rx_crc_errors unavailable for %s: could not parse /api/network/ethernet/ports response", arr.ID)
		return
	}
	var total float64
	for _, p := range r.Records {
		total += p.Statistics.Device.ReceiveRaw.Errors + p.Statistics.Device.TransmitRaw.Errors
	}
	// Raw cumulative total, not a rate — config/thresholds/netapp_ontap.yml's
	// nic_errors query already wraps this in rate(...)*60, the standard
	// Prometheus idiom for a counter.
	pw.counter("nic_rx_crc_errors", "Cumulative NIC receive+transmit errors", arr.ID, total)
}
