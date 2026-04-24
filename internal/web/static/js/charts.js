// fitbase charts — uPlot wrappers for workout data visualization

const IMPERIAL = document.body.dataset.imperial === "true";

const COLORS = {
  power: "#60a5fa",
  hr: "#f87171",
  cadence: "#a78bfa",
  speed: "#34d399",
  altitude: "#94a3b8",
  ctl: "#3b82f6",
  atl: "#f97316",
  tsb: "#22c55e",
};

// Zone colors: Z1–Z7 for power and HR
const POWER_ZONE_COLORS = [
  "#94a3b8", // Z1 active recovery
  "#60a5fa", // Z2 endurance
  "#34d399", // Z3 tempo
  "#facc15", // Z4 threshold
  "#f97316", // Z5 VO2 max
  "#ef4444", // Z6 anaerobic
  "#a855f7", // Z7 neuromuscular
];

const HR_ZONE_COLORS = [
  "#60a5fa", // Z1 active recovery
  "#34d399", // Z2 endurance
  "#facc15", // Z3 tempo
  "#f97316", // Z4 threshold
  "#ef4444", // Z5 VO2 max
];

const POWER_ZONE_NAMES = [
  "Z1 Active Recovery",
  "Z2 Endurance",
  "Z3 Tempo",
  "Z4 Threshold",
  "Z5 VO2 Max",
  "Z6 Anaerobic",
  "Z7 Neuromuscular",
];

const HR_ZONE_NAMES = ["Z1 Active Recovery", "Z2 Endurance", "Z3 Tempo", "Z4 Lactate Threshold", "Z5 VO2 Max"];

function getPowerZoneIndex(watts, ftp) {
  if (!ftp || ftp <= 0 || watts == null) return -1;
  const p = watts / ftp;
  if (p < 0.56) return 0;
  if (p < 0.76) return 1;
  if (p < 0.91) return 2;
  if (p < 1.06) return 3;
  if (p < 1.21) return 4;
  if (p < 1.51) return 5;
  return 6;
}

function getHRZoneIndex(bpm, lthr) {
  if (!lthr || lthr <= 0 || bpm == null) return -1;

  const p = bpm / lthr;

  if (p < 0.69) return 0; // Z1
  if (p < 0.84) return 1; // Z2
  if (p < 0.95) return 2; // Z3
  if (p < 1.06) return 3; // Z4

  return 4; // Z5
}

function getPowerZoneColor(watts, ftp) {
  if (!ftp || ftp <= 0 || watts == null) return COLORS.power;
  const p = watts / ftp;
  if (p < 0.56) return POWER_ZONE_COLORS[0];
  if (p < 0.76) return POWER_ZONE_COLORS[1];
  if (p < 0.91) return POWER_ZONE_COLORS[2];
  if (p < 1.06) return POWER_ZONE_COLORS[3];
  if (p < 1.21) return POWER_ZONE_COLORS[4];
  if (p < 1.51) return POWER_ZONE_COLORS[5];
  return POWER_ZONE_COLORS[6];
}

function getHRZoneColor(bpm, lthr) {
  if (!lthr || lthr <= 0 || bpm == null) return COLORS.hr;

  const p = bpm / lthr;

  if (p < 0.69) return HR_ZONE_COLORS[0]; // Z1: Active Recovery (< 69%)
  if (p < 0.84) return HR_ZONE_COLORS[1]; // Z2: Endurance (69% - 83%)
  if (p < 0.95) return HR_ZONE_COLORS[2]; // Z3: Tempo (84% - 94%)
  if (p < 1.06) return HR_ZONE_COLORS[3]; // Z4: Lactate Threshold (95% - 105%)

  return HR_ZONE_COLORS[4]; // Z5: VO2 Max (106%+)
}

// Continuous-color gradient anchors for smooth zone-to-zone transitions.
// Each entry is [ratio, hex] where ratio is the center of a zone
// (watts/FTP for power, bpm/LTHR for HR). Values between anchors are
// linearly blended, so transitions fade between zone colors instead of
// snapping at the boundaries.
const POWER_GRADIENT_STOPS = [
  [0.28, POWER_ZONE_COLORS[0]],
  [0.66, POWER_ZONE_COLORS[1]],
  [0.84, POWER_ZONE_COLORS[2]],
  [0.98, POWER_ZONE_COLORS[3]],
  [1.13, POWER_ZONE_COLORS[4]],
  [1.36, POWER_ZONE_COLORS[5]],
  [1.70, POWER_ZONE_COLORS[6]],
];

const HR_GRADIENT_STOPS = [
  [0.55, HR_ZONE_COLORS[0]],
  [0.77, HR_ZONE_COLORS[1]],
  [0.89, HR_ZONE_COLORS[2]],
  [1.00, HR_ZONE_COLORS[3]],
  [1.10, HR_ZONE_COLORS[4]],
];

function lerpHex(a, b, t) {
  const h = (v) => Math.round(v).toString(16).padStart(2, "0");
  const ar = parseInt(a.slice(1, 3), 16);
  const ag = parseInt(a.slice(3, 5), 16);
  const ab = parseInt(a.slice(5, 7), 16);
  const br = parseInt(b.slice(1, 3), 16);
  const bg = parseInt(b.slice(3, 5), 16);
  const bb = parseInt(b.slice(5, 7), 16);
  return `#${h(ar + (br - ar) * t)}${h(ag + (bg - ag) * t)}${h(ab + (bb - ab) * t)}`;
}

function colorFromStops(ratio, stops) {
  if (ratio == null) return null;
  if (ratio <= stops[0][0]) return stops[0][1];
  const last = stops[stops.length - 1];
  if (ratio >= last[0]) return last[1];
  for (let i = 0; i < stops.length - 1; i++) {
    const [r0, c0] = stops[i];
    const [r1, c1] = stops[i + 1];
    if (ratio < r1) return lerpHex(c0, c1, (ratio - r0) / (r1 - r0));
  }
  return last[1];
}
// Causal rolling average over a time window (seconds).
// Returns the original array unchanged when windowSecs is 0.
function rollingAvg(xd, yd, windowSecs) {
  if (windowSecs <= 0) return yd;
  const out = new Array(yd.length);
  let sum = 0,
    count = 0,
    left = 0;
  for (let i = 0; i < yd.length; i++) {
    if (yd[i] != null) {
      sum += yd[i];
      count++;
    }
    while (xd[i] - xd[left] > windowSecs) {
      if (yd[left] != null) {
        sum -= yd[left];
        count--;
      }
      left++;
    }
    out[i] = count > 0 ? sum / count : null;
  }
  return out;
}

