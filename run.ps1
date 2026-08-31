# Zero-install launcher for the native build: fetches the latest released
# Plumb binary for Windows and starts it. Mirrors run.sh's approach for
# macOS/Linux (see that file for the Gatekeeper-avoidance reasoning) --
# Windows' equivalent concern is the "Mark of the Web" that triggers a
# SmartScreen warning on files downloaded via a browser. Invoke-WebRequest
# can still apply that mark, so this doesn't rely on avoiding it the way
# run.sh avoids quarantine with curl -- instead it explicitly runs
# Unblock-File on everything before launching, same as start.bat already
# does defensively for anyone who downloads the release archive by hand.
$ErrorActionPreference = "Stop"
# Invoke-WebRequest renders a progress bar by default, which is extremely
# slow over a ~70MB download in older PowerShell hosts -- this is a
# download-speed fix, unrelated to the SmartScreen/Unblock-File handling.
$ProgressPreference = "SilentlyContinue"
Set-Location $PSScriptRoot

# Windows PowerShell 5.1's default SecurityProtocol on an unpatched/older
# system can still be TLS 1.0, which GitHub's API and CDN reject outright
# -- that fails before this script does anything visible, with a generic
# "Could not create SSL/TLS secure channel" error. Force 1.2 unconditionally
# rather than trying to detect whether it's needed.
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

$Repo = "ebeauzec/StoragePerf"

# The install itself lives outside the repo entirely, in the per-user
# local profile -- NOT inside this cloud-synced folder (OneDrive/Google
# Drive). It has to be: this script's own reinstall flow deletes the
# install directory and immediately re-creates + rewrites a ~12MB
# plumb.exe inside it, over and over across upgrades, and Google Drive's
# virtual filesystem treats that pattern as sync-worthy churn on a large
# binary -- it can hold the file locked mid-upload/verification for
# anywhere from under a second to several minutes, surfacing as Test-Path
# throwing UnauthorizedAccessException ("Access is denied") instead of
# returning $false, or a plain write failing outright. This was hit for
# real (not just in testing): reproducibly on this exact plumb-release
# path, but never on a fresh path or a local (non-Drive) directory --
# confirming it's Drive's file-locking behavior on repeated rewrites of
# this specific large binary, not a bug in the check itself or a one-off
# testing artifact. Retrying around it wasn't sufficient; not fighting a
# cloud sync client for a lock is the actual fix. $env:LOCALAPPDATA is
# never synced by Drive/OneDrive by convention, so this sidesteps the
# whole failure class rather than mitigating it.
$Dest = Join-Path $env:LOCALAPPDATA "Plumb"

# One-time migration for anyone who already has a previous install sitting
# in the old, repo-relative location (every release through v0.10.3
# installed there) -- carry their real data and config forward instead of
# silently starting over, then get out of the cloud-synced folder for good.
$oldDest = Join-Path $PSScriptRoot "plumb-release"
if ((Test-Path $oldDest) -and -not (Test-Path $Dest)) {
    Write-Host "==> Moving existing install from .\plumb-release to $Dest (out of the synced folder)"
    New-Item -ItemType Directory -Path $Dest -Force | Out-Null
    $oldDataDir = Join-Path $oldDest "data"
    if (Test-Path $oldDataDir) { Move-Item $oldDataDir (Join-Path $Dest "data") }
    $oldCfgDir = Join-Path $oldDest "config"
    if (Test-Path $oldCfgDir) {
        New-Item -ItemType Directory -Path (Join-Path $Dest "config") -Force | Out-Null
        foreach ($f in "arrays.yml", "settings.yml") {
            $src = Join-Path $oldCfgDir $f
            if (Test-Path $src) { Move-Item $src (Join-Path $Dest "config\$f") }
        }
    }
    Remove-Item -Recurse -Force $oldDest -ErrorAction SilentlyContinue
}

# Defense in depth, not the primary fix (moving $Dest off the synced
# folder above is): retry a transient Test-Path failure instead of
# aborting on the first one, in case $env:LOCALAPPDATA is itself
# redirected onto a network/synced location in some environment.
function Test-PathResilient {
    param([string]$Path)
    for ($i = 0; $i -lt 5; $i++) {
        try { return Test-Path $Path } catch {
            if ($i -eq 4) { throw }
            Start-Sleep -Milliseconds 500
        }
    }
}

