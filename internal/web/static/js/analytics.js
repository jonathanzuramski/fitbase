// Analytics page: a period against another, the years against each other,
// and every marker over time. Charts use uPlot on the shared theme from
// charts.js (baseOpts, containerWidth, chartHeight, watchResize, COLORS,
// POWER_ZONE_COLORS, HR_ZONE_COLORS, IMPERIAL, renderFitnessChart).
//
// The page arrives with its first data inline (initAnalytics). Changing a
// period, scope or range fetches /api/analytics/* and redraws in place; the
// URL keeps the same parameters, so a reload or a bookmark shows the same view.

const AN = {
  a: "#60a5fa", // the period looked at
  b: "#f97316", // what it is compared against (dashed), as last year is in year over year
  outdoor: "#cbd5e1", // the mix bars: neutral, so they never read as the two periods
  ftp: "#e2e8f0",
  indoor: "#94a3b8", // indoor rides and lines, always dashed
  estimate: "#facc15",
  allTime: "#64748b",
  d90: "#60a5fa",
  d28: "#f97316",
  ef: "#34d399",
  hr: "#f87171",
  power: "#60a5fa",
  dec: "#f87171",
  cadence: "#a78bfa",
  done: "#3b82f6",
  planned: "#64748b",
  up: "#3b82f6",
  down: "#64748b",
  cycling: "#3b82f6",
  other: "#64748b",
  recent: "#f97316",
  earlier: "#64748b",
  ref: "rgba(148,163,184,0.45)",
  years: ["#3b82f6", "#f97316", "#94a3b8", "#64748b", "#475569"],
  split: ["#60a5fa", "#facc15", "#f97316"],
};

const DAY = 86400;
const MINUS = "−";

// ── Formatting ───────────────────────────────────────────────────────────────

const esc = (s) =>
  String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);

const num = (v, digits = 0) =>
  Number(v).toLocaleString(undefined, { minimumFractionDigits: digits, maximumFractionDigits: digits });

function fmtHours(secs) {
  const mins = Math.round(Math.abs(secs) / 60);
  const h = Math.floor(mins / 60);
  const m = mins % 60;
  return h ? `${h}h ${String(m).padStart(2, "0")}m` : `${m}m`;
}

const distUnit = IMPERIAL ? "mi" : "km";
const toDist = (m) => (IMPERIAL ? m / 1609.344 : m / 1000);
const climbUnit = IMPERIAL ? "ft" : "m";
const toClimb = (m) => (IMPERIAL ? m * 3.28084 : m);

function fmtDist(m) {
  const v = toDist(Math.abs(m));
  return `${num(v, v >= 100 ? 0 : 1)} ${distUnit}`;
}

// FMT formats a value by the unit a comparison row names.
const FMT = {
  duration_secs: fmtHours,
  distance_meters: fmtDist,
  elevation_meters: (m) => `${num(toClimb(Math.abs(m)))} ${climbUnit}`,
  count: (v) => num(Math.abs(v)),
  tss: (v) => num(Math.abs(v)),
  kj: (v) => `${num(Math.abs(v))} kJ`,
  if: (v) => Math.abs(v).toFixed(2),
  watts: (v) => `${num(Math.abs(v))} W`,
  bpm: (v) => `${num(Math.abs(v))} bpm`,
  ef: (v) => Math.abs(v).toFixed(2),
  pct: (v) => `${num(Math.abs(v), 1)}%`,
  rpe: (v) => Math.abs(v).toFixed(1),
};

// dayTs is a YYYY-MM-DD day at local midnight, in unix seconds, so points land
// on the right calendar day whatever the browser's timezone.
function dayTs(s) {
  const [y, m, d] = s.slice(0, 10).split("-").map(Number);
  return new Date(y, m - 1, d).getTime() / 1000;
}

function fmtDay(s) {
  const [y, m, d] = s.slice(0, 10).split("-").map(Number);
  return new Date(y, m - 1, d).toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" });
}

// bucketMid is the middle of a week or month bucket starting on day s, where
// its average is plotted.
function bucketMid(s, bucket) {
  const t = dayTs(s);
  if (bucket === "week") return t + 3.5 * DAY;
  const [y, m] = s.split("-").map(Number);
  const days = new Date(y, m, 0).getDate();
  return t + (days / 2) * DAY;
}

const durLabel = (s) => (s < 60 ? `${s}s` : s < 3600 ? `${s / 60}m` : `${s / 3600}h`);

// curveTick labels a power-curve duration; phones label every other one.
const curveTick = (durations, i, k) => (isMobile() && k % 2 ? "" : durLabel(durations[i]));

// ── Chart plumbing ───────────────────────────────────────────────────────────

// fresh empties a chart container and returns a new element to draw into.
// Each redraw gets its own element, so the resize observer of the chart it
// replaces watches a detached node and stops.
function fresh(id) {
  const el = document.getElementById(id);
  if (!el) return null;
  el.textContent = "";
  el.classList.remove("an-empty");
  const host = document.createElement("div");
  el.appendChild(host);
  return host;
}

function empty(id, text) {
  const el = document.getElementById(id);
  if (!el) return;
  el.textContent = text;
  el.classList.add("an-empty");
}

// align lays several [t, v] point lists on one shared x axis. Where a list
// has no point, its column is undefined, which uPlot draws a line across; a
// point whose value is null (a bucket with no rides) breaks the line instead,
// so a line never bridges months that had nothing to measure.
function align(lists) {
  const xs = [...new Set(lists.flatMap((l) => l.map((p) => p[0])))].sort((a, b) => a - b);
  const at = new Map(xs.map((t, i) => [t, i]));
  const cols = lists.map((l) => {
    const col = new Array(xs.length).fill(undefined);
    for (const [t, v] of l) col[at.get(t)] = v;
    return col;
  });
  return { xs, cols };
}

// legendFor adds the phone legend under a chart: uPlot's own legend (with
// values on hover) is hidden on phones by baseOpts.
function legendFor(host, defs) {
  const items = defs.filter((d) => d.label && d.legend !== false);
  if (items.length < 2) return;
  const div = document.createElement("div");
  div.className = "an-legend";
  div.innerHTML = items
    .map((d) => {
      const kind = d.kind === "points" ? "dot" : d.kind === "bars" ? "box" : "line";
      const style = kind === "line" ? `border-color:${d.color}${d.dash ? ";border-top-style:dashed" : ""}` : `background:${d.color}`;
      return `<span><i class="an-key an-key-${kind}" style="${style}"></i>${esc(d.label)}</span>`;
    })
    .join("");
  host.parentElement.appendChild(div);
}

