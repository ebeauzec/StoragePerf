package netappnative

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"plumb/internal/config"
)

// This file implements aggr_disk_busy and nic_util_percent, both of which
// need ONTAP's raw performance counter-tables API rather than the simpler
// REST resource endpoints the rest of ontap.go uses. The exact request
// pattern below is copied from NetApp Harvest's own production Go source
// (cmd/collectors/restperf/restperf.go and cmd/tools/rest/href_builder.go),
// not guessed:
//
//  1. Fetch each table's schema ONCE (cached indefinitely per array+table —
//     schema doesn't change during a running process): GET
//     /api/cluster/counter/tables/{table} returns counter_schemas[], each
//     with a name, a type ("raw", "rate", "average", "percent", ...), and
//     — for "average"/"percent" counters — a denominator.name naming the
//     counter it must be divided by. This is exactly how Harvest itself
//     discovers which counter pairs with which, rather than hardcoding it.
//  2. Fetch actual values in ONE bulk call per poll, not one call per
//     disk/port: GET /api/cluster/counter/tables/{table}/rows?fields=*&
//     counters.name=<a>|<b> — the literal query Harvest's HrefBuilder
//     constructs for every RestPerf poll.
//  3. Apply the type-appropriate math: "percent" = delta(counter)/
//     delta(denominator)*100, matching the same corrected formula already
//     verified for node_cpu_busy in ontap.go (NetApp's own KB advisory
//     CONTAP-377586 confirms the naive single-sample division is wrong).

type counterMeta struct {
	Type        string
	Denominator string
}

type counterRow struct {
	ID         string `json:"id"`
	Properties []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"properties"`
	Counters []struct {
		Name  string      `json:"name"`
		Value json.Number `json:"value"`
	} `json:"counters"`
}

func (r counterRow) property(name string) (string, bool) {
	for _, p := range r.Properties {
		if p.Name == name {
			return p.Value, true
		}
	}
	return "", false
}

func (r counterRow) counterValue(name string) (float64, bool) {
	for _, c := range r.Counters {
		if c.Name == name {
			f, err := c.Value.Float64()
			return f, err == nil
		}
	}
	return 0, false
}

// counterSchema fetches and caches table's counter_schemas, keyed per
// array+table so different clusters (potentially different ONTAP versions)
// never share a cache entry.
func (c *ONTAPCollector) counterSchema(client *http.Client, arr config.Array, table string) (map[string]counterMeta, error) {
	cacheKey := arr.ID + "|" + table
	c.schemaMu.Lock()
	if m, ok := c.schemaCache[cacheKey]; ok {
		c.schemaMu.Unlock()
		return m, nil
	}
	c.schemaMu.Unlock()

	body, err := c.get(client, arr, "/api/cluster/counter/tables/"+table)
	if err != nil {
		return nil, err
	}
	var resp struct {
		CounterSchemas []struct {
			Name        string `json:"name"`
			Type        string `json:"type"`
			Denominator struct {
				Name string `json:"name"`
			} `json:"denominator"`
		} `json:"counter_schemas"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parsing %s counter schema: %w", table, err)
	}
	m := make(map[string]counterMeta, len(resp.CounterSchemas))
	for _, cs := range resp.CounterSchemas {
		m[cs.Name] = counterMeta{Type: cs.Type, Denominator: cs.Denominator.Name}
	}
	c.schemaMu.Lock()
	c.schemaCache[cacheKey] = m
	c.schemaMu.Unlock()
	return m, nil
}

