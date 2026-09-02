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

// collectAristaPortStats queries Arista eAPI — a JSON-RPC POST to
// https://<switch>/command-api, method "runCmds", "show interfaces" in one
// call covering every interface at once (eAPI's JSON output schema is
// publicly documented by Arista and shared identically across EOS
// versions, unlike NX-API's per-command body shape, so this can fetch
// everything and just pick out the ports actually asked for — one request
// per switch instead of one per port).
//
// Field names (interfaceStatistics.inBitsRate/outBitsRate, bandwidth,
// interfaceCounters.inErrors/outErrors) are drawn from Arista's own
// published eAPI command reference for "show interfaces" — not confirmed
// against a live switch. A port whose response is missing bandwidth (0, or
// absent) reports utilization as unavailable rather than dividing by zero.
func collectAristaPortStats(sw config.Switch, ports []string) ([]PortStats, error) {
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: sw.UseInsecureTLS}, DisableKeepAlives: true},
	}

	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"method":  "runCmds",
		"params": map[string]any{
			"version": 1,
			"cmds":    []string{"show interfaces"},
			"format":  "json",
		},
		"id": "plumb",
	}
	b, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", "https://"+sw.ManagementAddress+"/command-api", bytes.NewReader(b))
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

	var parsed struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Result []struct {
			Interfaces map[string]struct {
				Bandwidth           float64 `json:"bandwidth"`
				InterfaceStatistics struct {
					InBitsRate  float64 `json:"inBitsRate"`
					OutBitsRate float64 `json:"outBitsRate"`
				} `json:"interfaceStatistics"`
				InterfaceCounters struct {
					InErrors  float64 `json:"inErrors"`
					OutErrors float64 `json:"outErrors"`
				} `json:"interfaceCounters"`
			} `json:"interfaces"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("parsing eAPI response: %w", err)
	}
	if parsed.Error != nil {
		return nil, fmt.Errorf("eAPI error: %s", parsed.Error.Message)
	}
	if len(parsed.Result) == 0 {
		return nil, fmt.Errorf("eAPI returned no results for 'show interfaces'")
	}

	byPort := parsed.Result[0].Interfaces
	out := make([]PortStats, 0, len(ports))
	for _, port := range ports {
		stats := PortStats{Port: port}
		iface, ok := byPort[port]
		if !ok {
			out = append(out, stats) // unknown/unreachable port on this switch — reported unavailable, not skipped, so a typo'd port name in switches.yml is visible rather than silently dropped
			continue
		}
		stats.ErrorsCumulative = iface.InterfaceCounters.InErrors + iface.InterfaceCounters.OutErrors
		if iface.Bandwidth > 0 {
			rxPct := iface.InterfaceStatistics.InBitsRate / iface.Bandwidth * 100
			txPct := iface.InterfaceStatistics.OutBitsRate / iface.Bandwidth * 100
			worst := rxPct
			if txPct > worst {
				worst = txPct
			}
			stats.UtilizationPercent = &worst
		}
		out = append(out, stats)
	}
	return out, nil
}
