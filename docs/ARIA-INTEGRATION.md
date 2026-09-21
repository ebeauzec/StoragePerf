# ARIA integration

Plumb can hand its measured performance to **ARIA**, the NetApp Active IQ reporting tool an MSP or TAM
uses for proactive reporting. It answers the question Active IQ can't: not *what is this system* but
*how is it performing, and is a slowdown the array or the path in front of it?*

| Active IQ / ARIA already knows | Plumb adds |
|---|---|
| Model, firmware, configuration, risks, support cases, contracts, capacity as of the last AutoSupport | Latency, CPU, throughput and capacity **as measured on site over the period**, with trend |
| — | Findings with the *investigate* and *remediate* steps, including "bottleneck is likely upstream" |
| Capacity from the last AutoSupport | **Days until a capacity metric reaches its critical threshold**, from the measured growth rate |
| Cases and risks | Recent ONTAP EMS events |

There are two ways to deliver it. Both carry the same JSON.

## A. Direct pull (Plumb reachable from the MSP)

ARIA fetches `GET /api/aria/export` on a schedule. In Plumb: **Config → ARIA Integration**, optionally
set an **access token**. In ARIA: **Settings → StoragePerf Integration → Direct pull**, add the customer,
Plumb's address (`http://<plumb-host>:8000`) and the token, press **Test**, then **Pull now**.

| Endpoint | Purpose |
|---|---|
| `GET /api/aria/info` | Cheap connectivity/auth/version check: schema, Plumb version, array count, whether a token is required |
| `GET /api/aria/export?hours=168` | The snapshot. `hours` 1–2160 (default 168). Add `download=1` to receive it as a file |
| `GET /api/aria/exports`, `GET /api/aria/exports/{name}` | The scheduled files (below) |

**Access control.** With no token set, these endpoints are open, exactly like the rest of Plumb's API,
which is designed for a trusted network. Once a token is set it must be sent as `Authorization: Bearer <token>`
(or `X-Plumb-Token`, or `?token=` for a plain download link). The token protects only the `/api/aria/*`
endpoints; Plumb's dashboard and other endpoints remain unauthenticated, so use network controls (VPN,
firewall) to decide who can reach it at all. The snapshot contains no credentials.

## B. File import (Plumb not reachable — dark site, no VPN)

* **Download:** Config → ARIA Integration → **Download ARIA export** (choose 24 h / 3 d / 7 d / 30 d), or
* **Scheduled files:** switch on *Also write the snapshot to a folder*. Plumb then writes
  `data/aria-exports/aria-YYYYMMDD-HHMMSS.json` on the Scheduled Reports frequency (daily or weekly), keeping
  the last 60, ready to collect from a file share.

In ARIA: **Settings → StoragePerf Integration → Import a file**, choose the customer and the file. Every
import is kept as history for that customer.

## What the snapshot contains (`plumb.aria-export/1`)

```jsonc
{
  "schema": "plumb.aria-export/1",
  "generated_at": "2026-09-21T06:49:00Z", "plumb_version": "0.21.0", "site": "Dallas DC",
  "period_hours": 168, "period_start": "...", "period_end": "...",
  "mock_data": false,                      // true when Plumb is showing its built-in demo fleet
  "arrays": [{
    "id": "ontap-prod-01", "name": "ontap-prod-01", "model": "AFF-A400", "vendor": "netapp_ontap",
    "identity": {                          // ONTAP only, when the cluster answered
      "cluster_name": "ontap-prod-01", "cluster_uuid": "…", "version": "NetApp Release 9.15.1P7",
      "nodes": [{ "name": "ontap-prod-01-01", "serial_number": "721234000101", "model": "AFF-A400" }]
    },
    "health": "watch",                     // good | watch | critical | unknown
    "issue_count": 2, "coverage_note": "", "upstream_suspected": true,
    "latency": { "metric_id": "volume_avg_latency", "label": "…", "unit": "ms", "avg": 3.1, "p95": 4.4, "max": 6.0, "trend_pct": 12 },
    "metrics": [{ "id": "node_cpu_busy", "label": "…", "unit": "%", "category": "backend", "severity": "good",
                  "samples": 300, "min": 0, "avg": 0, "max": 0, "p90": 0, "p95": 0, "p99": 0,
                  "watch_pct": 0, "critical_pct": 0, "episodes": 0, "trend_pct": 0,
                  "watch": 70, "critical": 85, "analysis": "…", "sparkline": [[epoch_s, value]] }],
    "findings": [{ "severity": "watch", "tag": "…", "title": "…", "body": "…", "metric_id": "…",
                   "investigate": ["…"], "remediate": ["…"] }],
    "capacity": [{ "metric_id": "aggr_capacity", "label": "…", "unit": "%", "current": 86, "critical": 95, "days_to_critical": 34 }]
  }],
  "events": [{ "array_id": "…", "array_name": "…", "time": "…", "severity": "watch", "name": "wafl.aggr.almostFull", "node": "…", "message": "…" }]
}
```

Consumers must ignore fields they don't recognize. `schema` changes its major number only for a breaking
change; ARIA refuses a newer major with a message rather than mis-reading it. ARIA also refuses a snapshot
with `mock_data: true`, so demo-fleet numbers can never be filed against a real customer.

Thresholds behind `severity` are Plumb's own illustrative, workload-dependent guidance (see
`config/thresholds/`); `analysis` is Plumb's plain-language reading of the same numbers.

## How ARIA matches an array to an Active IQ system

1. **Node serial number** — exact. Plumb reads each ONTAP cluster's node serials over the same REST
   connection it already uses for metrics (cached for an hour; a failure never blocks the export).
2. **Cluster name**, then 3. **array name / ID** — for E-Series and StorageGRID, which publish no serial
   numbers to Plumb, the array's name in Plumb must equal the system or cluster name in Active IQ.

Arrays that can't be matched are listed in ARIA as "monitored but not matched" rather than dropped.

## Where it appears in ARIA

Action Planner → *16. Performance*; a card on Value & ROI (per customer or per system); a
*Performance & Capacity Runway* section in the QBR pack, MSP service report and customer success plan.
Customers without a StoragePerf snapshot get none of it.
