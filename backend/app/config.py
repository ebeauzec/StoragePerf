from __future__ import annotations

import os
from pathlib import Path

import yaml

CONFIG_DIR = Path(os.environ.get("PLUMB_CONFIG_DIR", "/config"))


def load_arrays() -> list[dict]:
    with open(CONFIG_DIR / "arrays.yml") as f:
        data = yaml.safe_load(f) or {}
    return data.get("arrays", [])


def save_arrays(arrays: list[dict]) -> None:
    with open(CONFIG_DIR / "arrays.yml", "w") as f:
        yaml.safe_dump({"arrays": arrays}, f, sort_keys=False)


def load_thresholds() -> list[dict]:
    with open(CONFIG_DIR / "thresholds.yml") as f:
        data = yaml.safe_load(f) or {}
    return data.get("metrics", [])


def get_array(array_id: str) -> dict | None:
    return next((a for a in load_arrays() if a["id"] == array_id), None)
