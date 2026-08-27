"""
The enrichment layer: evaluates config/thresholds.yml against live/historical
data for one array and produces the panels and findings the UI renders.

This is deliberately data-driven — adding a metric to thresholds.yml adds a
panel and a potential finding with no changes here. The one piece of logic
that isn't purely per-metric is the cross-panel "bottleneck is upstream"
finding below, which looks at the front-end/back-end split as a whole.
"""
from __future__ import annotations

import time

from .config import load_thresholds
from .metrics import instant_query, range_query


def classify(value: float | None, watch: float, critical: float) -> str:
    if value is None:
        return "unknown"
    if value >= critical:
        return "critical"
    if value >= watch:
        return "watch"
    return "good"


async def evaluate_array(array_id: str, hours: float = 24) -> dict:
    metric_defs = load_thresholds()
    now = time.time()
    panels = []
    findings = []
    severities = []

    for m in metric_defs:
        # plain substitution, not str.format() — PromQL's own {label="value"}
        # selector syntax collides with format()'s field-name parsing
        query = m["query"].replace("{array}", array_id)
        value = await instant_query(query)
        sev = classify(value, m["severity_watch"], m["severity_critical"])
        severities.append(sev)
        series = await range_query(query, now - hours * 3600, now, step=f"{max(60, int(hours * 12))}s")

        panels.append(
            {
                "id": m["id"],
                "label": m["label"],
                "unit": m["unit"],
                "category": m["category"],
                "value": value,
                "severity": sev,
                "threshold_label": m["threshold_label"],
                "watch": m["severity_watch"],
                "critical": m["severity_critical"],
                "series": series,
            }
        )

        if sev in ("watch", "critical") and value is not None:
            template = m.get("finding", {}).get(sev)
            if template:
                threshold = m["severity_watch"] if sev == "watch" else m["severity_critical"]
                findings.append(
                    {
                        "severity": sev,
                        "tag": m["category"],
                        "title": m["label"],
                        "body": template.format(value=value, threshold=threshold),
                        "ref": f"{array_id} · {m['id']}",
                    }
                )

    frontend_panels = [p for p in panels if p["category"] == "frontend"]
    backend_panels = [p for p in panels if p["category"] == "backend"]
    frontend_bad = any(p["severity"] in ("watch", "critical") for p in frontend_panels)
    backend_clean = frontend_panels and backend_panels and all(p["severity"] == "good" for p in backend_panels)

    if frontend_bad and backend_clean:
        worst = "critical" if any(p["severity"] == "critical" for p in frontend_panels) else "watch"
        findings.insert(
            0,
            {
                "severity": worst,
                "tag": "fleet",
                "title": "Bottleneck is likely upstream of the array",
                "body": (
                    "Front-end metrics are degraded while replication and capacity — the two "
                    "internal signals this array publishes — show no strain. That points to the "
                    "SAN fabric or host path as the likely cause. Caveat: Pure's public metrics "
                    "endpoint doesn't expose controller busy% or media service-time, so this "
                    "isn't a complete picture of the array's internal state, just the strongest "
                    "signal available from published metrics."
                ),
                "ref": f"{array_id} · derived from front-end + back-end panels",
            },
        )

    if "critical" in severities:
        health = "critical"
    elif "watch" in severities:
        health = "watch"
    else:
        health = "good"

    return {"panels": panels, "findings": findings, "health": health}
