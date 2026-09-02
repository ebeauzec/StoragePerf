const state = {
  fleet: [],
  selectedId: null,
  hours: 24,
  arraysConfig: [],
  fleetSort: "severity", // "severity" | "name" — see renderFleetStrip
  mockData: false,
  retentionPeriod: "100y",
  retentionOptions: [],
  // finding.ref values the user has expanded — kept across the 15s auto-refresh
  // (renderFleetView() replaces #findings' innerHTML wholesale every tick, which
  // would otherwise silently re-collapse anything the user had opened)
  expandedFindings: new Set(),
  notifyEnabled: false,
  notifyWebhookUrl: "",
  notifyMinSeverity: "critical",
  scheduledReportsEnabled: false,
  scheduledReportInterval: "daily",
  scheduleOptions: [],
  maintenanceWindows: [],
  events: [],
  eventsSeverityFilter: "all", // "all" | "critical" | "watch" — see renderEventsSeverityPills
};

// 15M is the shortest SMOOTH window given the 15s scrape interval: at
// window/300 points (see api.go's step calc, floored at 15s), 15M still
// renders a full ~60-point curve, where 5M would floor to ~20 points and
// look sparse for no real gain in freshness over 15M.
//
// Realtime is deliberately sparser than that tradeoff — an 8-point, 2-minute
// window where each point is one real scrape sample, for watching values
// arrive live rather than reading a smoothed trend. Still driven by the
// same 15s tick() auto-refresh every other range already uses (see
// setInterval(tick, ...) below); the difference is purely how much history
// the chart shows per refresh, not a faster poll.
const RANGES = [
  { label: "Realtime", hours: 2 / 60 },
  { label: "15M", hours: 0.25 },
  { label: "1H", hours: 1 },
  { label: "24H", hours: 24 },
  { label: "7D", hours: 24 * 7 },
  { label: "30D", hours: 24 * 30 },
  { label: "90D", hours: 24 * 90 },
  { label: "1Y", hours: 24 * 365 },
];

const healthColor = (h) => (h === "critical" ? "#f0526e" : h === "watch" ? "#f0aa3a" : h === "good" ? "#3ecb7a" : "#5c6675");
const healthChip = (h) =>
  h === "critical" ? '<span class="chip chip-critical">Critical</span>'
  : h === "watch" ? '<span class="chip chip-watch">Watch</span>'
  : h === "good" ? '<span class="chip chip-good">Optimal</span>'
  : '<span class="chip chip-unknown">No data</span>';

const VENDOR_LABELS = {
  pure_flasharray: "Pure FlashArray",
  pure_flashblade: "Pure FlashBlade",
  netapp_ontap: "NetApp ONTAP",
  netapp_storagegrid: "NetApp StorageGRID",
  netapp_eseries: "NetApp E-Series",
};
const vendorLabel = (v) => VENDOR_LABELS[v] || v || "—";
const isNetApp = (v) => v === "netapp_ontap" || v === "netapp_storagegrid" || v === "netapp_eseries";

function fmt(value, digits = 1) {
  if (value === null || value === undefined || Number.isNaN(value)) return "—";
  // whole-number values (queue depth, ops, object counts) are often in the
  // thousands — group them so e.g. "141642" reads as "141,642"
  return value.toLocaleString(undefined, { minimumFractionDigits: digits, maximumFractionDigits: digits });
}

/* ---------------- SVG chart ---------------- */
function svgLine(series, { w = 560, h = 64, color = "#45d0c4", threshold = null, thresholdColor = "#5c6675" } = {}) {
  const fillId = "f" + Math.random().toString(36).slice(2);
  if (!series || series.length < 2) {
    return `<svg viewBox="0 0 ${w} ${h}" width="100%" height="${h}"><text x="8" y="${h / 2}" fill="#5c6675" font-size="11">no data yet</text></svg>`;
  }
  const values = series.map((p) => p[1]);
  const max = Math.max(...values, threshold || 0) * 1.15 || 1;
  const min = Math.min(0, Math.min(...values));
  const x = (i) => (i / (series.length - 1)) * w;
  const y = (v) => h - ((v - min) / (max - min || 1)) * h;
  const path = series.map((p, i) => (i === 0 ? "M" : "L") + x(i).toFixed(1) + "," + y(p[1]).toFixed(1)).join(" ");
  const area = path + ` L${w},${h} L0,${h} Z`;
  const grid = [0.25, 0.5, 0.75]
    .map((f) => `<line x1="0" x2="${w}" y1="${(h * f).toFixed(1)}" y2="${(h * f).toFixed(1)}" stroke="#28323e" stroke-width="1" stroke-dasharray="2,4"/>`)
    .join("");
  const thresholdLine = threshold !== null ? `<line x1="0" x2="${w}" y1="${y(threshold).toFixed(1)}" y2="${y(threshold).toFixed(1)}" stroke="${thresholdColor}" stroke-width="1" stroke-dasharray="4,3"/>` : "";
  const last = values[values.length - 1];
  return `<svg viewBox="0 0 ${w} ${h}" width="100%" height="${h}" preserveAspectRatio="none" style="display:block;overflow:visible;">
    <defs><linearGradient id="${fillId}" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0%" stop-color="${color}" stop-opacity="0.28"/>
      <stop offset="100%" stop-color="${color}" stop-opacity="0"/>
    </linearGradient></defs>
    ${grid}${thresholdLine}
    <path d="${area}" fill="url(#${fillId})" />
    <path d="${path}" fill="none" stroke="${color}" stroke-width="1.75" stroke-linejoin="round" stroke-linecap="round"/>
    <circle cx="${x(series.length - 1).toFixed(1)}" cy="${y(last).toFixed(1)}" r="3.2" fill="${color}"/>
    <circle cx="${x(series.length - 1).toFixed(1)}" cy="${y(last).toFixed(1)}" r="6" fill="${color}" opacity="0.18"/>
  </svg>`;
}
const sparkSmall = (series, color) => svgLine(series, { w: 170, h: 34, color });