const barsPath = () => uPlot.paths.bars({ size: [0.72, 56], align: 0 });

// series turns a definition into a uPlot series. kind: line (default),
// points (a scatter), bars, step.
function series(d) {
  const fmt = d.fmt || ((v) => v);
  const value = d.value || ((_u, v) => (v == null ? "—" : fmt(v)));
  const s = { label: d.label, stroke: d.color, width: d.width ?? 2, spanGaps: false, value, scale: d.scale || "y" };
  if (d.dash) s.dash = d.dash;
  switch (d.kind) {
    case "points":
      s.paths = () => null;
      s.width = 1;
      s.points = { show: true, size: d.size || 6, stroke: d.color, fill: d.fill || d.color + "88", width: 1 };
      break;
    case "bars":
      s.paths = barsPath();
      s.fill = d.fill ?? d.color;
      s.width = d.width ?? 0;
      s.points = { show: false };
      s.spanGaps = false;
      break;
    case "step":
      s.paths = uPlot.paths.stepped({ align: 1 });
      s.spanGaps = true; // an FTP holds until it changes
      s.points = { show: false };
      break;
    default:
      s.points = d.markers ? { show: true, size: 5, fill: d.color } : { show: false };
  }
  return s;
}

// refLines draws dashed horizontal reference lines (5% decoupling, +8 ramp,
// all-time bests) behind the series.
function refLinesHook(refs) {
  return (u) => {
    const { ctx } = u;
    const { left, top, width, height } = u.bbox;
    ctx.save();
    ctx.beginPath();
    ctx.rect(left, top, width, height);
    ctx.clip();
    for (const r of refs) {
      if (r.y == null) continue;
      const y = u.valToPos(r.y, r.scale || "y", true);
      ctx.strokeStyle = r.color || AN.ref;
      ctx.lineWidth = 1;
      ctx.setLineDash([4, 4]);
      ctx.beginPath();
      ctx.moveTo(left, y);
      ctx.lineTo(left + width, y);
      ctx.stroke();
    }
    ctx.restore();
  };
}

// chart draws a uPlot chart of defs (each with its column in data) into host.
// opts: height, time (x is time; default true), y (scale overrides), yFmt
// (axis labels), xAxis (axis overrides), refs, xValue (legend x value).
function chart(host, xs, defs, opts = {}) {
  const height = opts.height || 220;
  const base = baseOpts(containerWidth(host), chartHeight(height));
  // Right padding: room for the last x label, which uPlot centers on the edge.
  base.padding = [8, 18, 0, 0];
  const yAxis = { ...base.axes[1] };
  if (opts.yFmt) yAxis.values = (_u, splits) => splits.map(opts.yFmt);
  const u = new uPlot(
    {
      ...base,
      series: [{ value: opts.xValue }, ...defs.map(series)],
      scales: { x: { time: opts.time !== false, ...(opts.x || {}) }, y: { auto: true, ...(opts.y || {}) } },
      axes: [{ ...base.axes[0], ...(opts.xAxis || {}) }, yAxis],
      hooks: opts.refs ? { drawAxes: [refLinesHook(opts.refs)] } : {},
    },
    [xs, ...defs.map((d) => d.data)],
    host,
  );
  watchResize(host, u, height);
  legendFor(host, defs);
  return u;
}

// stacked draws stacked bars: each band's bar runs from zero to the running
// total, drawn tallest first, so each shows between its neighbours. Legend
// values stay the band's own.
function stacked(host, xs, bands, opts = {}) {
  const totals = bands.map((_b, k) => xs.map((_x, i) => {
    let sum = 0;
    let any = false;
    for (let j = 0; j <= k; j++) {
      const v = bands[j].data[i];
      if (v != null) {
        sum += v;
        any = true;
      }
    }
    return any && sum > 0 ? sum : null;
  }));
  const defs = bands
    .map((b, k) => ({
      ...b,
      kind: "bars",
      data: totals[k],
      value: (_u, _v, _si, i) => (i == null || b.data[i] == null ? "—" : (b.fmt || ((v) => v))(b.data[i])),
    }))
    .reverse();
  return chart(host, xs, defs, { ...opts, y: { range: (_u, _min, max) => [0, opts.yMax || Math.max(max, 1)] } });
}

// ── State and fetching ───────────────────────────────────────────────────────

const state = { compare: null, trends: null, years: null, zones: [], yoyMetric: "distance_meters", loadMetric: "tss", loadBucket: "weeks", curveKg: false, bestKg: false };

function formParams() {
  const f = document.getElementById("cmp-form");
  const p = new URLSearchParams();
  const v = (n) => f.elements[n].value;
  p.set("period", v("period"));
  if (v("period") === "custom") {
    p.set("from", v("from"));
    p.set("to", v("to"));
  } else if (v("date")) {
    p.set("date", v("date"));
  }
  p.set("against", v("against"));
  if (v("against") === "custom") {
    p.set("against_from", v("against_from"));
    p.set("against_to", v("against_to"));
  }
  p.set("scope", v("scope"));
  return p;
}

function syncURL() {
  const p = formParams();
  if (state.trends) p.set("range", state.trends.range);
  history.replaceState(null, "", `${location.pathname}?${p}`);
}

async function fetchData(path, params) {
  const res = await fetch(`${path}?${params}`, { headers: { Accept: "application/json" } });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body.error || res.statusText);
  return body.data;
}

let compareSeq = 0;
async function refreshCompare() {
  const params = formParams();
  const seq = ++compareSeq;
  const body = document.getElementById("cmp-body");
  body.classList.add("an-loading");
  try {
    const scopeChanged = state.compare && params.get("scope") !== state.compare.scope;
    const c = await fetchData("/api/analytics/compare", params);
    if (seq !== compareSeq) return; // a newer request is on its way
    state.compare = c;
    renderCompare(c);
    if (scopeChanged || (state.years && state.years.scope !== c.scope)) {
      state.years = await fetchData("/api/analytics/years", new URLSearchParams({ scope: c.scope }));
      renderYears();
    }
    syncURL();
  } catch (err) {
    body.innerHTML = `<p class="an-error">Couldn't compare those periods: ${esc(err.message)}</p>`;
  } finally {
    if (seq === compareSeq) body.classList.remove("an-loading");
  }
}

async function refreshTrends(range) {
  const grid = document.getElementById("trends-grid");
  grid.classList.add("an-loading");
  try {
    state.trends = await fetchData("/api/analytics/trends", new URLSearchParams({ range }));
    renderTrends(false);
    syncURL();
  } catch (err) {
    alert("Couldn't load the trends: " + err.message);
  } finally {
    grid.classList.remove("an-loading");
  }
}

