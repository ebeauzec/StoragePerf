package netappnative

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

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

func TestONTAP_AggrDiskBusy_SchemaDrivenPercentOfDenominator(t *testing.T) {
	const schemaPath = "/api/cluster/counter/tables/disk:constituent"
	const rowsPath = "/api/cluster/counter/tables/disk:constituent/rows"
	schema := `{"name":"disk:constituent","counter_schemas":[
		{"name":"disk_busy_percent","type":"percent","denominator":{"name":"base_for_disk_busy_percent"}},
		{"name":"base_for_disk_busy_percent","type":"delta"}
	]}`
	rowFixtures := []string{
		`{"records":[
			{"id":"disk-1","counters":[{"name":"disk_busy_percent","value":1000},{"name":"base_for_disk_busy_percent","value":10000}]},
			{"id":"disk-2","counters":[{"name":"disk_busy_percent","value":2000},{"name":"base_for_disk_busy_percent","value":10000}]}
		]}`,
		`{"records":[
			{"id":"disk-1","counters":[{"name":"disk_busy_percent","value":1100},{"name":"base_for_disk_busy_percent","value":10500}]},
			{"id":"disk-2","counters":[{"name":"disk_busy_percent","value":2300},{"name":"base_for_disk_busy_percent","value":10500}]}
		]}`,
	}
	poll := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case schemaPath:
			w.Write([]byte(schema))
		case rowsPath:
			w.Write([]byte(rowFixtures[poll]))
			poll++
		default:
			w.Write([]byte(`{"records":[]}`))
		}
	}))
	defer srv.Close()

	c := NewONTAPCollector()
	arr := newTestArray(t, srv)

	var buf1 bytes.Buffer
	if err := c.WriteMetrics(&buf1, arr); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf1.String(), "aggr_disk_busy{") {
		t.Errorf("expected no aggr_disk_busy sample on first poll, got:\n%s", buf1.String())
	}
	mustContain(t, buf1.String(), "warming up")

	var buf2 bytes.Buffer
	if err := c.WriteMetrics(&buf2, arr); err != nil {
		t.Fatal(err)
	}
	// disk-1: delta(busy)=100, delta(denom)=500 -> 20%
	// disk-2: delta(busy)=300, delta(denom)=500 -> 60%
	// average across disks = 40%
	mustContain(t, buf2.String(), `aggr_disk_busy{array="test-ontap-01"} 40`)
}

func TestONTAP_AggrDiskBusy_UnexpectedCounterTypeIsNotedNotGuessed(t *testing.T) {
	srv := httptest.NewTLSServer(pathRouter(map[string]string{
		"/api/cluster/counter/tables/disk:constituent": `{"name":"disk:constituent","counter_schemas":[
			{"name":"disk_busy_percent","type":"rate"}
		]}`,
	}))
	defer srv.Close()

	c := NewONTAPCollector()
	arr := newTestArray(t, srv)
	var buf bytes.Buffer
	if err := c.WriteMetrics(&buf, arr); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "aggr_disk_busy{") {
		t.Errorf("expected no fabricated aggr_disk_busy value for an unexpected counter type, got:\n%s", buf.String())
	}
	mustContain(t, buf.String(), "unexpected counter type")
}

func TestONTAP_NICUtilization_ComputesPercentOfLinkSpeed(t *testing.T) {
	const rowsPath = "/api/cluster/counter/tables/nic_common/rows"
	poll := 0
	rowFixtures := []string{
		`{"records":[{"id":"node1:e0a","properties":[{"name":"speed","value":"1000M"}],"counters":[{"name":"receive_bytes","value":1000000},{"name":"transmit_bytes","value":500000}]}]}`,
		`{"records":[{"id":"node1:e0a","properties":[{"name":"speed","value":"1000M"}],"counters":[{"name":"receive_bytes","value":51000000},{"name":"transmit_bytes","value":500500}]}]}`,
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != rowsPath {
			w.Write([]byte(`{"records":[]}`))
			return
		}
		w.Write([]byte(rowFixtures[poll]))
		poll++
	}))
	defer srv.Close()

	c := NewONTAPCollector()
	arr := newTestArray(t, srv)

	var buf1 bytes.Buffer
	if err := c.WriteMetrics(&buf1, arr); err != nil {
		t.Fatal(err)
	}
	mustContain(t, buf1.String(), "warming up")

	time.Sleep(100 * time.Millisecond)
	var buf2 bytes.Buffer
	if err := c.WriteMetrics(&buf2, arr); err != nil {
		t.Fatal(err)
	}
	out := buf2.String()
	mustContain(t, out, "nic_util_percent")
	// receive_bytes jumped by 50MB over ~100ms (~500MB/s) against a 1000M
	// link (125,000,000 B/s) — comfortably over 100% utilization; exact
	// value is timing-dependent (wall-clock elapsed time, not a fixed
	// tick), so this only asserts the code path produces a large, sane,
	// finite value rather than pinning an exact number.
	idx := strings.Index(out, `nic_util_percent{array="test-ontap-01"} `)
	if idx == -1 {
		t.Fatalf("nic_util_percent sample not found in output:\n%s", out)
	}
	var val float64
	if _, err := fmt.Sscanf(out[idx:], `nic_util_percent{array="test-ontap-01"} %f`, &val); err != nil {
		t.Fatalf("could not parse nic_util_percent value: %v", err)
	}
	if val < 50 {
		t.Errorf("expected a large utilization percent (rx rate far exceeds link speed in this fixture), got %v", val)
	}
}

func TestONTAP_NICUtilization_MissingSpeedIsSkippedNotCrashed(t *testing.T) {
	srv := httptest.NewTLSServer(pathRouter(map[string]string{
		"/api/cluster/counter/tables/nic_common/rows": `{"records":[
			{"id":"node1:e0a","properties":[],"counters":[{"name":"receive_bytes","value":100},{"name":"transmit_bytes","value":50}]}
		]}`,
	}))
	defer srv.Close()

	c := NewONTAPCollector()
	arr := newTestArray(t, srv)
	var buf bytes.Buffer
	if err := c.WriteMetrics(&buf, arr); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "nic_util_percent{") {
		t.Errorf("expected no nic_util_percent value when speed property is missing, got:\n%s", buf.String())
	}
}

func TestParseNICSpeedBytesPerSec(t *testing.T) {
	got, ok := parseNICSpeedBytesPerSec("1000M")
	if !ok || got != 1000*125000 {
		t.Errorf("parseNICSpeedBytesPerSec(\"1000M\") = %v, %v; want %v, true", got, ok, 1000*125000)
	}
	if _, ok := parseNICSpeedBytesPerSec("10G"); ok {
		t.Errorf("parseNICSpeedBytesPerSec(\"10G\") should fail — only the \"M\" suffix form is handled, matching Harvest's own parser")
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