/* ---------------- API ---------------- */
async function api(path, opts) {
  const r = await fetch(path, opts);
  if (!r.ok) throw new Error(`${path} -> ${r.status}`);
  return r.json();
}

/* ---------------- fleet ---------------- */
const FLEET_SORTS = [
  { value: "severity", label: "Severity" },
  { value: "name", label: "Name" },
];
// Worst-first — matches the same ranking rules.Severity uses server-side
// (internal/rules/rules.go's severityRank) and the order BuildFleetReport
// already sorts by, so "Severity" here means the same thing it does in a
// fleet report, not a separate convention invented just for this view.
const SEVERITY_SORT_RANK = { critical: 0, watch: 1, good: 2, unknown: 3 };

function sortedFleet() {
  const list = state.fleet.slice();
  if (state.fleetSort === "name") {
    list.sort((a, b) => a.name.localeCompare(b.name));
  } else {
    list.sort((a, b) => {
      const rankDiff = (SEVERITY_SORT_RANK[a.health] ?? 9) - (SEVERITY_SORT_RANK[b.health] ?? 9);
      return rankDiff !== 0 ? rankDiff : a.name.localeCompare(b.name);
    });
  }
  return list;
}

function renderFleetSortPills() {
  const el = document.getElementById("fleet-sort-pills");
  if (!el) return;
  el.innerHTML = FLEET_SORTS.map((s) => `<div class="range-pill ${s.value === state.fleetSort ? "active" : ""}" data-sort="${s.value}">${s.label}</div>`).join("");
  el.querySelectorAll(".range-pill").forEach((pill) =>
    pill.addEventListener("click", () => {
      state.fleetSort = pill.dataset.sort;
      renderFleetSortPills();
      renderFleetStrip();
    })
  );
}

// Renders #fleet-strip from the already-fetched state.fleet — split out of
// loadFleet so switching the sort order re-renders instantly from cached
// data instead of waiting on a network round-trip to /api/fleet.
function renderFleetStrip() {
  document.getElementById("fleet-strip").innerHTML = sortedFleet()
    .map(
      (a) => `
    <div class="fleet-card ${a.id === state.selectedId ? "selected" : ""}" data-id="${a.id}">
      <div class="fleet-card-top">
        <div><div class="fleet-name mono">${a.name}</div><div class="fleet-model">${a.model}</div><div class="vendor-tag">${vendorLabel(a.vendor)}</div></div>
        ${healthChip(a.health)}
      </div>
      <div class="fleet-spark">${sparkSmall(a.sparkline, healthColor(a.health))}</div>
      <div class="fleet-stats"><span>${a.secondary_label || "Queue"} <b>${a.secondary_value !== null && a.secondary_value !== undefined ? fmt(a.secondary_value, 0) + (a.secondary_unit || "") : "—"}</b></span><span>Lat <b>${a.latency !== null && a.latency !== undefined ? fmt(a.latency, 1) + "ms" : "—"}</b></span></div>
    </div>`
    )
    .join("");

  document.querySelectorAll(".fleet-card").forEach((el) =>
    el.addEventListener("click", () => {
      state.selectedId = el.dataset.id;
      // renderFleetView() only redraws the panels/findings below, not this
      // strip — toggle the highlight here directly so it's instant instead
      // of waiting for the next 15s tick() to redraw the whole strip
      document.querySelectorAll(".fleet-card").forEach((c) => c.classList.toggle("selected", c === el));
      renderFleetView();
    })
  );
}

async function loadFleet() {
  state.fleet = await api("/api/fleet");
  if (!state.selectedId && state.fleet.length) state.selectedId = state.fleet[0].id;

  const critical = state.fleet.filter((a) => a.health === "critical").length;
  const watch = state.fleet.filter((a) => a.health === "watch").length;
  const good = state.fleet.filter((a) => a.health === "good").length;
  document.getElementById("fleet-count").textContent = `Fleet — ${state.fleet.length} array${state.fleet.length === 1 ? "" : "s"}`;
  document.getElementById("fleet-summary").textContent = `${critical} critical · ${watch} watch · ${good} optimal`;

  renderFleetStrip();

  const dot = document.getElementById("live-dot");
  const label = document.getElementById("live-label");
  dot.classList.remove("bad");
  label.textContent = `Live · ${state.fleet.length} arrays scraped`;
}

/* ---------------- range pills ---------------- */
function renderRangePills() {
  document.getElementById("range-pills").innerHTML = RANGES.map(
    (r) => `<div class="range-pill ${r.hours === state.hours ? "active" : ""}" data-hours="${r.hours}">${r.label}</div>`
  ).join("");
  document.querySelectorAll(".range-pill").forEach((el) =>
    el.addEventListener("click", () => {
      state.hours = Number(el.dataset.hours);
      renderFleetView();
    })
  );
}