// Returns a uPlot paths function that draws the series line in zone colors.
// Applies dynamic smoothing based on zoom level so the chart stays readable
// when the full ride is visible. The raw data is preserved for the legend.
// colorFn(value) → css color string
// Returns null to prevent uPlot from drawing its own stroke.
// smoothing: 0–10 manual bias (0 = auto only, 10 = ~30 s extra window)
function zonePathsFn(colorFn, smoothing = 0) {
  const mobile = isMobile(); // captured once at chart construction, not per frame
  const manualSecs = smoothing * 3; // map 0-10 → 0-30 seconds
  return (u, seriesIdx) => {
    const ctx = u.ctx;
    const xd = u.data[0];
    const yd = u.data[seriesIdx];
    const scale = u.series[seriesIdx].scale || "y";

    // Pick smoothing window based on how many seconds each pixel represents.
    // Scales down proportionally as you zoom in so detail is revealed.
    const secsPerPx = (u.scales.x.max - u.scales.x.min) / u.bbox.width;
    const autoSecs =
      secsPerPx > 20 ? 30 : secsPerPx > 10 ? 15 : secsPerPx > 5 ? 8 : secsPerPx > 2 ? 3 : secsPerPx > 1 ? 1 : 0;
    const mobileFloor = mobile ? Math.min(30, autoSecs + 10) : 0;
    // manualSecs scales with zoom: full effect at full zoom-out, fades to 0 as you zoom in
    const zoomFactor = Math.min(secsPerPx / 10, 1);
    const windowSecs = Math.max(mobileFloor, autoSecs + manualSecs * zoomFactor);
    const sd = rollingAvg(xd, yd, windowSecs);

    ctx.save();
    // Clip to the chart's data area (canvas pixels)
    const { left, top, width, height } = u.bbox;
    ctx.beginPath();
    ctx.rect(left, top, width, height);
    ctx.clip();

    ctx.lineWidth = mobile ? 5 : 2;
    ctx.lineJoin = "round";
    ctx.lineCap = "round";

    let curColor = null;

    // Pre-compute pixel positions
    const pxs = new Array(xd.length);
    for (let i = 0; i < xd.length; i++) {
      if (sd[i] != null) {
        pxs[i] = { x: u.valToPos(xd[i], "x", true), y: u.valToPos(sd[i], scale, true) };
      }
    }

    for (let i = 1; i < xd.length; i++) {
      const timeDiff = xd[i] - xd[i - 1];
      if (!pxs[i] || !pxs[i - 1] || timeDiff > 60) {
        if (curColor !== null) {
          ctx.stroke();
          curColor = null;
        }
        continue;
      }

      const color = colorFn(sd[i]);

      if (color !== curColor) {
        if (curColor !== null) ctx.stroke();
        ctx.beginPath();
        ctx.strokeStyle = color;
        curColor = color;
        ctx.moveTo(pxs[i - 1].x, pxs[i - 1].y);
      }

      // Use the midpoint of prev and current as the control point for a smooth curve
      const midX = (pxs[i - 1].x + pxs[i].x) / 2;
      ctx.bezierCurveTo(midX, pxs[i - 1].y, midX, pxs[i].y, pxs[i].x, pxs[i].y);
    }

    if (curColor !== null) ctx.stroke();
    ctx.restore();

    return null; // tell uPlot we handled drawing
  };
}

const CHART_OPTS = {
  background: "transparent",
  padding: [8, 0, 0, 0],
};

function baseOpts(width, height) {
  const mobile = isMobile();
  const axisFont = mobile ? "10px system-ui" : "11px system-ui";
  const yAxisSize = mobile ? 40 : undefined; // let uPlot auto-size on desktop
  return {
    width,
    height,
    ...CHART_OPTS,
    axes: [
      {
        stroke: "#475569",
        ticks: { stroke: "#334155", width: 1 },
        grid: { stroke: "#1e2330", width: 1 },
        font: axisFont,
        labelFont: axisFont,
      },
      {
        stroke: "#475569",
        ticks: { stroke: "#334155", width: 1 },
        grid: { stroke: "#1e2330", width: 1 },
        font: axisFont,
        labelFont: axisFont,
        ...(yAxisSize ? { size: yAxisSize } : {}),
      },
    ],
    cursor: {
      stroke: "#475569",
      points: { size: mobile ? 4 : 6 },
    },
    legend: {
      show: !mobile,
    },
  };
}

function containerWidth(el) {
  return Math.max(el.clientWidth || 600, 300);
}

// Return chart height for the current viewport.
// Keep heights close to desktop so the plot area isn't dwarfed by
// the fixed-size axes and legend overhead on smaller screens.
function chartHeight(desktopH) {
  if (window.innerWidth <= 900) return Math.round(desktopH * 0.9);
  return desktopH;
}

function isMobile() {
  return window.innerWidth <= 640;
}

// Observe container width changes and resize the uPlot instance accordingly.
function watchResize(el, u, desktopH) {
  if (!window.ResizeObserver) return;
  new ResizeObserver((entries) => {
    const w = Math.floor(entries[0].contentRect.width);
    const h = desktopH ? chartHeight(desktopH) : u.height;
    if (w > 0 && (w !== u.width || h !== u.height)) {
      u.setSize({ width: w, height: h });
    }
  }).observe(el);
}

// ── Dashboard ───────────────────────────────────────────────────────────────

// TSB zone bands drawn behind the fitness chart lines.
// Each band: [lo, hi, rgba-fill, label]
const TSB_ZONES = [
  [-10, 5, "rgba(34,197,94,0.07)", "Optimal form"],
  [-30, -10, "rgba(250,204,21,0.07)", "Productive training"],
  [-50, -30, "rgba(249,115,22,0.07)", "Overreaching"],
];

