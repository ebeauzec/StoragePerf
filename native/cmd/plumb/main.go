// Plumb — a self-contained storage performance console.
//
// This binary IS the whole application: it embeds the frontend, launches
// the bundled Prometheus and VictoriaMetrics (and, when NetApp arrays are
// configured, Harvest) as child processes, generates their config from
// config/arrays.yml, evaluates config/thresholds/*.yml against what they
// collect, and serves the result. Unzip the distribution for your OS and
// run this executable — nothing else needs to be installed.
package main

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"plumb/internal/api"
	"plumb/internal/config"
	"plumb/internal/harvest"
	"plumb/internal/paths"
	"plumb/internal/sidecar"
	"plumb/internal/updates"
	"plumb/internal/vm"
	"plumb/web"
)

const (
	listenPort = "8000"
	vmPort     = "8428"
	promPort   = "9090"
)

// set via -ldflags "-X main.version=..." at build time; "dev" for `go run`/`go build` with no flags
var version = "dev"

func webFS() fs.FS {
	return web.FS
}

func writePrometheusConfig(path, targetsPath, vmURL string) error {
	doc := map[string]any{
		"global": map[string]any{"scrape_interval": "15s", "evaluation_interval": "15s"},
		"remote_write": []map[string]any{
			{"url": vmURL + "/api/v1/write"},
		},
		"scrape_configs": []map[string]any{
			{
				"job_name": "plumb",
				"file_sd_configs": []map[string]any{
					{"files": []string{targetsPath}, "refresh_interval": "15s"},
				},
			},
		},
	}
	b, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// harvestSupervisor restarts the set of running Harvest poller processes
// whenever the array inventory changes (NetApp arrays added/removed).
type harvestSupervisor struct {
	mu        sync.Mutex
	cancel    context.CancelFunc
	harvestBin string
	harvestCfg string
	logDir    string
}

func (h *harvestSupervisor) apply(pollers []harvest.Poller) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancel != nil {
		h.cancel()
	}
	if len(pollers) == 0 {
		h.cancel = nil
		return
	}
	if _, err := os.Stat(h.harvestBin); err != nil {
		log.Printf("[harvest] %d NetApp array(s) configured but harvest binary not found at %s — NetApp collection unavailable on this platform bundle", len(pollers), h.harvestBin)
		h.cancel = nil
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	for _, p := range pollers {
		proc := sidecar.Process{
			Name:    "harvest:" + p.ArrayID,
			Path:    h.harvestBin,
			Args:    []string{"start", p.ArrayID, "--config", h.harvestCfg, "--foreground"},
			LogFile: filepath.Join(h.logDir, "harvest-"+p.ArrayID+".log"),
		}
		go proc.Run(ctx)
	}
}

func main() {
	layout, err := paths.Resolve()
	if err != nil {
		log.Fatalf("resolving paths: %v", err)
	}
	if err := layout.EnsureDataDirs(); err != nil {
		log.Fatalf("creating data directories: %v", err)
	}
	if _, err := os.Stat(filepath.Join(layout.Config, "arrays.yml")); os.IsNotExist(err) {
		example := filepath.Join(layout.Config, "arrays.example.yml")
		if b, err := os.ReadFile(example); err == nil {
			os.WriteFile(filepath.Join(layout.Config, "arrays.yml"), b, 0o644)
			log.Printf("no config/arrays.yml found — started from arrays.example.yml")
		}
	}

	targetsPath := filepath.Join(layout.Data, "generated", "targets.json")
	harvestCfgPath := filepath.Join(layout.Data, "generated", "harvest.yml")
	promCfgPath := filepath.Join(layout.Data, "generated", "prometheus.yml")
	vmURL := "http://127.0.0.1:" + vmPort

	if err := writePrometheusConfig(promCfgPath, targetsPath, vmURL); err != nil {
		log.Fatalf("writing prometheus config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// --- sidecars ---
	vmProc := sidecar.Process{
		Name: "victoriametrics",
		Path: layout.VictoriaMetrics,
		Args: []string{
			"--storageDataPath=" + filepath.Join(layout.Data, "victoriametrics"),
			"--retentionPeriod=100y",
			"--httpListenAddr=127.0.0.1:" + vmPort,
		},
		LogFile: filepath.Join(layout.Data, "logs", "victoriametrics.log"),
	}
	go vmProc.Run(ctx)

	promProc := sidecar.Process{
		Name: "prometheus",
		Path: layout.Prometheus,
		Args: []string{
			"--config.file=" + promCfgPath,
			"--storage.tsdb.path=" + filepath.Join(layout.Data, "prometheus"),
			"--storage.tsdb.retention.time=2d",
			"--web.listen-address=127.0.0.1:" + promPort,
		},
		LogFile: filepath.Join(layout.Data, "logs", "prometheus.log"),
	}
	go promProc.Run(ctx)

	hs := &harvestSupervisor{harvestBin: layout.Harvest, harvestCfg: harvestCfgPath, logDir: filepath.Join(layout.Data, "logs")}

	// --- initial config-derived generation (targets, harvest.yml, pollers) ---
	updateChecker := updates.NewChecker(os.Getenv("PLUMB_CHECK_FOR_UPDATES") != "false")

	app := &api.App{
		Version:                  version,
		ConfigDir:                layout.Config,
		TargetsPath:              targetsPath,
		HarvestPath:              harvestCfgPath,
		SelfAddr:                 "127.0.0.1:" + listenPort,
		VM:                       vm.New(vmURL),
		Updates:                  updateChecker,
		Frontend:                 webFS(),
		RegenerateHarvestPollers: hs.apply,
	}
	if err := app.LoadSettings(); err != nil {
		log.Fatalf("loading settings: %v", err)
	}
	if err := app.InitialRegenerate(); err != nil {
		log.Fatalf("generating initial scrape config: %v", err)
	}

	stopUpdates := make(chan struct{})
	go updateChecker.Run(24*time.Hour, stopUpdates)
	defer close(stopUpdates)

	srv := &http.Server{Addr: "0.0.0.0:" + listenPort, Handler: app.Routes()}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	arrays, _ := config.LoadArrays(layout.Config)
	log.Printf("Plumb %s starting — %d array(s) configured, data dir: %s", version, len(arrays), layout.Data)
	log.Printf("open http://localhost:%s", listenPort)

	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
	fmt.Println("Plumb stopped.")
}