/* ---------------- panels + findings for selected array ---------------- */
function panelHtml(p, systemLabel) {
  const color = p.category === "frontend" ? "#45d0c4" : "#7c8fef";
  // Informational panels (IOPS/bandwidth/ops-rate — "workload characterization,
  // not an alert to tune on its own", per each metric's own config comment)
  // never carry watch/critical severity from the API, but still deserve a
  // visibly different treatment from a real health check: a plain "good"
  // badge would read as "this passed a threshold," when there was never a
  // meaningful threshold to pass. No dotted reference line on the chart
  // either, for the same reason — it would imply a ceiling that isn't real.
  const badgeClass = p.informational ? "badge-info" : p.severity === "critical" ? "badge-critical" : p.severity === "watch" ? "badge-watch" : "badge-good";
  const badgeText = p.informational ? "info" : p.severity;
  const footLabel = p.informational ? "Reference (not an alert)" : "Best-practice ceiling";
  return `<div class="panel">
    <div class="panel-top">
      <div>
        <div class="panel-label">${p.label}</div>
        ${systemLabel ? `<div class="panel-system">${systemLabel}</div>` : ""}
        <div class="panel-value-row"><span class="panel-value">${fmt(p.value, p.unit === "%" || p.unit.includes("errors") || p.unit.includes("per port") ? 0 : 2)}</span><span class="panel-unit">${p.unit}</span></div>
      </div>
      <span class="panel-badge ${badgeClass}">${badgeText}</span>
    </div>
    <div class="panel-chart">${svgLine(p.series, { color, threshold: p.informational ? null : p.watch })}</div>
    ${nodeBreakdownHtml(p)}
    <div class="panel-foot">
      <span class="threshold-tag">${footLabel}: <b>${p.threshold_label}</b></span>
      <span>${RANGES.find((r) => r.hours === state.hours)?.label || ""} window</span>
    </div>
  </div>`;
}

// nodeBreakdownHtml renders the per-node view a panel carries when its
// underlying metric supports one (currently StorageGRID only — see
// config.MetricDef.NodeBreakdownQuery) — answering "which node" for a
// grid-wide number without a separate trip to Grid Manager. Nodes are
// already sorted worst-first by the API; only rendered when present, so
// this is a no-op for every other vendor's panels.
function nodeBreakdownHtml(p) {
  if (!p.nodes || !p.nodes.length) return "";
  const digits = p.unit === "%" || p.unit.includes("errors") || p.unit.includes("per port") ? 0 : 2;
  return `<div class="panel-nodes">
    <div class="panel-nodes-label">Breakdown</div>
    ${p.nodes
      .map(
        (n) => `<div class="panel-node-row">
      <span class="node-dot" style="background:${healthColor(n.severity)}"></span>
      <span class="panel-node-name mono">${n.node}</span>
      <span class="panel-node-value mono">${fmt(n.value, digits)}${p.unit === "%" ? "%" : ""}</span>
    </div>`
      )
      .join("")}
  </div>`;
}

function findingHtml(f, idx) {
  const investigate = f.investigate || [];
  const remediate = f.remediate || [];
  const hasDetail = investigate.length > 0 || remediate.length > 0;
  const isExpanded = hasDetail && state.expandedFindings.has(f.ref);
  // The fleet-wide "Bottleneck is likely upstream" finding has no
  // metric_id (it isn't any one metric) — nothing to acknowledge there.
  const ackButton = f.metric_id
    ? `<button class="btn" data-ack-array="${state.selectedId}" data-ack-metric="${f.metric_id}">Acknowledge</button>`
    : "";
  return `<div class="finding sev-${f.severity} ${hasDetail ? "has-detail" : ""} ${isExpanded ? "expanded" : ""}" data-idx="${idx}" data-ref="${f.ref}" ${hasDetail ? `tabindex="0" role="button" aria-expanded="${isExpanded ? "true" : "false"}"` : ""}>
    <div class="finding-top">
      <span class="finding-sev">${f.severity}</span>
      <span class="finding-tag tag-${f.tag}">${f.tag === "fe" || f.tag === "frontend" ? "Front-End" : f.tag === "be" || f.tag === "backend" ? "Back-End" : "Fleet-wide"}</span>
    </div>
    <div class="finding-title">${f.title}${hasDetail ? '<span class="finding-chevron">▾</span>' : ""}</div>
    <div class="finding-body">${f.body}</div>
    <div class="finding-foot" style="display:flex; align-items:center; justify-content:space-between; gap:8px; margin-top:6px;">
      <div class="finding-ref">${f.ref}</div>
      ${ackButton}
    </div>
    ${
      hasDetail
        ? `<div class="finding-detail"><div class="finding-detail-inner">
      ${
        investigate.length
          ? `<div class="finding-detail-section">
        <div class="finding-detail-heading">How to investigate</div>
        <ol>${investigate.map((s) => `<li>${s}</li>`).join("")}</ol>
      </div>`
          : ""
      }
      ${
        remediate.length
          ? `<div class="finding-detail-section">
        <div class="finding-detail-heading">Recommended remediation</div>
        <ul>${remediate.map((s) => `<li>${s}</li>`).join("")}</ul>
      </div>`
          : ""
      }
    </div></div>`
        : ""
    }
  </div>`;
}

// Delegated (not per-button) since #findings' innerHTML is replaced
// wholesale on every render — same reasoning as the updates-rows listener.
document.getElementById("findings").addEventListener("click", async (e) => {
  const btn = e.target.closest("[data-ack-metric]");
  if (!btn) return;
  e.stopPropagation(); // don't also toggle the finding's expand/collapse
  btn.disabled = true;
  btn.textContent = "…";
  try {
    await api("/api/findings/ack", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ array_id: btn.dataset.ackArray, metric_id: btn.dataset.ackMetric }),
    });
    btn.textContent = "Acknowledged";
  } catch (err) {
    btn.disabled = false;
    btn.textContent = "Acknowledge";
  }
});

function findingsHistoryRowHtml(h) {
  const resolved = new Date(h.resolved_at).toLocaleString();
  return `<div class="update-row">
    <div>
      <div class="update-name">${h.label} <span class="finding-tag" style="margin-left:6px;">${h.array_name || h.array_id}</span></div>
      <div class="update-versions">was ${h.severity} · resolved ${resolved}${h.was_acked ? " · was acknowledged" : ""}</div>
    </div>
  </div>`;
}

