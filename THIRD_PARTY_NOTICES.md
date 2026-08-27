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
| **NetApp Harvest** | v26.08.0 (linux/amd64 only — NetApp does not publish other platform builds) | Apache License 2.0 | NetApp, Inc. | https://github.com/NetApp/harvest |

Per Apache License 2.0 §4, the full license text for each is included at
[`LICENSES/Apache-2.0.txt`](LICENSES/Apache-2.0.txt), and this notice
accompanies every distributed copy of the compiled packages that contain
these binaries. Prometheus's and Harvest's own upstream NOTICE files (which
in turn credit the third-party components *those* projects bundle) are
preserved unmodified at
[`LICENSES/NOTICES/prometheus.NOTICE`](LICENSES/NOTICES/prometheus.NOTICE) and
[`LICENSES/NOTICES/harvest.NOTICE`](LICENSES/NOTICES/harvest.NOTICE).
VictoriaMetrics does not publish a NOTICE file upstream.

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
source code — **no source code from any of the projects below is included in
Plumb.** They are listed here purely for attribution and traceability, so
the exact metric-name source is auditable.

| Reference project | Publisher | License | Used for |
|---|---|---|---|
| [pure-fa-openmetrics-exporter](https://github.com/PureStorage-OpenConnect/pure-fa-openmetrics-exporter) | Pure Storage, Inc. (PureStorage-OpenConnect) | Apache License 2.0 | Confirming Pure FlashArray's `purefa_*` metric names |
| [pure-fb-openmetrics-exporter](https://github.com/PureStorage-OpenConnect/pure-fb-openmetrics-exporter) | Pure Storage, Inc. (PureStorage-OpenConnect) | Apache License 2.0 | Confirming Pure FlashBlade's `purefb_*` metric names |
| [NetApp/harvest](https://github.com/NetApp/harvest) (Grafana dashboard definitions) | NetApp, Inc. | Apache License 2.0 | Confirming ONTAP metric names Harvest exposes (`volume_avg_latency`, `node_cpu_busy`, `snapmirror_lag_time`, etc.) |
| NetApp StorageGRID product documentation ("Commonly used Prometheus metrics") | NetApp, Inc. | NetApp's standard documentation terms (proprietary vendor documentation; not open source) | Confirming StorageGRID's `storagegrid_*` metric names. No text was copied from this documentation — see [LEGAL.md §3](LEGAL.md#3-no-proprietary-third-party-information) |

---

## How this list is kept current

When `native/scripts/fetch-sidecars.sh`'s pinned versions change, or a new
runtime dependency is added to `native/go.mod` or `backend/requirements.txt`,
this file must be updated in the same change. `internal/updates/updates.go`'s
`PINNED` map and this file's version table should always agree.
