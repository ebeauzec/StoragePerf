#!/usr/bin/env bash
# Zero-install launcher for the native build: fetches the latest released
# Plumb binary for your platform and starts it. This is what makes
# "download the repo, unzip it, run one script" work again after the move
# away from Docker -- see native/README.md for why that move happened
# (cross-platform NetApp support, no Docker requirement for locked-down
# pilot sites) and README.md section 4 for the manual alternative.
#
# Deliberately downloads via curl + tar rather than a browser: files that
# arrive that way are never tagged with macOS's com.apple.quarantine
# attribute (only browser/Finder downloads are), so Gatekeeper has nothing
# to block and there's no "unidentified developer" prompt to click through.
# The xattr strip below is a defensive no-op for the rare case something
# in the chain quarantines it anyway.
#
# DARK SITES / NO INTERNET: this script never *requires* the internet.
# If GitHub can't be reached (or PLUMB_OFFLINE=1 is set, which skips the
# attempt entirely), it falls back in this order:
#   1. A release archive sitting next to this script
#      (plumb-<version>-<platform>.tar.gz, downloaded on a connected
#      machine and carried across) is installed if it is newer than what's
#      already installed -- this is how a dark site is upgraded.
#   2. Otherwise the already-installed version is started as-is.
#   3. Otherwise it says exactly what to copy here and stops.
# A download that fails partway never touches a working install.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

REPO="ebeauzec/StoragePerf"
DEST="plumb-release"
# Overridable so the launcher can be pointed at an internal mirror that
# serves GitHub's release JSON, and so the offline path can be tested by
# aiming this at a dead port. Not needed for normal use.
API="${PLUMB_RELEASE_API:-https://api.github.com/repos/$REPO/releases/latest}"
marker="$DEST/.installed_version"

os="$(uname -s)"
arch="$(uname -m)"
case "$os" in
  Darwin) platform_os=darwin ;;
  Linux) platform_os=linux ;;
  *)
    echo "This is the macOS/Linux launcher. On Windows, run run.ps1 instead" >&2
    echo "(same repo root) -- or download plumb-<version>-windows_amd64.zip" >&2
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

# ver_gt A B: succeeds when dotted version A is strictly newer than B.
# Plain awk rather than `sort -V`, which older macOS versions lack.
ver_gt() {
  awk -v a="$1" -v b="$2" 'BEGIN {
    n = split(a, x, "."); m = split(b, y, "."); k = (n > m ? n : m)
    for (i = 1; i <= k; i++) { xi = x[i] + 0; yi = y[i] + 0
      if (xi > yi) exit 0
      if (xi < yi) exit 1 }
    exit 1 }'
}

# installed_version prints the installed version without its leading "v",
# or nothing if there's no usable install. Requiring the binary itself (not
# just the marker) means a partial/corrupt install counts as "not installed".
installed_version() {
  if [ -x "$DEST/plumb" ] && [ -f "$marker" ]; then
    sed -e 's/^v//' -e 's/[[:space:]]*$//' "$marker"
  fi
}

start_plumb() {
  echo "==> Starting Plumb -- http://localhost:8000"
  cd "$DEST"
  exec ./start.sh
}

# install_archive ARCHIVE TAG DELETE(yes|no): replaces the installed copy
# with ARCHIVE's contents, carrying over what the user owns.
install_archive() {
  local archive="$1" tag="$2" delete_after="$3"

  # Preserve what the user actually owns across the upgrade: the collected
  # metrics database and their real array inventory/settings. An upgrade
  # replaces the application code and bundled defaults -- it must never
  # throw away a live database or real credentials to do that. Held in a
  # sibling dir (not a mktemp dir, which may sit on a different
  # filesystem/volume) so the final restore is a same-filesystem mv.
  local preserve=".plumb-upgrade-preserve"
  rm -rf "$preserve"
  mkdir -p "$preserve"
  [ -d "$DEST/data" ] && mv "$DEST/data" "$preserve/data"
  if [ -f "$DEST/config/arrays.yml" ] || [ -f "$DEST/config/settings.yml" ]; then
    mkdir -p "$preserve/config"
    [ -f "$DEST/config/arrays.yml" ] && mv "$DEST/config/arrays.yml" "$preserve/config/"
    [ -f "$DEST/config/settings.yml" ] && mv "$DEST/config/settings.yml" "$preserve/config/"
  fi

  echo "==> Installing to ./$DEST"
  rm -rf "$DEST"
  mkdir -p "$DEST"
  tar -xzf "$archive" -C "$DEST" --strip-components=1
  [ "$delete_after" = "yes" ] && rm -f "$archive"

  # Scoped to the freshly-extracted files, before the (possibly large,
  # possibly slow-to-traverse on a cloud-synced folder) preserved data/
  # directory gets moved back in below -- there is nothing to strip from a
  # database this script already had on disk. See this file's header
  # comment for why this is a defensive no-op even for the files it scans.
  if command -v xattr >/dev/null 2>&1; then
    xattr -dr com.apple.quarantine "$DEST" 2>/dev/null || true
  fi

  if [ -d "$preserve/data" ]; then
    echo "==> Restoring existing metrics database"
    mv "$preserve/data" "$DEST/data"
  fi
  [ -f "$preserve/config/arrays.yml" ] && mv "$preserve/config/arrays.yml" "$DEST/config/arrays.yml"
  [ -f "$preserve/config/settings.yml" ] && mv "$preserve/config/settings.yml" "$DEST/config/settings.yml"
  rm -rf "$preserve"

  echo "$tag" > "$marker"
}

