package switchnative

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"plumb/internal/config"
)

// collectNexusPortStats queries Cisco NX-API — a JSON-RPC-shaped POST to
// https://<switch>/ins, method "cli_show", one "show interface {port}" per
// requested port (NX-API doesn't take a port list in one call the way
// "show interface counters" would, but per-port keeps this collector from
// ever pulling counters for ports nobody asked about).
//
// Field names below (eth_load_interval1_rx/_tx as an already-computed load
// percentage, eth_inerr/eth_outerr as cumulative error counts) are drawn
// from Cisco's own published NX-API CLI reference for "show interface" —
// not confirmed against a live switch. If a real Nexus's response doesn't
// populate these under these exact keys, collectNexusPortStats degrades to
// reporting that port's utilization as unavailable rather than guessing;
// see the parsing loop below.
func collectNexusPortStats(sw config.Switch, ports []string) ([]PortStats, error) {
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: sw.UseInsecureTLS}, DisableKeepAlives: true},
	}

	out := make([]PortStats, 0, len(ports))
	for _, port := range ports {
		body, err := nxAPIRequest(client, sw, "show interface "+port)
		if err != nil {
			return out, fmt.Errorf("port %s: %w", port, err)
		}
		out = append(out, parseNexusInterface(port, body))
	}
	return out, nil
}

type nxAPIResponse struct {
	InsAPI struct {
		Outputs struct {
			Output struct {
				Body json.RawMessage `json:"body"`
				Code string          `json:"code"`
				Msg  string          `json:"msg"`
			} `json:"output"`
		} `json:"outputs"`
	} `json:"ins_api"`
}

func nxAPIRequest(client *http.Client, sw config.Switch, cliCommand string) (json.RawMessage, error) {
	reqBody := map[string]any{
		"ins_api": map[string]any{
			"version":       "1.0",
			"type":          "cli_show",
			"chunk":         "0",
			"sid":           "1",
			"input":         cliCommand,
			"output_format": "json",
		},
	}
	b, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", "https://"+sw.ManagementAddress+"/ins", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	password := ""
	if sw.PasswordEnv != "" {
		password = os.Getenv(sw.PasswordEnv)
	}
	req.SetBasicAuth(sw.Username, password)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: %s", resp.Status, string(respBody))
	}
	var parsed nxAPIResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("parsing NX-API response: %w", err)
	}
	if parsed.InsAPI.Outputs.Output.Code != "" && parsed.InsAPI.Outputs.Output.Code != "200" {
		return nil, fmt.Errorf("NX-API command %q failed: %s", cliCommand, parsed.InsAPI.Outputs.Output.Msg)
	}
	return parsed.InsAPI.Outputs.Output.Body, nil
}

func parseNexusInterface(port string, body json.RawMessage) PortStats {
	var iface struct {
		TableInterface struct {
			RowInterface []struct {
				LoadInterval1Rx *float64 `json:"eth_load_interval1_rx"`
				LoadInterval1Tx *float64 `json:"eth_load_interval1_tx"`
				InErrors        float64  `json:"eth_inerr"`
				OutErrors       float64  `json:"eth_outerr"`
			} `json:"ROW_interface"`
		} `json:"TABLE_interface"`
	}
	stats := PortStats{Port: port}
	if err := json.Unmarshal(body, &iface); err != nil || len(iface.TableInterface.RowInterface) == 0 {
		return stats // utilization stays nil, errors stay 0 — reported as unavailable by the caller, not guessed
	}
	row := iface.TableInterface.RowInterface[0]
	stats.ErrorsCumulative = row.InErrors + row.OutErrors
	if row.LoadInterval1Rx != nil && row.LoadInterval1Tx != nil {
		worst := *row.LoadInterval1Rx
		if *row.LoadInterval1Tx > worst {
			worst = *row.LoadInterval1Tx
		}
		stats.UtilizationPercent = &worst
	}
	return stats
}
