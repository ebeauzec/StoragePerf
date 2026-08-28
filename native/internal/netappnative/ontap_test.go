package netappnative

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"plumb/internal/config"
)

// newTestArray builds a config.Array pointed at a httptest.NewTLSServer,
// with UseInsecureTLS set so the collector's client accepts the server's
// self-signed test certificate.
func newTestArray(t *testing.T, srv *httptest.Server) config.Array {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return config.Array{
		ID:             "test-ontap-01",
		Vendor:         config.VendorNetAppONTAP,
		ManagementLIF:  u.Host,
		Username:       "test-user",
		UseInsecureTLS: true,
	}
}

func mustContain(t *testing.T, output, substr string) {
	t.Helper()
	if !strings.Contains(output, substr) {
		t.Errorf("expected output to contain %q, got:\n%s", substr, output)
	}
}

// pathRouter serves canned responses keyed by exact request path (ignoring
// the query string), and an empty {"records":[]} for any other path — so a
// test focused on one metric doesn't have to fabricate realistic fixtures
// for the other four endpoints WriteMetrics also calls on every poll.
func pathRouter(byPath map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if body, ok := byPath[r.URL.Path]; ok {
			w.Write([]byte(body))
			return
		}
		w.Write([]byte(`{"records":[]}`))
	}
}

func TestONTAP_ClusterLatency(t *testing.T) {
	srv := httptest.NewTLSServer(pathRouter(map[string]string{
		"/api/cluster/metrics": `{"records":[{"latency":{"total":5500},"timestamp":"2026-08-28 12:00:00 +0000"}]}`,
	}))
	defer srv.Close()

	c := NewONTAPCollector()
	arr := newTestArray(t, srv)
	var buf bytes.Buffer
	if err := c.WriteMetrics(&buf, arr); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	// 5500 microseconds -> 5.5 milliseconds
	mustContain(t, out, `volume_avg_latency{array="test-ontap-01"} 5.5`)
}

func TestONTAP_NodeCPU_WarmsUpThenComputesRatioOfDeltas(t *testing.T) {
	// Realistic fixture shape from NetApp's own documented (buggy) example
	// and Checkmk's corrected formula: percent = delta(raw)/delta(base)*100.
	poll := 0
	fixtures := []string{
		`{"records":[{"statistics":{"processor_utilization_raw":1000,"processor_utilization_base":10000}}]}`,
		`{"records":[{"statistics":{"processor_utilization_raw":1050,"processor_utilization_base":10500}}]}`,
	}
	const nodesPath = "/api/cluster/nodes"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != nodesPath {
			w.Write([]byte(`{"records":[]}`))
			return
		}
		w.Write([]byte(fixtures[poll]))
		poll++
	}))
	defer srv.Close()

	c := NewONTAPCollector()
	arr := newTestArray(t, srv)

	var buf1 bytes.Buffer
	if err := c.WriteMetrics(&buf1, arr); err != nil {
		t.Fatal(err)
	}
	// First poll: nothing to diff against yet — must not fabricate a value.
	if strings.Contains(buf1.String(), "node_cpu_busy{") {
		t.Errorf("expected no node_cpu_busy sample on first poll (nothing to diff), got:\n%s", buf1.String())
	}
	mustContain(t, buf1.String(), "warming up")

	var buf2 bytes.Buffer
	if err := c.WriteMetrics(&buf2, arr); err != nil {
		t.Fatal(err)
	}
	// delta(raw)=50, delta(base)=500 -> 50/500*100 = 10%
	mustContain(t, buf2.String(), `node_cpu_busy{array="test-ontap-01"} 10`)
}