// ── Controls ─────────────────────────────────────────────────────────────────

function wireControls() {
  const f = document.getElementById("cmp-form");
  const showFor = () => {
    const period = f.elements.period.value;
    f.querySelectorAll("[data-show]").forEach((el) => {
      const want = el.dataset.show;
      el.hidden = want === "calendar" ? period === "custom" : want === "custom" ? period !== "custom" : f.elements.against.value !== "custom";
    });
    const prev = f.elements.against.querySelector('option[value="previous"]');
    prev.textContent = `the ${period === "custom" ? "days" : period} before`;
    const year = f.elements.against.querySelector('option[value="year"]');
    year.textContent = period === "week" ? "52 weeks earlier" : period === "custom" ? "the same days a year earlier" : `the same ${period} a year earlier`;
    stepState();
  };
  // stepState disables › when the next period would start after today.
  const stepState = () => {
    const next = f.querySelector('[data-step="1"]');
    const d = f.elements.date.value;
    if (!next || !d) return;
    next.disabled = shiftDate(d, f.elements.period.value, 1, true) > f.elements.date.max;
  };

  f.querySelectorAll("[data-period]").forEach((btn) =>
    btn.addEventListener("click", () => {
      f.querySelectorAll("[data-period]").forEach((b) => b.classList.toggle("active", b === btn));
      f.elements.period.value = btn.dataset.period;
      // A new period type opens on the end of the one shown: from this month to
      // date, "week" is this week, not the week the month began in.
      if (btn.dataset.period !== "custom" && state.compare) f.elements.date.value = state.compare.a.last_day;
      if (btn.dataset.period === "custom" && state.compare) {
        f.elements.from.value = state.compare.a.first_day;
        f.elements.to.value = state.compare.a.last_day;
      }
      showFor();
      refreshCompare();
    }),
  );
  f.querySelectorAll("[data-step]").forEach((btn) =>
    btn.addEventListener("click", () => {
      f.elements.date.value = shiftDate(f.elements.date.value || f.elements.date.max, f.elements.period.value, Number(btn.dataset.step), false);
      stepState();
      refreshCompare();
    }),
  );
  f.elements.against.addEventListener("change", () => {
    if (f.elements.against.value === "custom" && state.compare) {
      f.elements.against_from.value = state.compare.b.first_day;
      f.elements.against_to.value = state.compare.b.last_day;
    }
    showFor();
    refreshCompare();
  });
  for (const name of ["date", "from", "to", "against_from", "against_to", "scope"]) {
    f.elements[name].addEventListener("change", () => {
      stepState();
      const e = f.elements;
      if (e.period.value === "custom" && (!e.from.value || !e.to.value)) return;
      if (e.against.value === "custom" && (!e.against_from.value || !e.against_to.value)) return;
      refreshCompare();
    });
  }
  f.addEventListener("submit", (e) => e.preventDefault());
  showFor();

  document.querySelectorAll("[data-range]").forEach((btn) =>
    btn.addEventListener("click", () => {
      document.querySelectorAll("[data-range]").forEach((b) => b.classList.toggle("active", b === btn));
      refreshTrends(btn.dataset.range);
    }),
  );
  const toggle = (attr, key, redraw) =>
    document.querySelectorAll(`[${attr}]`).forEach((btn) =>
      btn.addEventListener("click", () => {
        document.querySelectorAll(`[${attr}]`).forEach((b) => b.classList.toggle("active", b === btn));
        state[key] = btn.getAttribute(attr);
        redraw();
      }),
    );
  toggle("data-yoy", "yoyMetric", renderYears);
  toggle("data-load-metric", "loadMetric", renderLoad);
  toggle("data-load-bucket", "loadBucket", renderLoad);
  document.querySelectorAll("[data-curve-unit]").forEach((btn) =>
    btn.addEventListener("click", () => {
      document.querySelectorAll("[data-curve-unit]").forEach((b) => b.classList.toggle("active", b === btn));
      state.curveKg = btn.dataset.curveUnit === "wkg";
      renderCurve();
    }),
  );
  document.querySelectorAll("[data-best-unit]").forEach((btn) =>
    btn.addEventListener("click", () => {
      document.querySelectorAll("[data-best-unit]").forEach((b) => b.classList.toggle("active", b === btn));
      state.bestKg = btn.dataset.bestUnit === "wkg";
      renderBestEfforts();
    }),
  );
}

// shiftDate moves a YYYY-MM-DD day by n weeks, months or years. With
// toStart, it returns the first day of the period n away instead.
function shiftDate(s, period, n, toStart) {
  const [y, m, d] = s.split("-").map(Number);
  let t;
  if (period === "week") {
    t = new Date(y, m - 1, d + 7 * n);
    if (toStart) t.setDate(t.getDate() - ((t.getDay() + 6) % 7));
  } else if (period === "year") {
    t = new Date(y + n, toStart ? 0 : m - 1, toStart ? 1 : Math.min(d, 28));
  } else {
    t = new Date(y, m - 1 + n, toStart ? 1 : Math.min(d, 28));
  }
  return `${t.getFullYear()}-${String(t.getMonth() + 1).padStart(2, "0")}-${String(t.getDate()).padStart(2, "0")}`;
}

// ── Compare ──────────────────────────────────────────────────────────────────

const GROUPS = { volume: "volume", intensity: "intensity", efficiency: "efficiency", heart_rate: "heart rate", power: "power", rpe: "how it felt", mix: "mix" };

function renderCompare(c) {
  const sport = c.scope === "all" ? "all sports" : c.scope;
  document.getElementById("cmp-subtitle").textContent = `${c.a.label} against ${c.b.label} · ${sport}`;
  const body = document.getElementById("cmp-body");
  const periods = `
    <div class="an-periods">
      <div class="an-period"><i class="an-key an-key-line" style="border-color:${AN.a}"></i><strong>${esc(c.a.label)}</strong>
        <span class="muted">${c.a.days} day${c.a.days === 1 ? "" : "s"}${c.to_date ? ", to date" : ""}</span></div>
      <div class="an-period"><i class="an-key an-key-line" style="border-color:${AN.b};border-top-style:dashed"></i><strong>${esc(c.b.label)}</strong>
        <span class="muted">${c.b.days} day${c.b.days === 1 ? "" : "s"}</span></div>
    </div>`;
  if (c.a.activities === 0 && c.b.activities === 0) {
    body.innerHTML = periods + `<p class="an-empty">No ${c.scope === "all" ? "activities" : esc(c.scope)} in either period.</p>`;
    return;
  }
  // Wide screens: the table on the left, the pictures of it on the right.
  body.innerHTML = periods + `
    <div class="an-cmp-layout">
      <div class="an-cmp-table">${compareTable(c)}</div>
      <div class="an-cmp-grid">
        <div class="an-panel"><h3 class="an-sub">power curve</h3><div id="cmp-curve" class="an-chart"></div></div>
        <div class="an-panel"><h3 class="an-sub">time in zones</h3><div id="cmp-zones"></div></div>
        <div class="an-panel"><h3 class="an-sub">mix</h3><div id="cmp-mix"></div></div>
        <div class="an-panel"><h3 class="an-sub">how hard it felt <span class="muted">RPE against TSS</span></h3><div id="cmp-rpe" class="an-chart"></div></div>
      </div>
    </div>`;
  renderCompareCurve(c);
  renderCompareZones(c);
  renderCompareMix(c);
  renderCompareRPE(c);
}