function renderFitnessChart(containerId, data) {
  const el = document.getElementById(containerId);
  if (!el || !data || data.length === 0) return;

  const timestamps = data.map((d) => {
    // d.date is midnight UTC. Construct a local-midnight timestamp so the
    // point lands on the correct calendar day regardless of timezone.
    const [y, mo, day] = d.date.substring(0, 10).split("-").map(Number);
    // DO NOT MESS WITH mo - 1. JS months are 0th indexed lol.
    return new Date(y, mo - 1, day).getTime() / 1000;
  });
  const ctlSeries = data.map((d) => Math.round(d.fitness * 10) / 10);
  const atlSeries = data.map((d) => Math.round(d.fatigue * 10) / 10);
  const tsbSeries = data.map((d) => Math.round(d.form * 10) / 10);

  // Split real vs projected (future) points. Duplicate the last real point
  // into the projected series so the lines connect seamlessly.
  const nowSec = Date.now() / 1000;
  const lastReal = timestamps.findLastIndex((t) => t <= nowSec);
  const projCtl = timestamps.map((_, i) => (i >= lastReal ? ctlSeries[i] : null));
  const projAtl = timestamps.map((_, i) => (i >= lastReal ? atlSeries[i] : null));
  const projTsb = timestamps.map((_, i) => (i >= lastReal ? tsbSeries[i] : null));
  for (let i = lastReal + 1; i < timestamps.length; i++) {
    ctlSeries[i] = null;
    atlSeries[i] = null;
    tsbSeries[i] = null;
  }

  const chart_line_stroke = 1.5;
  const projStyle = {
    stroke: "#64748b",
    width: 1,
    dash: [4, 4],
    paths: uPlot.paths.spline(),
    points: { show: false },
  };

  const w = containerWidth(el);
  const opts = {
    ...baseOpts(w, chartHeight(350)),
    series: [
      {},
      {
        label: "Fitness",
        stroke: COLORS.ctl,
        width: chart_line_stroke,
        fill: COLORS.ctl + "22",
        paths: uPlot.paths.spline(),
      },
      { label: "Fatigue", stroke: COLORS.atl, width: chart_line_stroke, paths: uPlot.paths.spline() },
      { label: "Form", stroke: COLORS.tsb, width: chart_line_stroke, paths: uPlot.paths.spline() },
      { ...projStyle },
      { ...projStyle },
      { ...projStyle },
    ],
    scales: {
      x: { time: true },
      y: { auto: true },
    },
    hooks: {
      draw: [
        (u) => {
          const ctx = u.ctx;
          const { left, top, width, height } = u.bbox;

          // Draw TSB zone bands (clipped to the plot area).
          ctx.save();
          ctx.beginPath();
          ctx.rect(left, top, width, height);
          ctx.clip();

          for (const [lo, hi, fill] of TSB_ZONES) {
            const yHi = u.valToPos(hi, "y", true);
            const yLo = u.valToPos(lo, "y", true);
            ctx.fillStyle = fill;
            ctx.fillRect(left, Math.min(yHi, yLo), width, Math.abs(yLo - yHi));
          }

          // Dashed zero line for TSB orientation.
          const y0 = u.valToPos(0, "y", true);
          ctx.strokeStyle = "rgba(148,163,184,0.25)";
          ctx.lineWidth = 1;
          ctx.setLineDash([4, 4]);
          ctx.beginPath();
          ctx.moveTo(left, y0);
          ctx.lineTo(left + width, y0);
          ctx.stroke();
          ctx.setLineDash([]);

          ctx.restore();
        },
      ],
      ready: [
        (u) => {
          const rows = u.root.querySelectorAll(".u-series");
          // Remove the last 3 legend rows (projection series).
          for (let i = rows.length - 3; i < rows.length; i++) {
            rows[i]?.remove();
          }
        },
      ],
    },
  };

  const u = new uPlot(opts, [timestamps, ctlSeries, atlSeries, tsbSeries, projCtl, projAtl, projTsb], el);
  watchResize(el, u, 300);

  // Recommendation badge based on today's TSB.
  const recEl = document.getElementById("fitness-recommendation");
  const lastTSB = tsbSeries.findLast((value) => value != null);

  if (recEl && lastTSB != null) {
    const rec = fitnessRecommendation(lastTSB);

    // nosemgrep: javascript.browser.security.insecure-document-method.insecure-document-method
    recEl.innerHTML = `<div class="rec-badge ${rec.cls}"><span class="rec-label">
    ${rec.label}</span><span class="rec-detail">${rec.detail}</span><span class="rec-tsb">
    form ${lastTSB > 0 ? "+" : ""}${lastTSB}</span></div>`;
  }
}

function fitnessRecommendation(tsb) {
  if (tsb > 5) return { label: "Go Ride", cls: "rec-go", detail: "You're fresh — good day to train hard or race." };
  if (tsb > -10) return { label: "Go Ride", cls: "rec-go", detail: "Optimal form — peak performance window." };
  if (tsb > -30) return { label: "Train On", cls: "rec-train", detail: "Normal training fatigue — keep building." };
  if (tsb > -50) return { label: "Ease Up", cls: "rec-ease", detail: "Overreaching — a recovery day will pay off." };
  return { label: "Go Rest", cls: "rec-rest", detail: "Too much load — rest before your next hard effort." };
}

// ── Workout charts ──────────────────────────────────────────────────────────

function renderWorkoutCharts(streams, ftp, lthr) {
  if (!streams || streams.length === 0) return;

  ftp = ftp || 0;
  lthr = lthr || 0;

  const timestamps = streams.map((s) => new Date(s.timestamp).getTime() / 1000);

  const power = streams.map((s) => s.power_watts ?? null);
  const hr = streams.map((s) => s.heart_rate_bpm ?? null);
  const cadence = streams.map((s) => s.cadence_rpm ?? null);
  const speed = streams.map((s) => (s.speed_mps ? s.speed_mps * (IMPERIAL ? 2.23694 : 3.6) : null));
  const altitude = IMPERIAL
    ? streams.map((s) => (s.altitude_meters ? s.altitude_meters * 3.28084 : null))
    : streams.map((s) => s.altitude_meters ?? null);

  const hasAlt = altitude.some((v) => v !== null);
  const altBg = hasAlt ? altitude : null;

  renderPowerChart(timestamps, power, ftp, altBg);
  renderHRChart(timestamps, hr, lthr, altBg);
  renderCadenceChart(timestamps, cadence, altBg);
  renderSpeedChart(timestamps, speed, altBg);

  if (hasAlt) renderElevationChart(timestamps, altitude);
}

