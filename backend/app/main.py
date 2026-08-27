import asyncio
import os

import httpx2 as httpx
from fastapi import FastAPI, HTTPException
from fastapi.responses import PlainTextResponse
from fastapi.staticfiles import StaticFiles
from pydantic import BaseModel

from . import updates
from .config import get_array, load_arrays, save_arrays
from .rules import evaluate_array
from .targets import generate_file_sd

FRONTEND_DIR = os.environ.get("PLUMB_FRONTEND_DIR", "/frontend")

app = FastAPI(title="Plumb API")


@app.on_event("startup")
async def startup() -> None:
    generate_file_sd()
    asyncio.create_task(updates.background_loop())


@app.get("/api/updates")
async def get_updates() -> dict:
    return {"enabled": updates.CHECK_FOR_UPDATES, "checks": updates.get_cached()}


@app.get("/scrape/{array_id}")
async def scrape(array_id: str) -> PlainTextResponse:
    """Authenticated fetch of one array's native /metrics, proxied for Prometheus."""
    array = get_array(array_id)
    if not array:
        raise HTTPException(404, f"unknown array '{array_id}'")

    scheme = array.get("scheme", "https")
    path = array.get("metrics_path", "/metrics")
    url = f"{scheme}://{array['host']}{path}"

    headers = {}
    token_env = array.get("token_env")
    if token_env:
        token = os.environ.get(token_env)
        if token:
            headers["Authorization"] = f"Bearer {token}"

    try:
        async with httpx.AsyncClient(timeout=10, verify=array.get("verify_tls", True)) as client:
            r = await client.get(url, headers=headers)
            r.raise_for_status()
            return PlainTextResponse(r.text)
    except httpx.HTTPError as e:
        raise HTTPException(502, f"scrape of '{array_id}' failed: {e}") from e


@app.get("/api/fleet")
async def fleet() -> list[dict]:
    out = []
    for a in load_arrays():
        result = await evaluate_array(a["id"], hours=1)
        by_id = {p["id"]: p for p in result["panels"]}
        out.append(
            {
                "id": a["id"],
                "name": a["name"],
                "model": a.get("model", "-"),
                "health": result["health"],
                "queue_depth": (by_id.get("host_queue_depth") or {}).get("value"),
                "latency": (by_id.get("host_latency") or {}).get("value"),
                "sparkline": (by_id.get("host_latency") or {}).get("series", []),
            }
        )
    return out


@app.get("/api/arrays/{array_id}")
async def array_detail(array_id: str, hours: float = 24) -> dict:
    if not get_array(array_id):
        raise HTTPException(404, f"unknown array '{array_id}'")
    return await evaluate_array(array_id, hours=hours)


@app.get("/api/config/arrays")
async def get_arrays_config() -> dict:
    return {"arrays": load_arrays()}


class ArraysPayload(BaseModel):
    arrays: list[dict]


@app.put("/api/config/arrays")
async def put_arrays_config(payload: ArraysPayload) -> dict:
    save_arrays(payload.arrays)
    n = generate_file_sd()
    return {"saved": len(payload.arrays), "targets_written": n}


app.mount("/", StaticFiles(directory=FRONTEND_DIR, html=True), name="frontend")