function compareTable(c) {
  const ridesWord = c.scope === "cycling" ? "rides" : "activities";
  const markerOf = (stats, key) => {
    const m = /^(hr_at_power|power_at_hr)_(outdoor|indoor)$/.exec(key);
    if (!m) return null;
    const field = m[1] === "hr_at_power" ? "hr_at_power_bpm" : "power_at_hr_watts";
    return stats[field]?.[m[2]] || null;
  };
  const cell = (row, v, n, stats) => {
    if (v == null) return `<td class="muted">—</td>`;
    let sub = "";
    if (n != null) {
      const marker = markerOf(stats, row.key);
      const at = marker?.matched_at != null ? ` at ${row.key.startsWith("hr_") ? FMT.watts(marker.matched_at) : FMT.bpm(marker.matched_at)}` : "";
      sub = `<span class="an-n">${n} ${row.key === "avg_rpe" ? "rated" : n === 1 ? "ride" : "rides"}${at}</span>`;
    }
    return `<td>${FMT[row.unit](v)}${sub}</td>`;
  };
  let html = `<div class="an-table-wrap"><table class="an-table">
    <thead><tr><th></th><th>${esc(c.a.label)}</th><th>${esc(c.b.label)}</th><th>change</th></tr></thead><tbody>`;
  let group = "";
  for (const row of c.rows) {
    if (row.group !== group) {
      group = row.group;
      html += `<tr class="an-group"><th colspan="4">${GROUPS[group] || group}</th></tr>`;
    }
    const label = row.key === "activities" ? ridesWord : row.label;
    html += `<tr><th scope="row">${esc(label)}</th>${cell(row, row.a, row.rides_a, c.a)}${cell(row, row.b, row.rides_b, c.b)}${deltaCell(row)}</tr>`;
  }
  return html + `</tbody></table></div>
    <p class="an-note muted">Green and red only where better is plain: power bests, efficiency, heart rate and power at matched effort, decoupling. More time, TSS or intensity is neither, and changes under 2% are left uncoloured.</p>`;
}

function deltaCell(row) {
  if (row.delta == null) return `<td class="an-delta muted">—</td>`;
  const d = row.delta;
  if (d === 0) return `<td class="an-delta muted">same</td>`;
  // Changes inside a ride-to-ride wobble get no colour: under 2%, or under
  // half a point for a percentage.
  const small = row.unit === "pct" ? Math.abs(d) < 0.5 : row.delta_pct != null && Math.abs(row.delta_pct) < 2;
  let cls = "";
  let verdict = "";
  if (row.better && !small) {
    const good = (row.better === "higher") === d > 0;
    cls = good ? "an-good" : "an-bad";
    verdict = good ? " (better)" : " (worse)";
  }
  const sign = d > 0 ? "+" : MINUS;
  let val = row.unit === "pct" ? `${num(Math.abs(d), 1)} pts` : FMT[row.unit](d);
  if (Math.abs(d) < 1 && (row.unit === "watts" || row.unit === "bpm")) val = `${num(Math.abs(d), 1)} ${row.unit === "watts" ? "W" : "bpm"}`;
  const pct = row.delta_pct != null ? `<span class="an-pct">${row.delta_pct > 0 ? "+" : MINUS}${num(Math.abs(row.delta_pct), Math.abs(row.delta_pct) < 10 ? 1 : 0)}%</span>` : "";
  return `<td class="an-delta ${cls}" title="${sign}${esc(val)}${verdict}"><span class="an-arrow" aria-hidden="true">${d > 0 ? "▲" : "▼"}</span>${sign}${val}${pct}</td>`;
}

function renderCompareCurve(c) {
  const durations = c.curve_durations_secs;
  if (!c.a.curve_watts.some((v) => v != null) && !c.b.curve_watts.some((v) => v != null)) {
    empty("cmp-curve", c.scope === "cycling" || c.scope === "all" ? "No power data in either period." : "Power is compared for cycling only.");
    return;
  }
  const host = fresh("cmp-curve");
  const xs = durations.map((_d, i) => i);
  chart(
    host,
    xs,
    [
      { label: c.a.label, data: c.a.curve_watts, color: AN.a, fmt: FMT.watts },
      { label: c.b.label, data: c.b.curve_watts, color: AN.b, dash: [6, 4], fmt: FMT.watts },
    ],
    {
      height: 220,
      time: false,
      x: { range: [-0.3, xs.length - 0.7] },
      xAxis: { splits: xs, values: (_u, s) => s.map((i, k) => curveTick(durations, i, k)), ticks: { show: false } },
      xValue: (_u, i) => (i == null ? "—" : durLabel(durations[i])),
    },
  );
}

// zoneBar renders a zone distribution as the intensity split's bars.
function zoneBar(label, secs, colors, names) {
  const total = secs.reduce((a, b) => a + b, 0);
  if (!total) return `<div class="zsplit"><div class="zsplit-head"><span>${esc(label)}</span></div><p class="muted an-small">No data.</p></div>`;
  const segs = secs
    .map((s, i) => (s > 0 ? `<span class="zseg" style="width:${(s / total) * 100}%;background:${colors[i]}" title="${esc(names[i])} · ${Math.round((s / total) * 100)}% · ${fmtHours(s)}"></span>` : ""))
    .join("");
  return `<div class="zsplit"><div class="zsplit-head"><span>${esc(label)}</span><span class="muted">${fmtHours(total)}</span></div><div class="zsplit-bar">${segs}</div></div>`;
}

// legendRow names the colours of a zone bar.
function legendRow(names, colors) {
  return `<div class="zsplit-legend">${names.map((n, i) => `<span><i style="background:${colors[i]}"></i>${esc(n)}</span>`).join("")}</div>`;
}