# offline_start is everything that happens when there's no release to
# download: use a newer local archive if one was dropped next to this
# script, else the installed copy, else explain what's needed.
offline_start() {
  local best="" best_ver="" f v inst
  for f in plumb-*-"$platform".tar.gz; do
    [ -f "$f" ] || continue
    v="${f#plumb-}"; v="${v%-"$platform".tar.gz}"
    if [ -z "$best" ] || ver_gt "$v" "$best_ver"; then best="$f"; best_ver="$v"; fi
  done
  inst="$(installed_version)"

  if [ -n "$best" ] && { [ -z "$inst" ] || ver_gt "$best_ver" "$inst"; }; then
    echo "==> Installing from the local archive $best (version $best_ver)"
    install_archive "$best" "v$best_ver" no
    start_plumb
  elif [ -n "$inst" ]; then
    echo "==> Starting the installed version (v$inst)"
    start_plumb
  fi

  echo "" >&2
  echo "Plumb isn't installed yet, and there's no internet access to download it." >&2
  echo "On a machine that has internet, download plumb-<version>-$platform.tar.gz from" >&2
  echo "  https://github.com/$REPO/releases/latest" >&2
  echo "copy it into this folder ($(pwd)) and run this script again." >&2
  exit 1
}

tag=""
asset_url=""
case "${PLUMB_OFFLINE:-}" in
  ""|0|false|no)
    echo "==> Checking the latest release for $platform"
    # Every step below tolerates failure on purpose. Under `set -e` +
    # pipefail a bare curl failure, or a grep that matches nothing on an
    # empty response, would abort the whole script with no message at all
    # -- exactly the situation (no internet) this has to handle calmly.
    api_json="$(curl -fsSL --connect-timeout 5 --max-time 20 "$API" 2>/dev/null)" || api_json=""
    tag="$(printf '%s' "$api_json" | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')" || tag=""
    asset_url="$(printf '%s' "$api_json" \
      | grep -o "\"browser_download_url\": *\"[^\"]*plumb-[^\"]*-${platform}\.tar\.gz\"" \
      | sed -E 's/.*"(https:[^"]+)"/\1/' | head -1)" || asset_url=""
    if [ -z "$tag" ] || [ -z "$asset_url" ]; then
      echo "==> Couldn't reach the release server (no internet, a firewall, or GitHub's rate limit) -- continuing offline"
    fi
    ;;
  *)
    echo "==> PLUMB_OFFLINE is set -- not contacting the release server"
    ;;
esac

if [ -z "$tag" ] || [ -z "$asset_url" ]; then
  offline_start
fi

inst="$(installed_version)"
if [ -n "$inst" ] && [ "v$inst" = "$tag" ]; then
  echo "==> $tag already installed at ./$DEST -- starting"
  start_plumb
fi

asset_name="$(basename "$asset_url")"
echo "==> Downloading $asset_name ($tag)"
tmp="$(mktemp -d)"
if curl -fsSL --connect-timeout 10 -o "$tmp/$asset_name" "$asset_url"; then
  install_archive "$tmp/$asset_name" "$tag" yes
  rm -rf "$tmp"
  start_plumb
fi

# The release server answered but the download itself failed (a flaky or
# filtered connection). Nothing has been touched yet, so fall back exactly
# as if there were no internet at all rather than leaving the user stuck.
rm -rf "$tmp"
echo "==> The download failed -- continuing offline"
offline_start
