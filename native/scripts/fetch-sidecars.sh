#!/usr/bin/env bash
# Downloads the exact pinned Prometheus, VictoriaMetrics, and (Linux/amd64
# only — see below) NetApp Harvest release binaries for every platform
# Plumb ships for, and lays them out under sidecars/<os>_<arch>/ exactly
# where cmd/plumb expects to find them at runtime.
#
# NetApp's Harvest project only publishes a linux/amd64 build (no arm64, no
# darwin, no windows) as of this writing — so NetApp ONTAP/StorageGRID
# monitoring is only available in the linux_amd64 distribution. Pure
# FlashArray/FlashBlade monitoring works on every platform regardless,
# since it doesn't depend on Harvest at all.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

PROM_VERSION=3.14.0
VM_VERSION=1.150.0
HARVEST_VERSION=26.08.0

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

mkdir -p sidecars

fetch() {
  local url="$1" out="$2"
  echo "  fetching $url"
  curl -sL --fail "$url" -o "$out"
}

place_prometheus() {
  local os="$1" arch="$2" ext="$3" exe="$4"
  local dir="sidecars/${os}_${arch}"
  mkdir -p "$dir"
  local archive="$WORK/prometheus.$ext"
  fetch "https://github.com/prometheus/prometheus/releases/download/v${PROM_VERSION}/prometheus-${PROM_VERSION}.${os}-${arch}.${ext}" "$archive"
  local extract="$WORK/prom-${os}-${arch}"
  mkdir -p "$extract"
  if [ "$ext" = "zip" ]; then
    unzip -q -o "$archive" -d "$extract"
  else
    tar -xzf "$archive" -C "$extract"
  fi
  cp "$extract/prometheus-${PROM_VERSION}.${os}-${arch}/${exe}" "$dir/${exe}"
  chmod +x "$dir/${exe}"
  echo "  -> $dir/${exe}"
}

place_victoriametrics() {
  local os="$1" arch="$2" ext="$3" exe="$4"
  local dir="sidecars/${os}_${arch}"
  mkdir -p "$dir"
  local archive="$WORK/vm.$ext"
  fetch "https://github.com/VictoriaMetrics/VictoriaMetrics/releases/download/v${VM_VERSION}/victoria-metrics-${os}-${arch}-v${VM_VERSION}.${ext}" "$archive"
  local extract="$WORK/vm-${os}-${arch}"
  mkdir -p "$extract"
  if [ "$ext" = "zip" ]; then
    unzip -q -o "$archive" -d "$extract"
  else
    tar -xzf "$archive" -C "$extract"
  fi
  # the binary inside is named victoria-metrics-prod on unix, but
  # victoria-metrics-windows-amd64-prod.exe on windows — match loosely
  local src
  src=$(find "$extract" -maxdepth 1 -type f -name "victoria-metrics*prod*")
  cp "$src" "$dir/${exe}"
  chmod +x "$dir/${exe}"
  echo "  -> $dir/${exe}"
}

place_harvest_linux_amd64() {
  local dir="sidecars/linux_amd64"
  mkdir -p "$dir"
  local archive="$WORK/harvest.tar.gz"
  fetch "https://github.com/NetApp/harvest/releases/download/v${HARVEST_VERSION}/harvest-${HARVEST_VERSION}-1_linux_amd64.tar.gz" "$archive"
  local extract="$WORK/harvest-linux-amd64"
  mkdir -p "$extract"
  tar -xzf "$archive" -C "$extract"
  local src
  src=$(find "$extract" -type f -name "harvest" | head -1)
  cp "$src" "$dir/harvest"
  chmod +x "$dir/harvest"
  echo "  -> $dir/harvest"
}

echo "== Prometheus v${PROM_VERSION} =="
place_prometheus linux   amd64 tar.gz prometheus
place_prometheus linux   arm64 tar.gz prometheus
place_prometheus darwin  amd64 tar.gz prometheus
place_prometheus darwin  arm64 tar.gz prometheus
place_prometheus windows amd64 zip    prometheus.exe

echo "== VictoriaMetrics v${VM_VERSION} =="
place_victoriametrics linux   amd64 tar.gz victoria-metrics
place_victoriametrics linux   arm64 tar.gz victoria-metrics
place_victoriametrics darwin  amd64 tar.gz victoria-metrics
place_victoriametrics darwin  arm64 tar.gz victoria-metrics
place_victoriametrics windows amd64 zip    victoria-metrics.exe

echo "== NetApp Harvest v${HARVEST_VERSION} (linux/amd64 only — see script header) =="
place_harvest_linux_amd64

echo
echo "Sidecars staged under ./sidecars/. Not committed to source control by"
echo "default (see .gitignore) — run this script before scripts/build-native.sh"
echo "on any machine that doesn't already have them."
