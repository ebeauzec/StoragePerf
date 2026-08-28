// Package mockbackend turns the Config tab's "Show mock data" toggle into
// real systems collected through Plumb's real pipeline, instead of
// synthetic panels computed in-process. Pure mock arrays are served as
// OpenMetrics text through Plumb's own HTTP server (see pure.go); each
// NetApp mock array gets its own dedicated HTTPS listener on its own port,
// since ONTAP's REST API and StorageGRID's Grid Management API always
// live at fixed absolute paths with no array-ID segment to disambiguate
// them the way Pure's /scrape/{id} proxy can — exactly how a real cluster
// or grid works (one system, one management address).
//
// The result: turning mock mode on doesn't fabricate a dashboard directly
// — it stands up fake systems and lets the exact same scrape → Prometheus
// → VictoriaMetrics → rules-evaluation pipeline real arrays go through
// collect from them. Turning it off tears the listeners down and Plumb
// reverts to whatever's in config/arrays.yml.
package mockbackend

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"plumb/internal/config"
	"plumb/internal/mockdata"
)

const basePort = 19601 // arbitrary, high, unlikely to collide with anything real

// Backend owns the dedicated NetApp mock listeners' lifecycle. Pure mock
// routes are registered separately (RegisterPureRoutes) on Plumb's own
// mux, once, at startup — they're inert (never scraped) unless targets.go
// actually points at them, so there's nothing to start/stop for Pure.
type Backend struct {
	mu        sync.Mutex
	cancel    context.CancelFunc
	listeners map[string]string // array ID -> "127.0.0.1:port", while running
}

func New() *Backend {
	return &Backend{}
}

// Running reports whether the NetApp mock listeners are currently up.
func (b *Backend) Running() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cancel != nil
}

// Start brings up one dedicated HTTPS listener per NetApp array in the
// mock fleet. Idempotent — calling it while already running is a no-op.
func (b *Backend) Start() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cancel != nil {
		return nil
	}

	cert, err := selfSignedCert()
	if err != nil {
		return fmt.Errorf("generating mock backend certificate: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	listeners := map[string]string{}
	port := basePort
	for _, arr := range mockdata.Fleet {
		if !arr.IsNetApp() {
			continue
		}
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		var mux *http.ServeMux
		switch arr.Vendor {
		case config.VendorNetAppONTAP:
			mux = ontapMux(arr)
		case config.VendorNetAppStorageGRID:
			mux = storagegridMux(arr)
		default:
			continue
		}

		ln, err := net.Listen("tcp", addr)
		if err != nil {
			cancel()
			return fmt.Errorf("starting mock backend for %s on %s: %w", arr.ID, addr, err)
		}
		tlsLn := tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}})
		srv := &http.Server{Handler: mux}
		go func(arrID string) {
			if err := srv.Serve(tlsLn); err != nil && err != http.ErrServerClosed {
				log.Printf("[mockbackend] %s listener stopped: %v", arrID, err)
			}
		}(arr.ID)
		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			srv.Shutdown(shutdownCtx)
		}()

		listeners[arr.ID] = addr
		port++
	}

	b.cancel = cancel
	b.listeners = listeners
	log.Printf("[mockbackend] started %d NetApp mock listener(s)", len(listeners))
	return nil
}

// Stop tears down every NetApp mock listener. Idempotent.
func (b *Backend) Stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cancel == nil {
		return
	}
	b.cancel()
	b.cancel = nil
	b.listeners = nil
	log.Printf("[mockbackend] stopped")
}

// Arrays returns the full mock fleet as real config.Array entries: Pure
// systems pointed at this process's own /mockbackend/pure/{id} route
// (selfAddr), NetApp systems pointed at their own dedicated mock listener.
// Only meaningful while Start has been called — NetApp entries are omitted
// if their listener isn't up (nothing would answer them anyway).
func (b *Backend) Arrays(selfAddr string) []config.Array {
	b.mu.Lock()
	listeners := b.listeners
	b.mu.Unlock()

	out := make([]config.Array, 0, len(mockdata.Fleet))
	for _, arr := range mockdata.Fleet {
		if arr.IsNetApp() {
			addr, ok := listeners[arr.ID]
			if !ok {
				continue
			}
			c := arr.AsConfigArray()
			c.ManagementLIF = addr
			c.Username = "mock"
			c.UseInsecureTLS = true
			out = append(out, c)
			continue
		}
		out = append(out, PureArrayConfig(selfAddr, arr))
	}
	return out
}
