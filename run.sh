#!/usr/bin/env bash
# Zero-install launcher for the native build: fetches the latest released
# Plumb binary for your platform and starts it. This is what makes
# "download the repo, unzip it, run one script" work again after the move
# away from Docker — see native/README.md for why that move happened
# (cross-platform NetApp support, no Docker requirement for locked-down
# pilot sites) and README.md section 4 for the manual alternative.
#
# Deliberately downloads via curl + tar rather than a browser: files that
# arrive that way are never tagged with macOS's com.apple.quarantine
# attribute (only browser/Finder downloads are), so Gatekeeper has nothing
# to block and there's no "unidentified developer" prompt to click through.
# The xattr strip below is a defensive no-op for the rare case something
# in the chain quarantines it anyway.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

REPO="ebeauzec/StoragePerf"
DEST="plumb-release"

os="$(uname -s)"
arch="$(uname -m)"
case "$os" in
  Darwin) platform_os=darwin ;;
  Linux) platform_os=linux ;;
  *)
    echo "This is the macOS/Linux launcher. On Windows, run run.ps1 instead" >&2
    echo "(same repo root) — or download plumb-<version>-windows_amd64.zip" >&2
    echo "directly from https://github.com/$REPO/releases/latest" >&2
    exit 1
    ;;
esac
case "$arch" in
  arm64|aarch64) platform_arch=arm64 ;;
  x86_64|amd64) platform_arch=amd64 ;;
  *)
    echo "Unsupported architecture: $arch" >&2
    exit 1
    ;;
esac
platform="${platform_os}_${platform_arch}"

echo "==> Checking the latest release for $platform"
api_json=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest")
tag=$(printf '%s' "$api_json" | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')
asset_url=$(printf '%s' "$api_json" \
  | grep -o "\"browser_download_url\": *\"[^\"]*plumb-[^\"]*-${platform}\.tar\.gz\"" \
  | sed -E 's/.*"(https:[^"]+)"/\1/' | head -1)

if [ -z "$tag" ] || [ -z "$asset_url" ]; then
  echo "Couldn't find a $platform release asset automatically." >&2
  echo "Download it manually from: https://github.com/$REPO/releases/latest" >&2
  exit 1
fi

marker="$DEST/.installed_version"
if [ -x "$DEST/plumb" ] && [ -f "$marker" ] && [ "$(cat "$marker")" = "$tag" ]; then
  echo "==> $tag already installed at ./$DEST — starting"
else
  asset_name=$(basename "$asset_url")
  echo "==> Downloading $asset_name ($tag)"
  tmp=$(mktemp -d)
  curl -fsSL -o "$tmp/$asset_name" "$asset_url"

  echo "==> Installing to ./$DEST"
  rm -rf "$DEST"
  mkdir -p "$DEST"
  tar -xzf "$tmp/$asset_name" -C "$DEST" --strip-components=1
  rm -rf "$tmp"
  echo "$tag" > "$marker"
fi

if command -v xattr >/dev/null 2>&1; then
  xattr -dr com.apple.quarantine "$DEST" 2>/dev/null || true
fi

echo "==> Starting Plumb — http://localhost:8000"
cd "$DEST"
exec ./start.sh
