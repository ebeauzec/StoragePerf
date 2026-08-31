# Working on Plumb / PureAnalyzer

## Release policy (standing instruction, no need to ask)

After making a real code/behavior change (not a doc tweak, not a
scratch/debug file), cut a new release as part of finishing the task:

1. Bump `VERSION` at the repo root (semver patch bump for a fix, minor for
   a feature).
2. Commit the change with a real message (see repo convention in
   `git log` — explain the *why*, not the *what*).
3. Build all five platforms: `bash native/scripts/build-native.sh` (needs
   `sidecars/<platform>/` already staged — see `native/scripts/fetch-sidecars.sh`;
   they're gitignored and already present on this machine).
4. `git tag vX.Y.Z`, push the commit and the tag.
5. Create the GitHub release and upload all five `native/dist/*.tar.gz` /
   `*.zip` assets.

This was explicitly requested as default behavior on 2026-08-31 — don't
ask before doing it again for this repo. Do still surface what shipped
(version, what changed, what was verified) in the response.

**No `gh` CLI on this machine.** Authenticate with the token already
cached for git push:
```bash
TOKEN=$(printf 'protocol=https\nhost=github.com\n\n' | git credential fill 2>&1 | grep '^password=' | cut -d= -f2-)
```
Use it as `Authorization: Bearer $TOKEN` against the GitHub REST API
(`POST /repos/ebeauzec/StoragePerf/releases`, then `POST` each asset to
the release's `upload_url`). To replace a bad asset: `DELETE
/repos/.../releases/assets/{id}` then re-upload — GitHub won't let you
overwrite an asset by name.

**Verify the actual release, not just the build.** A local `go build`
proves the code compiles; it does NOT prove the packaged, downloaded
artifact works. After publishing, actually run the real install path
(`run.bat` -> `run.ps1` -> downloads the just-published release ->
starts `plumb.exe`) against a clean `plumb-release/` and confirm the
dashboard responds, before calling the release good. This caught a real
release-breaking bug (below) that a local build never would have.

## Windows-specific gotchas discovered the hard way (2026-08-31)

This project ships from a Mac but is meant to run standalone on Windows,
and this repo commonly lives in a cloud-synced folder (Google Drive /
OneDrive) rather than a local disk. Several failure modes only show up
in that combination:

- **`.ps1`/`.bat` files must be pure ASCII.** Windows PowerShell 5.1
  reads a BOM-less `.ps1` under the legacy system codepage, not UTF-8 --
  any non-ASCII byte (an em dash, a smart quote) can silently corrupt
  the parser with a misleading "Unexpected token" / "Missing closing
  brace" error nowhere near the actual bad character. Same failure mode
  hits `.bat` files if they're LF-only (which every bash heredoc
  produces) *and* contain a non-ASCII byte -- cmd.exe misparses `rem`
  comments in that combination and starts executing comment text as
  commands. Fix: ASCII only, and force `.bat` files to CRLF (`sed -i
  's/$/\r/' file.bat` after a heredoc).
- **`run.ps1` alone doesn't give Windows a double-click entry point.**
  `.ps1` has no default double-click "run" action, and PowerShell's
  default execution policy blocks it anyway. `run.bat` at the repo root
  is the real entry point (`powershell -NoProfile -ExecutionPolicy
  Bypass -File run.ps1`), scoped to that one process only.
- **`Compress-Archive -Path 'dir\*'` != `zip -qr archive.zip dir/`.**
  The PowerShell fallback (used when `zip` isn't on PATH, e.g. Git Bash)
  must point `-Path` at the directory itself, not its contents with a
  glob -- otherwise the archive has no top-level wrapping folder, and
  `run.ps1`'s extraction logic (which assumes exactly one) silently
  grabs the wrong single item and leaves everything else, including
  `plumb.exe`, unextracted. This broke the very first Windows release
  built this way; caught only by actually running the published
  release end-to-end, not by inspecting the build output.
  See `native/scripts/build-native.sh`.
- **Google Drive's virtual filesystem can throw, not just be slow.** A
  `Test-Path` against a path that was just deleted/moved (this script's
  own upgrade flow does exactly that: wipe `$Dest`, then immediately
  recreate and recheck it) can throw `UnauthorizedAccessException`
  ("Access is denied") instead of returning `$false`, for anywhere from
  under a second to several seconds -- contention-driven, not a fixed
  settling time. `run.ps1` retries these (`Test-PathResilient`); it's
  not a 100% guarantee against arbitrarily long contention, just turns
  the common case from a hard crash into a brief pause.
- **This checkout's executable bit is unreliable.** `chmod +x` on a
  `.sh` file sometimes doesn't stick when checked via `git diff` (the
  Drive-synced mount doesn't reliably report `stat()` permissions back to
  git). If a commit shows a spurious `100755 => 100644` mode change on a
  script you know is meant to stay executable, fix it in the index
  directly rather than trusting a plain `chmod`:
  `git update-index --chmod=+x path/to/script.sh`.
- **No Go toolchain on this machine by default.** Install with `winget
  install --id GoLang.Go -e --silent --accept-package-agreements
  --accept-source-agreements`, then add `C:\Program Files\Go\bin` to
  `PATH` for the session before building.
