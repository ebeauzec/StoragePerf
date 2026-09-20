package main

import (
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// browserCommand decides how to open url on goos, or reports ok=false when
// it shouldn't try at all. Pure function (env is passed in, not read) so
// every branch is unit-testable without launching anything.
//
// Skipped when:
//   - PLUMB_NO_BROWSER is set (anything but empty/0/false). The self-update
//     handoff sets this on the process it spawns: the already-open page
//     reloads itself once the new version answers (see app.js's
//     waitForRestart), so a second tab would just be a duplicate.
//   - Linux with no DISPLAY/WAYLAND_DISPLAY — a headless server, where
//     xdg-open would only fail noisily.
func browserCommand(goos, url string, env func(string) string) (name string, args []string, ok bool) {
	switch strings.ToLower(env("PLUMB_NO_BROWSER")) {
	case "", "0", "false":
	default:
		return "", nil, false
	}
	switch goos {
	case "windows":
		// rundll32 rather than `cmd /c start`: no shell, so no quoting
		// pitfalls, and no console window flashing up.
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}, true
	case "darwin":
		return "open", []string{url}, true
	default:
		if env("DISPLAY") == "" && env("WAYLAND_DISPLAY") == "" {
			return "", nil, false
		}
		return "xdg-open", []string{url}, true
	}
}

// openBrowser opens url in the user's default browser, best effort: a
// missing browser, a locked-down machine, or an SSH session must never
// stop Plumb from serving, so failure is logged and swallowed. Start, not
// Run — the opener is not waited on.
func openBrowser(url string) {
	name, args, ok := browserCommand(runtime.GOOS, url, os.Getenv)
	if !ok {
		log.Printf("not opening a browser automatically (PLUMB_NO_BROWSER is set, or no display was detected) — open %s yourself", url)
		return
	}
	log.Printf("opening %s in your default browser (set PLUMB_NO_BROWSER=1 to turn this off)", url)
	if err := exec.Command(name, args...).Start(); err != nil {
		log.Printf("couldn't open a browser automatically (%v) — open %s yourself", err, url)
	}
}
