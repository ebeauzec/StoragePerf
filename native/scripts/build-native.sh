#!/usr/bin/env bash
# Cross-compiles the Plumb binary for every platform it ships on and
# assembles one self-contained, zero-install distributable archive per
# platform: the executable, its sidecar binaries, and default config.
#
# Run scripts/fetch-sidecars.sh first — this script packages what's already
# staged under sidecars/, it doesn't download anything itself.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR/.."

DIST=dist
rm -rf "$DIST"
mkdir -p "$DIST"

VERSION=$(cat ../VERSION)

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

  # go build normally sets the executable bit on its own output -- this is
  # only needed because this script has been run from Git Bash on Windows
  # against a Drive-synced checkout, where that NTFS/MSYS permission
  # emulation is unreliable (see CLAUDE.md's note on this same checkout's
  # executable bit not sticking for tracked .sh files -- this is the same
  # class of problem, just hitting a freshly-built binary instead of a
  # git-tracked script). Confirmed live: v0.13.0-v0.16.0's darwin/linux
  # tarballs all shipped `plumb` as non-executable (644) while start.sh
  # (which already had its own explicit chmod below) was fine at 755 --
  # this is exactly why run.sh failed on a real Mac with no useful error,
  # since `exec ./plumb` inside start.sh hit a silent Permission Denied.
  # No-op on a real Unix build machine where go build already got it right.
  if [ "$os" != "windows" ]; then
    chmod +x "$outdir/plumb${exe}"
  fi

  if [ -d "sidecars/${platform}" ]; then
    cp -R "sidecars/${platform}" "$outdir/sidecars"
  else
    echo "  (no sidecars/${platform} found — run scripts/fetch-sidecars.sh first; shipping without them)"
  fi

  cp -R config "$outdir/config"
  # never ship a real arrays.yml or settings.yml with whoever built this
  # package's own demo/test/local state as the "live" config — ship only
  # the documented example; both are recreated fresh on first run
  # (see cmd/plumb/main.go and config.LoadSettings's missing-file default)
  rm -f "$outdir/config/arrays.yml" "$outdir/config/settings.yml"

  # defense in depth: data/ is runtime state (VictoriaMetrics/Prometheus
  # storage, generated scrape configs) and this loop never intentionally
  # copies it — but a distributable must never ship a real one regardless
  # of how it got there
  rm -rf "$outdir/data"

  # LICENSE/LEGAL.md/THIRD_PARTY_NOTICES.md/LICENSES/ must accompany every
  # distributed copy — this is what makes bundling the Apache-2.0 sidecar
  # binaries above license-compliant, not just legally described elsewhere.
  cp ../LICENSE ../LEGAL.md ../THIRD_PARTY_NOTICES.md "$outdir/"
  cp -R ../LICENSES "$outdir/LICENSES"

  if [ "$os" = "windows" ]; then
    cat > "$outdir/start.bat" <<'EOF'
@echo off
rem Files downloaded via a browser get marked with Windows' "Mark of the
rem Web", which triggers a SmartScreen warning on first run of an
rem unsigned executable. Unblock-File strips that mark before we launch,
rem so double-clicking this script doesn't hit that prompt. Harmless if
rem it's already unblocked or PowerShell/policy prevents it.
powershell -NoProfile -Command "Get-ChildItem -Path '%~dp0' -Recurse | Unblock-File" >nul 2>&1
plumb.exe
rem Only pause on a nonzero exit (crash, port in use, blocked by AV, etc.)
rem -- a normal Ctrl+C stop exits 0 and should just close the window, not
rem demand a keypress every time. Without this, a startup failure flashes
rem the window shut before its error message can be read.
set PLUMB_EXIT=%ERRORLEVEL%
if not "%PLUMB_EXIT%"=="0" (
    echo.
    echo Plumb exited with an error ^(code %PLUMB_EXIT%^) -- see the output above.
    pause
)
EOF
    # cmd.exe's batch parser can misparse a bare-LF file (which is all a
    # heredoc ever produces) the moment a "rem" comment contains a
    # non-ASCII byte, silently corrupting every line after it -- CRLF is
    # what makes that safe regardless of what a future edit adds to the
    # comments above. See run.bat at the repo root for the same fix and a
    # from-scratch repro of the failure this avoids.
    #
    # perl, not sed -i: BSD sed (macOS) requires an explicit (even if
    # empty) backup-suffix argument after -i, while GNU sed (Linux, Git
    # Bash) treats a bare -i as "no backup" -- the two are incompatible in
    # a way that silently breaks one platform whichever form is chosen.
    # perl -i behaves the same on both, so this build script works
    # regardless of which OS it's run from.
    perl -pi -e 's/\n/\r\n/' "$outdir/start.bat"
  else
    cat > "$outdir/start.sh" <<'EOF'
