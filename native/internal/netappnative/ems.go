package netappnative

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"

	"plumb/internal/config"
)

// EMSEvent is one occurrence from ONTAP's own Event Management System (EMS)
// log that Plumb considers worth surfacing — see emsAllowlist for which
// message names qualify and why. Unlike every other metric this collector
// produces, an EMS event is a discrete, already-timestamped occurrence, not
// a value to compare against a watch/critical threshold — it never goes
// through the Prometheus/VictoriaMetrics scrape path at all; CollectEMSEvents
// is called directly from the monitor loop and its results go straight into
// internal/eventstore.
type EMSEvent struct {
	ArrayID   string    `json:"array_id"`
	ArrayName string    `json:"array_name"`
	Index     int64     `json:"index"` // ONTAP's own monotonic per-cluster sequence number — used to dedup across polls
	Time      time.Time `json:"time"`
	Severity  string    `json:"severity"` // "critical" | "watch", mapped from ONTAP's own emergency/alert/error/notice
	Name      string    `json:"name"`     // ONTAP's own EMS message name, e.g. "disk.failed"
	Node      string    `json:"node"`
	Message   string    `json:"message"`
}

// emsAllowlist is an independently curated starting set of ONTAP EMS
// message names Plumb watches for — genuinely actionable, fleet-health
// events (hardware failure, capacity exhaustion, HA/quorum loss, licensing,
// replication breakage, security), not the thousands of message types ONTAP
// can emit. Sized for a real, correct first cut and meant to grow over
// time, not a claim of exhaustive coverage.
//
// Message names are ONTAP's own, per NetApp's public EMS message catalog
// (docs.netapp.com/us-en/ontap/error-messages/) — like every field name
// elsewhere in this package, treat a name that never matches on a live
// cluster as a signal to re-check the catalog for that ONTAP release rather
// than assume the underlying event doesn't exist; EMS message names have
// changed across major ONTAP versions before.
var emsAllowlist = []string{
	// Hardware
	"disk.failed",
	"diskShelf.pwrSupply.failed",
	"diskShelf.fan.failed",
	"monitor.fan.failed",
	"monitor.power.failed",
	"nvram.battery.low",
	"callhome.hainterconnect.down",
	// Capacity
	"wafl.vol.full",
	"wafl.aggr.almostFull",
	"monitor.volume.full",
	// HA / cluster health
	"ha.takeoverImpDisabled",
	"ha.takeover.start",
	"cluster.node.outofcluster",
	"arbiter.disabled",
	// Data protection
	"sm.mirror.transferFail",
	"snapmirror.dp.breakoff",
	// Networking
	"lif.migrate.failed",
	"pw.port.down",
	// Licensing / security
	"callhome.license.expired",
	"secd.audit.log.error",
}

func emsSeverity(ontapSeverity string) (mapped string, watch bool) {
	switch strings.ToLower(ontapSeverity) {
	case "emergency", "alert", "error":
		return "critical", true
	case "notice":
		return "watch", true
	default:
		// informational/debug — not actionable, not surfaced even if a
		// future allowlist entry's actual reported severity turns out
		// lower than expected on some ONTAP release.
		return "", false
	}
}

type emsEventsResp struct {
	Records []struct {
		Index int64  `json:"index"`
		Time  string `json:"time"`
		Node  struct {
			Name string `json:"name"`
		} `json:"node"`
		Message struct {
			Name        string `json:"name"`
			Severity    string `json:"severity"`
			Description string `json:"description"`
		} `json:"message"`
	} `json:"records"`
}

// CollectEMSEvents fetches the most recent EMS events matching emsAllowlist
// for one array, newest first. Best-effort like every other collector in
// this package — a failure here (older ONTAP version without this field
// set, a transient timeout) never affects metric collection, and is simply
// reported as no new events this poll rather than surfaced as an error the
// caller needs to handle specially.
func (c *ONTAPCollector) CollectEMSEvents(arr config.Array) ([]EMSEvent, error) {
	client := c.client(arr)
	nameFilter := url.QueryEscape(strings.Join(emsAllowlist, ","))
	path := "/api/support/ems/events?fields=index,time,node.name,message.name,message.severity,message.description" +
		"&message.name=" + nameFilter + "&max_records=50&order_by=time%20descending"

	body, err := c.get(client, arr, path)
	if err != nil {
		return nil, err
	}
	var r emsEventsResp
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}

	events := make([]EMSEvent, 0, len(r.Records))
	for _, rec := range r.Records {
		severity, ok := emsSeverity(rec.Message.Severity)
		if !ok {
			continue
		}
		t, err := time.Parse(time.RFC3339, rec.Time)
		if err != nil {
			continue
		}
		events = append(events, EMSEvent{
			ArrayID:   arr.ID,
			ArrayName: arr.Name,
			Index:     rec.Index,
			Time:      t,
			Severity:  severity,
			Name:      rec.Message.Name,
			Node:      rec.Node.Name,
			Message:   rec.Message.Description,
		})
	}
	return events, nil
}

// emsIndexKey is a small helper so callers (internal/eventstore) can dedup
// on (array, index) without importing anything from this package beyond
// EMSEvent itself.
func (e EMSEvent) DedupKey() string {
	return e.ArrayID + "|" + strconv.FormatInt(e.Index, 10)
}