async function renderFindingsHistory() {
  const el = document.getElementById("findings-history-rows");
  if (!el) return;
  try {
    const rows = await api("/api/findings/history?limit=20");
    el.innerHTML = rows.length ? rows.map(findingsHistoryRowHtml).join("") : '<div class="empty-note">Nothing has resolved yet.</div>';
  } catch (e) {
    el.innerHTML = "";
  }
}

/* ---------------- events (ONTAP EMS) ---------------- */
const EVENT_SEVERITY_FILTERS = [
  { value: "all", label: "All" },
  { value: "critical", label: "Critical" },
  { value: "watch", label: "Watch" },
];

function renderEventsSeverityPills() {
  const el = document.getElementById("events-severity-pills");
  if (!el) return;
  el.innerHTML = EVENT_SEVERITY_FILTERS.map(
    (f) => `<div class="range-pill ${f.value === state.eventsSeverityFilter ? "active" : ""}" data-severity="${f.value}">${f.label}</div>`
  ).join("");
  el.querySelectorAll(".range-pill").forEach((pill) =>
    pill.addEventListener("click", () => {
      state.eventsSeverityFilter = pill.dataset.severity;
      renderEventsSeverityPills();
      renderEventsRows(state.events || []);
    })
  );
}

function eventRowHtml(e) {
  const when = new Date(e.time).toLocaleString();
  return `<div class="update-row">
    <span class="node-dot" style="background:${healthColor(e.severity)}; flex:none;"></span>
    <div>
      <div class="update-name">${e.name} <span class="finding-tag" style="margin-left:6px;">${e.array_name || e.array_id}</span>${e.node ? ` <span class="finding-tag" style="margin-left:4px;">${e.node}</span>` : ""}</div>
      <div class="update-versions">${e.message || ""}</div>
      <div class="update-versions">${e.severity} · ${when}</div>
    </div>
  </div>`;
}

function renderEventsRows(events) {
  const el = document.getElementById("events-rows");
  if (!el) return;
  const filtered = state.eventsSeverityFilter === "all" ? events : events.filter((e) => e.severity === state.eventsSeverityFilter);
  el.innerHTML = filtered.length
    ? filtered.map(eventRowHtml).join("")
    : '<div class="empty-note">No EMS events logged yet — this is expected on a fresh install, before the first poll, or for arrays with nothing to report.</div>';
}

async function loadEventsView() {
  renderEventsSeverityPills();
  try {
    state.events = await api("/api/events?limit=200");
  } catch (e) {
    state.events = [];
  }
  const critical = state.events.filter((e) => e.severity === "critical").length;
  const watch = state.events.filter((e) => e.severity === "watch").length;
  document.getElementById("events-count").textContent = `Events — ${state.events.length} (${critical} critical, ${watch} watch)`;
  renderEventsRows(state.events);
}

/* ---------------- maintenance windows ---------------- */
async function renderMaintenanceBanner() {
  const banner = document.getElementById("maintenance-banner");
  if (!banner || !state.selectedId) return;
  try {
    state.maintenanceWindows = await api("/api/maintenance");
  } catch (e) {
    return;
  }
  const now = Date.now();
  const active = state.maintenanceWindows.find(
    (w) => new Date(w.until).getTime() > now && (w.array_id === state.selectedId || w.array_id === "*")
  );
  if (!active) {
    banner.style.display = "none";
    return;
  }
  const scope = active.array_id === "*" ? "the whole fleet" : "this array";
  banner.style.display = "block";
  banner.innerHTML = `<div class="finding sev-watch" style="margin:10px 0;">
    <div class="finding-title">Notifications silenced for ${scope} until ${new Date(active.until).toLocaleString()}${active.note ? " — " + active.note : ""}</div>
    <div class="finding-body">The dashboard and reports above are unaffected — this only mutes webhook notifications.</div>
    <button class="btn" id="maintenance-clear-btn" style="margin-top:8px;">End early</button>
  </div>`;
  document.getElementById("maintenance-clear-btn").addEventListener("click", async () => {
    await api(`/api/maintenance/${encodeURIComponent(active.array_id)}`, { method: "DELETE" });
    renderMaintenanceBanner();
  });
}

document.getElementById("maintenance-btn").addEventListener("click", async () => {
  if (!state.selectedId) return;
  const array = state.fleet.find((a) => a.id === state.selectedId);
  const scopeAll = confirm(`Silence notifications for the whole fleet? Cancel to silence just ${array ? array.name : state.selectedId}.`);
  const hoursStr = prompt("Silence for how many hours?", "4");
  if (!hoursStr) return;
  const hours = Number(hoursStr);
  if (!hours || hours <= 0) {
    alert("Enter a positive number of hours.");
    return;
  }
  const note = prompt("Optional note (e.g. \"firmware upgrade\"):", "") || "";
  try {
    await api("/api/maintenance", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ array_id: scopeAll ? "*" : state.selectedId, hours, note }),
    });
    await renderMaintenanceBanner();
  } catch (e) {
    alert(`Could not set maintenance window: ${e.message}`);
  }
});

function bindFindingToggles() {
  document.querySelectorAll("#findings .finding.has-detail").forEach((el) => {
    const toggle = () => {
      const expanded = el.classList.toggle("expanded");
      el.setAttribute("aria-expanded", expanded ? "true" : "false");
      const ref = el.dataset.ref;
      if (expanded) state.expandedFindings.add(ref);
      else state.expandedFindings.delete(ref);
    };
    el.addEventListener("click", toggle);
    el.addEventListener("keydown", (e) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        toggle();
      }
    });
  });
}

