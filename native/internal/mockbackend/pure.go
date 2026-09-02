package mockbackend

import (
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"plumb/internal/config"
	"plumb/internal/mockdata"
)

// counterAccumulator turns a bounded synthetic "target rate" into a
// monotonically increasing counter — needed anywhere a thresholds.yml
// query wraps a metric in rate(...), since mockdata.CurrentValue produces
// a wave-shaped instantaneous value, not a running total. Each call adds
// ratePerSecond × the real elapsed time since the previous call for that
// key, so the resulting rate() is correct regardless of how often the
// mock endpoint actually gets scraped — not tied to an assumed fixed
// interval.
type counterAccumulator struct {
	mu    sync.Mutex
	total map[string]float64
	at    map[string]time.Time
}

func newCounterAccumulator() *counterAccumulator {
	return &counterAccumulator{total: map[string]float64{}, at: map[string]time.Time{}}
}

func (c *counterAccumulator) accumulate(key string, ratePerSecond float64) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if last, had := c.at[key]; had {
		elapsed := now.Sub(last).Seconds()
		if elapsed > 0 {
			c.total[key] += ratePerSecond * elapsed
		}
	}
	c.at[key] = now
	return c.total[key]
}

// RegisterPureRoutes mounts one OpenMetrics text handler per Pure array in
// the mock fleet, matching exactly what internal/scrapeproxy expects to
// find at a real array's own /metrics endpoint. Metric names, label sets,
// and units match config/thresholds/pure_flasharray.yml and
// pure_flashblade.yml's actual queries — verified against the same public
// sources (PureStorage-OpenConnect's openmetrics-exporter specs) those
// threshold files themselves cite.
func RegisterPureRoutes(mux *http.ServeMux) {
	counters := newCounterAccumulator()
	for _, arr := range mockdata.Fleet {
		arr := arr
		switch arr.Vendor {
		case config.VendorPureFlashArray:
			mux.HandleFunc("GET "+pureMetricsPath(arr.ID), func(w http.ResponseWriter, r *http.Request) {
				writeFlashArrayMetrics(w, arr, counters)
			})
		case config.VendorPureFlashBlade:
			mux.HandleFunc("GET "+pureMetricsPath(arr.ID), func(w http.ResponseWriter, r *http.Request) {
				writeFlashBladeMetrics(w, arr)
			})
		}
	}
}

func pureMetricsPath(arrayID string) string {
	return "/mockbackend/pure/" + arrayID + "/metrics"
}

// PureArrayConfig returns the config.Array Plumb should scrape for this
// mock Pure system while mock mode is on — pointed at this process's own
// mock endpoint through the exact same scrape-proxy path a real array uses.
func PureArrayConfig(selfAddr string, arr mockdata.Array) config.Array {
	c := arr.AsConfigArray()
	c.Host = selfAddr
	c.Scheme = "http"
	c.MetricsPath = pureMetricsPath(arr.ID)
	return c
}

func fprintGauge(w io.Writer, name, help string, labels string, value float64) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s%s %g\n", name, help, name, name, labels, value)
}

