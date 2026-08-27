// Package paths resolves every directory Plumb needs relative to the
// running executable, so the whole distribution is relocatable: unzip it
// anywhere, on any OS, and it finds its own config/data/sidecars next to
// itself with no installation step and no hardcoded paths.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
)

type Layout struct {
	Root      string // directory containing the running executable
	Config    string // config/
	Data      string // data/ (VictoriaMetrics storage, Prometheus TSDB, Harvest state, reports)
	Sidecars  string // sidecars/<os>_<arch>/
	Prometheus string
	VictoriaMetrics string
	Harvest   string
}

func exeName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

// Resolve builds the Layout relative to the running executable's directory,
// unless PLUMB_ROOT overrides it (useful for development, running `go run`
// from the source tree instead of a packaged distribution).
//
// Each distributed archive (see scripts/build-native.sh) is already built
// for one specific OS/arch, so its sidecars/ directory is flat — no need
// for a platform subdirectory inside a package that only ever contains one
// platform's binaries. PLUMB_SIDECARS_DIR overrides this for development,
// where sidecars/<os>_<arch>/ (as fetch-sidecars.sh lays them out) is more
// convenient than copying one platform's binaries out to a flat directory.
func Resolve() (Layout, error) {
	root := os.Getenv("PLUMB_ROOT")
	if root == "" {
		exe, err := os.Executable()
		if err != nil {
			return Layout{}, err
		}
		resolved, err := filepath.EvalSymlinks(exe)
		if err != nil {
			resolved = exe
		}
		root = filepath.Dir(resolved)
	}

	sidecars := os.Getenv("PLUMB_SIDECARS_DIR")
	if sidecars == "" {
		sidecars = filepath.Join(root, "sidecars")
	}

	return Layout{
		Root:            root,
		Config:          filepath.Join(root, "config"),
		Data:            filepath.Join(root, "data"),
		Sidecars:        sidecars,
		Prometheus:      filepath.Join(sidecars, exeName("prometheus")),
		VictoriaMetrics: filepath.Join(sidecars, exeName("victoria-metrics")),
		Harvest:         filepath.Join(sidecars, exeName("harvest")),
	}, nil
}

func (l Layout) EnsureDataDirs() error {
	for _, d := range []string{
		filepath.Join(l.Data, "victoriametrics"),
		filepath.Join(l.Data, "prometheus"),
		filepath.Join(l.Data, "harvest"),
		filepath.Join(l.Data, "logs"),
		filepath.Join(l.Data, "reports"),
		filepath.Join(l.Data, "generated"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}
