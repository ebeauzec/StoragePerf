// Package targets writes the Prometheus file_sd JSON that tells the bundled
// Prometheus what to scrape. Two kinds of target exist side by side:
//   - Pure arrays: scraped through Plumb's own /scrape/{id} proxy, which
//     holds the per-array bearer token so Prometheus itself never needs it.
//   - NetApp arrays with credentials configured (ManagementLIF set):
//     scraped through Plumb's own /scrape/netapp/{id} proxy, which collects
//     directly from the cluster's REST API or grid's Management API itself
//     — see internal/netappnative. NetApp arrays with a `host` set instead
//     of credentials are scraped directly at that host: this covers
//     pointing at a Prometheus endpoint you already run yourself (e.g. an
//     existing Harvest deployment, or StorageGRID's own embedded
//     Prometheus federated some other way), and it's also what the demo
//     fleet uses to show NetApp data without a real cluster.
package targets

import (
	"encoding/json"
	"os"
	"path/filepath"

	"plumb/internal/config"
)

type target struct {
	Targets []string          `json:"targets"`
	Labels  map[string]string `json:"labels"`
}

// Generate writes one file_sd target per array. selfAddr is this process's
// own host:port (for the Pure and NetApp scrape-proxy paths).
func Generate(path string, arrays []config.Array, selfAddr string) (int, error) {
	var out []target
	for _, a := range arrays {
		labels := map[string]string{"array": a.ID, "model": a.Model, "vendor": a.Vendor}
		if a.IsNetApp() {
			if a.ManagementLIF != "" {
				labels["__metrics_path__"] = "/scrape/netapp/" + a.ID
				out = append(out, target{Targets: []string{selfAddr}, Labels: labels})
			} else if a.Host != "" {
				out = append(out, target{Targets: []string{a.Host}, Labels: labels})
			}
			// neither credentials nor a direct host — nothing to scrape
			// for this array; skip rather than break everyone else
		} else {
			labels["__metrics_path__"] = "/scrape/" + a.ID
			out = append(out, target{Targets: []string{selfAddr}, Labels: labels})
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
