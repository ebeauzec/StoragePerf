# Plumb — Storage Performance Console

[![Version](https://img.shields.io/github/v/release/ebeauzec/StoragePerf?color=0066cc&label=version)](https://github.com/ebeauzec/StoragePerf/releases)
[![License](https://img.shields.io/badge/license-Proprietary-red)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.22+-00ADD8?logo=go&logoColor=white)]()
[![Platform](https://img.shields.io/badge/platform-Linux%20%7C%20macOS%20%7C%20Windows-lightgrey)]()
[![Vendors](https://img.shields.io/badge/vendors-Pure%20Storage%20%7C%20NetApp-critical)]()

> **Plumb** — one console, every storage vendor in your fleet, one question
> answered fast: **is a performance problem coming from the array/cluster/grid
> itself, or from the network path in front of it?**
>
> Built for storage and infrastructure teams running mixed vendor
> environments — Pure Storage FlashArray/FlashBlade and NetApp
> ONTAP/StorageGRID — who need one operational view instead of a different
> vendor GUI, a different Grafana instance, and a different mental model per
> platform.

---

## Table of Contents

1. [Why This Tool — vs. Vendor-Native Monitoring](#1-why-this-tool--vs-vendor-native-monitoring)
2. [What It Delivers](#2-what-it-delivers)
3. [Use Cases](#3-use-cases)
4. [Getting Started](#4-getting-started)
5. [Dashboard Guide](#5-dashboard-guide)
6. [Findings & the Front-End / Back-End Split](#6-findings--the-front-end--back-end-split)
7. [Reports & Data Export](#7-reports--data-export)
8. [Performance Metrics Reference](#8-performance-metrics-reference)
9. [Multi-Vendor Support & Demo Mode](#9-multi-vendor-support--demo-mode)
10. [Security & Data Privacy](#10-security--data-privacy)
11. [Troubleshooting](#11-troubleshooting)
12. [Internal Architecture](#12-internal-architecture) *(Addendum — for developers)*
13. [Legal & Intellectual Property](#13-legal--intellectual-property)

---

## 1. Why This Tool — vs. Vendor-Native Monitoring

Every vendor here already has a monitoring story: Pure ships native
OpenMetrics endpoints and Grafana dashboards; NetApp ships Harvest and its
own Grafana dashboards; both have polished per-product GUIs. Plumb isn't a
replacement for any of that data — it's what sits on top when your fleet
isn't one vendor.

| Gap when running vendor-native tools per platform | What Plumb does instead |
|---|---|
| **A different dashboard per vendor** — Pure's Grafana boards look nothing like NetApp's, and neither shares layout, terminology, or severity language with the other | **One interface, one layout, per-vendor terminology** — a NetApp array shows "Aggregate Disk Busy" and "SnapMirror Lag"; a Pure array shows its own vendor-accurate labels; both render in the identical Front-End/Back-End split, so the *investigation pattern* is identical even when the *metric names* aren't |
| **No cross-vendor bottleneck logic** — a wall of panels doesn't tell you *where* a problem originates, per vendor or across vendors | **A correlation finding, built once, applied to every vendor** — "front-end degraded, back-end clean" fires the same way whether the back-end evidence is Pure's capacity/replication signals or NetApp's direct controller-busy% (see [Metrics Reference §8](docs/METRICS-REFERENCE.md#8-what-each-vendor-cannot-tell-you) for exactly how that confidence differs by vendor) |
| **Grafana retains what its backing TSDB is configured to retain** — often days to weeks in a default setup | **Unlimited retention by default** — VictoriaMetrics configured for 100-year retention out of the box; Prometheus itself is just a 2-day buffer in front of it |
| **No reporting layer** — Grafana renders a dashboard, it doesn't write you an analysis | **Two report types plus CSV export**, computed on demand from stored history, with a written (not LLM-generated — deterministic, reproducible) analysis per metric — see [Reports & Data Export](#7-reports--data-export) |
| **Nothing tells you if your dashboard's underlying components are outdated** | **Check-and-notify version checking** — looks up newer Prometheus/VictoriaMetrics/Harvest releases once a day, shows what it finds, never auto-installs anything |
| **Standing up Grafana + Prometheus + exporters per vendor means real infrastructure** | **One executable, zero install** — the native build embeds the frontend and launches Prometheus + VictoriaMetrics (and Harvest, where available) as child processes. Unzip, run, done — see [Getting Started](#4-getting-started) |

---

## 2. What It Delivers

**Collected, per system, depending on vendor** (see
[Metrics Reference](docs/METRICS-REFERENCE.md) for the full per-vendor
breakdown):

- Client-facing / front-end latency, throughput, and error signals
- Internal / back-end saturation, capacity, and replication signals
- Everything stored indefinitely, queryable at native resolution

**Added by Plumb, not present in any single vendor's own tooling:**

- A **Front-End vs. Back-End split** applied identically across all four
  vendors, so "where is this problem coming from" is answered the same way
  regardless of what's actually running
- A **correlation finding** that infers an upstream (network/SAN) bottleneck
  when front-end metrics are degraded but every back-end signal a vendor
  publishes is clean
- **Two report types** (per-system and fleet-wide) with real computed
  statistics (min/avg/p95/max, threshold-crossing percentage, trend
  direction) and deterministic written analysis — not a chart, an
  explanation
- **CSV export** of raw time series for anything a report's summary doesn't
  cover
- **Check-and-notify updates** for the bundled components, with zero
  outbound calls if you turn it off
- **A single, zero-install distribution** for Linux, macOS, and Windows —
  see [native/README.md](native/README.md)

---

## 3. Use Cases

### "Storage feels slow" — first triage

**Goal:** Find out, in under a minute, whether a reported slowness is one
system or several, and whether it's the storage system or the network in
front of it.

**Workflow:**
1. Open the **Fleet** view — is it one system flagged, or several?
2. Select the affected system — check which column (Front-End / Back-End)
   has findings
3. Read the top finding in the Findings feed — if it's the correlation
   finding, you already have a direction (network/SAN team) before opening
   a single vendor GUI

**Output:** A location (front-end vs. back-end) and a written reason, in
less time than it takes to log into one vendor's own console.

See [Performance Analysis §9](docs/PERFORMANCE-ANALYSIS.md#9-putting-it-together-a-troubleshooting-checklist)
for the full checklist this workflow is drawn from.

### Weekly fleet health review

**Goal:** A five-minute check across every system, regardless of vendor,
ranked by what needs attention.

**Workflow:**
1. Click **Fleet report** in the Fleet view
2. The table is already ranked worst-first — start at row one
3. Open that system's individual array report for the full metric detail

**Output:** A ranked, dated record suitable for a standing ops review —
see [Reports & Data Export §2](docs/REPORTS.md#2-fleet-report).

### Capacity/performance trend review before it's an incident

**Goal:** Catch a slow, steady degradation before it becomes a page.

**Workflow:**
1. Pull a 30-day array report for a system of interest
2. Check each metric's trend note — a ≥15% move between the start and end
   of the period is called out explicitly in the written analysis
3. Cross-reference against the min/avg/p95/max table for context

**Output:** A capacity/performance conversation on your own schedule instead
of during an incident — see
[Performance Analysis §5](docs/PERFORMANCE-ANALYSIS.md#5-reading-a-trend-not-just-a-value).

### Bringing evidence to a vendor escalation or internal change ticket

**Goal:** Attach real numbers, not a verbal description, to a support case
or change request.

**Workflow:**
1. Pull the array report for the affected system over the incident window
2. If the report's summary isn't enough detail, export the same window as
   CSV for the raw samples
3. Attach both — the report for the narrative, the CSV as backing evidence

**Output:** A self-contained HTML document plus raw data, both generated
from the same underlying stored history — see
[Reports & Data Export §6](docs/REPORTS.md#6-reading-a-report-as-evidence-not-just-a-summary).

### Standing up monitoring for a new mixed-vendor site

**Goal:** Get Pure and NetApp systems into one view without building
separate Grafana stacks per vendor.

**Workflow:**
1. Extract the platform's zero-install package (or use the Docker build)
2. Add each system to `config/arrays.yml` with the right `vendor` field —
   see [arrays.example.yml](native/config/arrays.example.yml)
3. NetApp systems need real credentials for the bundled Harvest poller;
   Pure systems need an API token — see
   [Getting Started §4](#4-getting-started)

**Output:** A running fleet view with no separate infrastructure to
provision — see [native/README.md](native/README.md).

---

## 4. Getting Started

### Native build (recommended — zero install)

**Prerequisites:** none. Not Docker, not Python, not Node — the executable
is fully self-contained.

```bash
# Extract the archive for your platform (native/dist/), then:
cd plumb-<version>-<platform>
./plumb        # or ./start.sh, or double-click start.bat on Windows
```

Open **http://localhost:8000**. First run creates `config/arrays.yml` from
the bundled example automatically. Edit that file (or use the **Config**
tab) to add your real systems — see
[native/config/arrays.example.yml](native/config/arrays.example.yml) for
every field, per vendor.

Full detail, including building it yourself and the platform/vendor support
matrix, is in [native/README.md](native/README.md).

### Try it with demo data first

Start `./plumb`, open the **Config** tab, and switch on **Show mock data** —
seven synthetic systems across all four vendors appear immediately, no real
hardware and nothing else to run. Switch it off to return to your real
inventory. See [Multi-Vendor Support & Demo Mode](#9-multi-vendor-support--demo-mode)
for the alternative (`run-demo.sh`) that exercises the real collection
pipeline instead of generating data in-process.

### Docker build (alternative — Pure Storage only)

The original build. Still functional, kept for anyone standing this up
alongside an existing Docker-based stack. No reports/export endpoints, no
NetApp support.

```bash
docker compose up --build
```

See the [Docker section below](#docker-build-detail) and
[backend/](backend/) for the full detail.

---

## 5. Dashboard Guide

### Fleet

Fleet-wide health at a glance: every system as a card (vendor, model, health
chip, sparkline, queue depth, latency), a fleet-wide summary count, and a
**Fleet report** link. Click any card to select it for the detail view
below.

### Array Detail (below the fleet strip)

The selected system's Front-End (left) and Back-End (right) columns, each
panel showing current value, severity badge, a chart with the illustrative
threshold line overlaid, and the threshold label itself. A time-range picker
(1H/24H/7D/30D/90D/1Y) controls both the charts and the Report/Export
buttons above them.

### Findings

Every metric currently at watch or critical severity, plus the correlation
finding when it applies, each tagged Front-End / Back-End / Fleet-wide and
shown with its full written explanation — not just a color, a reason.

### Config

The array inventory — add, edit, or remove systems per vendor, with
vendor-appropriate fields shown automatically when you change the vendor
dropdown. Saving regenerates Prometheus's scrape targets (and, for NetApp
systems, Harvest's poller config) immediately, no restart required. Also
shows the check-and-notify **Software & Reference Updates** panel.

---

## 6. Findings & the Front-End / Back-End Split

This is the core organizing idea — see
[Performance Analysis §3](docs/PERFORMANCE-ANALYSIS.md#3-front-end-vs-back-end-where-is-the-bottleneck)
for the full explanation of why this split matters more than any individual
metric. In short: front-end problems (SAN, network, host queueing) and
back-end problems (controllers, media, capacity) have almost entirely
different remediations and different owning teams — conflating them wastes
a troubleshooting cycle.

The **correlation finding** — "Bottleneck is likely upstream of the array" —
is Plumb's one piece of inferred (not directly measured) intelligence: it
fires when every front-end metric is degraded but every back-end metric a
vendor publishes is clean. See
[Performance Analysis §7](docs/PERFORMANCE-ANALYSIS.md#7-the-correlation-finding--how-plumb-decides-upstream-vs-internal)
for exactly how much confidence that carries per vendor — it's not the same
for all four, and the finding's own text says so.

---

## 7. Reports & Data Export

| Type | Endpoint | Contents |
|---|---|---|
| **Array report** | `/api/reports/array/{id}?hours=N` | One system, min/avg/p95/max per metric, written analysis, overall status |
| **Fleet report** | `/api/reports/fleet?hours=N` | Every system, ranked worst-first, one-paragraph narrative |
| **CSV export** | `/api/export/{id}?hours=N` | Raw long-format time series, every metric, for external analysis |

All three compute directly from VictoriaMetrics's stored history on request
— there's no separate findings-history database to fall out of sync. Full
detail on how percentiles and trends are calculated, and how to read the
generated analysis critically rather than at face value, is in
[Reports & Data Export](docs/REPORTS.md).

---

## 8. Performance Metrics Reference

The full reference lives in two documents, because "how do I read a
performance number" and "what does this specific vendor's metric mean" are
different questions:

- **[Performance Analysis](docs/PERFORMANCE-ANALYSIS.md)** — vendor-neutral
  fundamentals: latency vs. IOPS vs. throughput vs. queue depth, saturation
  vs. utilization, why thresholds are illustrative not universal, and a
  troubleshooting checklist.
- **[Metrics Reference](docs/METRICS-REFERENCE.md)** — every metric Plumb
  collects, per vendor, with a side-by-side comparison table showing how
  the same concept (latency, saturation, capacity, replication) is answered
  differently — or not at all — by each of the four vendors, plus an
  explicit accounting of what each vendor's public metrics *can't* tell you.

Read both before tuning `config/thresholds/*.yml` for your own environment.

---

## 9. Multi-Vendor Support & Demo Mode

| Vendor | `vendor` value | Collection | Platform support |
|---|---|---|---|
| Pure Storage FlashArray | `pure_flasharray` | Direct scrape via Plumb's authenticated proxy | All platforms |
| Pure Storage FlashBlade | `pure_flashblade` | Direct scrape via Plumb's authenticated proxy | All platforms |
| NetApp ONTAP | `netapp_ontap` | Bundled Harvest poller | **linux/amd64 only** — NetApp publishes no other platform's Harvest build |
| NetApp StorageGRID | `netapp_storagegrid` | Bundled Harvest poller (`StorageGrid` collector) | **linux/amd64 only** |

On other platforms, NetApp entries in `arrays.yml` are accepted but simply
won't collect data — Plumb logs why and keeps running everything else
normally.

**Demo mode has two forms:**

- **The "Show mock data" toggle** (Config tab) — the simplest option.
  Generates seven synthetic systems across all four vendors entirely
  in-process (`internal/mockdata`), with no separate exporters, no real
  arrays required, and no effect on your saved inventory — flip it off and
  your real `arrays.yml` is exactly as you left it. This is what to use for
  a demo, a screenshot, or just trying the interface before you have real
  systems to point at.
- **`native/scripts/run-demo.sh`** — for testing the *real* collection
  pipeline (Prometheus scraping, the scrape proxy, Harvest) end to end
  against synthetic data instead of a live array. It launches seven
  instances of [demo-exporter/exporter.py](demo-exporter/exporter.py) (real
  Python processes emitting real Prometheus text) and points
  `config/arrays.yml` at them. Use this when you're validating Plumb's
  plumbing itself, not just its UI.

Both use the same seven-system, four-vendor fleet, one of each severity
level, so the flagship "bottleneck is likely upstream" correlation finding
(see [Performance Analysis §7](docs/PERFORMANCE-ANALYSIS.md#7-the-correlation-finding--how-plumb-decides-upstream-vs-internal))
fires on the critical-profile systems in both. NetApp entries in
`run-demo.sh`'s fleet use a direct `host` field rather than the bundled
Harvest poller, since the demo exporters aren't real ONTAP/StorageGRID
systems for Harvest to authenticate against — this is also a legitimate way
to point Plumb at a Harvest/StorageGRID Prometheus endpoint you already run
yourself, not just a demo trick (see `internal/targets`).

---

## 10. Security & Data Privacy

| Guarantee | Detail |
|---|---|
| **Local by default** | All collected data lives in VictoriaMetrics's local storage next to the executable (or in the `vmdata` Docker volume). Nothing is transmitted to any third-party service by Plumb itself |
| **Check-and-notify only** | The update checker (`internal/updates`) makes outbound calls solely to check for newer releases of bundled components — it never downloads, installs, or modifies anything, and can be fully disabled (`PLUMB_CHECK_FOR_UPDATES=false`) for an air-gapped deployment |
| **Per-array credentials, isolated** | Pure API tokens and NetApp credentials are referenced by environment variable name in `arrays.yml`, never stored in it directly, and each array's credential is scoped to that array's own scrape/poller path |
| **No AI in the data path** | Report narratives are generated by a deterministic, rule-based function (`internal/report`) — see [Reports & Data Export §5](docs/REPORTS.md#5-how-the-written-analysis-is-generated) — not an LLM call. Nothing about a system's live performance data is sent to any AI service |
| **Read-only against monitored systems** | Plumb only ever issues `GET /metrics` (Pure) or read-only REST/RestPerf collection (Harvest, against NetApp systems) — it never writes to or executes commands against any monitored array, cluster, or grid |

---

## 11. Troubleshooting

| Symptom | Likely Cause | Fix |
|---|---|---|
| **Port 8000 already in use** | Another Plumb instance, or an unrelated process | Check `lsof -i :8000` (macOS/Linux) or `netstat -ano \| findstr :8000` (Windows); stop the conflicting process or wait for it to exit |
| **No data / panels show "no data yet"** | Not enough history collected yet, or the scrape target is down | Check `data/logs/prometheus.log` for target health; new deployments need a few collection cycles before charts populate |
| **NetApp system shows no data** | Harvest binary not bundled for this platform, or missing credentials | Check the startup log for `"harvest binary not found"` or `"NetApp array(s) configured but..."` — see [§9](#9-multi-vendor-support--demo-mode) for the platform matrix |
| **Config tab shows blank/undefined fields** | Old cached frontend | Hard-refresh the browser (Ctrl+Shift+R / Cmd+Shift+R) |
| **macOS blocks the binary ("unidentified developer")** | Gatekeeper, since the binary isn't code-signed | System Settings → Privacy & Security → Allow Anyway, then run `./start.sh` again |
| **Findings feed is empty** | Genuinely good news — no metric crossed a threshold in the current window | Check a longer time range if you expected to see history |
| **Report/export returns "unknown array"** | Array ID mismatch | Confirm the `id` field in `arrays.yml` matches what you're querying — it's case-sensitive |
| **Update checker never shows "up to date"** | `PLUMB_CHECK_FOR_UPDATES=false`, or no internet access | Check the Config tab's Software & Reference Updates panel for the exact status string |

---

## 12. Internal Architecture

> This section is an addendum for developers who want to understand, extend,
> or contribute to the codebase. It is not required reading for daily use.

### High-Level Stack (native build)

```
Browser (native/web/*)
  │  fetch /api/*
  ▼
plumb (single Go binary)  ─── port 8000 ───►  Prometheus (child process)  ─── remote_write ───►  VictoriaMetrics (child process)
  │                                                    ▲
  ├── /scrape/{id} — authenticated proxy to Pure arrays │  scrapes
  └── Harvest poller(s) (child processes, linux/amd64) ─┘  (Pure via proxy target; NetApp via Harvest's own exposed port)
```

### Repository Layout

```
StoragePerf/
├── native/                    ← the zero-install, multi-vendor Go build (current, primary)
│   ├── cmd/plumb/              ← entrypoint: sidecar supervision, HTTP server
│   ├── internal/
│   │   ├── config/              ← arrays.yml + thresholds/*.yml loading
│   │   ├── rules/                ← threshold evaluation, findings, correlation logic
│   │   ├── mockdata/              ← in-process synthetic fleet for the Config tab's mock-data toggle
│   │   ├── report/                ← array/fleet report generation (html/template)
│   │   ├── export/                 ← CSV export
│   │   ├── harvest/                 ← Harvest poller config generation
│   │   ├── targets/                  ← Prometheus file_sd generation
│   │   ├── scrapeproxy/                ← Pure array authenticated proxy
│   │   ├── vm/                          ← VictoriaMetrics query client
│   │   ├── sidecar/                      ← generic child-process supervisor
│   │   ├── paths/                         ← relocatable-executable path resolution
│   │   └── updates/                        ← check-and-notify version checker
│   ├── web/                    ← embedded frontend (Go embed)
│   ├── config/                 ← arrays.example.yml, arrays.demo.yml, thresholds/*.yml
│   ├── scripts/                ← fetch-sidecars.sh, build-native.sh, run-demo.sh, dev/ (local dev launcher)
│   └── dist/                   ← built platform packages (gitignored)
├── backend/                    ← original Python/FastAPI build (Docker path, Pure-only)
├── frontend/                   ← original frontend (Docker path; native/web/ is its own copy)
├── demo-exporter/               ← synthetic multi-vendor metrics generator (dev/demo tool)
├── config/                      ← Docker path's config
├── docker-compose.yml, install.sh, scripts/build-release.sh  ← Docker packaging
├── docs/                        ← this documentation set
├── LICENSE, LEGAL.md, THIRD_PARTY_NOTICES.md, LICENSES/       ← legal
└── README.md                    ← this file
```

### Data Flow (native build)

```
1. main.go resolves paths relative to the running executable (paths.Resolve)
2. Loads config/arrays.yml + config/thresholds/<vendor>.yml per array
3. Generates Prometheus file_sd targets (internal/targets) and, for NetApp
   arrays with real credentials, harvest.yml (internal/harvest)
4. Launches Prometheus, VictoriaMetrics, and any needed Harvest pollers as
   supervised child processes (internal/sidecar) — auto-restart with backoff
   on crash, capped at 30s
5. Frontend fetches /api/fleet, /api/arrays/{id}, /api/reports/*, /api/export/*
6. internal/rules evaluates each array's thresholds against VictoriaMetrics
   (via internal/vm) on every request — no caching layer, no stale state
```

### Development Workflow

```bash
cd native
go build ./...                      # compile check
go vet ./...                        # static analysis
go run ./scripts/dev                # build + run current source, always replacing whatever was previously running

./scripts/fetch-sidecars.sh         # download pinned Prometheus/VictoriaMetrics/Harvest releases
./scripts/build-native.sh           # cross-compile + package all 5 platforms
./scripts/run-demo.sh               # multi-vendor demo fleet
```

<a id="docker-build-detail"></a>
### Docker Build (Legacy Path)

```
Pure array /metrics ──(per-array token, HTTPS)──► plumb-api /scrape/{id}
                                                          ▲
                                                    Prometheus ──remote_write──► VictoriaMetrics
                                                          ▲
                                                    plumb-api (thresholds.yml → panels + findings)
                                                          ▲
                                                    frontend/
```

See [backend/](backend/), [docker-compose.yml](docker-compose.yml), and
[install.sh](install.sh) for the full detail — this path is Pure-only and
has no reports/export.

---

## 13. Legal & Intellectual Property

> **Full terms:** [LICENSE](LICENSE) · [LEGAL.md](LEGAL.md) ·
> [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)

### Ownership

This Software's original code is the **sole and exclusive intellectual
property of Obi1 - FZCO**. Copyright © 2026 Obi1 - FZCO. All Rights
Reserved.

### Independent Development

This tool was developed **entirely independently** — on independent time,
with independent resources, and without the involvement, direction, or
funding of any employer or client, including Pure Storage, Inc. or NetApp,
Inc. It does not contain or derive from any proprietary, confidential, or
internal information, customer data, or trade secrets belonging to either
vendor — see [LEGAL.md §3](LEGAL.md#3-no-proprietary-third-party-information)
for exactly what public sources it's built on instead.

Neither Pure Storage nor NetApp is affiliated with, sponsoring, or endorsing
this Software. Product names referenced (Pure Storage®, FlashArray®,
FlashBlade®, NetApp®, ONTAP®, StorageGRID®, etc.) are trademarks of their
respective owners, used solely for interoperability documentation.

### Third-Party Open-Source Components

This Software bundles official, unmodified binary releases of **Prometheus**,
**VictoriaMetrics**, and **NetApp Harvest** — all licensed Apache License
2.0 — plus several permissively-licensed (Apache-2.0/MIT/BSD-3-Clause)
libraries. None of these are covered by the proprietary terms above; each
remains under its own original license, reproduced in full in this
repository. See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for the
complete, itemized list.

### License Terms at a Glance

| Use | Permission |
|---|---|
| Personal / educational / research | ✅ Free |
| Internal non-commercial organisational use | ✅ Free |
| **Commercial use of any kind** | ⛔ **Requires Author's prior written consent** |
| Redistribution of the Author's original code | ⛔ Requires Author's prior written consent |
| Redistribution of bundled third-party open-source components | ✅ Permitted under each component's own license |
| Claiming authorship / removing attribution | ⛔ Prohibited |

This is **not** an open-source or MIT-licensed project (except for the
third-party components it bundles, each under its own license). All rights
not expressly granted are reserved by the Author.

### Attribution

All permitted uses must retain this notice:
> *Copyright © 2026 Obi1 - FZCO. All Rights Reserved.*
> *[LICENSE](LICENSE) · [LEGAL.md](LEGAL.md) · [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)*

---

<p align="center">
  <strong>Plumb — Storage Performance Console</strong><br>
  Copyright &copy; 2026 <strong>Obi1 - FZCO</strong>. All Rights Reserved.<br>
  <a href="LICENSE">Proprietary License</a> &middot; <a href="LEGAL.md">Legal &amp; IP</a> &middot; <a href="THIRD_PARTY_NOTICES.md">Third-Party Notices</a>
</p>
