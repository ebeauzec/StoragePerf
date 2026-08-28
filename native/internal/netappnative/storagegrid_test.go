package netappnative

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"plumb/internal/config"
)

// sgFixtures maps a bare PromQL query string to the numeric value
// StorageGRID's metric-query endpoint should return for it, keyed exactly
// as netappnative's collector sends them (array-label-free).
func newStorageGridServer(t *testing.T, fixtures map[string]float64, authFailures int) *httptest.Server {
	t.Helper()
	authCalls := 0
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v3/authorize":
			authCalls++
			if authCalls <= authFailures {
				// simulate a bad login attempt that should surface as an error
				w.WriteHeader(401)
				w.Write([]byte(`{"error":"invalid credentials"}`))
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"data": "test-token-" + string(rune('0'+authCalls))})
		case r.URL.Path == "/api/v3/grid/metric-query":
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer test-token-") {
				w.WriteHeader(401)
				return
			}
			q := r.URL.Query().Get("query")
			v, ok := fixtures[q]
			if !ok {
				// unrecognized query in this test — return no data, not a crash
				json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": []any{}}})
				return
			}
			resp := map[string]any{
				"status": "success",
				"data": map[string]any{
					"resultType": "vector",
					"result": []map[string]any{
						{"metric": map[string]string{}, "value": []any{1735689600, strconv.FormatFloat(v, 'g', -1, 64)}},
					},
				},
			}
			json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(404)
		}
	}))
}

func newStorageGridArray(srv *httptest.Server) config.Array {
	u := srv.URL[len("https://"):]
	return config.Array{
		ID:             "test-sg-01",
		Vendor:         config.VendorNetAppStorageGRID,
		ManagementLIF:  u,
		Username:       "test-user",
		UseInsecureTLS: true,
	}
}

func TestStorageGrid_AllSixMetrics(t *testing.T) {
	srv := newStorageGridServer(t, map[string]float64{
		"avg(storagegrid_metadata_queries_average_latency_milliseconds)": 12.5,
		"avg(storagegrid_node_cpu_utilization_percentage)":               42,
		"sum(storagegrid_ilm_awaiting_total_objects)":                    1500,
		"sum(storagegrid_storage_utilization_usable_space_bytes)":        800,
		"sum(storagegrid_storage_utilization_total_space_bytes)":         1000,
		"sum(storagegrid_s3_operations_failed)":                          77,
		"sum(node_network_receive_errs_total)":                           4,
		"sum(node_network_transmit_errs_total)":                          6,
	}, 0)
	defer srv.Close()

	c := NewStorageGridCollector()
	arr := newStorageGridArray(srv)
	var buf bytes.Buffer
	if err := c.WriteMetrics(&buf, arr); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	mustContain(t, out, `storagegrid_metadata_queries_average_latency_milliseconds{array="test-sg-01"} 12.5`)
	mustContain(t, out, `storagegrid_node_cpu_utilization_percentage{array="test-sg-01"} 42`)
	mustContain(t, out, `storagegrid_ilm_awaiting_total_objects{array="test-sg-01"} 1500`)
	mustContain(t, out, `storagegrid_storage_utilization_usable_space_bytes{array="test-sg-01"} 800`)
	mustContain(t, out, `storagegrid_storage_utilization_total_space_bytes{array="test-sg-01"} 1000`)

	// counter-type metrics must be exposed as raw cumulative totals (no
	// rate() applied here), since config/thresholds/netapp_storagegrid.yml
	// wraps them in rate(...) itself
	mustContain(t, out, "# TYPE storagegrid_s3_operations_failed counter")
	mustContain(t, out, `storagegrid_s3_operations_failed{array="test-sg-01"} 77`)
	mustContain(t, out, "# TYPE node_network_receive_errs_total counter")
	mustContain(t, out, `node_network_receive_errs_total{array="test-sg-01"} 4`)
	mustContain(t, out, "# TYPE node_network_transmit_errs_total counter")
	mustContain(t, out, `node_network_transmit_errs_total{array="test-sg-01"} 6`)
}

func TestStorageGrid_TokenExpiryRetriesLoginOnce(t *testing.T) {
	// First login succeeds, but the metric-query handler in this test
	// always 401s on the FIRST token issued (simulating an expired/invalid
	// cached token) and accepts the second — verifying the collector
	// actually discards a bad cached token and re-authenticates rather
	// than failing permanently after one 401.
	firstTokenRejected := false
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/authorize":
			tok := "token-A"
			if firstTokenRejected {
				tok = "token-B"
			}
			json.NewEncoder(w).Encode(map[string]any{"data": tok})
		case r.URL.Path == "/api/v3/grid/metric-query":
			if r.Header.Get("Authorization") == "Bearer token-A" {
				firstTokenRejected = true
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{"result": []map[string]any{
					{"value": []any{1735689600, "99"}},
				}},
			})
		}
	}))
	defer srv.Close()

	c := NewStorageGridCollector()
	arr := newStorageGridArray(srv)
	var buf bytes.Buffer
	if err := c.WriteMetrics(&buf, arr); err != nil {
		t.Fatal(err)
	}
	mustContain(t, buf.String(), `storagegrid_node_cpu_utilization_percentage{array="test-sg-01"} 99`)
}

func TestStorageGrid_LoginFailure_ProducesNotesNotCrash(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":"invalid credentials"}`))
	}))
	defer srv.Close()

	c := NewStorageGridCollector()
	arr := newStorageGridArray(srv)
	var buf bytes.Buffer
	if err := c.WriteMetrics(&buf, arr); err != nil {
		t.Fatalf("WriteMetrics should never return an error for per-metric failures, got: %v", err)
	}
	out := buf.String()
	mustContain(t, out, "unavailable for test-sg-01")
}
