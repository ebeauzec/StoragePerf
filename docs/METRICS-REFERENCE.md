# Metrics Reference — All Platforms

This is the detailed reference for every metric Plumb collects: what it
means, how it's collected, how to interpret it, and — critically — how the
same underlying question ("is this system's queue backing up?") is answered
differently by each vendor's published metrics. Read
[PERFORMANCE-ANALYSIS.md](PERFORMANCE-ANALYSIS.md) first if you want the
concepts before the specifics.

Every metric name below is verified against that vendor's own published
documentation or open-source reference project — see
[THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md) for exact sources. None
are guessed.

## Table of Contents

1. [Collection Architecture, Per Vendor](#1-collection-architecture-per-vendor)
2. [Side-by-Side: The Same Concept, Four Vendors](#2-side-by-side-the-same-concept-four-vendors)
3. [Pure Storage FlashArray](#3-pure-storage-flasharray)
4. [Pure Storage FlashBlade](#4-pure-storage-flashblade)
5. [NetApp ONTAP](#5-netapp-ontap)
6. [NetApp StorageGRID](#6-netapp-storagegrid)
7. [How to Verify a Metric Name Yourself](#7-how-to-verify-a-metric-name-yourself)
8. [What Each Vendor Cannot Tell You](#8-what-each-vendor-cannot-tell-you)

---

## 1. Collection Architecture, Per Vendor

| Vendor | Collection path | Why |
|---|---|---|
| Pure FlashArray / FlashBlade | Plumb's own `/scrape/{id}` proxy fetches the array's native OpenMetrics endpoint directly, using a per-array API token | The array itself serves the metrics — Purity has run a native OpenMetrics exporter since 6.4.2+ (FlashArray) / equivalent releases (FlashBlade). Plumb's proxy exists so each array's token stays in one config file, never in Prometheus's own config |
| NetApp ONTAP | A bundled [Harvest](https://github.com/NetApp/harvest) poller, run as a Plumb-managed subprocess, does its own authenticated REST/RestPerf collection against the cluster and exposes a plain local Prometheus endpoint for Plumb's Prometheus to scrape | ONTAP has no native Prometheus endpoint of its own — Harvest is NetApp's own official answer to this, and per NetApp's 2026 roadmap is explicitly the vendor-supported way to get ONTAP into a consistent Prometheus metric model |
| NetApp StorageGRID | Same Harvest mechanism, using Harvest's `StorageGrid` collector against the Grid Management API | StorageGRID also runs its own internal Prometheus on Admin Nodes (reachable directly via the Grid Management API with a client certificate) — Harvest is used here for a collection mechanism consistent with the ONTAP path, so both NetApp products land in Plumb the same way |

**Platform note:** NetApp only publishes a **linux/amd64** Harvest binary.
NetApp monitoring is only available in Plumb's linux/amd64 distribution;
Pure monitoring works on every platform Plumb ships for. See
[../native/README.md](../native/README.md).

---

## 2. Side-by-Side: The Same Concept, Four Vendors

This is the comparison table the whole project exists to make unnecessary to
build yourself. Each row is one performance concept; each column is how that
vendor's public metrics answer it.

| Concept | Pure FlashArray | Pure FlashBlade | NetApp ONTAP | NetApp StorageGRID |
|---|---|---|---|---|
| **Client-facing latency** | `purefa_array_performance_latency_usec` (µs, per read/write dimension) | `purefb_buckets_performance_latency_usec` (µs, per bucket + dimension) | `volume_avg_latency` (ms, per volume) | `storagegrid_metadata_queries_average_latency_milliseconds` (ms — one internal operation, not full request latency) |
| **IOPS / throughput** | `purefa_array_performance_throughput_iops`, `purefa_array_performance_bandwidth_bytes` | `purefb_buckets_performance_throughput_iops` | `volume_total_ops`, `fcp_total_ops` | `storagegrid_s3_operations_successful` / `_failed` (counts, not a rate metric) |
| **Saturation / backlog** | `purefa_array_performance_queue_depth_ops` (array-wide) | *(no confirmed public equivalent — see [Section 8](#8-what-each-vendor-cannot-tell-you))* | `volume_total_ops` as a load proxy; no direct queue-depth metric confirmed | `storagegrid_ilm_awaiting_total_objects` — a genuinely unique signal: object storage's lifecycle-management backlog, an early warning before client latency degrades |
| **Controller / node utilization** | *(not published — see Section 8)* | *(not published)* | `node_cpu_busy`, `aggr_disk_busy` — both real, both direct | `storagegrid_node_cpu_utilization_percentage` — real, direct |
| **Network/port errors** | `purefa_network_interface_performance_errors` (generic — no confirmed FC-vs-Ethernet label) | *(not confirmed public)* | `nic_rx_crc_errors`, plus general `nic_*` error counters, genuinely per-port | `node_network_receive_errs_total` / `transmit_errs_total` (standard node_exporter metrics, not StorageGRID-specific) |
| **Replication health** | `purefa_pod_replica_links_lag_average_msec` | *(not in current thresholds — bucket replication not covered)* | `snapmirror_lag_time` (seconds — the most direct unit of any vendor here) | *(grid-federation replication is architecturally different — not a single per-node lag metric)* |
| **Capacity / saturation** | `purefa_array_space_utilization` (already a %) | *(space label values not fully published — see [Section 4](#4-pure-storage-flashblade))* | `aggr_space_used_percent` | Computed from `storagegrid_storage_utilization_usable_space_bytes` / `_total_space_bytes` |

**Reading this table honestly:** NetApp ONTAP's public metric surface is the
most complete of the four for the classic "is it the array or the network"
question, because it's the only one that publishes true controller/disk
busy%. Pure's public OpenMetrics endpoint and FlashBlade's published spec are
both missing that specific signal — not because Plumb failed to find it, but
because it isn't part of what those endpoints publish. This asymmetry is
real, documented in each vendor's section below, and directly shapes how
confidently Plumb's correlation finding (see
[PERFORMANCE-ANALYSIS.md §7](PERFORMANCE-ANALYSIS.md#7-the-correlation-finding--how-plumb-decides-upstream-vs-internal))
can speak for each vendor.

---

## 3. Pure Storage FlashArray

**Config file:** `config/thresholds/pure_flasharray.yml`
**Collection:** direct scrape via Plumb's proxy, native OpenMetrics endpoint
(Purity//FA 6.4.2+)
**Reference:** [pure-fa-openmetrics-exporter](https://github.com/PureStorage-OpenConnect/pure-fa-openmetrics-exporter) (Apache-2.0, Pure Storage's own published spec)

| Metric ID | Prometheus metric | Category | What it measures | Interpretation |
|---|---|---|---|---|
| `host_latency` | `purefa_array_performance_latency_usec{dimension=~"read\|write"}` | Front-end | Average array-side latency across read and write operations, converted µs→ms | The headline "does this array feel slow" number. See [PERFORMANCE-ANALYSIS.md §2](PERFORMANCE-ANALYSIS.md#2-latency-is-the-number-that-matters-most--and-the-hardest-to-read) for what "array-side" does and doesn't include |
| `host_queue_depth` | `purefa_array_performance_queue_depth_ops` | Front-end | Outstanding host I/O, array-wide (not broken out per port) | A saturation signal — see [PERFORMANCE-ANALYSIS.md §4](PERFORMANCE-ANALYSIS.md#4-saturation-vs-utilization). Rising while latency is flat often means hosts are about to feel it next |
| `network_errors` | `purefa_network_interface_performance_errors` (rate, summed across all interfaces) | Front-end | Interface-level errors, generic — no confirmed label distinguishing FC from Ethernet ports | If your `/metrics` output has an interface-identifying label (check for a `ct0.FC1`-style pattern), scope this query to it for per-port granularity — the shipped default sums the whole array |
| `replication_lag` | `purefa_pod_replica_links_lag_average_msec` (ms→sec) | Back-end | Average replication lag across all replica links on the array | High values threaten your recovery point objective before they threaten performance — treat as a data-protection metric that happens to live in the "back-end" column |
| `pool_saturation` | `purefa_array_space_utilization` | Back-end | Array-wide capacity utilization, already a percentage | Most storage systems' performance degrades as capacity fills — this is as much a leading performance indicator as a capacity one |

**What's deliberately not here:** controller busy%/CPU and internal media
service-time have no published metric on Pure's native OpenMetrics endpoint
as of this writing. This is why the FlashArray back-end column is thinner
than NetApp ONTAP's — see [Section 8](#8-what-each-vendor-cannot-tell-you).

---

## 4. Pure Storage FlashBlade

**Config file:** `config/thresholds/pure_flashblade.yml`
**Collection:** direct scrape via Plumb's proxy, native OpenMetrics endpoint
**Reference:** [pure-fb-openmetrics-exporter](https://github.com/PureStorage-OpenConnect/pure-fb-openmetrics-exporter) (Apache-2.0) — **this spec is itself
incomplete as published**, with several sections (including the array-wide
rollup) marked "TODO" by Pure Storage at the time this was checked

| Metric ID | Prometheus metric | Category | What it measures | Interpretation |
|---|---|---|---|---|
| `bucket_latency` | `purefb_buckets_performance_latency_usec{dimension=~"read\|write"}` (µs→ms) | Front-end | Average latency across S3/object bucket operations | FlashBlade's workloads (object, file) behave differently from FlashArray's block workloads — expect different normal ranges, not the same thresholds |
| `bucket_throughput` | `purefb_buckets_performance_throughput_iops{dimension=~"read\|write"}` | Back-end | Aggregate bucket-level operation rate | Shipped with an intentionally generic placeholder threshold — see the file's own header comment. Set this from your own observed baseline, not the default |

**This is the thinnest vendor file in Plumb, on purpose.** FlashBlade's own
published metrics spec doesn't yet document an array-wide performance
rollup, a capacity-utilization metric with confirmed label values, or
anything analogous to FlashArray's `queue_depth_ops`. Padding this file with
guessed metric names would be worse than the current honest gap — see
`config/thresholds/pure_flashblade.yml`'s own header for the full caveat.
If you run FlashBlade and can confirm additional metric names against your
own array's `/metrics` output, this file is the place to extend.

---

## 5. NetApp ONTAP

**Config file:** `config/thresholds/netapp_ontap.yml`
**Collection:** bundled Harvest poller (linux/amd64 only)
**Reference:** [NetApp/harvest](https://github.com/NetApp/harvest) Grafana
dashboard definitions (Apache-2.0) — Harvest's own published visualizations
of the exact metrics it exposes

| Metric ID | Prometheus metric | Category | What it measures | Interpretation |
|---|---|---|---|---|
| `volume_avg_latency` | `volume_avg_latency` | Front-end | Average per-volume latency, in milliseconds directly (no unit conversion needed) | NetApp's own guidance is explicit that this is workload-dependent — see [PERFORMANCE-ANALYSIS.md §6](PERFORMANCE-ANALYSIS.md#6-why-plumbs-thresholds-are-illustrative-not-gospel) for their own published example |
| `nic_utilization` | `nic_util_percent` (max across ports) | Front-end | Network port utilization percentage | A real, confirmed utilization metric — unlike Pure, ONTAP's public metrics do publish this directly, per-port |
| `nic_errors` | `nic_rx_crc_errors` (rate) | Front-end | CRC/receive errors, genuinely attributable to a specific port | The most direct front-end error signal of any vendor Plumb supports — no generic-vs-specific caveat needed here |
| `node_cpu_busy` | `node_cpu_busy` | Back-end | Controller/node CPU busy percentage | This is the metric Pure's public endpoint doesn't have an equivalent for — ONTAP's correlation findings can speak to "is the controller itself busy" with real data, not just an absence of other symptoms |
| `aggr_disk_busy` | `aggr_disk_busy` | Back-end | Aggregate-level disk busy percentage | The media-side counterpart to `node_cpu_busy` — high values here point at the disks/SSDs themselves, not the controller |
| `snapmirror_lag` | `snapmirror_lag_time` | Back-end | Replication lag, already in seconds | The only vendor here whose replication-lag metric needs no unit conversion at all |
| `aggr_capacity` | `aggr_space_used_percent` | Back-end | Aggregate capacity utilization | Same interpretation as Pure's capacity metric — a leading performance indicator, not purely a capacity one |

**Why ONTAP's back-end column is the most complete:** Harvest's REST/RestPerf
collectors expose genuine controller- and media-level utilization metrics
that neither Pure's nor StorageGRID's public endpoints currently do. This
means the correlation finding (front-end bad, back-end clean →
"upstream") is making its strongest, most literal claim for ONTAP arrays —
it's checking real internal-load metrics, not just the absence of
capacity/replication problems.

---

## 6. NetApp StorageGRID

**Config file:** `config/thresholds/netapp_storagegrid.yml`
**Collection:** bundled Harvest poller with the `StorageGrid` collector
(linux/amd64 only)
**Reference:** NetApp's official documentation, "Commonly used Prometheus
metrics" (docs.netapp.com/us-en/storagegrid) — these are NetApp-published,
not third-party reverse-engineered

| Metric ID | Prometheus metric | Category | What it measures | Interpretation |
|---|---|---|---|---|
| `metadata_query_latency` | `storagegrid_metadata_queries_average_latency_milliseconds` | Front-end | Average time to run a query against the metadata store | A *component* of client-facing latency, not the whole request — see [PERFORMANCE-ANALYSIS.md §2](PERFORMANCE-ANALYSIS.md#2-latency-is-the-number-that-matters-most--and-the-hardest-to-read) |
| `s3_error_rate` | `storagegrid_s3_operations_failed` (rate) | Front-end | S3 operation failure rate | Distinguish 4xx (client/auth errors) from 5xx (server-side) where possible for faster triage — this metric doesn't split them, but StorageGRID's own logs do |
| `network_errors` | `node_network_receive_errs_total` + `_transmit_errs_total` | Front-end | Standard Linux network interface error counters (StorageGRID nodes run on Linux) | Not StorageGRID-specific — the same metric any Linux host running `node_exporter` would publish |
| `node_cpu` | `storagegrid_node_cpu_utilization_percentage` | Back-end | Node CPU utilization | A real, direct utilization metric — StorageGRID matches ONTAP here, unlike Pure |
| `ilm_backlog` | `storagegrid_ilm_awaiting_total_objects` | Back-end | Objects awaiting Information Lifecycle Management evaluation | **The one metric with no equivalent on any other platform Plumb supports.** ILM backlog is an object-storage-specific saturation signal — see [Section 2](#2-side-by-side-the-same-concept-four-vendors) — and tends to rise *before* client-facing latency does, making it a genuine early-warning metric unique to this vendor |
| `storage_capacity` | computed: `100 * (1 - usable_space_bytes / total_space_bytes)` | Back-end | Storage capacity used, as a percentage | The only capacity metric in Plumb computed from a ratio of two raw byte-count metrics rather than read directly as a percentage |

---

## 7. How to Verify a Metric Name Yourself

Every threshold file in `config/thresholds/` carries a header comment with
this same instruction, because vendor exporters change between releases and
the metric names here should never be trusted blindly:

**Pure FlashArray / FlashBlade:**
```bash
curl -sk -H "Authorization: Bearer <token>" https://<array>/metrics | less
```

**NetApp ONTAP / StorageGRID (via the bundled Harvest poller):**
```bash
curl http://127.0.0.1:<poller-port>/metrics | grep <metric_name>
```
(poller ports are assigned automatically — check `data/generated/harvest.yml`
for the port assigned to each array)

If a metric name in a threshold file doesn't match what you see, update the
`query` field in that file. Nothing else needs to change — the panels,
findings, and reports are all generated from whatever's in that file.

---

## 8. What Each Vendor Cannot Tell You

Being explicit about gaps is more useful than pretending every vendor
publishes the same completeness of data. As of this writing:

| Gap | Affects | Why it matters |
|---|---|---|
| No controller busy%/CPU metric | Pure FlashArray, Pure FlashBlade | The correlation finding for Pure arrays relies more heavily on capacity and replication signals being clean, since it can't directly confirm "the controller itself is idle" the way ONTAP/StorageGRID can |
| No confirmed FC-vs-Ethernet interface label | Pure FlashArray | The `network_errors` metric sums across all interfaces; you can't isolate fabric-specific errors without confirming your array's actual label naming first |
| No array-wide performance rollup, incomplete space-utilization labels | Pure FlashBlade | FlashBlade's own published metrics spec is incomplete — this isn't a Plumb limitation, it's the current state of Pure's own documentation for that platform |
| No confirmed queue-depth/saturation metric | NetApp ONTAP | ONTAP's back-end story is otherwise the most complete of the four, but there's no direct equivalent to Pure's `queue_depth_ops` — `volume_total_ops` is used as an imperfect load proxy |
| No per-node replication lag metric | NetApp StorageGRID | StorageGRID's grid-wide replication/erasure-coding architecture doesn't map onto a single "lag" number the way block/file replication does — this is an architectural difference, not a missing metric |

If your specific array/cluster/grid publishes more than what's listed here
— a newer Purity release, a newer Harvest version, or your own REST-API
bridge — extending any threshold file to use it is the intended way to close
these gaps for your own fleet.
