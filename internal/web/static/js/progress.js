// Progress page charts, on the shared uPlot theme from charts.js (baseOpts,
// containerWidth, watchResize, COLORS, CHART_THEME).

const PROGRESS_COLORS = {
  ftp: "#e2e8f0",
  indoor: "#94a3b8",
  estimate: "#facc15",
  allTime: "#64748b",
  d90: "#60a5fa",
  d28: "#f97316",
  ef: "#34d399",
  dec: "#f87171",
};

// FTP history as step lines (general, and indoor when there is one) with the
// rides' FTP estimate every two weeks as points.
function renderFTPChart(id, d) {
  const el = document.getElementById(id);
  if (!el || !d || (!d.general?.length && !d.estimate?.length)) {
    if (el) el.textContent = "No FTP history or power data yet.";
    return;
  }
  const all = [...(d.general || []), ...(d.indoor || []), ...(d.estimate || [])];
  const ts = [...new Set(all.map((p) => p.t))].sort((a, b) => a - b);
  const at = (pts) => {
    const byT = new Map((pts || []).map((p) => [p.t, p.w]));
    return ts.map((t) => (byT.has(t) ? byT.get(t) : null));
  };
  const stepped = uPlot.paths.stepped({ align: 1 });
  const series = [
    {},
    { label: "FTP", stroke: PROGRESS_COLORS.ftp, width: 2, paths: stepped, spanGaps: true, points: { show: false }, value: (_u, v) => (v == null ? "—" : v + " W") },
  ];
  const data = [ts, at(d.general)];
  if (d.indoor?.length) {
    series.push({ label: "indoor FTP", stroke: PROGRESS_COLORS.indoor, width: 1.5, dash: [6, 4], paths: stepped, spanGaps: true, points: { show: false }, value: (_u, v) => (v == null ? "—" : v + " W") });
    data.push(at(d.indoor));
  }
  series.push({
    label: "estimate",
    stroke: PROGRESS_COLORS.estimate,
    width: 1,
    dash: [2, 4],
    spanGaps: true,
    points: { show: true, size: 6, fill: PROGRESS_COLORS.estimate },
    value: (_u, v) => (v == null ? "—" : v + " W"),
  });
  data.push(at(d.estimate));

  const h = chartHeight(240);
  const base = baseOpts(containerWidth(el), h);
  const u = new uPlot({ ...base, series, scales: { x: { time: true }, y: { auto: true } }, axes: [base.axes[0], { ...base.axes[1], label: "W" }] }, data, el);
  watchResize(el, u, 240);
}

// Best power by duration: all-time, last 90 and last 28 days, in W or W/kg.
function renderCurveChart(id, c) {
  const el = document.getElementById(id);
  if (!el || !c || !c.durations?.length || !c.all_time?.some((v) => v != null)) {
    if (el) el.textContent = "No power data yet.";
    return;
  }
  const label = (s) => (s < 60 ? s + "s" : s < 3600 ? s / 60 + "m" : s / 3600 + "h");
  const xs = c.durations.map((_d, i) => i);
  let perKg = false;
  const scaled = (arr) => (arr || []).map((v) => (v == null ? null : perKg ? Math.round((v / c.weight_kg) * 100) / 100 : v));
  const unit = () => (perKg ? " W/kg" : " W");
  const fmt = (_u, v) => (v == null ? "—" : v + unit());
  const data = () => [xs, scaled(c.all_time), scaled(c.days_90), scaled(c.days_28)];

  const h = chartHeight(260);
  const base = baseOpts(containerWidth(el), h);
  const u = new uPlot(
    {
      ...base,
      series: [
        { value: (_u, i) => (i == null ? "—" : label(c.durations[i])) },
        { label: "all-time", stroke: PROGRESS_COLORS.allTime, width: 2, spanGaps: true, value: fmt },
        { label: "last 90 days", stroke: PROGRESS_COLORS.d90, width: 2, spanGaps: true, value: fmt },
        { label: "last 28 days", stroke: PROGRESS_COLORS.d28, width: 2, spanGaps: true, value: fmt },
      ],
      scales: { x: { time: false, range: [-0.3, xs.length - 0.7] }, y: { auto: true } },
      axes: [
        { ...base.axes[0], splits: xs, values: (_u, splits) => splits.map((i) => label(c.durations[i])), ticks: { show: false } },
        { ...base.axes[1] },
      ],
    },
    data(),
    el,
  );
  watchResize(el, u, 260);

  document.querySelectorAll("[data-curve-unit]").forEach((btn) => {
    btn.addEventListener("click", () => {
      perKg = btn.dataset.curveUnit === "wkg";
      document.querySelectorAll("[data-curve-unit]").forEach((b) => b.classList.toggle("active", b === btn));
      u.setData(data());
    });
  });
}

// Weekly EF (left axis) and decoupling % (right axis).
function renderDurabilityChart(id, points) {
  const el = document.getElementById(id);
  if (!el || !points?.some((p) => p.ef != null || p.dec != null)) {
    if (el) el.textContent = "Not enough steady rides with power and heart rate yet.";
    return;
  }
  const ts = points.map((p) => p.t);
  const h = chartHeight(220);
  const base = baseOpts(containerWidth(el), h);
  const u = new uPlot(
    {
      ...base,
      series: [
        {},
        { label: "EF", scale: "ef", stroke: PROGRESS_COLORS.ef, width: 2, spanGaps: true, points: { show: true, size: 6, fill: PROGRESS_COLORS.ef }, value: (_u, v) => (v == null ? "—" : v.toFixed(2)) },
        { label: "decoupling", scale: "dec", stroke: PROGRESS_COLORS.dec, width: 1.5, dash: [4, 4], spanGaps: true, points: { show: true, size: 6, fill: PROGRESS_COLORS.dec }, value: (_u, v) => (v == null ? "—" : v.toFixed(1) + "%") },
      ],
      scales: { x: { time: true }, ef: { auto: true }, dec: { auto: true } },
      axes: [
        base.axes[0],
        { ...base.axes[1], scale: "ef", stroke: PROGRESS_COLORS.ef, values: (_u, s) => s.map((v) => v.toFixed(2)) },
        { ...base.axes[1], scale: "dec", side: 1, stroke: PROGRESS_COLORS.dec, grid: { show: false }, values: (_u, s) => s.map((v) => v + "%") },
      ],
    },
    [ts, points.map((p) => p.ef), points.map((p) => p.dec)],
    el,
  );
  watchResize(el, u, 220);
}
