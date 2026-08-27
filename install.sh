#!/usr/bin/env bash
# Plumb installer. Run this from inside the extracted release directory
# (the one containing this script, docker-compose.yml, config/, frontend/).
#
# What it does, in order:
#   1. Checks Docker is present (does NOT install it for you — that's a
#      system-level change this script won't make silently).
#   2. If plumb-images.tar ships alongside this script, loads it — no
#      internet needed. Otherwise falls back to building from source,
#      which does need internet (base images + pip packages).
#   3. Starts the stack and waits for it to answer.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

echo "==> Checking prerequisites"
if ! command -v docker >/dev/null 2>&1; then
  echo "Docker is not installed or not on PATH." >&2
  echo "Install Docker (or start your existing Docker runtime) and re-run this script." >&2
  echo "See: https://docs.docker.com/engine/install/" >&2
  exit 1
fi
if ! docker info >/dev/null 2>&1; then
  echo "Docker is installed but not running/reachable." >&2
  echo "Start your Docker daemon (or 'colima start' if you're using Colima) and re-run." >&2
  exit 1
fi
if ! docker compose version >/dev/null 2>&1; then
  echo "The 'docker compose' plugin isn't available." >&2
  echo "See: https://docs.docker.com/compose/install/" >&2
  exit 1
fi
echo "    docker: $(docker --version)"
echo "    compose: $(docker compose version --short 2>/dev/null || docker compose version)"

if [ -f plumb-images.tar ]; then
  echo "==> Loading bundled images (offline — no internet needed for this step)"
  docker load -i plumb-images.tar
else
  echo "==> No bundled image tarball found next to this script."
  echo "    Falling back to building from source — this needs internet access"
  echo "    to pull base images and Python packages."
  docker compose build
  docker compose pull victoriametrics prometheus
fi

if [ ! -f config/arrays.yml ]; then
  echo "==> No config/arrays.yml found — starting from the bundled example"
  cp config/arrays.example.yml config/arrays.yml
fi

echo "==> Starting Plumb"
docker compose up -d

echo "==> Waiting for it to come up"
for i in $(seq 1 30); do
  if curl -sf http://localhost:8000/api/config/arrays >/dev/null 2>&1; then
    echo
    echo "Plumb is running: http://localhost:8000"
    echo "Edit config/arrays.yml (or use the Config tab) to point it at your real arrays."
    exit 0
  fi
  sleep 2
done

echo "Started, but it hasn't answered yet after 60s — check 'docker compose logs plumb-api'." >&2
exit 1
