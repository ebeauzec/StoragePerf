# Third-Party Notices

Plumb's own original code (Go, Python, JavaScript, HTML/CSS, and the
configuration/documentation content in this repository) is proprietary to
Obi1 - FZCO — see [LICENSE](LICENSE) and [LEGAL.md](LEGAL.md).

This file lists every third-party open-source component Plumb downloads,
bundles, statically links, or depends on to build/run, none of which are
covered by that proprietary license — each remains under its own original
license, reproduced in full under [`LICENSES/`](LICENSES/). All components
below are permissively licensed (Apache-2.0, MIT, or BSD-3-Clause); none are
copyleft, and none restrict Plumb's own licensing choices.

**Nothing in this file or in Plumb implies endorsement, sponsorship, or
certification of Plumb by any of the organizations listed below.**

---

## Bundled binaries (native distribution — `native/dist/*`)

These are official, unmodified, pre-built binary releases downloaded directly
from each project's own release channel by `native/scripts/fetch-sidecars.sh`
and shipped inside every platform archive under `native/dist/`. Plumb
launches them as child processes; it does not modify or relink them.

| Component | Version pinned | License | Copyright | Source |
|---|---|---|---|---|
| **Prometheus** | v3.14.0 | Apache License 2.0 | The Prometheus Authors | https://github.com/prometheus/prometheus |
| **VictoriaMetrics** | v1.150.0 (community/single-node edition) | Apache License 2.0 | VictoriaMetrics, Inc. | https://github.com/VictoriaMetrics/VictoriaMetrics |

Per Apache License 2.0 §4, the full license text for each is included at
[`LICENSES/Apache-2.0.txt`](LICENSES/Apache-2.0.txt), and this notice
accompanies every distributed copy of the compiled packages that contain
these binaries. Prometheus's own upstream NOTICE file (which in turn credits
the third-party components *it* bundles) is preserved unmodified at
[`LICENSES/NOTICES/prometheus.NOTICE`](LICENSES/NOTICES/prometheus.NOTICE).
VictoriaMetrics does not publish a NOTICE file upstream.

NetApp Harvest was previously bundled here (through v0.6.1) as the
NetApp ONTAP/StorageGRID collector, but only ever published linux/amd64
binaries — no arm64, darwin, or windows build exists. It has been replaced
with an independent, in-process collector (`native/internal/netappnative/`)
written by Plumb's author from Harvest's and NetApp's own publicly published
source code, documentation, and Grafana dashboard definitions (see the table
below) — Harvest's binary is no longer downloaded, bundled, or executed by
Plumb.

---

## Statically linked Go module (native build — `native/`)

| Component | License | Copyright | Source |
|---|---|---|---|
| **gopkg.in/yaml.v3** | Dual: Apache License 2.0 (original Go code) + MIT (files ported from libyaml: `apic.go`, `emitterc.go`, `parserc.go`, `readerc.go`, `scannerc.go`, `writerc.go`, `yamlh.go`, `yamlprivateh.go`) | Canonical Ltd. and/or the individual contributors; libyaml portions © the libyaml authors | https://github.com/go-yaml/yaml |

Full texts at [`LICENSES/Apache-2.0.txt`](LICENSES/Apache-2.0.txt) and
[`LICENSES/MIT.txt`](LICENSES/MIT.txt).

---

## Python dependencies (Docker/legacy build — `backend/`, and the demo tooling)

Used by the original Docker-based backend (`backend/requirements.txt`) and by
`demo-exporter/exporter.py` (a development/demonstration tool, not part of
any distributed Plumb package). Installed via `pip` at Docker build time or
into a local virtual environment; not vendored into this repository.

| Component | License | Source |
|---|---|---|
| **FastAPI** | MIT | https://github.com/fastapi/fastapi |
| **Uvicorn** | BSD-3-Clause | https://github.com/encode/uvicorn |
| **httpx2** | BSD-3-Clause | https://github.com/pydantic/httpx2 |
| **Pydantic** | MIT | https://github.com/pydantic/pydantic |
| **PyYAML** | MIT | https://github.com/yaml/pyyaml |
| **prometheus_client** (Python) | Apache License 2.0 | https://github.com/prometheus/client_python |