func TestONTAP_AggregateCapacity(t *testing.T) {
	srv := httptest.NewTLSServer(pathRouter(map[string]string{
		"/api/storage/aggregates": `{"records":[
			{"space":{"block_storage":{"size":1000,"used":600}}},
			{"space":{"block_storage":{"size":1000,"used":150}}}
		]}`,
	}))
	defer srv.Close()

	c := NewONTAPCollector()
	arr := newTestArray(t, srv)
	var buf bytes.Buffer
	if err := c.WriteMetrics(&buf, arr); err != nil {
		t.Fatal(err)
	}
	// (600+150) / (1000+1000) * 100 = 37.5%
	mustContain(t, buf.String(), `aggr_space_used_percent{array="test-ontap-01"} 37.5`)
}

func TestONTAP_SnapMirrorLag_ParsesISODurationAndTakesMax(t *testing.T) {
	srv := httptest.NewTLSServer(pathRouter(map[string]string{
		"/api/snapmirror/relationships": `{"records":[
			{"lag_time":"PT8H35M42S"},
			{"lag_time":"PT1M0S"}
		]}`,
	}))
	defer srv.Close()

	c := NewONTAPCollector()
	arr := newTestArray(t, srv)
	var buf bytes.Buffer
	if err := c.WriteMetrics(&buf, arr); err != nil {
		t.Fatal(err)
	}
	// 8h35m42s = 8*3600 + 35*60 + 42 = 28800+2100+42 = 30942
	mustContain(t, buf.String(), `snapmirror_lag_time{array="test-ontap-01"} 30942`)
}

func TestONTAP_SnapMirrorLag_NoRelationshipsIsZeroNotAbsent(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"records":[]}`))
	}))
	defer srv.Close()

	c := NewONTAPCollector()
	arr := newTestArray(t, srv)
	var buf bytes.Buffer
	if err := c.WriteMetrics(&buf, arr); err != nil {
		t.Fatal(err)
	}
	mustContain(t, buf.String(), `snapmirror_lag_time{array="test-ontap-01"} 0`)
}

func TestONTAP_NICErrors_SumsReceiveAndTransmitAsRawCounter(t *testing.T) {
	srv := httptest.NewTLSServer(pathRouter(map[string]string{
		"/api/network/ethernet/ports": `{"records":[
			{"statistics":{"device":{"receive_raw":{"errors":5},"transmit_raw":{"errors":3}}}},
			{"statistics":{"device":{"receive_raw":{"errors":2},"transmit_raw":{"errors":0}}}}
		]}`,
	}))
	defer srv.Close()

	c := NewONTAPCollector()
	arr := newTestArray(t, srv)
	var buf bytes.Buffer
	if err := c.WriteMetrics(&buf, arr); err != nil {
		t.Fatal(err)
	}
	// 5+3+2+0 = 10, exposed as a counter type
	mustContain(t, buf.String(), "# TYPE nic_rx_crc_errors counter")
	mustContain(t, buf.String(), `nic_rx_crc_errors{array="test-ontap-01"} 10`)
}

func TestONTAP_UnreachableHost_ProducesNotesNotCrash(t *testing.T) {
	c := NewONTAPCollector()
	arr := config.Array{ID: "unreachable-01", Vendor: config.VendorNetAppONTAP, ManagementLIF: "127.0.0.1:1", Username: "x"}
	var buf bytes.Buffer
	if err := c.WriteMetrics(&buf, arr); err != nil {
		t.Fatalf("WriteMetrics should never return an error for per-metric failures, got: %v", err)
	}
	out := buf.String()
	mustContain(t, out, "unavailable for unreachable-01")
	if strings.Contains(out, "panic") {
		t.Errorf("output should never contain a panic trace")
	}
}

func TestParseISODuration(t *testing.T) {
	cases := map[string]float64{
		"PT0S":       0,
		"PT1M0S":     60,
		"PT8H35M42S": 30942,
		"P1DT0H0M0S": 86400,
		"PT1H":       3600,
	}
	for in, want := range cases {
		got, ok := parseISODurationSeconds(in)
		if !ok {
			t.Errorf("parseISODurationSeconds(%q) failed to parse", in)
			continue
		}
		if got != want {
			t.Errorf("parseISODurationSeconds(%q) = %v, want %v", in, got, want)
		}
	}
}
