# Zero-install launcher for the native build: fetches the latest released
# Plumb binary for Windows and starts it. Mirrors run.sh's approach for
# macOS/Linux (see that file for the Gatekeeper-avoidance reasoning) --
# Windows' equivalent concern is the "Mark of the Web" that triggers a
# SmartScreen warning on files downloaded via a browser. Invoke-WebRequest
# can still apply that mark, so this doesn't rely on avoiding it the way
# run.sh avoids quarantine with curl -- instead it explicitly runs
# Unblock-File on everything before launching, same as start.bat already
# does defensively for anyone who downloads the release archive by hand.
#
# DARK SITES / NO INTERNET: this script never *requires* the internet.
# If GitHub can't be reached (or PLUMB_OFFLINE=1 is set, which skips the
# attempt entirely), it falls back in this order:
#   1. A release archive sitting next to this script
#      (plumb-<version>-windows_amd64.zip, downloaded on a connected
#      machine and carried across) is installed if it is newer than what's
#      already installed -- this is how a dark site is upgraded.
#   2. Otherwise the already-installed version is started as-is.
#   3. Otherwise it says exactly what to copy here and stops.
# A download that fails partway never touches a working install.
#
# NOTE: keep this file pure ASCII. Windows PowerShell 5.1 reads a BOM-less
# .ps1 under the legacy system codepage, so a single em dash or smart quote
# can produce a misleading parse error nowhere near the bad character.
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
# Overridable so the launcher can be pointed at an internal mirror that
# serves GitHub's release JSON, and so the offline path can be tested by
# aiming this at a dead port. Not needed for normal use.
$Api = if ($env:PLUMB_RELEASE_API) { $env:PLUMB_RELEASE_API } else { "https://api.github.com/repos/$Repo/releases/latest" }
$OfflineRequested = [bool]$env:PLUMB_OFFLINE -and (@("0", "false", "no") -notcontains $env:PLUMB_OFFLINE.ToLower())

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
$marker = Join-Path $Dest ".installed_version"

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

# Returns the installed version (no leading "v") or $null. Requiring
# plumb.exe itself (not just the marker) means a partial/corrupt previous
# install is treated as "not installed" and replaced, rather than silently
# trying (and failing) to run something that isn't there.
function Get-InstalledVersion {
    if ((Test-PathResilient (Join-Path $Dest "plumb.exe")) -and (Test-PathResilient $marker)) {
        return (Get-Content $marker -Raw).Trim().TrimStart("v")
    }
    return $null
}

function ConvertTo-PlumbVersion {
    param([string]$Text)
    try { return [version]$Text } catch { return $null }
}

function Start-Plumb {
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
}

# Replaces the installed copy with the contents of $ZipPath, carrying over
# what the user owns. $DeleteZip is $true only for a file this script itself
# downloaded -- a zip the user copied next to the script is theirs to keep.
function Install-PlumbArchive {
    param([string]$ZipPath, [string]$Tag, [bool]$DeleteZip)

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
    $tempExtract = Join-Path $env:TEMP "plumb-extract-$Tag"
    if (Test-Path $tempExtract) { Remove-Item -Recurse -Force $tempExtract }
    Expand-Archive -Path $ZipPath -DestinationPath $tempExtract

    # the archive's own top-level folder is plumb-<version>-windows_amd64 --
    # move its contents up a level so $Dest is always the same fixed path
    # regardless of version, matching run.sh's convention
    $inner = Get-ChildItem $tempExtract | Select-Object -First 1
    Move-Item $inner.FullName $Dest
    Remove-Item -Recurse -Force $tempExtract
    if ($DeleteZip) { Remove-Item $ZipPath }

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

    Set-Content -Path $marker -Value $Tag
}

# Everything that happens when there is no release to download: use a newer
# local archive if one was dropped next to this script, else the installed
# copy, else explain what's needed. Never returns normally on the "nothing
# to run" path -- it throws, so run.bat pauses on the message.
function Start-Offline {
    $best = $null
    foreach ($f in Get-ChildItem -Path $PSScriptRoot -Filter "plumb-*-windows_amd64.zip" -File -ErrorAction SilentlyContinue) {
        if ($f.Name -match '^plumb-(\d+(\.\d+){1,3})-windows_amd64\.zip$') {
            $text = $Matches[1]
            $v = ConvertTo-PlumbVersion $text
            if ($v -and ((-not $best) -or ($v -gt $best.Version))) {
                $best = [pscustomobject]@{ Path = $f.FullName; Name = $f.Name; Version = $v; Text = $text }
            }
        }
    }
    $instText = Get-InstalledVersion
    $inst = if ($instText) { ConvertTo-PlumbVersion $instText } else { $null }

    if ($best -and ((-not $instText) -or (-not $inst) -or ($best.Version -gt $inst))) {
        Write-Host "==> Installing from the local archive $($best.Name) (version $($best.Text))"
        Install-PlumbArchive -ZipPath $best.Path -Tag "v$($best.Text)" -DeleteZip $false
        Start-Plumb
        return
    }
    if ($instText) {
        Write-Host "==> Starting the installed version (v$instText)"
        Start-Plumb
        return
    }
    throw ("Plumb isn't installed yet, and there's no internet access to download it. " +
        "On a machine that has internet, download plumb-<version>-windows_amd64.zip from " +
        "https://github.com/$Repo/releases/latest, copy it into $PSScriptRoot and run this again.")
}

# This whole body is wrapped so a double-click via run.bat gets a readable
# "==> ERROR: ..." line instead of a raw PowerShell exception, and so
# run.bat can tell success from failure via the exit code and pause the
# window on failure instead of it flashing shut.
try {

$tag = $null
$asset = $null
if ($OfflineRequested) {
    Write-Host "==> PLUMB_OFFLINE is set -- not contacting the release server"
} else {
    Write-Host "==> Checking the latest release for windows_amd64"
    try {
        $release = Invoke-RestMethod -Uri $Api -TimeoutSec 15
        $tag = $release.tag_name
        $asset = $release.assets | Where-Object { $_.name -like "plumb-*-windows_amd64.zip" } | Select-Object -First 1
    } catch {
        $tag = $null
        $asset = $null
    }
    if ((-not $tag) -or (-not $asset)) {
        Write-Host "==> Couldn't reach the release server (no internet, a firewall, or GitHub's rate limit) -- continuing offline"
    }
}

if ((-not $tag) -or (-not $asset)) {
    Start-Offline
    exit 0
}

$instText = Get-InstalledVersion
if ($instText -and ("v$instText" -eq $tag)) {
    Write-Host "==> $tag already installed at $Dest -- starting"
    Start-Plumb
    exit 0
}

$zipPath = Join-Path $env:TEMP $asset.name
Write-Host "==> Downloading $($asset.name) ($tag)"
$downloaded = $false
try {
    Invoke-WebRequest -Uri $asset.browser_download_url -OutFile $zipPath
    $downloaded = $true
} catch {
    Write-Host "==> The download failed -- continuing offline"
    if (Test-Path $zipPath) { Remove-Item $zipPath -ErrorAction SilentlyContinue }
}

if (-not $downloaded) {
    # Nothing has been touched yet, so fall back exactly as if there were no
    # internet at all rather than leaving the user stuck.
    Start-Offline
    exit 0
}

Install-PlumbArchive -ZipPath $zipPath -Tag $tag -DeleteZip $true
Start-Plumb

} catch {
    Write-Host ""
    Write-Host "==> ERROR: $_" -ForegroundColor Red
    exit 1
}
