const state = {
  fleet: [],
  selectedId: null,
  hours: 24,
  arraysConfig: [],
};

const RANGES = [
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
};
const vendorLabel = (v) => VENDOR_LABELS[v] || v || "—";
const isNetApp = (v) => v === "netapp_ontap" || v === "netapp_storagegrid";

function fmt(value, digits = 1) {
  if (value === null || value === undefined || Number.isNaN(value)) return "—";
  return value.toFixed(digits);
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
async function loadFleet() {
  state.fleet = await api("/api/fleet");
  if (!state.selectedId && state.fleet.length) state.selectedId = state.fleet[0].id;

  const critical = state.fleet.filter((a) => a.health === "critical").length;
  const watch = state.fleet.filter((a) => a.health === "watch").length;
  const good = state.fleet.filter((a) => a.health === "good").length;
  document.getElementById("fleet-count").textContent = `Fleet — ${state.fleet.length} array${state.fleet.length === 1 ? "" : "s"}`;
  document.getElementById("fleet-summary").textContent = `${critical} critical · ${watch} watch · ${good} optimal`;

  document.getElementById("fleet-strip").innerHTML = state.fleet
    .map(
      (a) => `
    <div class="fleet-card ${a.id === state.selectedId ? "selected" : ""}" data-id="${a.id}">
      <div class="fleet-card-top">
        <div><div class="fleet-name mono">${a.name}</div><div class="fleet-model">${a.model}</div><div class="vendor-tag">${vendorLabel(a.vendor)}</div></div>
        ${healthChip(a.health)}
      </div>
      <div class="fleet-spark">${sparkSmall(a.sparkline, healthColor(a.health))}</div>
      <div class="fleet-stats"><span>Queue <b>${a.queue_depth !== null && a.queue_depth !== undefined ? fmt(a.queue_depth, 0) : "—"}</b></span><span>Lat <b>${a.latency !== null && a.latency !== undefined ? fmt(a.latency, 1) + "ms" : "—"}</b></span></div>
    </div>`
    )
    .join("");

  document.querySelectorAll(".fleet-card").forEach((el) =>
    el.addEventListener("click", () => {
      state.selectedId = el.dataset.id;
      renderFleetView();
    })
  );

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
function panelHtml(p) {
  const color = p.category === "frontend" ? "#45d0c4" : "#7c8fef";
  const badgeClass = p.severity === "critical" ? "badge-critical" : p.severity === "watch" ? "badge-watch" : "badge-good";
  return `<div class="panel">
    <div class="panel-top">
      <div>
        <div class="panel-label">${p.label}</div>
        <div class="panel-value-row"><span class="panel-value">${fmt(p.value, p.unit === "%" || p.unit.includes("errors") || p.unit.includes("per port") ? 0 : 2)}</span><span class="panel-unit">${p.unit}</span></div>
      </div>
      <span class="panel-badge ${badgeClass}">${p.severity}</span>
    </div>
    <div class="panel-chart">${svgLine(p.series, { color, threshold: p.watch })}</div>
    <div class="panel-foot">
      <span class="threshold-tag">Best-practice ceiling: <b>${p.threshold_label}</b></span>
      <span>${RANGES.find((r) => r.hours === state.hours)?.label || ""} window</span>
    </div>
  </div>`;
}

function findingHtml(f) {
  return `<div class="finding sev-${f.severity}">
    <div class="finding-top">
      <span class="finding-sev">${f.severity}</span>
      <span class="finding-tag tag-${f.tag}">${f.tag === "fe" || f.tag === "frontend" ? "Front-End" : f.tag === "be" || f.tag === "backend" ? "Back-End" : "Fleet-wide"}</span>
    </div>
    <div class="finding-title">${f.title}</div>
    <div class="finding-body">${f.body}</div>
    <div class="finding-ref">${f.ref}</div>
  </div>`;
}

async function renderFleetView() {
  renderRangePills();
  if (!state.selectedId) return;
  const array = state.fleet.find((a) => a.id === state.selectedId);
  document.getElementById("selected-array-note").innerHTML = `Analyzing <b>${array ? array.name : state.selectedId}</b> ${array ? "· " + array.model + " · " + vendorLabel(array.vendor) : ""}`;
  document.getElementById("array-report-link").href = `/api/reports/array/${state.selectedId}?hours=${state.hours}`;
  document.getElementById("array-export-link").href = `/api/export/${state.selectedId}?hours=${state.hours}`;
  document.getElementById("fleet-report-link").href = `/api/reports/fleet?hours=${state.hours}`;

  let detail;
  try {
    detail = await api(`/api/arrays/${state.selectedId}?hours=${state.hours}`);
  } catch (e) {
    document.getElementById("fe-panels").innerHTML = `<div class="empty-note">Could not load data for this array yet — is Prometheus still scraping it? (${e.message})</div>`;
    document.getElementById("be-panels").innerHTML = "";
    document.getElementById("findings").innerHTML = "";
    return;
  }

  const fe = detail.panels.filter((p) => p.category === "frontend");
  const be = detail.panels.filter((p) => p.category === "backend");
  document.getElementById("fe-panels").innerHTML = fe.map(panelHtml).join("") || '<div class="empty-note">No front-end metrics configured.</div>';
  document.getElementById("be-panels").innerHTML = be.map(panelHtml).join("") || '<div class="empty-note">No back-end metrics configured.</div>';
  document.getElementById("findings").innerHTML =
    detail.findings.length ? detail.findings.map(findingHtml).join("") : '<div class="empty-note">No best-practice findings for this array in the current window — everything is inside range.</div>';
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
    const vendor = get("vendor");
    const base = { id: get("id"), name: get("name"), model: get("model"), vendor };
    if (isNetApp(vendor)) {
      return { ...base, management_lif: get("management_lif"), username: get("username"), password_env: get("password_env") || undefined, datacenter: get("datacenter") };
    }
    return { ...base, host: get("host"), token_env: get("token_env") || undefined, scheme: get("scheme") || "https", metrics_path: "/metrics", verify_tls: get("scheme") !== "http" };
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

async function loadConfigView() {
  const [arraysData, settings] = await Promise.all([api("/api/config/arrays"), api("/api/config/settings")]);
  state.arraysConfig = arraysData.arrays || [];
  renderConfigRows();
  document.getElementById("mock-data-toggle").checked = !!settings.mock_data;
  await renderUpdatesRows();
}

document.getElementById("mock-data-toggle").addEventListener("change", async (e) => {
  const enabled = e.target.checked;
  try {
    await api("/api/config/settings", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ mock_data: enabled }),
    });
  } catch (err) {
    e.target.checked = !enabled; // revert on failure
    return;
  }
  document.getElementById("mock-pill").style.display = enabled ? "flex" : "none";
  state.selectedId = null;
  await tick();
});

