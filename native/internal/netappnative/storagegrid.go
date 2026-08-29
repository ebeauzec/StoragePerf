package netappnative

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	"plumb/internal/config"
)

// StorageGridCollector authenticates to a grid's Grid Management API and
// runs PromQL directly against StorageGRID's own embedded Prometheus via
// its metric-query passthrough — confirmed against a real, working
// third-party reference implementation (github.com/sepich/netapp-grafana-
// proxy) that does exactly this for Grafana. Unlike ONTAP, StorageGRID
// needs no REST-to-metric translation logic at all: every metric below is
// exactly what config/thresholds/netapp_storagegrid.yml already queries,
// just evaluated on StorageGRID's own Prometheus (which has no `array`
// label — there's only one grid per API call, so it's dropped from the
// query and re-added here on the way out) instead of Plumb's own.
type StorageGridCollector struct {
	mu     sync.Mutex
	tokens map[string]string // cached bearer token per array ID, re-fetched on 401
}

func NewStorageGridCollector() *StorageGridCollector {
	return &StorageGridCollector{tokens: map[string]string{}}
}

func (c *StorageGridCollector) client(arr config.Array) *http.Client {
	// DisableKeepAlives: a fresh Client/Transport is built for every scrape
	// (~15s per array) and discarded right after — no reuse for keep-alive
	// to help with. A bespoke *http.Transport's IdleConnTimeout zero-value
	// means "never time out," so without this, every scrape (and this
	// collector issues several per scrape — one per metric, plus the
	// per-node breakdown queries) leaked one permanently-idle established
	// connection neither side ever closed — confirmed live via lsof after
	// several hours of uptime.
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: arr.UseInsecureTLS}, DisableKeepAlives: true},
	}
}

type authRequest struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	Cookie    bool   `json:"cookie"`
	CSRFToken bool   `json:"csrfToken"`
}

type authResponse struct {
	Data string `json:"data"`
}

func (c *StorageGridCollector) login(client *http.Client, arr config.Array) (string, error) {
	password := ""
	if arr.PasswordEnv != "" {
		password = os.Getenv(arr.PasswordEnv)
	}
	body, err := json.Marshal(authRequest{Username: arr.Username, Password: password, Cookie: false, CSRFToken: false})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest("POST", "https://"+arr.ManagementLIF+"/api/v3/authorize", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("authorize: %s: %s", resp.Status, string(respBody))
	}
	var ar authResponse
	if err := json.Unmarshal(respBody, &ar); err != nil || ar.Data == "" {
		return "", fmt.Errorf("authorize: no token in response")
	}
	return ar.Data, nil
}

// token returns a cached bearer token for arr, logging in if there isn't
// one cached yet. Does not proactively refresh on expiry — query handles
// a 401 by discarding the cached token and retrying once.
func (c *StorageGridCollector) token(client *http.Client, arr config.Array) (string, error) {
	c.mu.Lock()
	tok, ok := c.tokens[arr.ID]
	c.mu.Unlock()
	if ok {
		return tok, nil
	}
	tok, err := c.login(client, arr)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.tokens[arr.ID] = tok
	c.mu.Unlock()
	return tok, nil
}

func (c *StorageGridCollector) invalidateToken(arr config.Array) {
	c.mu.Lock()
	delete(c.tokens, arr.ID)
	c.mu.Unlock()
}

