// Plumb — a self-contained storage performance console.
//
// This binary IS the whole application: it embeds the frontend, launches
// the bundled Prometheus and VictoriaMetrics as child processes, generates
// their config from config/arrays.yml, evaluates config/thresholds/*.yml
// against what they collect, and serves the result. Unzip the distribution
// for your OS and run this executable — nothing else needs to be
// installed. NetApp ONTAP/StorageGRID collection is done in-process (see
// internal/netappnative) rather than via a separate collector — earlier
// versions bundled NetApp's own Harvest as a sidecar, but Harvest only
// ships linux/amd64 binaries, which meant no NetApp support at all on
// Windows or macOS.
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
	"plumb/internal/findingstore"
	"plumb/internal/mockbackend"
	"plumb/internal/netappnative"
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
				// Explicit, not left to Prometheus's own default (10s):
				// Pure arrays are scraped through our own proxy
				// (internal/scrapeproxy), whose HTTP client also carries a
				// 10s timeout for the real request to the array. Leaving
				// scrape_timeout at its implicit 10s default means both
				// timeouts race each other on a slow-to-respond real
				// array; 15s (the max Prometheus allows — it can't exceed
				// scrape_interval) gives the proxy's own timeout room to
				// fire first and return a clean error instead.
				"scrape_timeout": "15s",
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

// vmSupervisor (re)starts VictoriaMetrics with a given -retentionPeriod.
// VictoriaMetrics only reads that flag at startup, so changing it at
// runtime (via the Config tab) means restarting the process — the previous
// instance is torn down first and a fresh one launched under a new
// cancelable sub-context, mirroring harvestSupervisor below. Restarting
// with a shorter retentionPeriod doesn't just stop accepting old data going
// forward: VictoriaMetrics's own background retention enforcement then
// purges everything already on disk that falls outside the new window.
type vmSupervisor struct {
	mu         sync.Mutex
	cancel     context.CancelFunc
	parentCtx  context.Context
	path       string
	dataPath   string
	listenAddr string
	logFile    string
}

func (v *vmSupervisor) start(retentionPeriod string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.cancel != nil {
		v.cancel()
	}
	ctx, cancel := context.WithCancel(v.parentCtx)
	v.cancel = cancel
	proc := sidecar.Process{
		Name: "victoriametrics",
		Path: v.path,
		Args: []string{
			"--storageDataPath=" + v.dataPath,
			"--retentionPeriod=" + retentionPeriod,
			"--httpListenAddr=" + v.listenAddr,
		},
		LogFile: v.logFile,
	}
	log.Printf("[victoriametrics] starting with retention period %s", retentionPeriod)
	go proc.Run(ctx)
}

// listenWithRetry binds addr, retrying with backoff for up to maxWait
// before giving up. Normally binds on the first attempt — this exists for
// the self-update handoff (internal/selfupdate), where a freshly spawned
// new-version process starts and races the old one for this exact port:
// the old process is still gracefully shutting down and hasn't released
// it yet. A genuinely misconfigured deployment (something else already
// squatting on the port) just takes up to maxWait longer to report that
// fatal error — an acceptable tradeoff for making the handoff work
// without the two processes needing to coordinate directly.
func listenWithRetry(addr string, maxWait time.Duration) (net.Listener, error) {
	deadline := time.Now().Add(maxWait)
	backoff := 200 * time.Millisecond
	for {
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			return ln, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(backoff)
		if backoff *= 2; backoff > 2*time.Second {
			backoff = 2 * time.Second
		}
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
	promCfgPath := filepath.Join(layout.Data, "generated", "prometheus.yml")
	vmURL := "http://127.0.0.1:" + vmPort

	if err := writePrometheusConfig(promCfgPath, targetsPath, vmURL); err != nil {
		log.Fatalf("writing prometheus config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// settings (retention period, mock-data toggle) are needed before the
	// VictoriaMetrics sidecar starts, since -retentionPeriod is a startup
	// flag — app.LoadSettings() re-reads the same file into the running
	// App below, so this is just to get the initial value early
	initialSettings, err := config.LoadSettings(layout.Config)
	if err != nil {
		log.Fatalf("loading settings: %v", err)
	}

	// --- sidecars ---
	vmSup := &vmSupervisor{
		parentCtx:  ctx,
		path:       layout.VictoriaMetrics,
		dataPath:   filepath.Join(layout.Data, "victoriametrics"),
		listenAddr: "127.0.0.1:" + vmPort,
		logFile:    filepath.Join(layout.Data, "logs", "victoriametrics.log"),
	}
	vmSup.start(initialSettings.EffectiveRetentionPeriod())

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

	// --- initial config-derived generation (Prometheus scrape targets) ---
	updateChecker := updates.NewChecker(os.Getenv("PLUMB_CHECK_FOR_UPDATES") != "false", version)

	findings, err := findingstore.Open(layout.Data)
	if err != nil {
		log.Fatalf("loading findings store: %v", err)
	}

	app := &api.App{
		Version:     version,
		Root:        layout.Root,
		ConfigDir:   layout.Config,
		DataDir:     layout.Data,
		TargetsPath: targetsPath,
		SelfAddr:    "127.0.0.1:" + listenPort,
		VM:          vm.New(vmURL),
		Updates:     updateChecker,
		Frontend:    webFS(),
		RestartVM:   vmSup.start,
		Shutdown:    stop,
		ONTAP:       netappnative.NewONTAPCollector(),
		StorageGrid: netappnative.NewStorageGridCollector(),
		MockBackend: mockbackend.New(),
		Findings:    findings,
	}
	if err := app.LoadSettings(); err != nil {
		log.Fatalf("loading settings: %v", err)
	}
	// mock mode may have been left on from a previous run — bring its mock
	// systems up before the first scrape cycle, same as any other startup
	// dependency, so InitialRegenerate below sees them.
	if err := app.EnsureMockBackend(); err != nil {
		log.Fatalf("starting mock backend: %v", err)
	}
	if err := app.InitialRegenerate(); err != nil {
		log.Fatalf("generating initial scrape config: %v", err)
	}

	stopUpdates := make(chan struct{})
	go updateChecker.Run(24*time.Hour, stopUpdates)
	defer close(stopUpdates)

	// 5 minutes: frequent enough that a webhook fires soon after something
	// actually goes critical, infrequent enough not to hammer VictoriaMetrics
	// re-evaluating every array's every metric on top of the dashboard's own
	// on-demand queries.
	stopMonitor := make(chan struct{})
	go app.RunMonitor(5*time.Minute, stopMonitor)
	defer close(stopMonitor)

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

	ln, err := listenWithRetry(srv.Addr, 20*time.Second)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
	fmt.Println("Plumb stopped.")
}