# This whole body is wrapped so a double-click via run.bat gets a readable
# "==> ERROR: ..." line instead of a raw PowerShell exception, and so
# run.bat can tell success from failure via the exit code and pause the
# window on failure instead of it flashing shut.
try {

Write-Host "==> Checking the latest release for windows_amd64"
$release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest"
$tag = $release.tag_name
$asset = $release.assets | Where-Object { $_.name -like "plumb-*-windows_amd64.zip" } | Select-Object -First 1

if (-not $asset) {
    throw "Couldn't find a windows_amd64 release asset. Download manually from https://github.com/$Repo/releases/latest"
}

# Requiring plumb.exe specifically (not just the directory) means a
# partial/corrupt previous install is treated as "not installed" and
# replaced, rather than silently trying (and failing) to run something
# that isn't there.
$marker = Join-Path $Dest ".installed_version"
$alreadyInstalled = $false
if ((Test-PathResilient (Join-Path $Dest "plumb.exe")) -and (Test-PathResilient $marker)) {
    $installed = (Get-Content $marker -Raw).Trim()
    $alreadyInstalled = $installed -eq $tag
}

if ($alreadyInstalled) {
    Write-Host "==> $tag already installed at $Dest -- starting"
} else {
    $zipPath = Join-Path $env:TEMP $asset.name
    Write-Host "==> Downloading $($asset.name) ($tag)"
    Invoke-WebRequest -Uri $asset.browser_download_url -OutFile $zipPath

    # Preserve what the user actually owns across the upgrade: the
    # collected metrics database and their real array inventory/settings.
    # An upgrade replaces the application code and bundled defaults -- it
    # must never throw away a live database or real credentials to do
    # that. Lives next to $Dest (also outside the synced repo folder), not
    # under it -- same reasoning as $Dest itself.
    $preserve = Join-Path $env:LOCALAPPDATA ".plumb-upgrade-preserve"
    if (Test-PathResilient $preserve) { Remove-Item -Recurse -Force $preserve }
    New-Item -ItemType Directory -Path $preserve | Out-Null
    $oldData = Join-Path $Dest "data"
    if (Test-PathResilient $oldData) { Move-Item $oldData (Join-Path $preserve "data") }
    $oldArrays = Join-Path $Dest "config\arrays.yml"
    $oldSettings = Join-Path $Dest "config\settings.yml"
    if ((Test-PathResilient $oldArrays) -or (Test-PathResilient $oldSettings)) {
        New-Item -ItemType Directory -Path (Join-Path $preserve "config") | Out-Null
        if (Test-PathResilient $oldArrays) { Move-Item $oldArrays (Join-Path $preserve "config\arrays.yml") }
        if (Test-PathResilient $oldSettings) { Move-Item $oldSettings (Join-Path $preserve "config\settings.yml") }
    }

    Write-Host "==> Installing to $Dest"
    if (Test-PathResilient $Dest) { Remove-Item -Recurse -Force $Dest }
    $tempExtract = Join-Path $env:TEMP "plumb-extract-$tag"
    if (Test-Path $tempExtract) { Remove-Item -Recurse -Force $tempExtract }
    Expand-Archive -Path $zipPath -DestinationPath $tempExtract

    # the archive's own top-level folder is plumb-<version>-windows_amd64 --
    # move its contents up a level so $Dest is always the same fixed path
    # regardless of version, matching run.sh's convention
    $inner = Get-ChildItem $tempExtract | Select-Object -First 1
    Move-Item $inner.FullName $Dest
    Remove-Item -Recurse -Force $tempExtract
    Remove-Item $zipPath

    # Scoped to the freshly-extracted files, before the (possibly large)
    # preserved data/ directory gets moved back in below -- there's nothing
    # to unblock in a database this script already had on disk, and
    # recursively unblocking it on every single launch (not just a fresh
    # install) was a real, needless cost.
    Get-ChildItem -Path $Dest -Recurse | Unblock-File

    if (Test-PathResilient (Join-Path $preserve "data")) {
        Write-Host "==> Restoring existing metrics database"
        Move-Item (Join-Path $preserve "data") (Join-Path $Dest "data")
    }
    $newArrays = Join-Path $preserve "config\arrays.yml"
    $newSettings = Join-Path $preserve "config\settings.yml"
    if (Test-PathResilient $newArrays) { Move-Item $newArrays $oldArrays }
    if (Test-PathResilient $newSettings) { Move-Item $newSettings $oldSettings }
    Remove-Item -Recurse -Force $preserve

    Set-Content -Path $marker -Value $tag
}

Write-Host "==> Starting Plumb - http://localhost:8000"
Push-Location $Dest
try {
    & .\plumb.exe
    # & doesn't throw on a nonzero exit by itself -- check explicitly so a
    # plumb.exe that fails immediately (port in use, blocked by AV, etc.)
    # is reported as a failure instead of this script quietly finishing
    # "successfully" a fraction of a second after it started.
    if ($LASTEXITCODE -ne 0) {
        throw "plumb.exe exited with code $LASTEXITCODE -- see the output above"
    }
} finally {
    Pop-Location
}

} catch {
    Write-Host ""
    Write-Host "==> ERROR: $_" -ForegroundColor Red
    exit 1
}