/* ---------------- updates (check-and-notify) ---------------- */
function updateRowHtml(u) {
  const badge =
    u.update_available === true
      ? '<span class="update-badge chip-watch" style="background:rgba(240,170,58,0.14);color:var(--warn);">Update available</span>'
      : u.update_available === false
      ? '<span class="update-badge chip-good" style="background:rgba(62,203,122,0.12);color:var(--good);">Up to date</span>'
      : '<span class="update-badge" style="background:rgba(140,150,165,0.1);color:var(--text-dim);">' + (u.status || "unknown") + "</span>";
  return `<div class="update-row">
    <div>
      <div class="update-name">${u.label}</div>
      <div class="update-versions">shipped: ${u.current || "—"} ${u.latest ? "· latest seen: " + u.latest : ""}</div>
      ${u.note ? `<div class="update-note">${u.note}</div>` : ""}
    </div>
    ${badge}
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
document.querySelectorAll(".tab").forEach((tab) =>
  tab.addEventListener("click", async () => {
    document.querySelectorAll(".tab").forEach((t) => t.classList.remove("active"));
    tab.classList.add("active");
    const isConfig = tab.dataset.tab === "config";
    document.getElementById("view-fleet").style.display = isConfig ? "none" : "block";
    document.getElementById("view-config").style.display = isConfig ? "block" : "none";
    if (isConfig) await loadConfigView();
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

async function syncMockPill() {
  try {
    const settings = await api("/api/config/settings");
    document.getElementById("mock-pill").style.display = settings.mock_data ? "flex" : "none";
  } catch (e) {
    /* non-fatal — pill just stays hidden until the Config tab is opened */
  }
}

tick();
syncMockPill();
renderUpdatesRows();
setInterval(tick, 15000);
setInterval(renderUpdatesRows, 5 * 60 * 1000);