function renderCompareZones(c) {
  const el = document.getElementById("cmp-zones");
  const names = state.zones.map((z) => `Z${z.N} ${z.Name}`);
  const split = (s) =>
    s.intensity_split
      ? `<div class="zsplit-summary"><span>easy <strong>${Math.round(s.intensity_split.easy_pct)}%</strong></span><span>middle <strong>${Math.round(s.intensity_split.middle_pct)}%</strong></span><span>hard <strong>${Math.round(s.intensity_split.hard_pct)}%</strong></span></div>`
      : "";
  const hasPower = c.a.power_zone_secs.some((v) => v) || c.b.power_zone_secs.some((v) => v);
  const hasHR = c.a.hr_zone_secs.some((v) => v) || c.b.hr_zone_secs.some((v) => v);
  let html = "";
  if (hasPower) {
    html += `<div class="an-zone-group"><div class="an-small muted">power zones (cycling)</div>`;
    html += zoneBar(c.a.label, c.a.power_zone_secs, POWER_ZONE_COLORS, names) + split(c.a);
    html += zoneBar(c.b.label, c.b.power_zone_secs, POWER_ZONE_COLORS, names) + split(c.b);
    html += legendRow(names, POWER_ZONE_COLORS) + `</div>`;
  }
  if (hasHR) {
    html += `<div class="an-zone-group"><div class="an-small muted">heart-rate zones</div>`;
    html += zoneBar(c.a.label, c.a.hr_zone_secs, HR_ZONE_COLORS, HR_ZONE_NAMES);
    html += zoneBar(c.b.label, c.b.hr_zone_secs, HR_ZONE_COLORS, HR_ZONE_NAMES);
    html += legendRow(HR_ZONE_NAMES, HR_ZONE_COLORS) + `</div>`;
  }
  el.innerHTML = html || `<p class="an-empty">No power or heart-rate zone time in either period.</p>`;
}

function renderCompareMix(c) {
  const el = document.getElementById("cmp-mix");
  const env = (s) => {
    const total = s.indoor.duration_secs + s.outdoor.duration_secs;
    if (!total) return `<p class="muted an-small">Nothing in ${esc(s.label)}.</p>`;
    const inPct = (s.indoor.duration_secs / total) * 100;
    return `<div class="zsplit"><div class="zsplit-head"><span>${esc(s.label)}</span><span class="muted">${Math.round(100 - inPct)}% outdoors</span></div>
      <div class="zsplit-bar"><span class="zseg" style="width:${100 - inPct}%;background:${AN.outdoor}" title="outdoor · ${fmtHours(s.outdoor.duration_secs)}"></span><span class="zseg" style="width:${inPct}%;background:${AN.indoor}" title="indoor · ${fmtHours(s.indoor.duration_secs)}"></span></div></div>`;
  };
  const sports = [...new Set([...c.a.sports, ...c.b.sports].map((s) => s.sport))];
  const of = (s, sport) => s.sports.find((x) => x.sport === sport);
  const rows = sports
    .map((sport) => {
      const a = of(c.a, sport);
      const b = of(c.b, sport);
      const cellOf = (x) => (x ? `${fmtHours(x.duration_secs)}<span class="an-n">${x.activities} · ${fmtDist(x.distance_meters)}</span>` : `<span class="muted">—</span>`);
      return `<tr><th scope="row">${esc(sport)}</th><td>${cellOf(a)}</td><td>${cellOf(b)}</td></tr>`;
    })
    .join("");
  el.innerHTML = `<div class="an-small muted">outdoors and indoors, by time</div>${env(c.a)}${env(c.b)}
    <div class="an-small muted an-gap">every sport, whatever the filter</div>
    <table class="an-table an-table-compact"><thead><tr><th></th><th>${esc(c.a.label)}</th><th>${esc(c.b.label)}</th></tr></thead><tbody>${rows}</tbody></table>
    <div class="an-legend an-legend-always"><span><i class="an-key an-key-box" style="background:${AN.outdoor}"></i>outdoor</span><span><i class="an-key an-key-box" style="background:${AN.indoor}"></i>indoor</span></div>`;
}

function renderCompareRPE(c) {
  const pts = [
    ...c.a.rpe.filter((p) => p.tss != null).map((p) => [p.tss, p.rpe, 0]),
    ...c.b.rpe.filter((p) => p.tss != null).map((p) => [p.tss, p.rpe, 1]),
  ].sort((x, y) => x[0] - y[0]);
  if (!pts.length) {
    empty("cmp-rpe", "No rated rides in either period. Rate a ride on its page (how did it feel?).");
    return;
  }
  const host = fresh("cmp-rpe");
  chart(
    host,
    pts.map((p) => p[0]),
    [
      { label: c.a.label, kind: "points", data: pts.map((p) => (p[2] === 0 ? p[1] : null)), color: AN.a, size: 8 },
      { label: c.b.label, kind: "points", data: pts.map((p) => (p[2] === 1 ? p[1] : null)), color: AN.b, size: 8 },
    ],
    { height: 200, time: false, y: { range: [0, 10.5] }, xValue: (_u, v) => (v == null ? "—" : `${Math.round(v)} TSS`) },
  );
}

// ── Year over year ───────────────────────────────────────────────────────────

const MONTH_STARTS = [1, 32, 61, 92, 122, 153, 183, 214, 245, 275, 306, 336]; // on a leap-year calendar
const MONTH_NAMES = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

function renderYears() {
  const y = state.years;
  if (!y) return;
  document.getElementById("yoy-subtitle").textContent = y.scope === "all" ? "all sports" : y.scope;
  if (!y.years.length) {
    empty("an-yoy", "No activities in the last five years yet.");
    return;
  }
  const metric = state.yoyMetric;
  const conv = {
    distance_meters: [toDist, (v) => `${num(v)} ${distUnit}`],
    duration_secs: [(s) => s / 3600, (v) => `${num(v)} h`],
    tss: [(v) => v, (v) => `${num(v)} TSS`],
    elevation_gain_meters: [toClimb, (v) => `${num(v)} ${climbUnit}`],
  }[metric];
  const xs = Array.from({ length: 366 }, (_v, i) => i + 1);
  const years = [...y.years].reverse(); // newest first: it gets the first colour
  const defs = years.map((yr, k) => {
    const daily = new Array(367).fill(0);
    for (const d of yr.days) daily[d.day_of_year] += d[metric];
    const last = yr.year === y.today.year ? y.today.day_of_year : 366;
    let sum = 0;
    const data = xs.map((doy) => {
      sum += daily[doy];
      return doy <= last ? Math.round(conv[0](sum) * 10) / 10 : null;
    });
    return { label: String(yr.year), data, color: AN.years[k] || AN.years[AN.years.length - 1], width: k === 0 ? 2.5 : 1.5, fmt: conv[1] };
  });
  const host = fresh("an-yoy");
  chart(host, xs, defs, {
    height: 260,
    time: false,
    x: { range: [1, 366] },
    xAxis: { splits: MONTH_STARTS, values: (_u, s) => s.map((d) => MONTH_NAMES[MONTH_STARTS.indexOf(d)] || "") },
    xValue: (_u, d) => {
      if (d == null) return "—";
      const t = new Date(2000, 0, d);
      return t.toLocaleDateString(undefined, { month: "short", day: "numeric" });
    },
    yFmt: (v) => num(v),
  });
}