async function renderFleetView() {
  renderRangePills();
  if (!state.selectedId) return;
  const array = state.fleet.find((a) => a.id === state.selectedId);
  document.getElementById("selected-array-note").innerHTML = `Analyzing <b>${array ? array.name : state.selectedId}</b> ${array ? "· " + array.model + " · " + vendorLabel(array.vendor) : ""}`;
  document.getElementById("array-report-link").href = `/api/reports/array/${state.selectedId}?hours=${state.hours}`;
  document.getElementById("array-report-pdf-link").href = `/api/reports/array/${state.selectedId}/pdf?hours=${state.hours}`;
  document.getElementById("array-export-link").href = `/api/export/${state.selectedId}?hours=${state.hours}`;
  document.getElementById("array-baseline-link").href = `/api/reports/array/${state.selectedId}/suggested-thresholds`;
  document.getElementById("fleet-report-link").href = `/api/reports/fleet?hours=${state.hours}`;
  document.getElementById("fleet-report-pdf-link").href = `/api/reports/fleet/pdf?hours=${state.hours}`;
  await renderMaintenanceBanner();

  let detail;
  try {
    detail = await api(`/api/arrays/${state.selectedId}?hours=${state.hours}`);
  } catch (e) {
    document.getElementById("fe-panels").innerHTML = `<div class="empty-note">Could not load data for this array yet — is Prometheus still scraping it? (${e.message})</div>`;
    document.getElementById("be-panels").innerHTML = "";
    document.getElementById("findings").innerHTML = "";
    return;
  }

  // defensive: treat a missing/null panels or findings array the same as an
  // empty one rather than throwing — the backend should always send "[]",
  // never "null", but this keeps the UI alive even if that ever regresses
  const panels = detail.panels || [];
  const findings = detail.findings || [];
  const fe = panels.filter((p) => p.category === "frontend");
  const be = panels.filter((p) => p.category === "backend");
  // bare `.map(panelHtml)` would pass Array.map's index as the 2nd arg
  // (systemLabel) instead of this — has to be a wrapping arrow function
  const systemLabel = array ? `${array.name} · ${array.model}` : state.selectedId;
  document.getElementById("fe-panels").innerHTML = fe.map((p) => panelHtml(p, systemLabel)).join("") || '<div class="empty-note">No front-end metrics configured.</div>';
  document.getElementById("be-panels").innerHTML = be.map((p) => panelHtml(p, systemLabel)).join("") || '<div class="empty-note">No back-end metrics configured.</div>';
  document.getElementById("findings").innerHTML = findings.length
    ? findings.map((f, i) => findingHtml(f, i)).join("")
    : '<div class="empty-note">No best-practice findings for this array in the current window — everything is inside range.</div>';
  bindFindingToggles();
  renderFindingsHistory();
}

/* ---------------- config view ---------------- */
function vendorSelectHtml(i, current) {
  return `<select data-field="vendor" data-index="${i}" class="config-vendor-select">
    ${Object.entries(VENDOR_LABELS).map(([v, label]) => `<option value="${v}" ${v === current ? "selected" : ""}>${label}</option>`).join("")}
  </select>`;
}

function field(label, inputHtml, extraClass = "") {
  return `<div class="config-field ${extraClass}"><span class="config-label">${label}</span>${inputHtml}</div>`;
}
function textField(label, dataField, value, placeholder = "", extraClass = "") {
  return field(label, `<input data-field="${dataField}" placeholder="${placeholder}" value="${value || ""}">`, extraClass);
}

function configCardHtml(a, i) {
  const vendor = a.vendor || "pure_flasharray";
  const pureFields =
    textField("Host:port", "host", a.host) +
    textField("Token env var", "token_env", a.token_env, "PURE_TOKEN_...") +
    field(
      "Scheme",
      `<select data-field="scheme">
        <option value="https" ${a.scheme !== "http" ? "selected" : ""}>https</option>
        <option value="http" ${a.scheme === "http" ? "selected" : ""}>http</option>
      </select>`
    ) +
    // Native Purity's OpenMetrics exporter serves several category-scoped
    // paths (/metrics, /metrics/array, /metrics/pods, ...) — bare /metrics
    // is documented to return everything combined on the exporter this was
    // built against, but that's not guaranteed across every Purity version,
    // so this needs to be a field you can change on-site if it 404s.
    textField("Metrics path", "metrics_path", a.metrics_path || "/metrics", "/metrics") +
    field(
      "Verify TLS certificate",
      `<label class="inline-checkbox"><input type="checkbox" data-field="verify_tls" ${a.verify_tls ? "checked" : ""}> <span>Off by default — most arrays ship a self-signed cert</span></label>`,
      "span-2"
    );
  const netappFields =
    textField("Management LIF / Grid address", "management_lif", a.management_lif, "", "span-2") +
    textField("Username", "username", a.username) +
    textField("Password env var", "password_env", a.password_env, "NETAPP_PASSWORD_...") +
    textField("Datacenter", "datacenter", a.datacenter);

  return `<div class="config-card" data-index="${i}">
    <div class="config-card-head">
      <div class="config-card-id">
        <input data-field="id" class="mono" placeholder="system-id" value="${a.id || ""}" style="background:transparent;border:none;color:var(--text);font-weight:600;font-size:13.5px;width:auto;min-width:80px;padding:0;">
        ${vendorSelectHtml(i, vendor)}
      </div>
      <button class="btn btn-remove" data-remove="${i}">Remove</button>
    </div>
    <div class="config-grid">
      ${textField("Display name", "name", a.name)}
      ${textField("Model", "model", a.model)}
      ${isNetApp(vendor) ? netappFields : pureFields}
    </div>
  </div>`;
}