#!/usr/bin/env bash
cd "$(dirname "${BASH_SOURCE[0]}")"
# Files downloaded via a browser get tagged with macOS's
# com.apple.quarantine attribute, which makes Gatekeeper block an
# unsigned binary with an "unidentified developer" prompt. Strip it
# before launching so that doesn't happen — harmless no-op if the
# archive was fetched some other way (e.g. curl) and was never tagged.
if command -v xattr >/dev/null 2>&1; then
  xattr -dr com.apple.quarantine . 2>/dev/null || true
fi
# Defense in depth, not the fix itself (see build-native.sh's own comment
# on why plumb sometimes ships without its executable bit set) -- cheap
# insurance so a build-machine permission quirk can never again turn into
# a silent "Permission denied" on exec below with no useful error shown.
chmod +x ./plumb 2>/dev/null || true
# exec, not a plain call: this replaces the shell with plumb (same PID)
# instead of running it as a child — otherwise killing/Ctrl+C-ing this
# script leaves plumb (and its own sidecar children) running as orphans.
exec ./plumb
EOF
    chmod +x "$outdir/start.sh"
  fi

  ( cd "$DIST" && \
    if [ "$archive_ext" = "zip" ]; then
      if command -v zip >/dev/null 2>&1; then
        zip -qr "plumb-${VERSION}-${platform}.zip" "plumb-${VERSION}-${platform}"
      else
        # Git Bash on Windows doesn't ship a zip binary, and this build
        # script is now routinely run from a Windows dev machine (not just
        # macOS/Linux) -- fall back to PowerShell's own Compress-Archive,
        # which is present on every Windows install with no extra tool to
        # source.
        #
        # -Path must point at the directory itself, NOT "dir\*": run.ps1's
        # extraction picks Get-ChildItem $tempExtract | Select -First 1 and
        # moves that one item to become $Dest, exactly matching zip -qr's
        # behavior of wrapping everything in one top-level folder. Pointing
        # at "dir\*" instead zips the *contents* at the archive root with no
        # wrapping folder, so that Get-ChildItem call grabs one arbitrary
        # file (alphabetically first) instead of the whole release --
        # silently shipping a zip that installs as a handful of stray files
        # with plumb.exe and everything else left behind unextracted.
        win_src=$(cd "plumb-${VERSION}-${platform}" && pwd -W)
        win_dest="$(pwd -W)/plumb-${VERSION}-${platform}.zip"
        powershell.exe -NoProfile -Command "Compress-Archive -Path '${win_src}' -DestinationPath '${win_dest}' -Force"
      fi
    else
      # Not a plain `tar -czf`: see make-tar.py's own doc comment for why
      # -- chmod is a confirmed no-op on this Windows/Drive-synced
      # checkout, so a shell tar here would silently archive `plumb` as
      # non-executable regardless of the chmod calls above (which do
      # nothing real for tar/ls to pick up either). This sets each entry's
      # mode explicitly, independent of the host filesystem.
      python3 "$SCRIPT_DIR/make-tar.py" \
        "plumb-${VERSION}-${platform}" \
        "plumb-${VERSION}-${platform}.tar.gz" \
        "plumb-${VERSION}-${platform}"
    fi
  )
  echo "  -> $DIST/plumb-${VERSION}-${platform}.${archive_ext}"
done

echo
echo "Done. Distributables in $DIST/ — unzip/untar one on the target machine"
echo "and run plumb (or start.sh / start.bat). Nothing else to install."
