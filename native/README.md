# Plumb (native) — zero-install distribution

This is the self-contained build: one executable per OS that embeds the
frontend and launches bundled Prometheus + VictoriaMetrics as child
processes, and collects from NetApp systems directly in-process (no
separate collector). No Docker, no Python, no separately-installed
anything — unzip and run, on any platform.

## Using a released distribution

**Quickest: one command, auto-fetches the latest release.** From the repo
root:

```bash
./run.sh          # macOS/Linux
```
```powershell
.\run.ps1         # Windows (PowerShell)
```

**Manual, per OS** — download `plumb-<version>-<os>_<arch>.(tar.gz|zip)`
for your platform from the
[Releases page](https://github.com/ebeauzec/StoragePerf/releases/latest),
extract it, then:

| OS | Command |
|---|---|
| **macOS** | `./plumb` or `./start.sh` from inside the extracted folder (Terminal) |
| **Linux** | `./plumb` or `./start.sh` from inside the extracted folder |
| **Windows** | Double-click `start.bat`, or run `plumb.exe` directly, from inside the extracted folder |

Either way: open **http://localhost:8000**, then edit `config/arrays.yml`
(or use the **Config** tab) to point it at your real systems.

macOS may show an "unidentified developer" warning, and Windows a
SmartScreen warning, if the archive was downloaded via a browser rather
than `run.sh`/`run.ps1` — `start.sh`/`start.bat` handle both
automatically; see the root README's
[Troubleshooting](../README.md#11-troubleshooting) section for the manual
fix either way.

If you're working from this same repo checkout rather than a fresh
download, `native/latest/<os>_<arch>/` (when present) is a pre-extracted,
always-current copy of the most recent build for every platform — run
directly from there with no download or extraction step at all. It's
`.gitignore`d and gets refreshed on every release, not something every
clone of this repo will have.

Everything Plumb writes — VictoriaMetrics's data, Prometheus's short-term
buffer, logs, generated reports — lives under `data/` next to the
executable. Move the whole folder and it keeps working; nothing is written
outside it.

## Upgrading without losing data

The one rule that makes an upgrade safe: everything that matters is three
things sitting next to the executable — `data/` (the metrics database),
`config/arrays.yml` (your real array inventory), and `config/settings.yml`
(retention period, mock-mode preference). Everything else — the binary,
the bundled sidecars, `config/thresholds/*.yml` — is disposable and should
come from the new version, not be carried over.

**From inside the app (recommended):** the Config tab's Software &
Reference Updates panel checks Plumb's own version against GitHub the
same way it already checks Prometheus/VictoriaMetrics/vendor references.
When a newer release exists, an **Update now** button appears on that
row — click it and Plumb downloads the new version, carries over `data/`,
`arrays.yml`, and `settings.yml` into a fresh sibling install directory
(`plumb-<version>-update/`, next to the current one), launches it, and
hands off the port once the old process shuts down. The browser
reconnects on its own once the new version answers. This is the one
thing in that panel that isn't purely check-and-notify — see
`internal/selfupdate`'s doc comment for exactly what it does and doesn't
verify before running a new binary, and its safety properties (the old
install directory is never touched, so a bad update is trivially
reversible: stop the new process, start the old one from where it still
sits). `PLUMB_CHECK_FOR_UPDATES=false` disables checking *and* this
button together, for air-gapped or high-security sites.

**Using `run.sh` / `run.ps1`:** just re-run it. Since v0.8.6+, both
scripts move `data/`, `arrays.yml`, and `settings.yml` aside before
replacing the install folder, then restore them into the freshly
extracted version — the running instance ends up on the new code with
its full history and real inventory intact, no manual steps. (Versions
before that wiped everything on every upgrade — if you're upgrading a
long-running pre-0.8.6 instance, do the manual steps below at least this
once.)

**Manual upgrade** (a fresh archive download, or `native/latest/`):

1. Stop the currently-running instance.
2. Extract the new version to a **new** folder — don't extract on top of
   the old one.
3. Move three things from the old install into the new folder, replacing
   whatever the new archive shipped in their place:
   - `data/` (the whole directory)
   - `config/arrays.yml`
   - `config/settings.yml`
4. Start the new version from its own folder.

That's it — nothing needs converting or migrating. VictoriaMetrics's
on-disk format is stable across Plumb releases (Plumb doesn't touch it
directly; it only ever talks to VictoriaMetrics over its HTTP API), and
`arrays.yml`/`settings.yml` are plain YAML Plumb already knows how to
read regardless of which release wrote them.

## Local development — always run the current source

Don't extract a release archive to iterate on the code — that's a snapshot,
and it's easy to end up unknowingly testing yesterday's build in a
random-versioned folder. Instead, from `native/`:

```bash
go run ./scripts/dev
```

This builds Plumb from whatever's currently in the source tree, always runs
it from the same fixed location (`native/run/`, gitignored), and — critically
— stops whatever it previously started there first, including the sidecar
processes (Prometheus/VictoriaMetrics), not just the top-level binary. Run
it again after any change and you're always looking at current
code; there's no version to track or stale copy to accidentally be running.
Works the same way on Linux, macOS, and Windows — it's a Go program, not a
shell script, so there's nothing platform-specific to invoke.

`native/run/` seeds its own `config/` from `config/arrays.example.yml` and
`sidecars/` from whatever's staged under `sidecars/<your-platform>/` (run
`scripts/fetch-sidecars.sh` once first if that's empty) — separate from
anything in `dist/`, so this never touches a packaged release.

## Multi-vendor support

`vendor` on each array entry selects both the collection method and the
threshold file (`config/thresholds/<vendor>.yml`) that scores it:

| vendor | collected via | platforms |
|---|---|---|
| `pure_flasharray`, `pure_flashblade` | Plumb's own authenticated scrape proxy, direct to the array | all |
| `netapp_ontap`, `netapp_storagegrid` | Plumb's own in-process collector (`internal/netappnative`), direct to the cluster's REST API / grid's Management API | all |

See `config/arrays.example.yml` for the fields each vendor needs. Earlier
versions collected NetApp systems via a bundled copy of NetApp's own
Harvest, which only published linux/amd64 binaries; `internal/netappnative`
replaces that with an independent collector (written from Harvest's and
NetApp's own published source — see `../THIRD_PARTY_NOTICES.md`) that works
on every platform, with full metric parity — including `aggr_disk_busy`
and `nic_util_percent`, which need ONTAP's raw performance counter-tables
API rather than its simpler REST resource endpoints; see
`ontap_countertables.go`'s doc comment for how that's implemented.

## Reports and export

- `GET /api/reports/array/{id}?hours=N` — a self-contained HTML report:
  per-metric stats (min/avg/p95/max), what fraction of the period was in
  watch/critical, trend direction, and a written analysis — generated by
  re-evaluating stored history, not a separate findings log.
- `GET /api/reports/fleet?hours=N` — the same, rolled up and ranked across
  every configured system.
- `GET /api/export/{id}?hours=N` — that system's metrics as CSV, long
  format (one row per metric per timestamp), for anything else you want to
  do with the data.

The Fleet/Report/Export CSV buttons in the UI are these same endpoints.

## Building it yourself

Requires Go 1.22+.

```bash
./scripts/fetch-sidecars.sh   # downloads pinned Prometheus/VictoriaMetrics releases into sidecars/
./scripts/build-native.sh     # cross-compiles plumb + assembles dist/plumb-<version>-<platform>.(tar.gz|zip)
```

`fetch-sidecars.sh` only needs to run again when you bump the pinned
versions in that script (and in `internal/updates/updates.go`, which tracks
the same numbers for the check-and-notify feature).

## Versioning

Plumb's own version — as opposed to the pinned third-party sidecar versions
above — is Semantic Versioning (`MAJOR.MINOR.PATCH`), sourced from the single
`VERSION` file at the repository root. `build-native.sh` reads it to name
the distributable archives and to bake it into the binary
(`-ldflags -X main.version=...`), which is what the UI's version tag (top
left) and `GET /api/version` report at runtime. Each release is tagged
`vX.Y.Z` and published as a GitHub Release at
[github.com/ebeauzec/StoragePerf/releases](https://github.com/ebeauzec/StoragePerf/releases)
— the version badge on the main README tracks that automatically.

To cut a new version: bump `VERSION`, commit, tag (`git tag vX.Y.Z`), push
the tag, and create the matching GitHub Release.

## What I could actually verify

Built and tested end-to-end (real Prometheus/VictoriaMetrics binaries, real
scrape flow, fleet view, findings, export, both report types) on
**darwin/arm64**. The other four platform binaries cross-compile cleanly
from the same source with no platform-specific code paths, and their
sidecar binaries are the same upstream releases Prometheus/VictoriaMetrics
publish for those platforms — but I don't have a Linux or Windows machine
in this environment to run them on directly. If something platform-specific
breaks, `data/logs/*.log` for each sidecar is the first place to look.