func writeFlashArrayMetrics(w http.ResponseWriter, arr mockdata.Array, counters *counterAccumulator) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	now := time.Now()

	// host_latency / host_latency_read / host_latency_write: dimension
	// values here must be Pure's REAL ones (usec_per_read_op,
	// usec_per_write_op — see specification/metrics/purefa-metrics.md in
	// PureStorage-OpenConnect/pure-fa-openmetrics-exporter), not the
	// placeholder "read"/"write" this used before. That placeholder only
	// ever worked by accident: the old thresholds.yml query,
	// dimension=~"read|write", was an unanchored regex that matched the
	// literal strings "read"/"write" as a trivial special case. Now that
	// the query is anchored to the exact real dimension names, this must
	// emit them for real too, thresholds (watch 2.0ms/critical 3.5ms) are
	// in ms, so emit microseconds (×1000).
	readMs := arr.CurrentValue("host_latency", arr.ID+"|host_latency|read", "frontend", 2.0, 3.5, now)
	writeMs := arr.CurrentValue("host_latency", arr.ID+"|host_latency|write", "frontend", 2.0, 3.5, now)
	fprintGauge(w, "purefa_array_performance_latency_usec", "FlashArray array latency in microseconds", `{dimension="usec_per_read_op"}`, readMs*1000)
	fprintGauge(w, "purefa_array_performance_latency_usec", "FlashArray array latency in microseconds", `{dimension="usec_per_write_op"}`, writeMs*1000)

	// host_latency's node_breakdown_query: purefa_volume_performance_latency_usec,
	// same dimension convention, one level down (per volume). vol-1 tracks
	// this array's real severity, vol-2 stays healthy — same masking demo
	// as every other breakdown in this codebase.
	vol1ReadMs := arr.CurrentValue("host_latency", arr.ID+"|host_latency|read", "frontend", 2.0, 3.5, now)
	vol1WriteMs := arr.CurrentValue("host_latency", arr.ID+"|host_latency|write", "frontend", 2.0, 3.5, now)
	vol2ReadMs := arr.ValueForSeverity(arr.ID+"|host_latency|vol-2|read", "healthy", 2.0, 3.5, now)
	vol2WriteMs := arr.ValueForSeverity(arr.ID+"|host_latency|vol-2|write", "healthy", 2.0, 3.5, now)
	fprintGauge(w, "purefa_volume_performance_latency_usec", "FlashArray volume latency in microseconds", `{name="vol-1",dimension="usec_per_read_op"}`, vol1ReadMs*1000)
	fprintGauge(w, "purefa_volume_performance_latency_usec", "FlashArray volume latency in microseconds", `{name="vol-1",dimension="usec_per_write_op"}`, vol1WriteMs*1000)
	fprintGauge(w, "purefa_volume_performance_latency_usec", "FlashArray volume latency in microseconds", `{name="vol-2",dimension="usec_per_read_op"}`, vol2ReadMs*1000)
	fprintGauge(w, "purefa_volume_performance_latency_usec", "FlashArray volume latency in microseconds", `{name="vol-2",dimension="usec_per_write_op"}`, vol2WriteMs*1000)

	// volume_space_used_percent: purefa_volume_space_bytes, total_physical
	// vs total_provisioned per volume. vol-1 tracks real severity, vol-2
	// stays healthy.
	volSizeBytes := 2_000_000_000_000.0 // 2TB provisioned, a round number for a readable mock response
	vol1Pct := arr.CurrentValue("volume_space_used_percent", arr.ID+"|volume_space_used_percent", "backend", 80, 95, now)
	vol2Pct := arr.ValueForSeverity(arr.ID+"|volume_space_used_percent|vol-2", "healthy", 80, 95, now)
	fprintGauge(w, "purefa_volume_space_bytes", "FlashArray volume space in bytes", `{name="vol-1",space="total_provisioned"}`, volSizeBytes)
	fprintGauge(w, "purefa_volume_space_bytes", "FlashArray volume space in bytes", `{name="vol-1",space="total_physical"}`, volSizeBytes*vol1Pct/100)
	fprintGauge(w, "purefa_volume_space_bytes", "FlashArray volume space in bytes", `{name="vol-2",space="total_provisioned"}`, volSizeBytes)
	fprintGauge(w, "purefa_volume_space_bytes", "FlashArray volume space in bytes", `{name="vol-2",space="total_physical"}`, volSizeBytes*vol2Pct/100)

	// host_iops_read / host_iops_write: purefa_array_performance_throughput_iops,
	// dimension reads_per_sec/writes_per_sec — genuinely new metric, not
	// previously emitted at all. Independent wave functions from latency
	// (real IOPS and real latency don't move in lockstep), same
	// illustrative-baseline framing as the thresholds file.
	readIOPS := arr.CurrentValue("host_iops_read", arr.ID+"|host_iops_read", "frontend", 100000, 200000, now)
	writeIOPS := arr.CurrentValue("host_iops_write", arr.ID+"|host_iops_write", "frontend", 100000, 200000, now)
	fprintGauge(w, "purefa_array_performance_throughput_iops", "FlashArray array throughput in operations per second", `{dimension="reads_per_sec"}`, readIOPS)
	fprintGauge(w, "purefa_array_performance_throughput_iops", "FlashArray array throughput in operations per second", `{dimension="writes_per_sec"}`, writeIOPS)

	// host_bandwidth_read / host_bandwidth_write: purefa_array_performance_bandwidth_bytes,
	// dimension read_bytes_per_sec/write_bytes_per_sec — thresholds are in
	// MB/s, so emit bytes/sec (×1,000,000).
	readMBs := arr.CurrentValue("host_bandwidth_read", arr.ID+"|host_bandwidth_read", "frontend", 1000, 2000, now)
	writeMBs := arr.CurrentValue("host_bandwidth_write", arr.ID+"|host_bandwidth_write", "frontend", 1000, 2000, now)
	fprintGauge(w, "purefa_array_performance_bandwidth_bytes", "FlashArray array bandwidth in bytes per second", `{dimension="read_bytes_per_sec"}`, readMBs*1000000)
	fprintGauge(w, "purefa_array_performance_bandwidth_bytes", "FlashArray array bandwidth in bytes per second", `{dimension="write_bytes_per_sec"}`, writeMBs*1000000)

	// host_queue_depth: avg(...), no conversion, thresholds already in ops.
	queue := arr.CurrentValue("host_queue_depth", arr.ID+"|host_queue_depth", "frontend", 350, 450, now)
	fprintGauge(w, "purefa_array_performance_queue_depth_ops", "FlashArray array queue depth size", "", queue)

	// network_errors: sum(rate(...)[5m])*60, thresholds already in
	// errors/min — accumulate at that target per-second rate so the
	// resulting rate() lands in the intended severity band regardless of
	// the real scrape interval. Band args (0.01, 5) mirror the real
	// severity_watch/severity_critical in pure_flasharray.yml: industry
	// practice treats any sustained nonzero CRC/link error rate as a fault,
	// not background noise, so "healthy" here targets genuine near-zero
	// rather than a comfortable nonzero band like every other metric.
	targetPerMin := arr.CurrentValue("network_errors", arr.ID+"|network_errors", "frontend", 0.01, 5, now)
	total := counters.accumulate(arr.ID+"|network_errors", targetPerMin/60.0)
	fmt.Fprintf(w, "# HELP purefa_network_interface_performance_errors FlashArray network interfaces errors per second\n")
	fmt.Fprintf(w, "# TYPE purefa_network_interface_performance_errors counter\n")
	fmt.Fprintf(w, `purefa_network_interface_performance_errors{interface="ct0.FC1"} %g`+"\n", total)

	// replication_lag: avg(...) / 1000, thresholds in seconds -> emit ms.
	// Two pods (not one) so the per-pod breakdown has something to
	// demonstrate: dr-secondary tracks this array's real severity,
	// dr-tertiary stays current, same masking-demo shape as everywhere else.
	lagSec := arr.CurrentValue("replication_lag", arr.ID+"|replication_lag", "backend", 90, 180, now)
	lagSec2 := arr.ValueForSeverity(arr.ID+"|replication_lag|dr-tertiary", "healthy", 90, 180, now)
	fprintGauge(w, "purefa_pod_replica_links_lag_average_msec", "FlashArray pod replica links average lag in milliseconds", `{pod="dr-secondary"}`, lagSec*1000)
	fprintGauge(w, "purefa_pod_replica_links_lag_average_msec", "FlashArray pod replica links average lag in milliseconds", `{pod="dr-tertiary"}`, lagSec2*1000)

	// replication_bandwidth: sum(...) / 1e6, thresholds in MB/s -> emit bytes/sec.
	replMBs := arr.CurrentValue("replication_bandwidth", arr.ID+"|replication_bandwidth", "backend", 1000, 2000, now)
	fprintGauge(w, "purefa_pod_replica_links_performance_bandwidth_bytes", "FlashArray pod replica links bandwidth in bytes per second", `{remote="dr-target",local_pod="prod-pod",remote_pod="dr-secondary",direction="in",dimension="bytes_per_sec_total"}`, replMBs*1000000)

	// pool_saturation: avg(...), already a percent, no conversion.
	sat := arr.CurrentValue("pool_saturation", arr.ID+"|pool_saturation", "backend", 75, 85, now)
	fprintGauge(w, "purefa_array_space_utilization", "FlashArray array space utilization in percent", "", sat)
}