// promInstantResponse is StorageGRID's metric-query response shape, which
// is the standard Prometheus HTTP API v1 instant-query format (confirmed
// against the sepich/netapp-grafana-proxy reference implementation, which
// exists specifically to let Grafana's native Prometheus datasource query
// StorageGRID directly through this same endpoint).
type promInstantResponse struct {
	Status string `json:"status"`
	Data   struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

// queryRaw runs a single PromQL expression against arr's grid, retrying
// once with a fresh token if the cached one has expired, and returns every
// result series with its labels intact — the shared plumbing behind both
// query() (single-series callers, which use only Result[0]) and
// queryVector() (multi-series callers, e.g. a `by (node)` grouping).
func (c *StorageGridCollector) queryRaw(client *http.Client, arr config.Array, promql string) (promInstantResponse, error) {
	for attempt := 0; attempt < 2; attempt++ {
		tok, err := c.token(client, arr)
		if err != nil {
			return promInstantResponse{}, err
		}
		u := "https://" + arr.ManagementLIF + "/api/v3/grid/metric-query?query=" + url.QueryEscape(promql)
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			return promInstantResponse{}, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := client.Do(req)
		if err != nil {
			return promInstantResponse{}, err
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			c.invalidateToken(arr)
			continue // one retry with a fresh login
		}
		if readErr != nil {
			return promInstantResponse{}, readErr
		}
		if resp.StatusCode != 200 {
			return promInstantResponse{}, fmt.Errorf("metric-query: %s: %s", resp.Status, string(body))
		}
		var pr promInstantResponse
		if err := json.Unmarshal(body, &pr); err != nil {
			return promInstantResponse{}, fmt.Errorf("metric-query: could not parse response: %w", err)
		}
		return pr, nil
	}
	return promInstantResponse{}, fmt.Errorf("metric-query: authentication failed after retry")
}

// query runs promql and returns its first (and normally only) result's
// value — for the existing grid-wide avg()/sum() queries, which always
// collapse to a single series.
func (c *StorageGridCollector) query(client *http.Client, arr config.Array, promql string) (float64, bool, error) {
	pr, err := c.queryRaw(client, arr, promql)
	if err != nil {
		return 0, false, err
	}
	if len(pr.Data.Result) == 0 {
		return 0, false, nil
	}
	v, ok := pr.Data.Result[0].Value[1].(string)
	if !ok {
		return 0, false, fmt.Errorf("metric-query: unexpected value type in response")
	}
	f, err := strconv.ParseFloat(v, 64)
	return f, err == nil, err
}

// nodeSample is one series from a `by (node)` query result.
type nodeSample struct {
	node  string
	value float64
}

// queryVector runs promql and returns every result series paired with its
// node identity — for the per-node breakdown queries, which deliberately
// don't aggregate away the node dimension.
//
// StorageGRID's own Grid Management API docs don't publish a label schema
// for these metrics directly, but its metric-labels endpoint
// (/api/v3/grid/metric-labels/instance/values) confirms "instance" —
// Prometheus's standard scrape-target label — is what identifies which
// node a sample came from: the Admin Node's Prometheus scrapes each node's
// own local exporters, so every series it collects gets tagged with that
// target's instance the same way any Prometheus setup would, rather than a
// StorageGRID-specific "node" label. Results missing it (shouldn't happen
// for the queries this collector sends, but a StorageGRID version change
// is exactly the kind of thing that could break silently) are skipped
// rather than surfaced as an unlabeled row.
func (c *StorageGridCollector) queryVector(client *http.Client, arr config.Array, promql string) ([]nodeSample, error) {
	pr, err := c.queryRaw(client, arr, promql)
	if err != nil {
		return nil, err
	}
	samples := make([]nodeSample, 0, len(pr.Data.Result))
	for _, r := range pr.Data.Result {
		node := r.Metric["instance"]
		if node == "" {
			continue
		}
		v, ok := r.Value[1].(string)
		if !ok {
			continue
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			continue
		}
		samples = append(samples, nodeSample{node: node, value: f})
	}
	return samples, nil
}

// WriteMetrics writes a Prometheus exposition-format scrape for one
// StorageGRID array, covering all six metrics
// config/thresholds/netapp_storagegrid.yml defines. Each is independent
// and best-effort, matching ontap.go's behavior.
func (c *StorageGridCollector) WriteMetrics(w io.Writer, arr config.Array) error {
	pw := &promWriter{}
	client := c.client(arr)

	gauges := []struct {
		metric, help, query string
	}{
		{"storagegrid_metadata_queries_average_latency_milliseconds", "Average metadata query latency, milliseconds",
			"avg(storagegrid_metadata_queries_average_latency_milliseconds)"},
		{"storagegrid_node_cpu_utilization_percentage", "Node CPU utilization, percent",
			"avg(storagegrid_node_cpu_utilization_percentage)"},
		{"storagegrid_ilm_awaiting_total_objects", "Objects awaiting ILM evaluation",
			"sum(storagegrid_ilm_awaiting_total_objects)"},
		{"storagegrid_storage_utilization_usable_space_bytes", "Usable storage space, bytes",
			"sum(storagegrid_storage_utilization_usable_space_bytes)"},
		{"storagegrid_storage_utilization_total_space_bytes", "Total storage space, bytes",
			"sum(storagegrid_storage_utilization_total_space_bytes)"},
	}
	for _, g := range gauges {
		v, ok, err := c.query(client, arr, g.query)
		if err != nil {
			pw.note(g.metric, "%s unavailable for %s: %v", g.metric, arr.ID, err)
			continue
		}
		if !ok {
			pw.note(g.metric, "%s unavailable for %s: no data returned", g.metric, arr.ID)
			continue
		}
		pw.gauge(g.metric, g.help, arr.ID, v)
	}

	// Counter-type metrics: config/thresholds/netapp_storagegrid.yml wraps
	// these in rate(...) itself, so this queries StorageGRID for the raw
	// cumulative total (no rate() here) and exposes it as a counter,
	// letting Plumb's own existing, unmodified query do the rate math —
	// the same reasoning as ontap.go's nic_rx_crc_errors.
	counters := []struct {
		metric, help, query string
	}{
		{"storagegrid_s3_operations_failed", "Cumulative failed S3 operations",
			"sum(storagegrid_s3_operations_failed)"},
	}
	for _, cm := range counters {
		v, ok, err := c.query(client, arr, cm.query)
		if err != nil {
			pw.note(cm.metric, "%s unavailable for %s: %v", cm.metric, arr.ID, err)
			continue
		}
		if !ok {
			pw.note(cm.metric, "%s unavailable for %s: no data returned", cm.metric, arr.ID)
			continue
		}
		pw.counter(cm.metric, cm.help, arr.ID, v)
	}

	// network_errors sums two separate counters
	// (node_network_receive_errs_total + node_network_transmit_errs_total)
	// under one rate() in the thresholds query — exposed here as two
	// separate raw counters so that query's own arithmetic is unchanged.
	for _, nc := range []struct{ metric, help, query string }{
		{"node_network_receive_errs_total", "Cumulative network receive errors", "sum(node_network_receive_errs_total)"},
		{"node_network_transmit_errs_total", "Cumulative network transmit errors", "sum(node_network_transmit_errs_total)"},
	} {
		v, ok, err := c.query(client, arr, nc.query)
		if err != nil {
			pw.note(nc.metric, "%s unavailable for %s: %v", nc.metric, arr.ID, err)
			continue
		}
		if !ok {
			pw.note(nc.metric, "%s unavailable for %s: no data returned", nc.metric, arr.ID)
			continue
		}
		pw.counter(nc.metric, nc.help, arr.ID, v)
	}

	c.writeNodeBreakdowns(pw, client, arr)

	return pw.Emit(w)
}

// writeNodeBreakdowns fetches and writes a per-node view of the same six
// metrics above, under distinct `_by_node` metric names rather than adding
// a `node` label onto the existing grid-wide names — so this can never
// change what the grid-wide avg()/sum() queries in
// config/thresholds/netapp_storagegrid.yml compute (they select on the
// original metric names only), while still letting the UI answer "which
// node" for a finding that already fired on the grid-wide number.
//
// Each query below asks StorageGRID for the raw, unaggregated metric (or,
// for rate-based ones, a per-node rate) instead of wrapping it in avg()/
// sum() the way the grid-wide queries do — the point here is to keep the
// node dimension, not collapse it. Best-effort per metric, matching the
// rest of this collector: one metric failing doesn't block the others.
func (c *StorageGridCollector) writeNodeBreakdowns(pw *promWriter, client *http.Client, arr config.Array) {
	byNode := []struct {
		metric, help, query string
	}{
		{"storagegrid_metadata_query_latency_by_node_ms", "Metadata query latency by node, milliseconds",
			"storagegrid_metadata_queries_average_latency_milliseconds"},
		{"storagegrid_node_cpu_by_node_pct", "Node CPU utilization by node, percent",
			"storagegrid_node_cpu_utilization_percentage"},
		{"storagegrid_ilm_backlog_by_node_objects", "Objects awaiting ILM evaluation by node",
			"storagegrid_ilm_awaiting_total_objects"},
		{"storagegrid_s3_error_rate_by_node_per_min", "Failed S3 operations by node, per minute",
			"rate(storagegrid_s3_operations_failed[5m]) * 60"},
		{"storagegrid_network_errors_by_node_per_min", "Network interface errors by node, per minute",
			"(rate(node_network_receive_errs_total[5m]) + rate(node_network_transmit_errs_total[5m])) * 60"},
		{"storagegrid_storage_capacity_by_node_pct", "Storage capacity used by node, percent",
			"100 * (1 - (storagegrid_storage_utilization_usable_space_bytes / storagegrid_storage_utilization_total_space_bytes))"},
	}
	for _, bn := range byNode {
		samples, err := c.queryVector(client, arr, bn.query)
		if err != nil {
			pw.note(bn.metric, "%s unavailable for %s: %v", bn.metric, arr.ID, err)
			continue
		}
		if len(samples) == 0 {
			pw.note(bn.metric, "%s unavailable for %s: no per-node data returned", bn.metric, arr.ID)
			continue
		}
		for _, s := range samples {
			pw.gaugeNode(bn.metric, bn.help, arr.ID, s.node, s.value)
		}
	}
}
