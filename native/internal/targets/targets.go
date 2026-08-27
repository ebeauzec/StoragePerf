// Package targets writes the Prometheus file_sd JSON that tells the bundled
// Prometheus what to scrape. Three kinds of target exist side by side:
//   - Pure arrays: scraped through Plumb's own /scrape/{id} proxy, which
//     holds the per-array bearer token so Prometheus itself never needs it.
//   - NetApp arrays with credentials configured: scraped directly from the
//     Harvest poller Plumb started for that array, which already exposes a
//     plain local Prometheus endpoint (Harvest did its own authenticated
//     collection upstream).
//   - NetApp arrays with a `host` set instead of credentials: scraped
//     directly at that host, no bundled Harvest involved. This covers
//     pointing at a Harvest/StorageGRID Prometheus endpoint you already run
//     yourself, and it's also what the demo fleet uses to show NetApp data
//     without a real cluster.
package targets

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"plumb/internal/config"
	"plumb/internal/harvest"
)

type target struct {
	Targets []string          `json:"targets"`
	Labels  map[string]string `json:"labels"`
}

// Generate writes one file_sd target per array. selfAddr is this process's
// own host:port (for the Pure scrape-proxy path).
func Generate(path string, arrays []config.Array, pollers []harvest.Poller, selfAddr string) (int, error) {
	portByArray := map[string]int{}
	for _, p := range pollers {
		portByArray[p.ArrayID] = p.Port
	}

	var out []target
	for _, a := range arrays {
		labels := map[string]string{"array": a.ID, "model": a.Model, "vendor": a.Vendor}
		if a.IsNetApp() {
			if port, ok := portByArray[a.ID]; ok {
				out = append(out, target{
					Targets: []string{fmt.Sprintf("127.0.0.1:%d", port)},
					Labels:  labels,
				})
			} else if a.Host != "" {
				out = append(out, target{
					Targets: []string{a.Host},
					Labels:  labels,
				})
			}
			// neither a Harvest poller nor a direct host — nothing to
			// scrape for this array; skip rather than break everyone else
		} else {
			labels["__metrics_path__"] = "/scrape/" + a.ID
			out = append(out, target{
				Targets: []string{selfAddr},
				Labels:  labels,
			})
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, err
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return 0, err
	}
	return len(out), nil
}
