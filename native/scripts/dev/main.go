// Command dev is the local development launcher: it always builds Plumb
// fresh from whatever's currently in native/, always runs it from one fixed
// location (native/run/), and always replaces whatever was previously
// running there — including its sidecar processes, not just the top-level
// binary. There is nothing to extract, no dated or versioned directory to
// remember, and no way to end up running stale code: run this and you are,
// by construction, running exactly what's in the source tree right now.
//
// Usage (from anywhere — Go resolves the path):
//
//	go run ./scripts/dev
//
// This is a development convenience, not the end-user install path. A real
// deployment should still use a specific tagged release from
// native/scripts/build-native.sh / GitHub Releases — see native/README.md.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

func main() {
	nativeDir, err := findNativeDir()
	if err != nil {
		fatal(err)
	}

	runDir := filepath.Join(nativeDir, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		fatal(err)
	}

	fmt.Println("==> Stopping any previous dev instance")
	stopPrevious(runDir)

	version := strings.TrimSpace(readFileOrDefault(filepath.Join(nativeDir, "..", "VERSION"), "0.0.0")) + "-dev"

	exeName := "plumb"
	if runtime.GOOS == "windows" {
		exeName = "plumb.exe"
	}
	binPath := filepath.Join(runDir, exeName)

	fmt.Printf("==> Building plumb %s for %s/%s\n", version, runtime.GOOS, runtime.GOARCH)
	build := exec.Command("go", "build", "-ldflags", "-X main.version="+version, "-o", binPath, "./cmd/plumb")
	build.Dir = nativeDir
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fatal(fmt.Errorf("build failed: %w", err))
	}

	platform := runtime.GOOS + "_" + runtime.GOARCH
	sidecarSrc := filepath.Join(nativeDir, "sidecars", platform)
	sidecarDst := filepath.Join(runDir, "sidecars")
	if _, err := os.Stat(sidecarSrc); err == nil {
		os.RemoveAll(sidecarDst)
		if err := copyDir(sidecarSrc, sidecarDst); err != nil {
			fatal(fmt.Errorf("copying sidecars: %w", err))
		}
	} else {
		fmt.Printf("    (no sidecars staged for %s — run scripts/fetch-sidecars.sh first if you need Prometheus/VictoriaMetrics)\n", platform)
	}

	configDst := filepath.Join(runDir, "config")
	if _, err := os.Stat(configDst); os.IsNotExist(err) {
		if err := copyDir(filepath.Join(nativeDir, "config"), configDst); err != nil {
			fatal(fmt.Errorf("seeding config: %w", err))
		}
		os.Remove(filepath.Join(configDst, "arrays.yml")) // let the binary bootstrap it from arrays.example.yml, same as a real install
		os.Remove(filepath.Join(configDst, "settings.yml"))
	}

	fmt.Println("==> Starting plumb")
	cmd := exec.Command(binPath)
	cmd.Dir = runDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	setStartAttrs(cmd) // platform-specific: unix puts it in its own process group so we can stop the whole tree next time
	if err := cmd.Start(); err != nil {
		fatal(fmt.Errorf("starting plumb: %w", err))
	}

	pidPath := filepath.Join(runDir, "dev.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write %s: %v\n", pidPath, err)
	}

	fmt.Printf("\nRunning: http://localhost:8000  (pid %d, logs above)\nRun this again anytime to rebuild + restart with the latest source.\n\n", cmd.Process.Pid)
	cmd.Wait()
}

func findNativeDir() (string, error) {
	// works whether invoked as `go run ./scripts/dev` from native/, or from
	// the repo root, or from anywhere else — walk up from the executable's
	// source location instead of assuming a caller-relative cwd.
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("couldn't find native/go.mod above %s — run this from inside the native/ directory (go run ./scripts/dev)", wd)
}

func readFileOrDefault(path, def string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return def
	}
	return string(b)
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