// ── Trends ───────────────────────────────────────────────────────────────────

function renderTrends(first) {
  const t = state.trends;
  const range = { "3m": "3 months", "6m": "6 months", "1y": "1 year", "2y": "2 years", all: "everything" }[t.range];
  document.getElementById("trends-subtitle").textContent = `${range}, by ${t.bucket} · ${fmtDay(t.first_day)} – ${fmtDay(t.last_day)}`;
  renderFTP();
  if (first) renderCurve(); // the curve doesn't depend on the range
  renderFitness();
  renderRamp();
  renderLoad();
  renderBestEfforts();
  renderEF();
  renderMatched();
  renderDecoupling();
  renderCadence();
  renderIFBands();
  renderSplit();
  renderConsistency();
  renderRPE();
}

function renderFTP() {
  const d = state.trends.ftp;
  if (!d.general.length && !d.estimate.length && !d.indoor.length) {
    empty("an-ftp", "No FTP history or power data in this range yet.");
    return;
  }
  const lists = [d.general, d.indoor, d.estimate, d.indoor_estimate].map((l) => l.map((p) => [p.t, p.w]));
  const { xs, cols } = align(lists);
  const defs = [{ label: "FTP", kind: "step", data: cols[0], color: AN.ftp, fmt: FMT.watts }];
  if (d.indoor.length) defs.push({ label: "indoor FTP", kind: "step", data: cols[1], color: AN.indoor, dash: [6, 4], width: 1.5, fmt: FMT.watts });
  defs.push({ label: "estimate", kind: "points", data: cols[2], color: AN.estimate, fill: AN.estimate, size: 6, fmt: FMT.watts });
  if (d.indoor_estimate.length) defs.push({ label: "indoor estimate", kind: "points", data: cols[3], color: AN.indoor, fill: AN.indoor, size: 6, fmt: FMT.watts });
  chart(fresh("an-ftp"), xs, defs, { height: 240 });
}

function renderCurve() {
  const t = state.trends;
  const c = t.power_curve;
  if (!c.all_time.some((v) => v != null)) {
    empty("an-curve", "No power data yet.");
    return;
  }
  const kg = state.curveKg && t.weight_kg > 0;
  const scale = (arr) => arr.map((v) => (v == null ? null : kg ? Math.round((v / t.weight_kg) * 100) / 100 : v));
  const fmt = (v) => (kg ? `${v.toFixed(2)} W/kg` : FMT.watts(v));
  const xs = c.durations.map((_d, i) => i);
  chart(
    fresh("an-curve"),
    xs,
    [
      { label: "all-time", data: scale(c.all_time), color: AN.allTime, fmt },
      { label: "last 90 days", data: scale(c.days_90), color: AN.d90, fmt },
      { label: "last 28 days", data: scale(c.days_28), color: AN.d28, fmt },
    ],
    {
      height: 240,
      time: false,
      x: { range: [-0.3, xs.length - 0.7] },
      xAxis: { splits: xs, values: (_u, s) => s.map((i, k) => curveTick(c.durations, i, k)), ticks: { show: false } },
      xValue: (_u, i) => (i == null ? "—" : durLabel(c.durations[i])),
    },
  );
}

let fitnessSeq = 0;
function renderFitness() {
  const data = state.trends.fitness;
  const el = document.getElementById("an-fitness");
  el.textContent = "";
  el.classList.remove("an-empty");
  if (!data.length || !data.some((p) => p.fitness > 0.05)) {
    empty("an-fitness", "No training load in this range yet.");
    return;
  }
  // renderFitnessChart draws into an element by id; a fresh one per redraw.
  const host = document.createElement("div");
  host.id = `an-fitness-chart-${++fitnessSeq}`;
  el.appendChild(host);
  renderFitnessChart(host.id, data);
  legendFor(host, [
    { label: "fitness", color: COLORS.ctl },
    { label: "fatigue", color: COLORS.atl },
    { label: "form", color: COLORS.tsb },
  ]);
}

function renderRamp() {
  const ramp = state.trends.ramp;
  if (!ramp.some((r) => r.ctl_change !== 0)) {
    empty("an-ramp", "Not enough training load to show a ramp yet.");
    return;
  }
  const xs = ramp.map((r) => dayTs(r.week_start) + 3.5 * DAY);
  const fmt = (v) => `${v > 0 ? "+" : v < 0 ? MINUS : ""}${num(Math.abs(v), 1)}`;
  chart(
    fresh("an-ramp"),
    xs,
    [
      { label: "gained", kind: "bars", data: ramp.map((r) => (r.ctl_change >= 0 ? r.ctl_change : null)), color: AN.up, fmt },
      { label: "lost", kind: "bars", data: ramp.map((r) => (r.ctl_change < 0 ? r.ctl_change : null)), color: AN.down, fmt },
    ],
    { height: 150, refs: [{ y: 8, color: "rgba(249,115,22,0.6)" }, { y: 0 }], y: { range: (_u, min, max) => [Math.min(min, -6), Math.max(max, 10)] } },
  );
}

