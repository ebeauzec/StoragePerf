//go:build !windows

package selfupdate

import (
	"os"
	"os/exec"
	"syscall"
)

// spawnDetached starts exePath as an independent process in its own
// session (Setsid) so it survives this process exiting — the whole point
// of a handoff. It will race this process for port 8000 and lose at
// first (see main.go's bounded listen-retry), until the caller shuts this
// process down.
func spawnDetached(exePath, dir string, logFile *os.File) error {
	cmd := exec.Command(exePath)
	cmd.Dir = dir
	// The page that triggered the update reloads itself once this process answers
	// (app.js waitForRestart) — opening another tab here would just duplicate it.
	cmd.Env = append(os.Environ(), "PLUMB_NO_BROWSER=1")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}