// fetchCounterRows is the bulk data fetch — one HTTP call for every row in
// the table, not one call per row. Mirrors Harvest's own HrefBuilder
// output exactly: fields=* plus a pipe-separated counters.name filter.
func (c *ONTAPCollector) fetchCounterRows(client *http.Client, arr config.Array, table string, counterNames []string) ([]counterRow, error) {
	path := fmt.Sprintf("/api/cluster/counter/tables/%s/rows?fields=*&counters.name=%s", table, strings.Join(counterNames, "|"))
	body, err := c.get(client, arr, path)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Records []counterRow `json:"records"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parsing %s rows: %w", table, err)
	}
	return resp.Records, nil
}

type diskSample struct{ busy, denom float64 }
type nicSample struct {
	rx, tx float64
	at     time.Time
}

func (c *ONTAPCollector) collectAggrDiskBusy(pw *promWriter, client *http.Client, arr config.Array) {
	const table = "disk:constituent"
	const counterName = "disk_busy_percent"

	schema, err := c.counterSchema(client, arr, table)
	if err != nil {
		pw.note("aggr_disk_busy", "aggr_disk_busy unavailable for %s: could not fetch counter schema: %v", arr.ID, err)
		return
	}
	meta, ok := schema[counterName]
	if !ok {
		pw.note("aggr_disk_busy", "aggr_disk_busy unavailable for %s: %q not found in %s's counter schema on this cluster", arr.ID, counterName, table)
		return
	}
	if meta.Type != "percent" && meta.Type != "average" {
		// Schema disagrees with every public source consulted when this was
		// written (all indicated "percent") — rather than guess a formula
		// for an unexpected type, report it and let it be investigated.
		pw.note("aggr_disk_busy", "aggr_disk_busy unavailable for %s: %s has unexpected counter type %q (expected percent/average)", arr.ID, counterName, meta.Type)
		return
	}
	counters := []string{counterName}
	if meta.Denominator != "" {
		counters = append(counters, meta.Denominator)
	}

	rows, err := c.fetchCounterRows(client, arr, table, counters)
	if err != nil {
		pw.note("aggr_disk_busy", "aggr_disk_busy unavailable for %s: %v", arr.ID, err)
		return
	}
	if len(rows) == 0 {
		pw.note("aggr_disk_busy", "aggr_disk_busy unavailable for %s: no disk rows returned", arr.ID)
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	var pctSum float64
	var pctCount int
	for _, row := range rows {
		busy, ok1 := row.counterValue(counterName)
		denomVal, ok2 := 1.0, true
		if meta.Denominator != "" {
			denomVal, ok2 = row.counterValue(meta.Denominator)
		}
		if !ok1 || !ok2 {
			continue
		}
		key := arr.ID + "|" + row.ID
		prev, had := c.diskState[key]
		c.diskState[key] = diskSample{busy: busy, denom: denomVal}
		if !had || denomVal <= prev.denom {
			continue // first sample for this disk, or a counter reset — nothing to diff yet
		}
		pct := (busy - prev.busy) / (denomVal - prev.denom)
		if meta.Type == "percent" {
			pct *= 100
		}
		pctSum += pct
		pctCount++
	}
	if pctCount == 0 {
		pw.note("aggr_disk_busy", "aggr_disk_busy warming up for %s (needs a second sample to compute a rate)", arr.ID)
		return
	}
	pw.gauge("aggr_disk_busy", "Average disk busy percent across all disks", arr.ID, pctSum/float64(pctCount))
}

// parseNICSpeedBytesPerSec converts ONTAP's NIC speed property (e.g.
// "10000M" for 10Gbps) to bytes/sec — the exact conversion Harvest's own
// Nic plugin performs (cmd/collectors/restperf/plugins/nic/nic.go):
// strip the "M" (Mbps) suffix, multiply by 125000 (1 Mbps = 125,000 B/s).
func parseNICSpeedBytesPerSec(s string) (float64, bool) {
	before, ok := strings.CutSuffix(s, "M")
	if !ok {
		return 0, false
	}
	mbps, err := strconv.Atoi(before)
	if err != nil {
		return 0, false
	}
	return float64(mbps) * 125000, true
}

func (c *ONTAPCollector) collectNICUtilization(pw *promWriter, client *http.Client, arr config.Array) {
	const table = "nic_common"
	rows, err := c.fetchCounterRows(client, arr, table, []string{"receive_bytes", "transmit_bytes"})
	if err != nil {
		pw.note("nic_util_percent", "nic_util_percent unavailable for %s: %v", arr.ID, err)
		return
	}
	if len(rows) == 0 {
		pw.note("nic_util_percent", "nic_util_percent unavailable for %s: no NIC rows returned", arr.ID)
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	var maxPct float64
	var sawAny bool
	for _, row := range rows {
		rx, ok1 := row.counterValue("receive_bytes")
		tx, ok2 := row.counterValue("transmit_bytes")
		speedStr, ok3 := row.property("speed")
		if !ok1 || !ok2 || !ok3 {
			continue
		}
		speedBps, ok := parseNICSpeedBytesPerSec(speedStr)
		if !ok || speedBps <= 0 {
			continue
		}
		key := arr.ID + "|" + row.ID
		prev, had := c.nicState[key]
		c.nicState[key] = nicSample{rx: rx, tx: tx, at: now}
		if !had {
			continue
		}
		elapsed := now.Sub(prev.at).Seconds()
		if elapsed <= 0 || rx < prev.rx || tx < prev.tx {
			continue // clock oddity or a counter reset (e.g. port flap) — skip this port this poll
		}
		rxPct := (rx - prev.rx) / elapsed / speedBps * 100
		txPct := (tx - prev.tx) / elapsed / speedBps * 100
		sawAny = true
		if p := math.Max(rxPct, txPct); p > maxPct {
			maxPct = p
		}
	}
	if !sawAny {
		pw.note("nic_util_percent", "nic_util_percent warming up for %s (needs a second sample to compute a rate)", arr.ID)
		return
	}
	pw.gauge("nic_util_percent", "Maximum network port utilization percent, any port", arr.ID, maxPct)
}

// schemaState holds the counter-tables-specific cache/mutex fields mixed
// into ONTAPCollector — split out so ontap.go's own struct literal stays
// focused on the simpler REST-resource metrics.
type schemaState struct {
	schemaMu    sync.Mutex
	schemaCache map[string]map[string]counterMeta
	diskState   map[string]diskSample
	nicState    map[string]nicSample
}

func newSchemaState() schemaState {
	return schemaState{
		schemaCache: map[string]map[string]counterMeta{},
		diskState:   map[string]diskSample{},
		nicState:    map[string]nicSample{},
	}
}