function renderLoad() {
  const t = state.trends;
  const buckets = state.loadBucket === "months" ? t.load_months : t.load_weeks;
  const bucket = state.loadBucket === "months" ? "month" : "week";
  if (!buckets.length || !buckets.some((b) => b.activities || b.planned_tss)) {
    empty("an-load", "No training in this range yet.");
    return;
  }
  const m = state.loadMetric;
  const pick = {
    tss: [(b) => b.tss, (b) => b.planned_tss, (v) => `${num(v)} TSS`],
    duration_secs: [(b) => b.duration_secs / 3600, (b) => b.planned_secs / 3600, (v) => `${num(v, 1)} h`],
    distance_meters: [(b) => toDist(b.distance_meters), null, (v) => `${num(v, v >= 100 ? 0 : 1)} ${distUnit}`],
  }[m];
  const xs = buckets.map((b) => bucketMid(b.start, bucket));
  const defs = [];
  if (pick[1] && buckets.some((b) => pick[1](b) > 0)) {
    defs.push({ label: "planned", kind: "bars", data: buckets.map((b) => pick[1](b) || null), color: AN.planned, fill: "transparent", width: 1.5, dash: [4, 3], fmt: pick[2] });
  }
  defs.push({ label: "done", kind: "bars", data: buckets.map((b) => pick[0](b) || null), color: AN.done, fill: AN.done + "cc", fmt: pick[2] });
  chart(fresh("an-load"), xs, defs, {
    height: 220,
    y: { range: (_u, _min, max) => [0, Math.max(max, 1)] },
    xValue: (_u, x) => (x == null ? "—" : bucket === "week" ? `week of ${new Date((x - 3.5 * DAY) * 1000).toLocaleDateString(undefined, { month: "short", day: "numeric" })}` : new Date(x * 1000).toLocaleDateString(undefined, { month: "short", year: "numeric" })),
  });
}

function renderBestEfforts() {
  const t = state.trends;
  const be = t.best_efforts;
  const el = document.getElementById("an-best");
  el.textContent = "";
  el.classList.remove("an-empty");
  if (!be.months.some((m) => m.watts.some((w) => w != null))) {
    empty("an-best", "No power data in this range yet.");
    return;
  }
  const kg = state.bestKg && t.weight_kg > 0;
  const conv = (w) => (w == null ? null : kg ? Math.round((w / t.weight_kg) * 100) / 100 : w);
  const fmt = (v) => (kg ? `${v.toFixed(2)} W/kg` : FMT.watts(v));
  const xs = be.months.map((m) => bucketMid(`${m.month}-01`, "month"));
  be.durations_secs.forEach((secs, k) => {
    const box = document.createElement("div");
    box.className = "an-multiple";
    const curveIdx = t.power_curve.durations.indexOf(secs);
    const best = curveIdx >= 0 ? conv(t.power_curve.all_time[curveIdx]) : null;
    box.innerHTML = `<h3 class="an-sub">${durLabel(secs)}${best != null ? ` <span class="muted">best ${fmt(best)}</span>` : ""}</h3>`;
    const host = document.createElement("div");
    box.appendChild(host);
    el.appendChild(box);
    const data = be.months.map((m) => conv(m.watts[k]));
    if (!data.some((v) => v != null)) {
      host.className = "an-empty";
      host.textContent = "No efforts this long.";
      return;
    }
    chart(host, xs, [{ label: "month best", data, color: AN.power, markers: true, fmt }], {
      height: 150,
      refs: best != null ? [{ y: best, color: "rgba(148,163,184,0.7)" }] : [],
      y: { range: (_u, min, max) => [Math.min(min, best ?? min) * 0.92, Math.max(max, best ?? max) * 1.04] },
      yFmt: (v) => (kg ? v.toFixed(1) : num(v)),
      xValue: (_u, x) => (x == null ? "—" : new Date(x * 1000).toLocaleDateString(undefined, { month: "short", year: "numeric" })),
    });
  });
}

// ridePoints spreads a day's rides a little apart, so two rides on one day are
// both drawn.
function ridePoints(filter, value) {
  const perDay = new Map();
  return state.trends.rides.filter(filter).map((r) => {
    const n = perDay.get(r.day) || 0;
    perDay.set(r.day, n + 1);
    return [dayTs(r.day) + 0.5 * DAY + n * 0.15 * DAY, value(r)];
  });
}

// bucketLine is one value per bucket, at its middle: null where the bucket had
// nothing to measure (the line breaks there), and no points at all when no
// bucket had anything.
function bucketLine(pick) {
  const pts = state.trends.buckets.map((b) => [bucketMid(b.start, state.trends.bucket), pick(b) ?? null]);
  return pts.some((p) => p[1] != null) ? pts : [];
}

function renderEF() {
  const out = ridePoints((r) => r.ef != null && !r.indoor, (r) => r.ef);
  const ind = ridePoints((r) => r.ef != null && r.indoor, (r) => r.ef);
  if (!out.length && !ind.length) {
    empty("an-ef", "No steady rides with power and heart rate in this range.");
    return;
  }
  const outAvg = bucketLine((b) => b.ef.outdoor?.value);
  const inAvg = bucketLine((b) => b.ef.indoor?.value);
  const { xs, cols } = align([out, ind, outAvg, inAvg]);
  const fmt = (v) => v.toFixed(2);
  const defs = [];
  if (out.length) defs.push({ label: "outdoor ride", kind: "points", data: cols[0], color: AN.ef, size: 5, fmt });
  if (ind.length) defs.push({ label: "indoor ride", kind: "points", data: cols[1], color: AN.indoor, size: 5, fmt });
  if (outAvg.length) defs.push({ label: `outdoor, ${state.trends.bucket} average`, data: cols[2], color: AN.ef, fmt });
  if (inAvg.length) defs.push({ label: `indoor, ${state.trends.bucket} average`, data: cols[3], color: AN.indoor, dash: [6, 4], fmt });
  chart(fresh("an-ef"), xs, defs, { height: 220, yFmt: (v) => v.toFixed(2) });
}

function renderMatched() {
  const t = state.trends;
  const ref = t.reference;
  const one = (id, title, field, refOut, refIn, color, fmt, unitWord) => {
    const out = bucketLine((b) => b[field].outdoor?.value);
    const ind = bucketLine((b) => b[field].indoor?.value);
    if (!out.length && !ind.length) {
      empty(id, `No steady rides with power and heart rate to match ${unitWord} in this range.`);
      return;
    }
    const el = document.getElementById(id);
    el.textContent = "";
    el.classList.remove("an-empty");
    const head = document.createElement("h3");
    head.className = "an-sub";
    head.textContent = title;
    el.appendChild(head);
    const host = document.createElement("div");
    el.appendChild(host);
    const { xs, cols } = align([out, ind]);
    const defs = [];
    if (out.length) defs.push({ label: `outdoor, near ${refOut}`, data: cols[0], color, markers: true, fmt });
    if (ind.length) defs.push({ label: `indoor, near ${refIn}`, data: cols[1], color: AN.indoor, dash: [6, 4], markers: true, fmt });
    chart(host, xs, defs, { height: 170 });
  };
  const w = (v) => (v == null ? "—" : `${v} W`);
  const bpm = (v) => (v == null ? "—" : `${v} bpm`);
  one("an-hr-at-power", "heart rate at the same power", "hr_at_power_bpm", w(ref.power_outdoor_watts), w(ref.power_indoor_watts), AN.hr, FMT.bpm, "power");
  one("an-power-at-hr", "power at the same heart rate", "power_at_hr_watts", bpm(ref.hr_outdoor_bpm), bpm(ref.hr_indoor_bpm), AN.power, FMT.watts, "heart rate");
}

