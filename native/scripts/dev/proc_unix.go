//go:build !windows

// Unix (macOS/Linux) process-tree handling. Plumb is started in its own
// process group (Setpgid) specifically so we can stop the *group* next
// time — that's what takes the sidecar children (Prometheus,
// VictoriaMetrics) down along with the top-level binary, instead of
// orphaning them to keep holding their ports.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func setStartAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func stopPrevious(runDir string) {
	pidPath := filepath.Join(runDir, "dev.pid")
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return
	}
	// negative PID = signal the whole process group
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	time.Sleep(1500 * time.Millisecond)
	_ = syscall.Kill(-pid, syscall.SIGKILL) // in case anything ignored SIGTERM
}
