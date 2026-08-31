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
$Dest = "plumb-release"

# This repo routinely lives in a cloud-synced folder (OneDrive/Google
# Drive), and this script's own reinstall flow deletes $Dest then
# immediately re-creates and re-checks paths inside it while also doing
# heavy I/O nearby (extracting a ~70MB archive) -- exactly the kind of
# contention that can make Google Drive's virtual filesystem briefly throw
# UnauthorizedAccessException ("Access is denied") from Test-Path instead
# of just returning $false, while its own sync/reconciliation reacts to
# what this script just did. Confirmed by hitting it directly: deleting
# plumb-release and immediately re-running this script threw that exact
# exception from the very next Test-Path call -- and it wasn't consistently
# reproducible on a fixed delay, consistent with contention rather than a
# predictable settling time. Retry for a few seconds instead of letting one
# blip abort the whole install; this isn't a 100% guarantee against an
# inherently flaky virtual filesystem, but it rides out the common case.
function Test-PathResilient {
    param([string]$Path)
    for ($i = 0; $i -lt 12; $i++) {
        try { return Test-Path $Path } catch {
            if ($i -eq 11) { throw }
            Start-Sleep -Milliseconds 700
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

# A plain "plumb-release" here may not even be a Windows install: this repo
# commonly lives in a cloud-synced folder (OneDrive/Google Drive) shared
# with a Mac/Linux machine, and run.sh installs into this same $Dest name
# with a "plumb" binary (no .exe) instead of "plumb.exe". Requiring
# plumb.exe specifically means a synced-over macOS/Linux install is
# correctly treated as "not installed for this platform" and replaced,
# rather than silently trying (and failing) to run someone else's binary.
$marker = Join-Path $Dest ".installed_version"
$alreadyInstalled = $false
if ((Test-PathResilient (Join-Path $Dest "plumb.exe")) -and (Test-PathResilient $marker)) {
    $installed = (Get-Content $marker -Raw).Trim()
    $alreadyInstalled = $installed -eq $tag
}

if ($alreadyInstalled) {
    Write-Host "==> $tag already installed at .\$Dest -- starting"
} else {
    $zipPath = Join-Path $env:TEMP $asset.name
    Write-Host "==> Downloading $($asset.name) ($tag)"
    Invoke-WebRequest -Uri $asset.browser_download_url -OutFile $zipPath

    # Preserve what the user actually owns across the upgrade: the
    # collected metrics database and their real array inventory/settings.
    # An upgrade replaces the application code and bundled defaults -- it
    # must never throw away a live database or real credentials to do
    # that.
    $preserve = ".plumb-upgrade-preserve"
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

    Write-Host "==> Installing to .\$Dest"
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
    # install) was a real, needless cost, especially on a cloud-synced
    # folder (OneDrive/Google Drive) where every file operation is far
    # slower than on a local disk.
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