// Returns a uPlot `draw` hook that paints a ghost elevation fill behind all
// series using destination-over compositing (draws under existing pixels).
function elevBgHook(altData) {
  if (!altData) return null;
  // Precompute bounds once — avoids repeating this on every draw frame, and
  // prevents spread-operator stack overflow on long rides with many samples.
  let minAlt = Infinity,
    maxAlt = -Infinity;
  for (const v of altData) {
    if (v != null) {
      if (v < minAlt) minAlt = v;
      if (v > maxAlt) maxAlt = v;
    }
  }
  if (minAlt === Infinity) return null;
  const range = maxAlt - minAlt || 1;

  return (u) => {
    const ctx = u.ctx;
    const xd = u.data[0];
    const { left, top, width, height } = u.bbox;

    // Map altitude into the bottom 35% of the plot area.
    const plotBottom = top + height;
    const band = height * 0.35;
    function altToY(v) {
      if (v == null) return null;
      return plotBottom - ((v - minAlt) / range) * band;
    }

    ctx.save();
    ctx.beginPath();
    ctx.rect(left, top, width, height);
    ctx.clip();

    ctx.beginPath();
    let started = false;
    for (let i = 0; i < xd.length; i++) {
      const cx = Math.round(u.valToPos(xd[i], "x", true));
      const cy = altToY(altData[i]);
      if (cy == null) {
        started = false;
        continue;
      }
      if (!started) {
        ctx.moveTo(cx, cy);
        started = true;
      } else ctx.lineTo(cx, cy);
    }
    // close down to bottom
    const lastX = Math.round(u.valToPos(xd[xd.length - 1], "x", true));
    const firstX = Math.round(u.valToPos(xd[0], "x", true));
    ctx.lineTo(lastX, plotBottom);
    ctx.lineTo(firstX, plotBottom);
    ctx.closePath();
    // destination-over paints behind existing canvas content (the series lines)
    ctx.globalCompositeOperation = "destination-over";
    ctx.fillStyle = "rgba(148,163,184,0.09)";
    ctx.fill();
    ctx.globalCompositeOperation = "source-over";

    ctx.restore();
  };
}

function renderPowerChart(timestamps, power, ftp, altData) {
  const el = document.getElementById("power-chart");
  if (!el) return;

  const hasPower = power.some((v) => v !== null);
  if (!hasPower) return;

  const maxPower = Math.max(...power.filter((v) => v !== null));
  let fullScale = false;

  const w = containerWidth(el);
  const h = chartHeight(320);
  const base = baseOpts(w, h);
  const elevHook = elevBgHook(altData);
  const opts = {
    ...base,
    series: [
      {},
      {
        label: "Power",
        stroke: COLORS.power,
        width: 1.5,
        spanGaps: false,
        paths: zonePathsFn((v) => getPowerZoneColor(v, ftp), 16),
        value: (_u, v) => {
          if (v == null) return "—";
          const zi = getPowerZoneIndex(v, ftp);
          const zn = zi >= 0 ? "  ·  " + POWER_ZONE_NAMES[zi] : "";
          return v + " W" + zn;
        },
      },
    ],
    scales: {
      x: { time: true },
      y: { range: () => (fullScale ? [0, maxPower * 1.1] : [0, ftp * 1.5]) },
    },
    axes: [base.axes[0], { ...base.axes[1], label: "W", stroke: COLORS.power }],
    hooks: { draw: elevHook ? [elevHook] : [] },
  };

  const u = new uPlot(opts, [timestamps, power], el);
  watchResize(el, u, h);

  if (ftp > 0) {
    const btn = document.createElement("button");
    btn.textContent = "Full Scale";
    btn.className = "chart-scale-toggle";
    btn.addEventListener("click", () => {
      fullScale = !fullScale;
      btn.textContent = fullScale ? "Zone Scale" : "Full Scale";
      u.redraw();
    });
    el.style.position = "relative";
    el.appendChild(btn);
  }
}

function renderHRChart(timestamps, hr, lthr, altData) {
  const el = document.getElementById("hr-chart");
  if (!el) return;

  const hasHR = hr.some((v) => v !== null);
  if (!hasHR) return;

  const w = containerWidth(el);
  const h = chartHeight(320);
  const base = baseOpts(w, h);
  const elevHook = elevBgHook(altData);
  const opts = {
    ...base,
    series: [
      {},
      {
        label: "HR",
        stroke: COLORS.hr,
        width: 1.5,
        spanGaps: false,
        paths: zonePathsFn((v) => getHRZoneColor(v, lthr)),
        value: (_u, v) => {
          if (v == null) return "—";
          const zi = getHRZoneIndex(v, lthr);
          const zn = zi >= 0 ? "  ·  " + HR_ZONE_NAMES[zi] : "";
          return v + " bpm" + zn;
        },
      },
    ],
    scales: { x: { time: true }, y: { auto: true } },
    axes: [base.axes[0], { ...base.axes[1], label: "bpm", stroke: COLORS.hr }],
    hooks: { draw: elevHook ? [elevHook] : [] },
  };

  const u = new uPlot(opts, [timestamps, hr], el);
  watchResize(el, u, h);
}

function renderCadenceChart(timestamps, cadence, altData) {
  const el = document.getElementById("cadence-chart");
  if (!el) return;

  const hasCadence = cadence.some((v) => v !== null);
  if (!hasCadence) return;

  const w = containerWidth(el);
  const h = chartHeight(160);
  const base = baseOpts(w, h);

  const elevHook = elevBgHook(altData);
  const opts = {
    ...base,
    series: [
      {},
      {
        label: "Cadence (rpm)",
        stroke: COLORS.cadence,
        width: 1,
        fill: COLORS.cadence + "18",
        spanGaps: false,
      },
    ],
    scales: { x: { time: true }, y: { auto: true } },
    axes: [base.axes[0], { ...base.axes[1], label: "rpm", stroke: COLORS.cadence }],
    hooks: { draw: elevHook ? [elevHook] : [] },
  };

  const u = new uPlot(opts, [timestamps, cadence], el);
  watchResize(el, u, 160);
}

function renderSpeedChart(timestamps, speed, altData) {
  const el = document.getElementById("speed-chart");
  if (!el) return;

  const hasSpeed = speed.some((v) => v !== null);
  if (!hasSpeed) return;

  const w = containerWidth(el);
  const h = chartHeight(160);
  const base = baseOpts(w, h);
  const label = IMPERIAL ? "Speed (mph)" : "Speed (km/h)";
  const elevHook = elevBgHook(altData);
  const opts = {
    ...base,
    series: [{}, { label, stroke: COLORS.speed, width: 1, fill: COLORS.speed + "18", spanGaps: false }],
    scales: { x: { time: true }, y: { auto: true } },
    axes: [base.axes[0], { ...base.axes[1], label: IMPERIAL ? "mph" : "km/h", stroke: COLORS.speed }],
    hooks: { draw: elevHook ? [elevHook] : [] },
  };

  const u = new uPlot(opts, [timestamps, speed], el);
  watchResize(el, u, 160);
}

function renderElevationChart(timestamps, altitude) {
  const el = document.getElementById("elevation-chart");
  if (!el) return;

  const w = containerWidth(el);
  const h = chartHeight(120);
  const opts = {
    ...baseOpts(w, h),
    series: [
      {},
      {
        label: IMPERIAL ? "Altitude (ft)" : "Altitude (m)",
        stroke: COLORS.altitude,
        width: isMobile() ? 2 : 1,
        fill: COLORS.altitude + "33",
        spanGaps: false,
      },
    ],
    scales: { x: { time: true }, y: { auto: true } },
  };

  const u = new uPlot(opts, [timestamps, altitude], el);
  watchResize(el, u, 120);
}

