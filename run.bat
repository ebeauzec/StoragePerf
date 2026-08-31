@echo off
rem Windows double-click entry point for the zero-install launcher --
rem mirrors what run.sh gives macOS/Linux users directly (a script you can
rem just run). run.ps1 alone doesn't serve that purpose on Windows: .ps1
rem files have no default double-click "run" action (Explorer opens them
rem in a text editor), and even invoked deliberately, PowerShell's default
rem execution policy blocks an unsigned local script on plenty of
rem out-of-the-box machines. This .bat sidesteps both by calling
rem PowerShell with an explicit -ExecutionPolicy Bypass scoped to just
rem this one process -- it changes nothing system-wide.
rem
rem Batch files must be saved with CRLF line endings, not LF: cmd.exe's
rem "rem" comment parser can misparse a bare-LF file the moment a comment
rem contains any non-ASCII byte, silently corrupting everything after it.
setlocal
cd /d "%~dp0"
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0run.ps1"
set PLUMB_EXIT=%ERRORLEVEL%
if not "%PLUMB_EXIT%"=="0" (
    echo.
    echo Plumb exited with an error ^(code %PLUMB_EXIT%^) -- see the output above.
    pause
)
endlocal