func writeFlashBladeMetrics(w http.ResponseWriter, arr mockdata.Array) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	now := time.Now()

	// bucket_latency / bucket_latency_read / bucket_latency_write: real
	// dimension values (usec_per_read_op/usec_per_write_op — see
	// specification/metrics/purefb-metrics.md), not the old placeholder
	// "read"/"write" strings, same correction as pure.go's FlashArray
	// path and for the same reason (the old unanchored regex tolerated
	// the placeholder; the corrected one doesn't). Thresholds (watch
	// 5ms/critical 15ms) in ms -> emit microseconds.
	readMs := arr.CurrentValue("bucket_latency", arr.ID+"|bucket_latency|read", "frontend", 5, 15, now)
	writeMs := arr.CurrentValue("bucket_latency", arr.ID+"|bucket_latency|write", "frontend", 5, 15, now)
	fprintGauge(w, "purefb_buckets_performance_latency_usec", "FlashBlade buckets latency in microseconds", `{name="media-archive",dimension="usec_per_read_op"}`, readMs*1000)
	fprintGauge(w, "purefb_buckets_performance_latency_usec", "FlashBlade buckets latency in microseconds", `{name="media-archive",dimension="usec_per_write_op"}`, writeMs*1000)

	// bucket_throughput / bucket_throughput_read / bucket_throughput_write:
	// real dimension values (reads_per_sec/writes_per_sec), independent
	// wave functions per direction rather than one total split by a fixed
	// ratio, so read and write can genuinely diverge in the demo.
	readIOPS := arr.CurrentValue("bucket_throughput_read", arr.ID+"|bucket_throughput_read", "backend", 100000, 200000, now)
	writeIOPS := arr.CurrentValue("bucket_throughput_write", arr.ID+"|bucket_throughput_write", "backend", 100000, 200000, now)
	fprintGauge(w, "purefb_buckets_performance_throughput_iops", "FlashBlade buckets throughput in operations per second", `{name="media-archive",dimension="reads_per_sec"}`, readIOPS)
	fprintGauge(w, "purefb_buckets_performance_throughput_iops", "FlashBlade buckets throughput in operations per second", `{name="media-archive",dimension="writes_per_sec"}`, writeIOPS)

	// bucket_bandwidth_read / bucket_bandwidth_write: genuinely new metric,
	// not previously emitted. Thresholds in MB/s -> emit bytes/sec.
	readMBs := arr.CurrentValue("bucket_bandwidth_read", arr.ID+"|bucket_bandwidth_read", "backend", 1000, 2000, now)
	writeMBs := arr.CurrentValue("bucket_bandwidth_write", arr.ID+"|bucket_bandwidth_write", "backend", 1000, 2000, now)
	fprintGauge(w, "purefb_buckets_performance_bandwidth_bytes", "FlashBlade buckets bandwidth in bytes per second", `{name="media-archive",dimension="read_bytes_per_sec"}`, readMBs*1000000)
	fprintGauge(w, "purefb_buckets_performance_bandwidth_bytes", "FlashBlade buckets bandwidth in bytes per second", `{name="media-archive",dimension="write_bytes_per_sec"}`, writeMBs*1000000)
}
