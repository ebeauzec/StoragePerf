# Performance Analysis — Fundamentals

This document explains the concepts Plumb is built around, independent of any
one vendor's terminology. If you already know storage performance analysis,
skim the section headers. If you're newer to it, this is written to be
readable end to end.

## Table of Contents

1. [The Four Numbers That Matter](#1-the-four-numbers-that-matter)
2. [Latency Is the Number That Matters Most — and the Hardest to Read](#2-latency-is-the-number-that-matters-most--and-the-hardest-to-read)
3. [Front-End vs. Back-End: Where Is the Bottleneck?](#3-front-end-vs-back-end-where-is-the-bottleneck)
4. [Saturation vs. Utilization](#4-saturation-vs-utilization)
5. [Reading a Trend, Not Just a Value](#5-reading-a-trend-not-just-a-value)
6. [Why Plumb's Thresholds Are "Illustrative," Not Gospel](#6-why-plumbs-thresholds-are-illustrative-not-gospel)
7. [The Correlation Finding — How Plumb Decides "Upstream" vs. "Internal"](#7-the-correlation-finding--how-plumb-decides-upstream-vs-internal)
8. [Common Failure Patterns and What They Look Like](#8-common-failure-patterns-and-what-they-look-like)
9. [Putting It Together: A Troubleshooting Checklist](#9-putting-it-together-a-troubleshooting-checklist)

---

## 1. The Four Numbers That Matter

Every storage performance question — on every platform, from every vendor —
reduces to four measurements, and almost every real problem is a
disagreement between them.

| Metric | What it answers | Unit |
|---|---|---|
| **Latency** | How long did one operation take? | milliseconds (ms) or microseconds (µs) |
| **IOPS** (I/O operations per second) | How many operations happened? | ops/sec |
| **Throughput / Bandwidth** | How much data moved? | MB/s or GB/s |
| **Queue depth / outstanding operations** | How much work is in flight, waiting? | a count, not a rate |

These four are related but not interchangeable, and conflating them is the
single most common analysis mistake:

- High IOPS with low latency and low queue depth = the system is doing a lot
  of small, fast work comfortably. Healthy.
- High throughput with moderate latency = large sequential transfers.
  Expected for backups, restores, media workloads.
- **Low IOPS with high latency and high queue depth = the classic bottleneck
  signature.** The system isn't doing more work — it's doing the same or less
  work, slower, with a backlog building up. This is the pattern every
  threshold in Plumb is ultimately trying to catch.
- High IOPS with high latency (both up) — usually means real demand growth
  outpacing capacity, not a fault. Worth confirming with a look at
  utilization/saturation (Section 4) before treating it as a problem.

Plumb's fleet cards deliberately show latency and queue/outstanding-I/O side
by side for exactly this reason — one number alone can look fine while the
pairing tells the real story.

---

## 2. Latency Is the Number That Matters Most — and the Hardest to Read

Latency is what a human or an application actually experiences. IOPS and
throughput are proxies for load; latency is the proxy for pain. But three
things make latency easy to misread:

**Average latency hides tail latency.** An average of 2ms across 10,000
operations is consistent with 9,999 operations at 1ms and one operation at
10,000ms. Applications that feel "occasionally slow" are almost always a
tail-latency problem an average won't show. Where a platform publishes a
p95/p99 or a max alongside the average (Plumb's reports compute p95 and max
for every metric — see [REPORTS.md](REPORTS.md)), use those, not the average,
to judge user-visible pain.

**Latency is measured at different points depending on the platform**, and
those points aren't always comparable:

- Pure's native `latency_usec` metrics are measured **at the array**, end to
  end for an operation the array processed — not at the host's HBA/initiator,
  so SAN fabric hop latency isn't included.
- NetApp Harvest's `volume_avg_latency` is similarly array-side, aggregated
  per volume.
- StorageGRID's `storagegrid_metadata_queries_average_latency_milliseconds`
  measures a specific internal operation (a metadata store query), which is
  a component of, not identical to, the end-to-end S3/Swift request latency
  a client sees.

None of these measure "the whole path including the network," because none
of them run on the client. If you need true end-to-end latency including the
network hop, you need a client-side or application-side measurement to pair
with Plumb's array-side numbers — Plumb tells you what the storage system
saw, which is necessary but not sufficient for a full picture.

**A latency number without a baseline is not informative.** "4ms" is not
inherently bad — for some workloads it's excellent, for a latency-sensitive
database it may already be a problem. This is why NetApp's own documentation
gives an example rather than a fixed number (a Microsoft Exchange workload
that becomes unstable above 20ms, with a warning threshold set at 12ms and
critical at 15ms specifically for that workload) — see
[Section 6](#6-why-plumbs-thresholds-are-illustrative-not-gospel).

---

## 3. Front-End vs. Back-End: Where Is the Bottleneck?

This is the organizing idea behind Plumb's whole layout. Every performance
problem on a shared storage system originates in one of two places:

**Front-end (the path to the system)** — the SAN fabric, network switches,
host HBAs/NICs, multipathing configuration, or the host's own queueing. The
storage system itself might be completely healthy while a host still
experiences terrible latency, because the bottleneck is in the pipe, not the
system at the end of it.

**Back-end (inside the system)** — the controllers/nodes doing the actual
work, the media (SSD/NVMe/HDD), the internal fabric connecting them, and
background operations (garbage collection, rebuilds, RAID reconstruction,
replication) competing for the same resources.

The diagnostic value of this split is that **the two have almost entirely
different remediations.** A front-end problem (SAN congestion, a failing
SFP, an overloaded fabric port) is fixed by SAN/network teams, cabling, or
zoning changes. A back-end problem (an overloaded controller, media wearing
out, undersized capacity for the workload) is fixed by the storage team,
usually with a hardware, configuration, or capacity change. Treating one as
the other wastes a troubleshooting cycle escalating to the wrong team.

Plumb's dashboard puts front-end metrics in the left column and back-end
metrics in the right column for every vendor, specifically so this
comparison is a glance, not a query.

---

## 4. Saturation vs. Utilization

These sound similar and get confused constantly, but they answer different
questions:

- **Utilization** — what fraction of a resource's capacity is currently
  being used. A CPU at 60% utilization has 40% headroom, in principle.
- **Saturation** — how much work is *queued, waiting* for that resource. A
  resource can be at 60% utilization and still have a growing queue if the
  *arrival rate* of work is uneven (bursty) rather than smooth — the average
  utilization looks fine while individual moments are fully saturated.

Queue depth (Section 1) is a saturation signal, not a utilization signal.
This is why Plumb tracks both a percentage-based metric (e.g., `node_cpu_busy`,
`aggr_disk_busy`, `storagegrid_node_cpu_utilization_percentage` — utilization)
and a count-based metric (e.g., `purefa_array_performance_queue_depth_ops`,
`storagegrid_ilm_awaiting_total_objects` — saturation/backlog) wherever a
vendor publishes both: a system can look fine on one and be in real trouble
on the other.

---

## 5. Reading a Trend, Not Just a Value

A single instant reading tells you the current state. A trend tells you
whether that state is stable, improving, or heading toward a problem before
it becomes one. Three trend patterns are worth learning to recognize on
sight:

- **Sustained plateau above a threshold** — the system has settled into a new,
  worse normal. Usually means real demand growth or a change (new workload,
  new host added, a background job now running continuously) rather than a
  transient event. This is the case Plumb's reports call out as "consistently
  elevated rather than spiky" — see [REPORTS.md](REPORTS.md).
- **A single sharp spike, then recovery** — often a specific event (a backup
  window, a batch job, a rebuild starting) rather than a systemic issue.
  Worth checking what else happened at that timestamp before over-reacting.
- **A slow, steady climb** — the most valuable pattern to catch early,
  because it's predictable capacity/performance planning rather than an
  incident. Plumb's report generator explicitly computes this (comparing the
  first quarter of a period's average against the last quarter) and flags
  moves of 15% or more in either direction.

A metric that's merely *above threshold* is a today problem. A metric that's
*trending toward* a threshold is a tomorrow problem you get to solve on your
own schedule instead of during an incident.

---

## 6. Why Plumb's Thresholds Are "Illustrative," Not Gospel

Every `severity_watch` / `severity_critical` value in
`config/thresholds/*.yml` is a starting point, not a certified number from
Pure Storage or NetApp. This isn't a hedge — it reflects how these vendors
themselves talk about performance thresholds:

> "If you have a Microsoft Exchange Server and you know that it crashes if
> volume latency exceeds 20 milliseconds, you can set a warning threshold at
> 12 milliseconds and a critical threshold at 15 milliseconds."
> — paraphrased from NetApp's own performance-threshold guidance

The number that matters is **the number your specific workload actually
breaks at**, which depends on the application, the SLA it's held to, and
what "normal" has looked like on that system historically — not a single
number that's correct for every array, every cluster, and every grid a
vendor ever ships. Neither Pure Storage nor NetApp publishes a universal
numeric SLA for latency, queue depth, or utilization, for exactly this
reason.

**What this means practically:** run Plumb for a few weeks before tightening
any threshold. Look at what your own systems' "normal" range actually is
(the per-metric min/avg/p95/max in every array report — see
[REPORTS.md](REPORTS.md) — is the fastest way to see this), and set
`severity_watch`/`severity_critical` relative to that baseline, not the
shipped defaults. A threshold copied from this repository without
adjustment is a reasonable *starting* alarm, not a validated one for your
environment.

---

## 7. The Correlation Finding — How Plumb Decides "Upstream" vs. "Internal"

Plumb's single most useful finding is the one it doesn't have a metric for
directly: *"Bottleneck is likely upstream of the array."* This is inferred,
not measured, by the logic in `internal/rules/rules.go`:

```
IF any front-end metric is at watch/critical severity
AND every back-end metric this vendor publishes is at good severity
THEN flag a "bottleneck is likely upstream" finding
```

This directly operationalizes [Section 3](#3-front-end-vs-back-end-where-is-the-bottleneck):
if the system's own internal signals (however complete or incomplete they
are for that vendor — see [Section 8 of METRICS-REFERENCE.md](METRICS-REFERENCE.md#8-what-each-vendor-cannot-tell-you))
are all clean, but the client-facing signals are degraded, the honest
inference is that something between the client and the system is the
problem, not the system itself.

**This is a heuristic, not a certainty.** It's exactly as reliable as the
back-end metrics it checks are complete — which varies significantly by
vendor (NetApp ONTAP publishes controller CPU and disk-busy directly; Pure's
public metrics endpoint publishes neither, so the finding leans more heavily
on capacity and replication signals for Pure arrays). Every instance of this
finding says so explicitly in its own text, rather than presenting a
best-effort inference as a certain diagnosis.

---

## 8. Common Failure Patterns and What They Look Like

| Pattern | What you'd see in Plumb | Likely cause | Where to look next |
|---|---|---|---|
| Front-end critical, back-end clean | The correlation finding fires | SAN congestion, a failing/degrading link, host-side queueing, an oversubscribed fabric port | Physical layer (SFPs, cabling), fabric zoning, host HBA queue depth settings, multipathing config |
| Both front-end and back-end degraded together | No correlation finding; both columns show findings | Real demand exceeding the system's capacity, or a genuine internal fault (media failure, controller issue) coinciding with load | Check for a recent workload change, new host, or background job (rebuild, replication resync) before assuming the system needs more capacity |
| Back-end degraded, front-end clean | Only back-end findings | Background operations (garbage collection, scrub, rebuild) consuming internal resources without yet showing up as client-visible latency | Often self-resolving; worth confirming it's a known background task and not a developing hardware issue |
| Error-rate metric (CRC/link errors) climbing, everything else stable | One frontend finding, isolated | A specific physical link problem — often the earliest visible symptom, before it becomes a latency problem | The specific interface/port named in the finding — check for a marginal SFP, a bent/damaged cable, or a failing switch port |
| Capacity utilization climbing steadily, performance still fine | A backend capacity finding, everything else clean | Normal growth — not yet a performance issue, but will become one as most storage systems slow down as they approach full | Capacity planning conversation now, before it becomes a performance incident |
| A single system on the fleet report ranked far worse than the rest | High issue count on one row of the fleet report | An outlier — worth checking what's different about that system (workload, age, recent change) rather than assuming a fleet-wide problem | That system's individual array report — see [REPORTS.md](REPORTS.md) |

---

## 9. Putting It Together: A Troubleshooting Checklist

When someone reports "storage feels slow," this is the order Plumb is
designed to make fast:

1. **Open the Fleet view.** Is it one system or several? One system points
   to something specific to that system or its workload; several at once
   points to something shared (network core, a common host cluster, time
   of day / batch window).
2. **Check the front-end/back-end split for the affected system(s).** Which
   column has findings? That's [Section 3](#3-front-end-vs-back-end-where-is-the-bottleneck)
   doing its job.
3. **Read the Findings feed, not just the panel colors.** The correlation
   finding (Section 7) and the per-metric finding text both tell you *why*
   a panel is flagged, in plain language, not just that it is.
4. **Check the trend, not just the current value** (Section 5). Is this new,
   or has it been climbing for days? That changes whether it's an incident
   or a capacity conversation.
5. **Pull the array report for the affected system** (see
   [REPORTS.md](REPORTS.md)) if you need the min/avg/p95/max numbers and
   written analysis to bring to a wider team or a change request.
6. **If it's genuinely ambiguous, export the raw data** (CSV export — see
   [REPORTS.md](REPORTS.md)) and look at it alongside whatever the affected
   application's own logs show for the same time window — Plumb tells you
   what the storage system experienced, not what any specific application
   experienced.
