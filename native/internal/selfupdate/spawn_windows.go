//go:build windows

package selfupdate

import (
	"os"
	"os/exec"
	"syscall"
)

// windows doesn't have Unix's Setsid — CREATE_NEW_PROCESS_GROUP plus
// DETACHED_PROCESS (0x00000008, not exposed as a named syscall constant in
// the standard library) is the equivalent: a console-free process in its
// own group that survives this one exiting.
const detachedProcess = 0x00000008

func spawnDetached(exePath, dir string, logFile *os.File) error {
	cmd := exec.Command(exePath)
	cmd.Dir = dir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess}
	return cmd.Start()
}
