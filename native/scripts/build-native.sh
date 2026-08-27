#!/usr/bin/env bash
# Cross-compiles the Plumb binary for every platform it ships on and
# assembles one self-contained, zero-install distributable archive per
# platform: the executable, its sidecar binaries, and default config.
#
# Run scripts/fetch-sidecars.sh first — this script packages what's already
# staged under sidecars/, it doesn't download anything itself.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

DIST=dist
rm -rf "$DIST"
mkdir -p "$DIST"

VERSION=$(date +%Y.%m.%d)

# os:arch:exe-suffix:archive-ext
TARGETS=(
  "linux:amd64::tar.gz"
  "linux:arm64::tar.gz"
  "darwin:amd64::tar.gz"
  "darwin:arm64::tar.gz"
  "windows:amd64:.exe:zip"
)

for row in "${TARGETS[@]}"; do
  IFS=':' read -r os arch exe archive_ext <<< "$row"
  platform="${os}_${arch}"
  echo "== Building $platform =="

  outdir="$DIST/plumb-${VERSION}-${platform}"
  rm -rf "$outdir"
  mkdir -p "$outdir"

  GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o "$outdir/plumb${exe}" ./cmd/plumb

  if [ -d "sidecars/${platform}" ]; then
    cp -R "sidecars/${platform}" "$outdir/sidecars"
  else
    echo "  (no sidecars/${platform} found — run scripts/fetch-sidecars.sh first; shipping without them)"
  fi

  cp -R config "$outdir/config"
  # never ship a real arrays.yml with someone's demo/test data as the
  # "live" config — ship only the documented example, first run creates
  # arrays.yml from it automatically (see cmd/plumb/main.go)
  rm -f "$outdir/config/arrays.yml"

  # LICENSE/LEGAL.md/THIRD_PARTY_NOTICES.md/LICENSES/ must accompany every
  # distributed copy — this is what makes bundling the Apache-2.0 sidecar
  # binaries above license-compliant, not just legally described elsewhere.
  cp ../LICENSE ../LEGAL.md ../THIRD_PARTY_NOTICES.md "$outdir/"
  cp -R ../LICENSES "$outdir/LICENSES"

  if [ "$os" = "windows" ]; then
    cat > "$outdir/start.bat" <<'EOF'
@echo off
plumb.exe
EOF
  else
    cat > "$outdir/start.sh" <<'EOF'
#!/usr/bin/env bash
cd "$(dirname "${BASH_SOURCE[0]}")"
./plumb
EOF
    chmod +x "$outdir/start.sh"
  fi

  ( cd "$DIST" && \
    if [ "$archive_ext" = "zip" ]; then
      zip -qr "plumb-${VERSION}-${platform}.zip" "plumb-${VERSION}-${platform}"
    else
      tar -czf "plumb-${VERSION}-${platform}.tar.gz" "plumb-${VERSION}-${platform}"
    fi
  )
  echo "  -> $DIST/plumb-${VERSION}-${platform}.${archive_ext}"
done

echo
echo "Done. Distributables in $DIST/ — unzip/untar one on the target machine"
echo "and run plumb (or start.sh / start.bat). Nothing else to install."
