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

	// host_latency: avg(...{dimension=~"read|write"}) / 1000 — thresholds
	// (watch 2.0ms/critical 3.5ms) are in ms, so emit microseconds (×1000).
	readMs := arr.CurrentValue("host_latency", arr.ID+"|host_latency|read", "frontend", 2.0, 3.5, now)
	writeMs := arr.CurrentValue("host_latency", arr.ID+"|host_latency|write", "frontend", 2.0, 3.5, now)
	fprintGauge(w, "purefa_array_performance_latency_usec", "FlashArray array latency in microseconds", `{dimension="read"}`, readMs*1000)
	fprintGauge(w, "purefa_array_performance_latency_usec", "FlashArray array latency in microseconds", `{dimension="write"}`, writeMs*1000)

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
	lagSec := arr.CurrentValue("replication_lag", arr.ID+"|replication_lag", "backend", 90, 180, now)
	fprintGauge(w, "purefa_pod_replica_links_lag_average_msec", "FlashArray pod replica links average lag in milliseconds", `{pod="dr-secondary"}`, lagSec*1000)

	// pool_saturation: avg(...), already a percent, no conversion.
	sat := arr.CurrentValue("pool_saturation", arr.ID+"|pool_saturation", "backend", 75, 85, now)
	fprintGauge(w, "purefa_array_space_utilization", "FlashArray array space utilization in percent", "", sat)
}

func writeFlashBladeMetrics(w http.ResponseWriter, arr mockdata.Array) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	now := time.Now()

	// bucket_latency: avg(...{dimension=~"read|write"}) / 1000, thresholds
	// (watch 5ms/critical 15ms) in ms -> emit microseconds.
	readMs := arr.CurrentValue("bucket_latency", arr.ID+"|bucket_latency|read", "frontend", 5, 15, now)
	writeMs := arr.CurrentValue("bucket_latency", arr.ID+"|bucket_latency|write", "frontend", 5, 15, now)
	fprintGauge(w, "purefb_buckets_performance_latency_usec", "FlashBlade buckets latency in microseconds", `{name="media-archive",dimension="read"}`, readMs*1000)
	fprintGauge(w, "purefb_buckets_performance_latency_usec", "FlashBlade buckets latency in microseconds", `{name="media-archive",dimension="write"}`, writeMs*1000)

	// bucket_throughput: sum(...{dimension=~"read|write"}), illustrative
	// placeholder threshold — split across the two dimensions Pure
	// publishes, read-heavy to match a typical media-archive workload.
	total := arr.CurrentValue("bucket_throughput", arr.ID+"|bucket_throughput", "backend", 100000, 200000, now)
	fprintGauge(w, "purefb_buckets_performance_throughput_iops", "FlashBlade buckets throughput in operations per second", `{name="media-archive",dimension="read"}`, total*0.6)
	fprintGauge(w, "purefb_buckets_performance_throughput_iops", "FlashBlade buckets throughput in operations per second", `{name="media-archive",dimension="write"}`, total*0.4)
}
