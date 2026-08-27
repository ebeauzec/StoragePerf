"""
Synthetic stand-in for a real array/cluster/grid's metrics endpoint —
covers all four vendors Plumb supports, for demonstration purposes.

Emits exactly the metric names each vendor's config/thresholds/*.yml
queries. Those names are verified against each vendor's own published
documentation (see the comments at the top of each thresholds file) —
this exporter mirrors reality, it doesn't invent it. Set VENDOR to pick
which metric family to emit, and PROFILE to pick a severity story:

  VENDOR:  pure_flasharray | pure_flashblade | netapp_ontap | netapp_storagegrid
  PROFILE: healthy | watch | critical

Not a faithful emulation of any real product's exporter — it exists to
exercise Plumb's pipeline and UI across every vendor, not to model real
workload behavior. See native/scripts/run-demo.sh to launch a full
multi-vendor demo fleet at once.
"""
import os
import random
import threading
import time

from prometheus_client import Counter, Gauge, start_http_server

VENDOR = os.environ.get("VENDOR", "pure_flasharray")
PROFILE = os.environ.get("PROFILE", "healthy")
PORT = int(os.environ.get("PORT", "9491"))


def drift(center, spread, floor=0.0, ceil=None):
    v = max(floor, random.gauss(center, spread))
    if ceil is not None:
        v = min(ceil, v)
    return v


# ---------------------------------------------------------------------------
# Pure FlashArray — purefa_* (verified: pure-fa-openmetrics-exporter docs)
# ---------------------------------------------------------------------------
def run_pure_flasharray(profile):
    latency = Gauge("purefa_array_performance_latency_usec", "Latency", ["dimension"])
    queue_depth = Gauge("purefa_array_performance_queue_depth_ops", "Outstanding host I/O")
    network_errors = Counter("purefa_network_interface_performance_errors", "Network interface errors", ["interface"])
    replica_lag = Gauge("purefa_pod_replica_links_lag_average_msec", "Replication lag (ms)", ["pod"])
    space_utilization = Gauge("purefa_array_space_utilization", "Capacity utilization percent")

    profiles = {
        "healthy":  dict(lat=(300, 100), q=(120, 25), crc=0.01, lag=(30, 8), space=(45, 4)),
        "watch":    dict(lat=(2100, 300), q=(370, 30), crc=0.06, lag=(60, 12), space=(60, 4)),
        "critical": dict(lat=(3900, 400), q=(420, 30), crc=1.4, lag=(45, 12), space=(64, 3)),
    }
    p = profiles[profile]
    while True:
        latency.labels(dimension="read").set(drift(*p["lat"]))
        latency.labels(dimension="write").set(drift(p["lat"][0] * 1.15, p["lat"][1]))
        queue_depth.set(drift(*p["q"]))
        if random.random() < p["crc"]:
            network_errors.labels(interface="ct0.FC1").inc(random.randint(1, 4))
        replica_lag.labels(pod="dr-secondary").set(drift(*p["lag"]))
        space_utilization.set(drift(*p["space"], ceil=100))
        time.sleep(5)


# ---------------------------------------------------------------------------
# Pure FlashBlade — purefb_* (verified: pure-fb-openmetrics-exporter spec;
# deliberately minimal, matching config/thresholds/pure_flashblade.yml's
# own caveat that FlashBlade's public metric spec is itself incomplete)
# ---------------------------------------------------------------------------
def run_pure_flashblade(profile):
    bucket_latency = Gauge("purefb_buckets_performance_latency_usec", "Bucket latency", ["name", "dimension"])
    bucket_iops = Gauge("purefb_buckets_performance_throughput_iops", "Bucket IOPS", ["name", "dimension"])

    profiles = {
        "healthy":  dict(lat=(1500, 400), iops=(60000, 12000)),
        "watch":    dict(lat=(6500, 800), iops=(120000, 15000)),
        "critical": dict(lat=(18000, 2000), iops=(210000, 20000)),
    }
    p = profiles[profile]
    while True:
        bucket_latency.labels(name="media-archive", dimension="read").set(drift(*p["lat"]))
        bucket_latency.labels(name="media-archive", dimension="write").set(drift(p["lat"][0] * 1.1, p["lat"][1]))
        bucket_iops.labels(name="media-archive", dimension="read").set(drift(*p["iops"]))
        bucket_iops.labels(name="media-archive", dimension="write").set(drift(p["iops"][0] * 0.4, p["iops"][1]))
        time.sleep(5)


