from __future__ import annotations

import os

import httpx2 as httpx

VM_URL = os.environ.get("VM_URL", "http://victoriametrics:8428")


async def instant_query(promql: str) -> float | None:
    async with httpx.AsyncClient(timeout=10) as client:
        r = await client.get(f"{VM_URL}/api/v1/query", params={"query": promql})
        r.raise_for_status()
        result = r.json()["data"]["result"]
        if not result:
            return None
        return float(result[0]["value"][1])


async def range_query(promql: str, start: float, end: float, step: str = "300s") -> list[list[float]]:
    async with httpx.AsyncClient(timeout=15) as client:
        r = await client.get(
            f"{VM_URL}/api/v1/query_range",
            params={"query": promql, "start": start, "end": end, "step": step},
        )
        r.raise_for_status()
        result = r.json()["data"]["result"]
        if not result:
            return []
        return [[float(ts), float(v)] for ts, v in result[0]["values"]]