// ── Zone distribution ────────────────────────────────────────────────────────

// Renders a horizontal bar chart showing time spent in each zone.
// times: pre-computed seconds per zone (int array from server).
// zoneRanges (optional): array of strings like "< 149W" or "138–167 bpm".
function renderZoneDistribution(containerId, times, zoneNames, zoneColors, zoneRanges) {
  const el = document.getElementById(containerId);
  if (!el) return;

  const total = times.reduce((a, b) => a + b, 0);
  if (total === 0) {
    el.closest("section").hidden = true;
    return;
  }

  const maxTime = Math.max(...times);

  function fmtTime(secs) {
    const m = Math.floor(secs / 60),
      s = Math.floor(secs % 60);
    return m > 0 ? `${m}m ${String(s).padStart(2, "0")}s` : `${s}s`;
  }

  // nosemgrep: javascript.browser.security.insecure-document-method.insecure-document-method
  el.innerHTML = times
    .map((secs, i) => {
      if (secs === 0) return "";
      const barPct = (secs / maxTime) * 100;
      const pct = (secs / total) * 100;
      const rangeEl = zoneRanges ? `<span class="zd-range">${zoneRanges[i]}</span>` : "";
      return `
      <div class="zd-row">
        <div class="zd-label" style="color:${zoneColors[i]}">${zoneNames[i]}${rangeEl}</div>
        <div class="zd-bar-wrap">
          <div class="zd-bar" style="width:${barPct.toFixed(1)}%;background:${zoneColors[i]}"></div>
        </div>
        <div class="zd-time">${fmtTime(secs)}</div>
        <div class="zd-pct">${pct.toFixed(0)}%</div>
      </div>`;
    })
    .join("");
}

// ── Power curve ─────────────────────────────────────────────────────────────

// curve: pre-computed {duration_secs: watts} map from the server (fitness.ComputePowerCurve).
// allTimeCurve: same shape, best across all workouts.
function renderPowerCurve(containerId, curve, ftp, allTimeCurve) {
  const el = document.getElementById(containerId);
  if (!el) return;

  const labels = {
    1: "1s",
    5: "5s",
    10: "10s",
    30: "30s",
    60: "1m",
    120: "2m",
    300: "5m",
    600: "10m",
    1200: "20m",
    1800: "30m",
    2700: "45m",
    3600: "1h",
    5400: "90m",
    7200: "2h",
    10800: "3h",
    14400: "4h",
    18000: "5h",
    21600: "6h",
  };

  ftp = ftp || 0;
  const atBests = allTimeCurve || {};
  const computed = Object.entries(curve)
    .map(([s, w]) => ({ secs: Number(s), watts: w, label: labels[s] ?? s + "s" }))
    .filter((d) => d.watts > 0)
    .sort((a, b) => a.secs - b.secs);
  if (!computed.length) {
    el.closest("section").hidden = true;
    return null;
  }

  // Scale bars to max of current + all-time so proportions are accurate.
  const maxBest = Math.max(...computed.map((d) => d.watts), ...computed.map((d) => atBests[d.secs]?.watts || 0));

  // nosemgrep: javascript.browser.security.insecure-document-method.insecure-document-method
  el.innerHTML = computed
    .map((d) => {
      const pct = maxBest > 0 ? d.watts / maxBest : 0;
      const color = getPowerZoneColor(d.watts, ftp);
      const at = atBests[d.secs];
      const atW = at?.watts || 0;
      const atPct = atW > 0 ? Math.min(atW / maxBest, 1) : 0;
      const isPR = atW > 0 && d.watts >= atW;
      const atMark = atW > 0 ? `<div class="pc-at-mark" style="left:${Math.round(atPct * 100)}%"></div>` : "";
      const prBadge =
        isPR && at?.workout_id
          ? `<a href="/workouts/${at.workout_id}" class="pc-pr">PR</a>`
          : isPR
            ? '<span class="pc-pr">PR</span>'
            : "";
      return `
      <div class="pc-cell">
        <div class="pc-dur">${d.label}</div>
        <div class="pc-bar-wrap">
          <div class="pc-bar" style="width:${Math.round(pct * 100)}%;background:${color}"></div>
          ${atMark}
        </div>
        <div class="pc-watts" style="color:${color}">${d.watts}<span class="pc-unit">W</span>${prBadge}</div>
      </div>`;
    })
    .join("");

  return computed; // consumed by renderPowerCurveLine on toggle
}

function renderPowerCurveLine(containerId, data, ftp, allTimeCurve) {
  const el = document.getElementById(containerId);
  if (!el || !data || data.length < 2) return;

  ftp = ftp || 0;
  const atBests = allTimeCurve || {};
  const xData = data.map((_, i) => i);
  const yData = data.map((d) => d.watts);
  const atData = data.map((d) => atBests[d.secs]?.watts ?? null);
  const hasAT = atData.some((v) => v != null);
  const w = containerWidth(el);
  const h = chartHeight(220);
  const base = baseOpts(w, h);

  let prLink = null;

  function updatePRLink(di) {
    if (!prLink) return;
    const at = di != null ? atBests[data[di]?.secs] : null;
    if (at?.workout_id) {
      prLink.href = "/workouts/" + at.workout_id;
      prLink.style.display = "";
    } else {
      prLink.style.display = "none";
    }
  }

  const series = [
    {},
    {
      label: "This ride",
      stroke: COLORS.power,
      width: 2,
      spanGaps: false,
      points: { show: false },
    },
  ];
  if (hasAT) {
    series.push({
      label: "All-time",
      stroke: "#475569",
      width: 1.5,
      dash: [4, 4],
      spanGaps: false,
      points: { show: false },
    });
  }

  const opts = {
    ...base,
    series,
    scales: {
      x: { time: false, range: [-0.5, xData.length - 0.5] },
      y: { auto: true },
    },
    axes: [
      {
        ...base.axes[0],
        splits: xData,
        values: (_u, splits) => splits.map((i) => data[i]?.label ?? ""),
        ticks: { show: false },
        gap: 8,
      },
      { ...base.axes[1], label: "W", stroke: COLORS.power },
    ],
    hooks: {
      draw: [
        (u) => {
          const ctx = u.ctx;
          ctx.save();
          const { left, top, width, height } = u.bbox;
          ctx.beginPath();
          ctx.rect(left, top, width, height);
          ctx.clip();
          for (let i = 0; i < xData.length; i++) {
            if (yData[i] == null) continue;
            const cx = Math.round(u.valToPos(xData[i], "x", true));
            const cy = Math.round(u.valToPos(yData[i], "y", true));
            const color = getPowerZoneColor(yData[i], ftp);
            ctx.beginPath();
            ctx.arc(cx, cy, 5, 0, Math.PI * 2);
            ctx.fillStyle = color;
            ctx.fill();
            ctx.strokeStyle = "#0f1117";
            ctx.lineWidth = 1.5;
            ctx.stroke();
          }
          ctx.restore();
        },
      ],
      ready: [
        (u) => {
          // Remove the "Value" x-axis row uPlot always adds as the first legend entry.
          u.root.querySelector(".u-series")?.remove();

          if (hasAT) {
            // u-series rows after removal: [0]=This ride, [1]=All-time
            const atRow = u.root.querySelectorAll(".u-series")[1];
            if (atRow) {
              prLink = document.createElement("a");
              prLink.className = "pc-pr";
              prLink.textContent = "PR";
              prLink.style.cssText = "margin-left:6px;display:none;";
              atRow.appendChild(prLink);
            }
          }
          // Click anywhere on chart to pin cursor at that duration; click again to unpin.
          u.over.addEventListener("click", () => {
            if (u.cursor._lock) {
              u.cursor._lock = false;
              u.over.style.cursor = "";
            } else if (u.cursor.idx != null) {
              u.cursor._lock = true;
              u.over.style.cursor = "pointer";
              updatePRLink(u.cursor.idx);
            }
          });
        },
      ],
      setCursor: [
        (u) => {
          if (!u.cursor._lock) updatePRLink(u.cursor.idx);
        },
      ],
    },
  };

  const plotData = hasAT ? [xData, yData, atData] : [xData, yData];
  const u = new uPlot(opts, plotData, el);
  watchResize(el, u, 220);
}