function readConfigRows() {
  return Array.from(document.querySelectorAll(".config-card")).map((row) => {
    const get = (f) => row.querySelector(`[data-field="${f}"]`)?.value.trim() || "";
    const getChecked = (f) => !!row.querySelector(`[data-field="${f}"]`)?.checked;
    const vendor = get("vendor");
    const base = { id: get("id"), name: get("name"), model: get("model"), vendor };
    if (isNetApp(vendor)) {
      return { ...base, management_lif: get("management_lif"), username: get("username"), password_env: get("password_env") || undefined, datacenter: get("datacenter") };
    }
    return {
      ...base,
      host: get("host"),
      token_env: get("token_env") || undefined,
      scheme: get("scheme") || "https",
      metrics_path: get("metrics_path") || "/metrics",
      verify_tls: getChecked("verify_tls"),
    };
  });
}

function renderConfigRows() {
  document.getElementById("config-rows").innerHTML =
    state.arraysConfig.map(configCardHtml).join("") || '<div class="empty-note">No arrays configured yet — add one below, or turn on mock data above to explore the interface first.</div>';
  document.querySelectorAll("[data-remove]").forEach((btn) =>
    btn.addEventListener("click", () => {
      state.arraysConfig.splice(Number(btn.dataset.remove), 1);
      renderConfigRows();
    })
  );
  document.querySelectorAll(".config-vendor-select").forEach((sel) =>
    sel.addEventListener("change", () => {
      // preserve whatever's currently in the card before switching field sets
      state.arraysConfig = readConfigRows();
      state.arraysConfig[Number(sel.dataset.index)].vendor = sel.value;
      renderConfigRows();
    })
  );
}