Full texts at [`LICENSES/Apache-2.0.txt`](LICENSES/Apache-2.0.txt),
[`LICENSES/MIT.txt`](LICENSES/MIT.txt), and
[`LICENSES/BSD-3-Clause.txt`](LICENSES/BSD-3-Clause.txt).

---

## Base container images (Docker build only)

| Image | Publisher | License notes |
|---|---|---|
| `python:3.13-slim` | Python Software Foundation / Docker Official Images | PSF License (Python itself) plus the licenses of the underlying Debian packages it includes |
| `prom/prometheus` | The Prometheus Authors | Apache License 2.0 (same project as above, distributed as an official container image) |
| `victoriametrics/victoria-metrics` | VictoriaMetrics, Inc. | Apache License 2.0 (same project as above) |

---

## Metric naming and schema references (documentation only — no code used)

Plumb's `config/thresholds/*.yml` files query metric names that Pure Storage
and NetApp themselves publish for third-party Prometheus/OpenMetrics
consumption. Writing PromQL against a vendor's own published metric names is
an interoperability activity, not a reproduction of the referenced project's
source code. For NetApp ONTAP specifically, `native/internal/netappnative/`
also consults Harvest's published Go source (not just its metric names) to
confirm the *calculation* a metric requires — e.g. that ONTAP's REST API
returns raw cumulative counters requiring a delta-of-two-samples computation,
not a pre-computed value, and which counter pairs with which as numerator
and denominator. This is the same category of activity as implementing a
documented algorithm from a specification: **no source code from any of the
projects below is copied into Plumb** — `netappnative`'s Go implementation
is the author's own, independently written to reproduce the same
well-established, standard performance-counter math (delta-of-rates,
counter/denominator ratios) that Harvest's own code documents, not a port or
derivative of Harvest's implementation. They are listed here purely for
attribution and traceability, so the exact source of every metric name and
formula is auditable.

| Reference project | Publisher | License | Used for |
|---|---|---|---|
| [pure-fa-openmetrics-exporter](https://github.com/PureStorage-OpenConnect/pure-fa-openmetrics-exporter) | Pure Storage, Inc. (PureStorage-OpenConnect) | Apache License 2.0 | Confirming Pure FlashArray's `purefa_*` metric names |
| [pure-fb-openmetrics-exporter](https://github.com/PureStorage-OpenConnect/pure-fb-openmetrics-exporter) | Pure Storage, Inc. (PureStorage-OpenConnect) | Apache License 2.0 | Confirming Pure FlashBlade's `purefb_*` metric names |
| [NetApp/harvest](https://github.com/NetApp/harvest) (RestPerf collector source, object templates, Grafana dashboard definitions) | NetApp, Inc. | Apache License 2.0 | Confirming ONTAP metric names (`volume_avg_latency`, `node_cpu_busy`, `snapmirror_lag_time`, `nic_rx_crc_errors`, `aggr_space_used_percent`) and the counter-delta/ratio math `native/internal/netappnative/ontap.go` independently implements to compute them from ONTAP's REST API |
| NetApp StorageGRID product documentation ("Commonly used Prometheus metrics") | NetApp, Inc. | NetApp's standard documentation terms (proprietary vendor documentation; not open source) | Confirming StorageGRID's `storagegrid_*` metric names. No text was copied from this documentation — see [LEGAL.md §3](LEGAL.md#3-no-proprietary-third-party-information) |

---

## How this list is kept current

When `native/scripts/fetch-sidecars.sh`'s pinned versions change, or a new
runtime dependency is added to `native/go.mod` or `backend/requirements.txt`,
this file must be updated in the same change. `internal/updates/updates.go`'s
`PINNED` map and this file's version table should always agree.