// ── Route map ────────────────────────────────────────────────────────────────

function renderRouteMap(containerId, streams, ftp, lthr) {
  if (typeof maplibregl === "undefined") return;
  const el = document.getElementById(containerId);
  if (!el) return;

  const gps = streams.filter((s) => s.lat != null && s.lng != null);
  if (gps.length < 2) {
    el.closest("section").hidden = true;
    return;
  }

  // Compute bounds from all GPS points
  let minLat = Infinity, maxLat = -Infinity, minLng = Infinity, maxLng = -Infinity;
  for (const s of gps) {
    if (s.lat < minLat) minLat = s.lat;
    if (s.lat > maxLat) maxLat = s.lat;
    if (s.lng < minLng) minLng = s.lng;
    if (s.lng > maxLng) maxLng = s.lng;
  }
  const centerLat = (minLat + maxLat) / 2;
  const centerLng = (minLng + maxLng) / 2;

  const map = new maplibregl.Map({
    container: el,
    style: "https://tiles.openfreemap.org/styles/liberty",
    center: [centerLng, centerLat],
    zoom: 12,
    scrollZoom: true,
    touchZoomRotate: true,
    attributionControl: false,
  });

  // Add navigation controls (zoom buttons + compass)
  map.addControl(new maplibregl.NavigationControl(), "top-right");

  // Add attribution control
  map.addControl(new maplibregl.AttributionControl({ compact: false }), "bottom-right");

  map.on("load", () => {
    // Color route by power zone, HR zone, or solid blue — whichever is available.
    // Smooth power/HR with a 30s rolling average so the map shows zone trends
    // instead of noisy second-by-second spikes.
    const hasPower = ftp > 0 && streams.some((s) => s.power_watts != null);
    const hasHR = lthr > 0 && streams.some((s) => s.heart_rate_bpm != null);

    // Collect GPS coordinates and per-point smoothed values together so the
    // gradient expression only covers points that are actually drawn.
    const coords = [];
    const rawVals = [];
    for (const s of streams) {
      if (s.lat == null || s.lng == null) continue;
      coords.push([s.lng, s.lat]);
      rawVals.push(hasPower ? s.power_watts : hasHR ? s.heart_rate_bpm : null);
    }

    // Smooth over a 30s window (samples are ~1Hz, so index doubles as seconds).
    const smoothed = (hasPower || hasHR)
      ? rollingAvg(rawVals.map((_, i) => i), rawVals, 30)
      : rawVals;

    // Continuous color per point. With 30s smoothing + linear blending between
    // zone-center anchors, zone boundaries fade smoothly instead of snapping.
    const SOLID_FALLBACK = "#3b82f6";
    const pointColor = (v) => {
      if (v == null) return SOLID_FALLBACK;
      if (hasPower) return colorFromStops(v / ftp, POWER_GRADIENT_STOPS) ?? SOLID_FALLBACK;
      if (hasHR) return colorFromStops(v / lthr, HR_GRADIENT_STOPS) ?? SOLID_FALLBACK;
      return SOLID_FALLBACK;
    };

    // Cumulative haversine distance → normalized line-progress (0..1).
    const cumDist = [0];
    for (let i = 1; i < coords.length; i++) {
      const [lng1, lat1] = coords[i - 1];
      const [lng2, lat2] = coords[i];
      const R = 6371000;
      const phi1 = (lat1 * Math.PI) / 180;
      const phi2 = (lat2 * Math.PI) / 180;
      const dphi = ((lat2 - lat1) * Math.PI) / 180;
      const dlam = ((lng2 - lng1) * Math.PI) / 180;
      const a = Math.sin(dphi / 2) ** 2 + Math.cos(phi1) * Math.cos(phi2) * Math.sin(dlam / 2) ** 2;
      cumDist.push(cumDist[i - 1] + 2 * R * Math.atan2(Math.sqrt(a), Math.sqrt(1 - a)));
    }
    const total = cumDist[cumDist.length - 1] || 1;

    // Build a line-gradient expression. Cap stops at ~800 to keep the expression
    // lightweight; smoothing makes downsampling visually lossless.
    const MAX_STOPS = 800;
    const stride = Math.max(1, Math.ceil(coords.length / MAX_STOPS));
    const gradient = ["interpolate", ["linear"], ["line-progress"]];
    let lastProg = -1;
    const pushStop = (i) => {
      const p = cumDist[i] / total;
      if (p <= lastProg) return;
      lastProg = p;
      gradient.push(p, pointColor(smoothed[i]));
    };
    for (let i = 0; i < coords.length; i += stride) pushStop(i);
    if (coords.length > 0) pushStop(coords.length - 1);

    map.addSource("route", {
      type: "geojson",
      lineMetrics: true,
      data: {
        type: "Feature",
        geometry: { type: "LineString", coordinates: coords },
        properties: {},
      },
    });

    map.addLayer({
      id: "route",
      type: "line",
      source: "route",
      layout: {
        "line-cap": "butt",
        "line-join": "round",
      },
      paint: {
        "line-gradient": gradient,
        "line-width": 5,
        "line-opacity": 0.9,
      },
    });

    // Add markers for start and finish
    const startMarker = document.createElement("div");
    startMarker.style.width = "12px";
    startMarker.style.height = "12px";
    startMarker.style.borderRadius = "50%";
    startMarker.style.backgroundColor = "#22c55e";
    startMarker.style.border = "2px solid white";
    startMarker.style.boxShadow = "0 0 4px rgba(0,0,0,0.5)";

    new maplibregl.Marker({ element: startMarker })
      .setLngLat([gps[0].lng, gps[0].lat])
      .setPopup(new maplibregl.Popup({ offset: 25 }).setText("start"))
      .addTo(map);

    const finishMarker = document.createElement("div");
    finishMarker.style.width = "12px";
    finishMarker.style.height = "12px";
    finishMarker.style.borderRadius = "50%";
    finishMarker.style.backgroundColor = "#ef4444";
    finishMarker.style.border = "2px solid white";
    finishMarker.style.boxShadow = "0 0 4px rgba(0,0,0,0.5)";

    new maplibregl.Marker({ element: finishMarker })
      .setLngLat([gps[gps.length - 1].lng, gps[gps.length - 1].lat])
      .setPopup(new maplibregl.Popup({ offset: 25 }).setText("finish"))
      .addTo(map);

    // Fit to bounds with padding
    const bounds = [
      [minLng, minLat],
      [maxLng, maxLat],
    ];
    map.fitBounds(bounds, { padding: 40 });
  });
}

