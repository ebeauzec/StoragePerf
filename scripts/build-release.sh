#!/usr/bin/env bash
# Builds every image this stack needs, pulls the two upstream images, and
# saves the whole set into one offline-loadable tarball plus the config/
# files needed to run it — everything install.sh needs, nothing it has to
# fetch from the internet at install time.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

DIST=dist
RELEASE_DIR="$DIST/plumb-release"
IMAGES_TAR="$RELEASE_DIR/plumb-images.tar"

rm -rf "$RELEASE_DIR"
mkdir -p "$RELEASE_DIR"

echo "==> Building plumb-api and demo-exporter images"
docker compose build

echo "==> Pulling upstream images (Prometheus, VictoriaMetrics)"
docker compose pull victoriametrics prometheus

echo "==> Saving all images to $IMAGES_TAR"
IMAGES=$(docker compose config --images)
docker save -o "$IMAGES_TAR" $IMAGES
echo "    $(du -h "$IMAGES_TAR" | cut -f1) — $(echo "$IMAGES" | tr '\n' ' ')"

echo "==> Copying deploy-time files"
cp docker-compose.yml install.sh README.md LICENSE LEGAL.md THIRD_PARTY_NOTICES.md "$RELEASE_DIR/"
cp -R LICENSES "$RELEASE_DIR/LICENSES"
cp -R config "$RELEASE_DIR/config"
cp -R frontend "$RELEASE_DIR/frontend"
mkdir -p "$RELEASE_DIR/prometheus"
cp prometheus/prometheus.yml "$RELEASE_DIR/prometheus/"

echo "==> Archiving release"
tar -czf "$DIST/plumb-release.tar.gz" -C "$DIST" plumb-release
echo
echo "Done: $DIST/plumb-release.tar.gz ($(du -h "$DIST/plumb-release.tar.gz" | cut -f1))"
echo "Ship that one file. On the target machine: tar xzf plumb-release.tar.gz && cd plumb-release && ./install.sh"