function renderDecoupling() {
  const pts = ridePoints((r) => r.decoupling_pct != null, (r) => r.decoupling_pct);
  if (!pts.length) {
    empty("an-decoupling", "No long steady rides (90 minutes or more, with power and heart rate) in this range.");
    return;
  }
  const avg = bucketLine((b) => b.decoupling_pct?.value);
  const { xs, cols } = align([pts, avg]);
  const fmt = (v) => `${num(v, 1)}%`;
  chart(
    fresh("an-decoupling"),
    xs,
    [
      { label: "ride", kind: "points", data: cols[0], color: AN.dec, size: 6, fmt },
      { label: `${state.trends.bucket} average`, data: cols[1], color: AN.dec, fmt },
    ],
    { height: 220, refs: [{ y: 5, color: "rgba(148,163,184,0.7)" }], yFmt: (v) => `${v}%` },
  );
}

function renderCadence() {
  const out = bucketLine((b) => b.cadence_rpm.outdoor?.value);
  const ind = bucketLine((b) => b.cadence_rpm.indoor?.value);
  if (!out.length && !ind.length) {
    empty("an-cadence", "No cadence recorded on rides in this range.");
    return;
  }
  const { xs, cols } = align([out, ind]);
  const fmt = (v) => `${Math.round(v)} rpm`;
  const defs = [];
  if (out.length) defs.push({ label: "outdoor", data: cols[0], color: AN.cadence, markers: true, fmt });
  if (ind.length) defs.push({ label: "indoor", data: cols[1], color: AN.indoor, dash: [6, 4], markers: true, fmt });
  chart(fresh("an-cadence"), xs, defs, { height: 200 });
}

const IF_BANDS = ["under 0.65", "0.65–0.75", "0.75–0.85", "0.85–0.95", "0.95 and up"];

function renderIFBands() {
  const t = state.trends;
  if (!t.buckets.some((b) => b.if_bands.some((n) => n))) {
    empty("an-if", "No rides with power in this range.");
    return;
  }
  const xs = t.buckets.map((b) => bucketMid(b.start, t.bucket));
  const fmt = (v) => `${v} ride${v === 1 ? "" : "s"}`;
  stacked(
    fresh("an-if"),
    xs,
    IF_BANDS.map((label, k) => ({ label, data: t.buckets.map((b) => b.if_bands[k] || null), color: POWER_ZONE_COLORS[k], fmt })),
    { height: 220 },
  );
}

function renderSplit() {
  const t = state.trends;
  if (!t.buckets.some((b) => b.intensity_split)) {
    empty("an-split", "No power-zone time in this range.");
    return;
  }
  const xs = t.buckets.map((b) => bucketMid(b.start, t.bucket));
  const fmt = (v) => `${Math.round(v)}%`;
  const pick = (f) => t.buckets.map((b) => (b.intensity_split ? b.intensity_split[f] : null));
  stacked(
    fresh("an-split"),
    xs,
    [
      { label: "easy Z1–Z2", data: pick("easy_pct"), color: AN.split[0], fmt },
      { label: "middle Z3–Z4", data: pick("middle_pct"), color: AN.split[1], fmt },
      { label: "hard Z5+", data: pick("hard_pct"), color: AN.split[2], fmt },
    ],
    { height: 220, yMax: 100, yFmt: (v) => `${v}%` },
  );
}

function renderConsistency() {
  const t = state.trends;
  const c = t.consistency;
  const tile = (value, label) => `<div class="an-tile"><div class="an-tile-val">${value}</div><div class="an-tile-lbl">${label}</div></div>`;
  document.getElementById("an-streaks").innerHTML =
    tile(c.current_streak_days, `day${c.current_streak_days === 1 ? "" : "s"} in a row now`) +
    tile(c.longest_streak_days, `longest run of days${c.longest_streak_end ? `, to ${fmtDay(c.longest_streak_end)}` : ""}`) +
    tile(c.current_week_streak, `week${c.current_week_streak === 1 ? "" : "s"} in a row now`) +
    tile(c.avg_days_per_week != null ? num(c.avg_days_per_week, 1) : "—", "days a week, this range");
  if (!t.buckets.some((b) => b.cycling_days || b.other_days)) {
    empty("an-consistency", "No activities in this range.");
    return;
  }
  const xs = t.buckets.map((b) => bucketMid(b.start, t.bucket));
  const perWeek = (n, b) => (t.bucket === "week" ? n : Math.round((n / (b.days / 7)) * 10) / 10);
  const fmt = (v) => (t.bucket === "week" ? `${v} day${v === 1 ? "" : "s"}` : `${num(v, 1)} days/week`);
  stacked(
    fresh("an-consistency"),
    xs,
    [
      { label: "riding", data: t.buckets.map((b) => perWeek(b.cycling_days, b) || null), color: AN.cycling, fmt },
      { label: "other sports only", data: t.buckets.map((b) => perWeek(b.other_days, b) || null), color: AN.other, fmt },
    ],
    { height: 180, yMax: 7 },
  );
}

function renderRPE() {
  const t = state.trends;
  const cutoff = dayTs(t.last_day) - 27 * DAY;
  const pts = t.rides
    .filter((r) => r.rpe != null && r.tss != null)
    .map((r) => [r.tss, r.rpe, dayTs(r.day) >= cutoff])
    .sort((a, b) => a[0] - b[0]);
  if (!pts.length) {
    empty("an-rpe", "No rated rides in this range. Rate a ride on its page (how did it feel?).");
    return;
  }
  chart(
    fresh("an-rpe"),
    pts.map((p) => p[0]),
    [
      { label: "earlier", kind: "points", data: pts.map((p) => (p[2] ? null : p[1])), color: AN.earlier, size: 7 },
      { label: "last 4 weeks", kind: "points", data: pts.map((p) => (p[2] ? p[1] : null)), color: AN.recent, size: 8 },
    ],
    { height: 220, time: false, y: { range: [0, 10.5] }, xValue: (_u, v) => (v == null ? "—" : `${Math.round(v)} TSS`) },
  );
}

// ── Start ────────────────────────────────────────────────────────────────────

function initAnalytics(data) {
  state.compare = data.compare;
  state.trends = data.trends;
  state.years = data.years;
  state.zones = data.zones || [];
  wireControls();
  renderCompare(state.compare);
  renderYears();
  renderTrends(true);
}