function formatBytes(n) {
  if (n === null || n === undefined) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i === 0 ? 0 : v < 10 ? 2 : 1)} ${units[i]}`;
}

async function loadConfigView() {
  const [arraysData, settings] = await Promise.all([api("/api/config/arrays"), api("/api/config/settings")]);
  state.arraysConfig = arraysData.arrays || [];
  renderConfigRows();
  state.mockData = !!settings.mock_data;
  document.getElementById("mock-data-toggle").checked = state.mockData;

  state.retentionPeriod = settings.retention_period || "100y";
  state.retentionOptions = settings.retention_options || [];
  const select = document.getElementById("retention-select");
  select.innerHTML = state.retentionOptions.map((o) => `<option value="${o.value}">${o.label}</option>`).join("");
  select.value = state.retentionPeriod;
  document.getElementById("db-size-note").textContent = formatBytes(settings.db_size_bytes);
  applyRetentionLabel();

  state.notifyEnabled = !!settings.notify_enabled;
  state.notifyWebhookUrl = settings.notify_webhook_url || "";
  state.notifyMinSeverity = settings.notify_min_severity || "critical";
  document.getElementById("notify-enabled-toggle").checked = state.notifyEnabled;
  document.getElementById("notify-webhook-url").value = state.notifyWebhookUrl;
  document.getElementById("notify-min-severity").value = state.notifyMinSeverity;

  state.scheduledReportsEnabled = !!settings.scheduled_reports_enabled;
  state.scheduledReportInterval = settings.scheduled_report_interval || "daily";
  state.scheduleOptions = settings.schedule_options || [];
  const scheduleSelect = document.getElementById("schedule-interval");
  scheduleSelect.innerHTML = state.scheduleOptions.map((o) => `<option value="${o.value}">${o.label}</option>`).join("");
  scheduleSelect.value = state.scheduledReportInterval;
  document.getElementById("schedule-enabled-toggle").checked = state.scheduledReportsEnabled;

  renderDiscoveryPicker();

  await Promise.all([renderUpdatesRows(), renderReportHistory()]);
}

/* ---------------- metrics discovery ---------------- */
function renderDiscoveryPicker() {
  const select = document.getElementById("discover-array-select");
  if (!select) return;
  const prior = select.value;
  select.innerHTML =
    state.fleet.map((a) => `<option value="${a.id}">${a.name} (${vendorLabel(a.vendor)})</option>`).join("") ||
    '<option value="">No systems available — turn on mock data or add an array above</option>';
  if (prior && state.fleet.some((a) => a.id === prior)) select.value = prior;
  updateDiscoveryLink();
}

function updateDiscoveryLink() {
  const select = document.getElementById("discover-array-select");
  const link = document.getElementById("discover-link");
  if (!select || !link) return;
  const id = select.value;
  link.href = id ? `/api/arrays/${id}/discover` : "#";
}

document.getElementById("discover-array-select").addEventListener("change", updateDiscoveryLink);

/* ---------------- notifications ---------------- */
document.getElementById("save-notify").addEventListener("click", async () => {
  const status = document.getElementById("notify-status");
  try {
    await saveSettings({
      notify_enabled: document.getElementById("notify-enabled-toggle").checked,
      notify_webhook_url: document.getElementById("notify-webhook-url").value.trim(),
      notify_min_severity: document.getElementById("notify-min-severity").value,
    });
    status.textContent = "Saved.";
  } catch (e) {
    status.textContent = `Save failed: ${e.message}`;
  }
});

document.getElementById("test-notify").addEventListener("click", async () => {
  const status = document.getElementById("notify-status");
  status.textContent = "Sending…";
  try {
    await api("/api/notify/test", { method: "POST" });
    status.textContent = "Test webhook sent — check your destination.";
  } catch (e) {
    status.textContent = `Test failed: ${e.message}`;
  }
});

/* ---------------- scheduled reports ---------------- */
document.getElementById("save-schedule").addEventListener("click", async () => {
  const status = document.getElementById("schedule-status");
  try {
    await saveSettings({
      scheduled_reports_enabled: document.getElementById("schedule-enabled-toggle").checked,
      scheduled_report_interval: document.getElementById("schedule-interval").value,
    });
    status.textContent = "Saved.";
  } catch (e) {
    status.textContent = `Save failed: ${e.message}`;
  }
});

function reportHistoryRowHtml(r) {
  const when = new Date(r.generated_at).toLocaleString();
  return `<div class="update-row">
    <div><div class="update-name">${when}</div><div class="update-versions">${formatBytes(r.size_bytes)}</div></div>
    <a class="btn" href="/api/reports/history/${encodeURIComponent(r.name)}" target="_blank" rel="noopener">Open</a>
  </div>`;
}

async function renderReportHistory() {
  const el = document.getElementById("report-history-rows");
  try {
    const rows = await api("/api/reports/history");
    el.innerHTML = rows.length
      ? `<div class="toggle-title" style="margin-bottom:8px;">Archived reports</div>` + rows.map(reportHistoryRowHtml).join("")
      : '<div class="empty-note">No scheduled reports generated yet.</div>';
  } catch (e) {
    el.innerHTML = "";
  }
}

// PUT /api/config/settings always writes every field together — sending
// only the one(s) that changed would silently reset the rest back to their
// zero value server-side, since the handler decodes a full Settings-shaped
// payload. Every caller passes only the field(s) it's actually changing;
// this fills in the rest from state so nothing else gets clobbered.
async function saveSettings(overrides = {}) {
  const body = {
    mock_data: state.mockData ?? document.getElementById("mock-data-toggle").checked,
    retention_period: state.retentionPeriod,
    notify_enabled: state.notifyEnabled,
    notify_webhook_url: state.notifyWebhookUrl,
    notify_min_severity: state.notifyMinSeverity,
    scheduled_reports_enabled: state.scheduledReportsEnabled,
    scheduled_report_interval: state.scheduledReportInterval,
    ...overrides,
  };
  const res = await api("/api/config/settings", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  Object.assign(state, {
    mockData: body.mock_data,
    retentionPeriod: body.retention_period,
    notifyEnabled: body.notify_enabled,
    notifyWebhookUrl: body.notify_webhook_url,
    notifyMinSeverity: body.notify_min_severity,
    scheduledReportsEnabled: body.scheduled_reports_enabled,
    scheduledReportInterval: body.scheduled_report_interval,
  });
  return res;
}

document.getElementById("mock-data-toggle").addEventListener("change", async (e) => {
  const enabled = e.target.checked;
  try {
    await saveSettings({ mock_data: enabled });
  } catch (err) {
    e.target.checked = !enabled; // revert on failure
    return;
  }
  document.getElementById("mock-pill").style.display = enabled ? "flex" : "none";
  state.selectedId = null;
  await tick();
});

// shorter → longer ordering, used to detect when a retention change would
// purge existing history so we can confirm before sending it
const RETENTION_ORDER = ["1w", "1M", "3M", "6M", "1y", "2y", "5y", "100y"];

document.getElementById("retention-select").addEventListener("change", async (e) => {
  const select = e.target;
  const previous = state.retentionPeriod;
  const next = select.value;
  const status = document.getElementById("retention-status");

  if (RETENTION_ORDER.indexOf(next) < RETENTION_ORDER.indexOf(previous)) {
    const label = select.selectedOptions[0]?.textContent || next;
    const ok = confirm(
      `Shortening retention to ${label} will permanently delete data older than that window from the database. This cannot be undone. Continue?`
    );
    if (!ok) {
      select.value = previous;
      return;
    }
  }

  try {
    await saveSettings({ retention_period: next });
    applyRetentionLabel();
    status.textContent = "Retention updated — the database is restarting to apply it; dashboards may show a brief gap in live data.";
  } catch (err) {
    select.value = previous;
    status.textContent = `Failed to update retention: ${err.message}`;
  }
});

/* ---------------- updates (check-and-notify) ---------------- */
function updateRowHtml(u) {
  const badge =
    u.update_available === true
      ? '<span class="update-badge chip-watch" style="background:rgba(240,170,58,0.14);color:var(--warn);">Update available</span>'
      : u.update_available === false
      ? '<span class="update-badge chip-good" style="background:rgba(62,203,122,0.12);color:var(--good);">Up to date</span>'
      : '<span class="update-badge" style="background:rgba(140,150,165,0.1);color:var(--text-dim);">' + (u.status || "unknown") + "</span>";
  // Plumb is the one row that can actually be acted on — every other
  // check here (sidecars, vendor metric-schema references) stays purely
  // informational, per the panel's own "check-and-notify" promise.
  const updateButton =
    u.id === "plumb" && u.update_available === true
      ? `<button class="btn btn-primary" data-self-update="${u.latest}" style="margin-left:10px;">Update now</button>`
      : "";
  return `<div class="update-row">
    <div>
      <div class="update-name">${u.label}</div>
      <div class="update-versions">shipped: ${u.current || "—"} ${u.latest ? "· latest seen: " + u.latest : ""}</div>
      ${u.note ? `<div class="update-note">${u.note}</div>` : ""}
    </div>
    <div style="display:flex; align-items:center;">${badge}${updateButton}</div>
  </div>`;
}

async function renderUpdatesRows() {
  try {
    const data = await api("/api/updates");
    const rows = data.checks || [];
    document.getElementById("updates-rows").innerHTML =
      (data.enabled ? "" : '<div class="empty-note">Update checking is disabled (PLUMB_CHECK_FOR_UPDATES=false) — fully offline mode.</div>') +
      rows.map(updateRowHtml).join("");
    const pill = document.getElementById("updates-pill");
    const anyAvailable = rows.some((r) => r.update_available === true);
    if (anyAvailable) {
      pill.style.display = "flex";
      document.getElementById("updates-label").textContent = "Updates available";
    } else {
      pill.style.display = "none";
    }
  } catch (e) {
    document.getElementById("updates-rows").innerHTML = `<div class="empty-note">Could not load update status (${e.message}).</div>`;
  }
}

// Delegated on the container (not per-button) since renderUpdatesRows()
// replaces #updates-rows' innerHTML wholesale on every 5-minute refresh —
// a listener attached directly to a button would be gone by the time
// anyone actually clicked it.
document.getElementById("updates-rows").addEventListener("click", async (e) => {
  const btn = e.target.closest("[data-self-update]");
  if (!btn) return;
  const targetVersion = btn.dataset.selfUpdate;
  btn.disabled = true;
  btn.textContent = "Downloading…";
  // Captured before the click so waitForRestart can detect "the version
  // actually changed" without needing to string-match GitHub's "v0.8.6"
  // tag against /api/version's bare "0.8.6" (different, deliberately —
  // see normalizeVersion in internal/updates).
  let priorVersion;
  try {
    priorVersion = (await api("/api/version")).version;
  } catch (e) {
    priorVersion = undefined;
  }
  try {
    await api("/api/self-update", { method: "POST" });
  } catch (err) {
    btn.disabled = false;
    btn.textContent = "Update now";
    alert(`Update failed: ${err.message}\n\nNothing was changed — this instance is still running normally.`);
    return;
  }
  btn.textContent = "Restarting…";
  document.getElementById("live-label").textContent = `Updating to ${targetVersion} — reconnecting…`;
  document.getElementById("live-dot").classList.add("bad");
  waitForRestart(priorVersion);
});

// Polls until a response comes back reporting a version different from
// the one that was running before the click, or a reasonable window
// elapses. The old process is shutting down and the new one is
// retry-binding the same port (see main.go's listenWithRetry), so a
// handful of failed requests right after the click is the expected,
// healthy path, not an error.
async function waitForRestart(priorVersion) {
  const deadline = Date.now() + 90_000;
  while (Date.now() < deadline) {
    await new Promise((r) => setTimeout(r, 1500));
    try {
      const v = await api("/api/version");
      if (v.version && v.version !== priorVersion) {
        location.reload();
        return;
      }
    } catch (e) {
      // still down between the old process exiting and the new one binding — keep polling
    }
  }
  document.getElementById("live-label").textContent = "Update is taking longer than expected — check data/logs/plumb.log in the new install directory";
}

document.getElementById("add-array").addEventListener("click", () => {
  state.arraysConfig.push({ id: "", name: "", host: "", model: "", vendor: "pure_flasharray", scheme: "https" });
  renderConfigRows();
});

document.getElementById("save-arrays").addEventListener("click", async () => {
  const status = document.getElementById("config-status");
  try {
    const arrays = readConfigRows();
    const res = await api("/api/config/arrays", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ arrays }),
    });
    status.textContent = `Saved ${res.saved} array(s) — scrape targets regenerated.`;
    state.selectedId = null;
    await loadFleet();
    await renderFleetView();
  } catch (e) {
    status.textContent = `Save failed: ${e.message}`;
  }
});

/* ---------------- tabs ---------------- */
// One entry per tab: which view div it shows, and what (if anything) to
// load the first time it's opened. Every other tab's view is hidden.
const TABS = [
  { tab: "fleet", view: "view-fleet" },
  { tab: "events", view: "view-events", onShow: loadEventsView },
  { tab: "config", view: "view-config", onShow: loadConfigView },
];
document.querySelectorAll(".tab").forEach((tabEl) =>
  tabEl.addEventListener("click", async () => {
    document.querySelectorAll(".tab").forEach((t) => t.classList.remove("active"));
    tabEl.classList.add("active");
    const active = tabEl.dataset.tab;
    for (const t of TABS) {
      document.getElementById(t.view).style.display = t.tab === active ? "block" : "none";
    }
    const entry = TABS.find((t) => t.tab === active);
    if (entry && entry.onShow) await entry.onShow();
  })
);

/* ---------------- boot ---------------- */
async function tick() {
  try {
    await loadFleet();
    await renderFleetView();
  } catch (e) {
    document.getElementById("live-dot").classList.add("bad");
    document.getElementById("live-label").textContent = `Disconnected — ${e.message}`;
  }
}

function applyRetentionLabel() {
  const el = document.getElementById("retention-note");
  if (!el) return;
  const opt = state.retentionOptions.find((o) => o.value === state.retentionPeriod);
  const label = opt ? opt.label : state.retentionPeriod;
  el.innerHTML = `Retained history: <b>${label}</b> (VictoriaMetrics) · 15s native resolution`;
}

async function syncMockPill() {
  try {
    const settings = await api("/api/config/settings");
    document.getElementById("mock-pill").style.display = settings.mock_data ? "flex" : "none";
    state.retentionPeriod = settings.retention_period || "100y";
    state.retentionOptions = settings.retention_options || [];
    applyRetentionLabel();
  } catch (e) {
    /* non-fatal — pill/label just stay at their defaults until the Config tab is opened */
  }
}

async function loadVersion() {
  try {
    const v = await api("/api/version");
    if (v.version) document.getElementById("version-tag").textContent = `v${v.version}`;
  } catch (e) {
    /* non-fatal — version tag just stays blank */
  }
}

renderFleetSortPills();
tick();
syncMockPill();
loadVersion();
renderUpdatesRows();
setInterval(tick, 15000);
setInterval(renderUpdatesRows, 5 * 60 * 1000);
