//go:build windows

// Windows process-tree handling. Go's os.Process has no cross-process
// signal support on Windows (SIGTERM isn't implemented), so instead of
// fighting that, we lean on `taskkill /T` — a standard Windows mechanism
// for killing a whole process tree in one call, which takes the sidecar
// children (Prometheus, VictoriaMetrics) down along with the top-level
// binary the same way the unix process-group kill does.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func setStartAttrs(cmd *exec.Cmd) {
	// nothing to set — taskkill /T walks the tree without needing the
	// child to have been started in any special group
}

func stopPrevious(runDir string) {
	pidPath := filepath.Join(runDir, "dev.pid")
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return
	}
	pid := strings.TrimSpace(string(data))
	if pid == "" {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", pid).Run()
}
