# Zero-install launcher for the native build: fetches the latest released
# Plumb binary for Windows and starts it. Mirrors run.sh's approach for
# macOS/Linux (see that file for the Gatekeeper-avoidance reasoning) —
# Windows' equivalent concern is the "Mark of the Web" that triggers a
# SmartScreen warning on files downloaded via a browser. Invoke-WebRequest
# can still apply that mark, so this doesn't rely on avoiding it the way
# run.sh avoids quarantine with curl — instead it explicitly runs
# Unblock-File on everything before launching, same as start.bat already
# does defensively for anyone who downloads the release archive by hand.
$ErrorActionPreference = "Stop"
# Invoke-WebRequest renders a progress bar by default, which is extremely
# slow over a ~70MB download in older PowerShell hosts — this is a
# download-speed fix, unrelated to the SmartScreen/Unblock-File handling.
$ProgressPreference = "SilentlyContinue"
Set-Location $PSScriptRoot

$Repo = "ebeauzec/StoragePerf"
$Dest = "plumb-release"

Write-Host "==> Checking the latest release for windows_amd64"
$release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest"
$tag = $release.tag_name
$asset = $release.assets | Where-Object { $_.name -like "plumb-*-windows_amd64.zip" } | Select-Object -First 1

if (-not $asset) {
    Write-Error "Couldn't find a windows_amd64 release asset. Download manually from https://github.com/$Repo/releases/latest"
    exit 1
}

$marker = Join-Path $Dest ".installed_version"
$alreadyInstalled = $false
if ((Test-Path (Join-Path $Dest "plumb.exe")) -and (Test-Path $marker)) {
    $installed = (Get-Content $marker -Raw).Trim()
    $alreadyInstalled = $installed -eq $tag
}

if ($alreadyInstalled) {
    Write-Host "==> $tag already installed at .\$Dest — starting"
} else {
    $zipPath = Join-Path $env:TEMP $asset.name
    Write-Host "==> Downloading $($asset.name) ($tag)"
    Invoke-WebRequest -Uri $asset.browser_download_url -OutFile $zipPath

    # Preserve what the user actually owns across the upgrade: the
    # collected metrics database and their real array inventory/settings.
    # An upgrade replaces the application code and bundled defaults — it
    # must never throw away a live database or real credentials to do
    # that.
    $preserve = ".plumb-upgrade-preserve"
    if (Test-Path $preserve) { Remove-Item -Recurse -Force $preserve }
    New-Item -ItemType Directory -Path $preserve | Out-Null
    $oldData = Join-Path $Dest "data"
    if (Test-Path $oldData) { Move-Item $oldData (Join-Path $preserve "data") }
    $oldArrays = Join-Path $Dest "config\arrays.yml"
    $oldSettings = Join-Path $Dest "config\settings.yml"
    if ((Test-Path $oldArrays) -or (Test-Path $oldSettings)) {
        New-Item -ItemType Directory -Path (Join-Path $preserve "config") | Out-Null
        if (Test-Path $oldArrays) { Move-Item $oldArrays (Join-Path $preserve "config\arrays.yml") }
        if (Test-Path $oldSettings) { Move-Item $oldSettings (Join-Path $preserve "config\settings.yml") }
    }

    Write-Host "==> Installing to .\$Dest"
    if (Test-Path $Dest) { Remove-Item -Recurse -Force $Dest }
    $tempExtract = Join-Path $env:TEMP "plumb-extract-$tag"
    if (Test-Path $tempExtract) { Remove-Item -Recurse -Force $tempExtract }
    Expand-Archive -Path $zipPath -DestinationPath $tempExtract

    # the archive's own top-level folder is plumb-<version>-windows_amd64 —
    # move its contents up a level so $Dest is always the same fixed path
    # regardless of version, matching run.sh's convention
    $inner = Get-ChildItem $tempExtract | Select-Object -First 1
    Move-Item $inner.FullName $Dest
    Remove-Item -Recurse -Force $tempExtract
    Remove-Item $zipPath

    # Scoped to the freshly-extracted files, before the (possibly large)
    # preserved data/ directory gets moved back in below — there's nothing
    # to unblock in a database this script already had on disk, and
    # recursively unblocking it on every single launch (not just a fresh
    # install) was a real, needless cost, especially on a cloud-synced
    # folder (OneDrive/Google Drive) where every file operation is far
    # slower than on a local disk.
    Get-ChildItem -Path $Dest -Recurse | Unblock-File

    if (Test-Path (Join-Path $preserve "data")) {
        Write-Host "==> Restoring existing metrics database"
        Move-Item (Join-Path $preserve "data") (Join-Path $Dest "data")
    }
    $newArrays = Join-Path $preserve "config\arrays.yml"
    $newSettings = Join-Path $preserve "config\settings.yml"
    if (Test-Path $newArrays) { Move-Item $newArrays $oldArrays }
    if (Test-Path $newSettings) { Move-Item $newSettings $oldSettings }
    Remove-Item -Recurse -Force $preserve

    Set-Content -Path $marker -Value $tag
}

Write-Host "==> Starting Plumb - http://localhost:8000"
Push-Location $Dest
try {
    & .\plumb.exe
} finally {
    Pop-Location
}
