package mockbackend

import (
	"net/http"
)

// mockSwitchArrayID and mockSwitchPort are the one demo link mock mode
// wires up: mock-fa-prod-east-01 is the flagship "front-end critical,
// back-end clean" scenario (mockdata.go's first entry), whose correlation
// finding already fires without a switch configured — linking a mock
// switch on top of it demonstrates internal/rules.go's actual enhancement
// (citing real port evidence) on a finding a reader can directly compare
// against what it says without one.
const (
	mockSwitchArrayID = "mock-fa-prod-east-01"
	mockSwitchPort    = "Eth1/1"
)

// nexusMux serves one fixed, always-saturated port under Cisco NX-API's
// JSON-RPC shape (see internal/switchnative/nexus.go for the real request/
// response shape this mirrors) — mock mode's switch link is deliberately a
// single, simple, always-critical scenario, not a configurable fleet the
// way arrays are; there's only one story here to demonstrate.
func nexusMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /ins", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"ins_api": map[string]any{
				"outputs": map[string]any{
					"output": map[string]any{
						"code": "200",
						"body": map[string]any{
							"TABLE_interface": map[string]any{
								"ROW_interface": []map[string]any{
									{
										"eth_load_interval1_rx": 94.0,
										"eth_load_interval1_tx": 61.0,
										"eth_inerr":             0,
										"eth_outerr":            0,
									},
								},
							},
						},
					},
				},
			},
		})
	})
	return mux
}
