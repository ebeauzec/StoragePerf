// Package harvest generates the config file for NetApp's own official
// collector (github.com/NetApp/harvest), which Plumb bundles and runs as a
// sidecar for every netapp_ontap / netapp_storagegrid array. Harvest does
// its own authenticated collection against the cluster/grid and exposes a
// plain, unauthenticated Prometheus endpoint on localhost per poller —
// Plumb's own scrape-proxy (built for Pure, where the array itself must be
// authenticated to) isn't needed on this path at all.
package harvest

import (
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"

	"plumb/internal/config"
)

const BasePort = 12990

// Poller is one NetApp target's assigned local port and collector type,
// returned so the Prometheus file_sd generator knows where to scrape it.
type Poller struct {
	ArrayID string
	Port    int
}

type pollerYAML struct {
	Datacenter     string   `yaml:"datacenter,omitempty"`
	Addr           string   `yaml:"addr"`
	AuthStyle      string   `yaml:"auth_style,omitempty"`
	Username       string   `yaml:"username,omitempty"`
	Password       string   `yaml:"password,omitempty"`
	UseInsecureTLS bool     `yaml:"use_insecure_tls,omitempty"`
	Collectors     []string `yaml:"collectors"`
	Exporters      []string `yaml:"exporters"`
}

type exporterYAML struct {
	Exporter     string `yaml:"exporter"`
	LocalHTTPAddr string `yaml:"local_http_addr"`
	Port         int    `yaml:"port"`
}

type harvestYAML struct {
	Pollers   map[string]pollerYAML   `yaml:"Pollers"`
	Exporters map[string]exporterYAML `yaml:"Exporters"`
}

// Generate writes harvest.yml for every NetApp array in `arrays`, assigning
// each a stable, deterministic local port (sorted by array ID so ports
// don't shuffle around on every regeneration). Returns the assignments so
// the Prometheus target generator can point at them.
func Generate(path string, arrays []config.Array) ([]Poller, error) {
	var netapp []config.Array
	for _, a := range arrays {
		// Only arrays with real credentials get a bundled Harvest poller.
		// A NetApp array with just `host` set (no management_lif) is meant
		// to be scraped directly instead — see internal/targets — so it's
		// deliberately excluded here.
		if a.IsNetApp() && a.ManagementLIF != "" {
			netapp = append(netapp, a)
		}
	}
	sort.Slice(netapp, func(i, j int) bool { return netapp[i].ID < netapp[j].ID })

	doc := harvestYAML{
		Pollers:   map[string]pollerYAML{},
		Exporters: map[string]exporterYAML{},
	}
	var pollers []Poller

	for i, a := range netapp {
		port := BasePort + i
		exporterName := fmt.Sprintf("prom_%s", a.ID)

		collectors := []string{"Rest", "RestPerf"}
		if a.Vendor == config.VendorNetAppStorageGRID {
			collectors = []string{"StorageGrid"}
		}

		password := ""
		if a.PasswordEnv != "" {
			password = os.Getenv(a.PasswordEnv)
		}

		doc.Pollers[a.ID] = pollerYAML{
			Datacenter:     a.Datacenter,
			Addr:           a.ManagementLIF,
			AuthStyle:      "basic_auth",
			Username:       a.Username,
			Password:       password,
			UseInsecureTLS: a.UseInsecureTLS,
			Collectors:     collectors,
			Exporters:      []string{exporterName},
		}
		doc.Exporters[exporterName] = exporterYAML{
			Exporter:      "Prometheus",
			LocalHTTPAddr: "127.0.0.1",
			Port:          port,
		}
		pollers = append(pollers, Poller{ArrayID: a.ID, Port: port})
	}

	b, err := yaml.Marshal(doc)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return nil, err
	}
	return pollers, nil
}

// PollerNames returns every configured poller name, used as the -poller
// arguments Harvest needs on its command line.
func PollerNames(pollers []Poller) []string {
	names := make([]string, len(pollers))
	for i, p := range pollers {
		names[i] = p.ArrayID
	}
	return names
}