// ── Activities map (heatmap page) ────────────────────────────────────────────

const SPORT_COLORS = {
  cycling:  "#005cad",
  running:  "#ef4444",
  swimming: "#a855f7",
};

function renderActivitiesMap(containerId, tracks) {
  if (typeof maplibregl === "undefined") return;
  const el = document.getElementById(containerId);
  if (!el) return;

  const valid = tracks.filter((t) => t.coords && t.coords.length >= 2);
  if (!valid.length) {
    el.textContent = "No outdoor activities with GPS data yet.";
    el.style.cssText = "display:flex;align-items:center;justify-content:center;color:var(--text-muted);font-size:13px";
    return;
  }

  // Center on rides from the last 14 days; fall back to all rides if none.
  const cutoff = new Date();
  cutoff.setDate(cutoff.getDate() - 14);
  const recent = valid.filter(t => new Date(t.date) >= cutoff);
  const boundsSource = recent.length > 0 ? recent : valid;

  let minLng = Infinity, maxLng = -Infinity, minLat = Infinity, maxLat = -Infinity;
  for (const t of boundsSource) {
    for (const [lng, lat] of t.coords) {
      if (lng === 0 && lat === 0) continue; // skip GPS-glitch null-island points
      if (lng < minLng) minLng = lng;
      if (lng > maxLng) maxLng = lng;
      if (lat < minLat) minLat = lat;
      if (lat > maxLat) maxLat = lat;
    }
  }
  if (minLng === Infinity) {
    el.textContent = "No outdoor activities with GPS data yet.";
    el.style.cssText = "display:flex;align-items:center;justify-content:center;color:var(--text-muted);font-size:13px";
    return;
  }

  // Expand bounds by 50% on each side to zoom out ~1 level from the tight fit.
  const lngSpan = (maxLng - minLng) || 0.01;
  const latSpan = (maxLat - minLat) || 0.01;
  const expandedBounds = [
    [minLng - lngSpan * 0.5, minLat - latSpan * 0.5],
    [maxLng + lngSpan * 0.5, maxLat + latSpan * 0.5],
  ];

  const map = new maplibregl.Map({
    container: el,
    style: "https://tiles.openfreemap.org/styles/liberty",
    bounds: expandedBounds,
    fitBoundsOptions: { padding: 48, maxZoom: 13 },
    scrollZoom: true,
    touchZoomRotate: true,
    attributionControl: false,
  });

  map.addControl(new maplibregl.NavigationControl(), "top-right");
  map.addControl(new maplibregl.AttributionControl({ compact: true }), "bottom-right");

  map.on("load", () => {
    const features = valid.map((t) => ({
      type: "Feature",
      geometry: { type: "LineString", coordinates: t.coords },
      properties: {
        workout_id: t.workout_id,
        color: SPORT_COLORS[t.sport] || "#60a5fa",
        sport: t.sport,
        date: t.date,
      },
    }));

    map.addSource("tracks", {
      type: "geojson",
      data: { type: "FeatureCollection", features },
    });

    // Wide transparent layer for easier hover hit-testing
    map.addLayer({
      id: "tracks-hit",
      type: "line",
      source: "tracks",
      layout: { "line-cap": "round", "line-join": "round" },
      paint: { "line-color": "transparent", "line-width": 12 },
    });

    map.addLayer({
      id: "tracks-line",
      type: "line",
      source: "tracks",
      layout: { "line-cap": "round", "line-join": "round" },
      paint: {
        "line-color": ["get", "color"],
        "line-width": 2.5,
        "line-opacity": 0.65,
      },
    });

    let hovered = null;

    map.on("mousemove", "tracks-hit", (e) => {
      if (!e.features.length) return;
      const id = e.features[0].properties.workout_id;
      if (id === hovered) return;
      hovered = id;
      map.getCanvas().style.cursor = "pointer";
      map.setPaintProperty("tracks-line", "line-opacity", [
        "case", ["==", ["get", "workout_id"], hovered], 1, 0.25,
      ]);
      map.setPaintProperty("tracks-line", "line-width", [
        "case", ["==", ["get", "workout_id"], hovered], 4, 2,
      ]);
    });

    map.on("mouseleave", "tracks-hit", () => {
      hovered = null;
      map.getCanvas().style.cursor = "";
      map.setPaintProperty("tracks-line", "line-opacity", 0.65);
      map.setPaintProperty("tracks-line", "line-width", 2.5);
    });

    map.on("click", "tracks-hit", (e) => {
      if (e.features.length) window.location.href = "/workouts/" + e.features[0].properties.workout_id;
    });
  });
}

// ── Activity cards (homepage map toggle) ─────────────────────────────────────

function mercatorY(lat) {
  const r = lat * Math.PI / 180;
  return (1 - Math.log(Math.tan(r) + 1 / Math.cos(r)) / Math.PI) / 2;
}

