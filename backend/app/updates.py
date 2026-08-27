"""
Check-and-notify update checking — the ONLY thing this module does is look
and report. It never downloads, installs, or changes a running container.
That's a deliberate scope limit, not a missing feature: this box sits next
to production SAN traffic, so nothing here should be able to change what's
running without a human deciding to.

Two things are tracked:
  - Component versions (Prometheus, VictoriaMetrics) against their GitHub
    releases, so you know when a newer build exists.
  - The Pure metric-schema reference this project's thresholds.yml was
    checked against (PureStorage-OpenConnect/pure-fa-openmetrics-exporter),
    so you know if the public metric naming may have moved since.

Set PLUMB_CHECK_FOR_UPDATES=false to disable this entirely — reaches
nothing, schedules nothing. Safe default for a fully air-gapped deployment.
"""
from __future__ import annotations

import asyncio
import os
import time

import httpx2 as httpx

CHECK_FOR_UPDATES = os.environ.get("PLUMB_CHECK_FOR_UPDATES", "true").lower() == "true"
CHECK_INTERVAL_SECONDS = float(os.environ.get("PLUMB_UPDATE_CHECK_INTERVAL_SECONDS", 24 * 3600))

# What we shipped with — bump these when you deliberately upgrade a pinned
# version (docker-compose.yml image tags, backend/requirements.txt).
PINNED = {
    "prometheus": "v3.14.0",
    "victoriametrics": "v1.150.0",
}
THRESHOLDS_CHECKED_AGAINST = "2026-08-27"

TARGETS = [
    {"id": "prometheus", "label": "Prometheus", "repo": "prometheus/prometheus", "current": PINNED["prometheus"]},
    {"id": "victoriametrics", "label": "VictoriaMetrics", "repo": "VictoriaMetrics/VictoriaMetrics", "current": PINNED["victoriametrics"]},
    {"id": "pure_exporter_reference", "label": "Pure metric-schema reference", "repo": "PureStorage-OpenConnect/pure-fa-openmetrics-exporter", "current": None},
]

_last_result: list[dict] = [
    {**{k: v for k, v in t.items() if k != "repo"}, "latest": None, "update_available": None, "status": "not checked yet", "checked_at": None, "url": f"https://github.com/{t['repo']}/releases"}
    for t in TARGETS
]


async def _fetch_latest_tag(client: httpx.AsyncClient, repo: str) -> str | None:
    r = await client.get(f"https://api.github.com/repos/{repo}/releases/latest")
    if r.status_code == 200:
        return r.json().get("tag_name")
    # some repos don't publish GitHub "releases", only tags
    r = await client.get(f"https://api.github.com/repos/{repo}/tags", params={"per_page": 1})
    if r.status_code == 200 and r.json():
        return r.json()[0].get("name")
    return None


async def check_now() -> list[dict]:
    results = []
    async with httpx.AsyncClient(timeout=10, headers={"Accept": "application/vnd.github+json"}) as client:
        for t in TARGETS:
            entry = {
                "id": t["id"],
                "label": t["label"],
                "current": t["current"],
                "url": f"https://github.com/{t['repo']}/releases",
                "checked_at": time.time(),
            }
            try:
                latest = await _fetch_latest_tag(client, t["repo"])
                entry["latest"] = latest
                entry["status"] = "ok" if latest else "no releases found"
                entry["update_available"] = bool(latest and t["current"] and latest != t["current"])
            except httpx.HTTPError as e:
                entry["latest"] = None
                entry["status"] = f"unreachable ({e.__class__.__name__})"
                entry["update_available"] = None
            results.append(entry)
    results[-1]["note"] = (
        f"Informational only — config/thresholds.yml's PromQL was checked against this "
        f"project's published metric names on {THRESHOLDS_CHECKED_AGAINST}. A newer tag here "
        f"doesn't mean your thresholds are wrong, just that it may be worth re-checking."
    )
    return results


def get_cached() -> list[dict]:
    return _last_result


async def background_loop() -> None:
    global _last_result
    if not CHECK_FOR_UPDATES:
        for entry in _last_result:
            entry["status"] = "disabled (PLUMB_CHECK_FOR_UPDATES=false)"
        return
    while True:
        try:
            _last_result = await check_now()
        except Exception:
            # never let a flaky network or GitHub outage take the app down —
            # keep serving whatever we last knew
            pass
        await asyncio.sleep(CHECK_INTERVAL_SECONDS)
