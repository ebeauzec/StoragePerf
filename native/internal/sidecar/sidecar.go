// Package sidecar launches and supervises the bundled Prometheus and
// VictoriaMetrics binaries as child processes of the main Plumb executable
// — no system service installation, no Docker. If a sidecar dies, it's
// restarted with backoff; its stdout/stderr are captured to a log file
// under data/logs/ so a crash is diagnosable without a terminal attached.
package sidecar

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type Process struct {
	Name    string
	Path    string
	Args    []string
	LogFile string
}

// Run starts the process and keeps it running until ctx is cancelled,
// restarting it with a growing backoff (capped at 30s) if it exits.
func (p Process) Run(ctx context.Context) {
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if _, err := os.Stat(p.Path); err != nil {
			log.Printf("[%s] sidecar binary not found at %s (%v) — this platform's bundle may be incomplete; %s will be unavailable", p.Name, p.Path, err, p.Name)
			return
		}

		if err := p.runOnce(ctx); err != nil {
			log.Printf("[%s] exited: %v — restarting in %s", p.Name, err, backoff)
		} else {
			log.Printf("[%s] exited cleanly — restarting in %s", p.Name, backoff)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

func (p Process) runOnce(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(p.LogFile), 0o755); err != nil {
		return err
	}
	logf, err := os.OpenFile(p.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logf.Close()

	cmd := exec.CommandContext(ctx, p.Path, p.Args...)
	cmd.Stdout = logf
	cmd.Stderr = logf
	fmt.Fprintf(logf, "\n--- starting %s at %s ---\n", p.Name, time.Now().Format(time.RFC3339))

	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Wait()
}