async function drawRouteThumbnail(canvas, coords, color) {
  const W = canvas.width, H = canvas.height;
  const ctx = canvas.getContext('2d');
  ctx.fillStyle = '#111827';
  ctx.fillRect(0, 0, W, H);
  if (!coords || coords.length < 2) return;

  const lngs = coords.map(c => c[0]), lats = coords.map(c => c[1]);
  const minLng = Math.min(...lngs), maxLng = Math.max(...lngs);
  const minLat = Math.min(...lats), maxLat = Math.max(...lats);

  const PAD = 0.18;
  const lngSpan = (maxLng - minLng) || 0.003;
  const latSpan = (maxLat - minLat) || 0.003;
  const pMinLng = minLng - lngSpan * PAD, pMaxLng = maxLng + lngSpan * PAD;
  const pMinLat = minLat - latSpan * PAD, pMaxLat = maxLat + latSpan * PAD;

  const wxMin = (pMinLng + 180) / 360, wxMax = (pMaxLng + 180) / 360;
  const wyMin = mercatorY(pMaxLat), wyMax = mercatorY(pMinLat);

  // Uniform scale so routes aren't stretched — same pixels-per-unit in X and Y
  const wSpan = wxMax - wxMin, hSpan = wyMax - wyMin;
  const scale = Math.min(W / wSpan, H / hSpan);
  const offX = (W - wSpan * scale) / 2;
  const offY = (H - hSpan * scale) / 2;

  const toCanvas = (lng, lat) => [
    offX + (((lng + 180) / 360) - wxMin) * scale,
    offY + (mercatorY(lat) - wyMin) * scale,
  ];

  // Tile range covering the full canvas (not just the route bbox)
  const canvasWxMin = wxMin - offX / scale, canvasWxMax = canvasWxMin + W / scale;
  const canvasWyMin = wyMin - offY / scale, canvasWyMax = canvasWyMin + H / scale;

  // Find zoom where route fits in ≤3 tiles on each axis
  let zoom = 6;
  for (let z = 14; z >= 6; z--) {
    const n = 1 << z;
    if ((Math.floor(wxMax * n) - Math.floor(wxMin * n)) <= 2 &&
        (Math.floor(wyMax * n) - Math.floor(wyMin * n)) <= 2) {
      zoom = z; break;
    }
  }
  const n = 1 << zoom;
  const tx1 = Math.floor(canvasWxMin * n), tx2 = Math.floor(canvasWxMax * n);
  const ty1 = Math.floor(canvasWyMin * n), ty2 = Math.floor(canvasWyMax * n);

  const cols = tx2 - tx1 + 1;
  const tiles = await Promise.all(
    Array.from({ length: cols * (ty2 - ty1 + 1) }, (_, i) => {
      const tx = tx1 + (i % cols);
      const ty = ty1 + Math.floor(i / cols);
      return new Promise(resolve => {
        const img = new Image();
        img.crossOrigin = 'anonymous';
        img.onload = () => resolve({ tx, ty, img });
        img.onerror = () => resolve(null);
        img.src = `https://basemaps.cartocdn.com/dark_nolabels/${zoom}/${tx}/${ty}.png`;
      });
    })
  );

  for (const t of tiles) {
    if (!t) continue;
    const { tx, ty, img } = t;
    const cx1 = offX + (tx / n - wxMin) * scale;
    const cy1 = offY + (ty / n - wyMin) * scale;
    const cx2 = offX + ((tx + 1) / n - wxMin) * scale;
    const cy2 = offY + ((ty + 1) / n - wyMin) * scale;
    ctx.drawImage(img, cx1, cy1, cx2 - cx1, cy2 - cy1);
  }

  const pts = coords.map(([lng, lat]) => toCanvas(lng, lat));

  ctx.strokeStyle = color;
  ctx.lineWidth = 2.5;
  ctx.lineCap = 'round';
  ctx.lineJoin = 'round';
  ctx.beginPath();
  pts.forEach(([x, y], i) => i === 0 ? ctx.moveTo(x, y) : ctx.lineTo(x, y));
  ctx.stroke();

  const dot = ([x, y], fill) => {
    ctx.beginPath();
    ctx.arc(x, y, 4, 0, Math.PI * 2);
    ctx.fillStyle = fill;
    ctx.fill();
    ctx.strokeStyle = 'rgba(255,255,255,0.8)';
    ctx.lineWidth = 1;
    ctx.stroke();
  };
  dot(pts[0], '#4ade80');
  dot(pts[pts.length - 1], '#f87171');
}

function fmtDurJS(secs) {
  const h = Math.floor(secs / 3600);
  const m = Math.floor((secs % 3600) / 60);
  const s = secs % 60;
  if (h > 0) return `${h}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
  return `${m}:${String(s).padStart(2, "0")}`;
}

function renderActivityCards(containerId, tracks, workouts) {
  const el = document.getElementById(containerId);
  if (!el) return;

  const trackMap = {};
  for (const t of tracks) trackMap[t.workout_id] = t;

  const html = workouts.map((w) => {
    const track = trackMap[w.id];
    const color = SPORT_COLORS[w.sport] || "#60a5fa";
    const dist = IMPERIAL
      ? (w.distance_meters / 1609.344).toFixed(1) + " mi"
      : (w.distance_meters / 1000).toFixed(1) + " km";
    const dur = fmtDurJS(w.duration_secs);
    const power = w.avg_power_watts ? Math.round(w.avg_power_watts) + " W" : "";
    const date = new Date(w.recorded_at).toLocaleDateString("en-US", {
      month: "short", day: "numeric", year: "numeric",
    });
    const thumb = track
      ? `<canvas class="ac-thumb-canvas" data-id="${w.id}" width="240" height="140"></canvas>`
      : `<span class="sport-badge sport-${w.sport}">${w.sport}</span>`;
    return `<div class="activity-card" onclick="window.location.href='/workouts/${w.id}'">
      <div class="ac-thumb">${thumb}</div>
      <div class="ac-body">
        <div class="ac-header">
          <span class="sport-badge sport-${w.sport}">${w.sport}</span>
          <span class="ac-date">${date}</span>
        </div>
        <div class="ac-stats">
          <span>${dist}</span><span>${dur}</span>${power ? `<span>${power}</span>` : ""}
        </div>
      </div>
    </div>`;
  }).join("");

  el.innerHTML = `<div class="activity-cards-grid">${html}</div>`;

  el.querySelectorAll('.ac-thumb-canvas').forEach(canvas => {
    const thumb = canvas.parentElement;
    canvas.width = thumb.clientWidth || 240;
    canvas.height = thumb.clientHeight || 140;
    const track = trackMap[canvas.dataset.id];
    if (track) drawRouteThumbnail(canvas, track.coords, SPORT_COLORS[track.sport] || "#60a5fa");
  });
}