# ---------------------------------------------------------------------------
# NetApp ONTAP — via Harvest's own metric names (verified against Harvest's
# published Grafana dashboards)
# ---------------------------------------------------------------------------
def run_netapp_ontap(profile):
    volume_latency = Gauge("volume_avg_latency", "Volume average latency (ms)")
    nic_util = Gauge("nic_util_percent", "NIC utilization percent", ["port"])
    nic_crc_errors = Counter("nic_rx_crc_errors", "NIC CRC errors", ["port"])
    node_cpu_busy = Gauge("node_cpu_busy", "Node CPU busy percent")
    aggr_disk_busy = Gauge("aggr_disk_busy", "Aggregate disk busy percent")
    snapmirror_lag = Gauge("snapmirror_lag_time", "SnapMirror lag (seconds)")
    aggr_space_used = Gauge("aggr_space_used_percent", "Aggregate capacity used percent")

    profiles = {
        "healthy":  dict(lat=(4, 2), nic=(30, 8), crc=0.01, cpu=(35, 6), disk=(30, 6), lag=(20, 8), space=(50, 4)),
        "watch":    dict(lat=(10, 2), nic=(80, 5), crc=0.05, cpu=(60, 5), disk=(72, 5), lag=(100, 15), space=(78, 4)),
        "critical": dict(lat=(20, 3), nic=(93, 3), crc=1.2, cpu=(50, 5), disk=(45, 5), lag=(50, 15), space=(60, 3)),
    }
    p = profiles[profile]
    while True:
        volume_latency.set(drift(*p["lat"]))
        nic_util.labels(port="e0a").set(drift(*p["nic"], ceil=100))
        if random.random() < p["crc"]:
            nic_crc_errors.labels(port="e0a").inc(random.randint(1, 4))
        node_cpu_busy.set(drift(*p["cpu"], ceil=100))
        aggr_disk_busy.set(drift(*p["disk"], ceil=100))
        snapmirror_lag.set(drift(*p["lag"]))
        aggr_space_used.set(drift(*p["space"], ceil=100))
        time.sleep(5)


# ---------------------------------------------------------------------------
# NetApp StorageGRID — official metric names (verified against
# docs.netapp.com "Commonly used Prometheus metrics")
# ---------------------------------------------------------------------------
def run_netapp_storagegrid(profile):
    metadata_latency = Gauge("storagegrid_metadata_queries_average_latency_milliseconds", "Metadata query latency (ms)")
    s3_failed = Counter("storagegrid_s3_operations_failed", "Failed S3 operations")
    net_rx_errors = Counter("node_network_receive_errs_total", "Network receive errors")
    net_tx_errors = Counter("node_network_transmit_errs_total", "Network transmit errors")
    node_cpu = Gauge("storagegrid_node_cpu_utilization_percentage", "Node CPU utilization percent")
    ilm_backlog = Gauge("storagegrid_ilm_awaiting_total_objects", "Objects awaiting ILM evaluation")
    usable_bytes = Gauge("storagegrid_storage_utilization_usable_space_bytes", "Usable storage bytes")
    total_bytes = Gauge("storagegrid_storage_utilization_total_space_bytes", "Total storage bytes")

    total = 200e12  # 200TB node, fixed
    profiles = {
        "healthy":  dict(lat=(20, 8), s3fail=0.02, neterr=0.01, cpu=(35, 6), ilm=(2000, 800), used_frac=(0.45, 0.04)),
        "watch":    dict(lat=(80, 15), s3fail=0.15, neterr=0.05, cpu=(60, 5), ilm=(150000, 30000), used_frac=(0.72, 0.04)),
        "critical": dict(lat=(220, 30), s3fail=0.6, neterr=0.8, cpu=(50, 5), ilm=(1500000, 200000), used_frac=(0.55, 0.03)),
    }
    p = profiles[profile]
    total_bytes.set(total)
    while True:
        metadata_latency.set(drift(*p["lat"]))
        if random.random() < p["s3fail"]:
            s3_failed.inc(random.randint(1, 5))
        if random.random() < p["neterr"]:
            net_rx_errors.inc(random.randint(1, 3))
            net_tx_errors.inc(random.randint(1, 3))
        node_cpu.set(drift(*p["cpu"], ceil=100))
        ilm_backlog.set(drift(*p["ilm"], floor=0))
        used_frac = drift(*p["used_frac"], ceil=0.98)
        usable_bytes.set(total * (1 - used_frac))
        time.sleep(5)


RUNNERS = {
    "pure_flasharray": run_pure_flasharray,
    "pure_flashblade": run_pure_flashblade,
    "netapp_ontap": run_netapp_ontap,
    "netapp_storagegrid": run_netapp_storagegrid,
}


if __name__ == "__main__":
    runner = RUNNERS.get(VENDOR)
    if runner is None:
        raise SystemExit(f"unknown VENDOR={VENDOR!r}, expected one of {list(RUNNERS)}")
    threading.Thread(target=runner, args=(PROFILE,), daemon=True).start()
    start_http_server(PORT)
    while True:
        time.sleep(3600)
