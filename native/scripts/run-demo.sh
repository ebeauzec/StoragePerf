#!/usr/bin/env bash
# Launches the full 7-system demo fleet (all four vendors) and points
# config/arrays.yml at it, so `./plumb` shows real-looking data across
# every vendor immediately — no real hardware, no Docker, just this script
# plus Python (for the demo exporters only; Plumb itself needs neither).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

EXPORTER_DIR="../demo-exporter"
if [ ! -f "$EXPORTER_DIR/exporter.py" ]; then
  echo "Can't find $EXPORTER_DIR/exporter.py — run this from the native/ directory of the full repo (not a packaged distribution)." >&2
  exit 1
fi

VENV=".demo-venv"
if [ ! -d "$VENV" ]; then
  echo "==> Setting up a local Python venv for the demo exporters (one-time)"
  python3 -m venv "$VENV"
  "$VENV/bin/pip" install --quiet prometheus_client==0.26.0
fi

PIDS=()
cleanup() {
  echo
  echo "==> Stopping demo exporters"
  for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null || true; done
}
trap cleanup EXIT INT TERM

start() {
  local id="$1" vendor="$2" profile="$3" port="$4"
  VENDOR="$vendor" PROFILE="$profile" PORT="$port" \
    "$VENV/bin/python" "$EXPORTER_DIR/exporter.py" > "/tmp/plumb-demo-${id}.log" 2>&1 &
  PIDS+=("$!")
  echo "  $id  ($vendor, $profile)  -> 127.0.0.1:$port  [pid $!]"
}

echo "==> Starting demo fleet"
start fa-prod-east-01   pure_flasharray     critical 9491
start fa-prod-west-02   pure_flasharray     watch    9492
start fa-erp-cluster    pure_flasharray     healthy  9493
start fb-media-01       pure_flashblade     watch    9494
start ontap-cluster-01  netapp_ontap        critical 9495
start ontap-cluster-02  netapp_ontap        healthy  9496
start sg-grid-01        netapp_storagegrid  watch    9497

cp config/arrays.demo.yml config/arrays.yml
echo
echo "==> config/arrays.yml now points at the demo fleet above."
echo "    In another terminal: ./plumb   (or ./start.sh), then open http://localhost:8000"
echo "    Press Ctrl+C here to stop the demo fleet when you're done."
echo

wait
