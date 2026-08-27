"""
Writes the Prometheus file_sd targets file from config/arrays.yml.

Each array becomes one scrape target pointed back at *this* service's own
/scrape/{id} proxy route (see main.py), rather than at the array directly.
That's what lets every array carry its own bearer token and TLS setting
independently, while Prometheus itself needs no per-target auth config at
all — it just scrapes plumb-api, which does the authenticated fetch on the
array's behalf and returns the raw exposition text.

The `array` and `model` labels set here are attached by Prometheus to every
series scraped from that target, which is how PromQL in thresholds.yml can
filter with array="{array}" even though the array's own /metrics output
carries no such label itself.
"""
import json
import os
from pathlib import Path

from .config import load_arrays

TARGETS_FILE = Path(os.environ.get("PROMETHEUS_TARGETS_FILE", "/prometheus-targets/pure-arrays.json"))
SELF_ADDRESS = os.environ.get("PLUMB_SELF_ADDRESS", "plumb-api:8000")


def generate_file_sd() -> int:
    arrays = load_arrays()
    targets = [
        {
            "targets": [SELF_ADDRESS],
            "labels": {
                "__metrics_path__": f"/scrape/{a['id']}",
                "array": a["id"],
                "model": a.get("model", "unknown"),
            },
        }
        for a in arrays
    ]
    TARGETS_FILE.parent.mkdir(parents=True, exist_ok=True)
    TARGETS_FILE.write_text(json.dumps(targets, indent=2))
    return len(targets)
