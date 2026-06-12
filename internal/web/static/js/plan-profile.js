// fitbase planned-workout profile — renders a structured interval workout as a
// zone-colored power profile on a canvas. Pure rendering: the quick-add dialog
// that feeds it lives in plan-modal.js.
//
// Loaded after charts.js (see base.html), so it reuses that file's shared
// palette/theme — POWER_ZONE_COLORS, POWER_ZONE_BOUNDS, CHART_THEME — instead of
// redeclaring them. That keeps a planned interval in the same zone color as the
// recorded power graph.

// Athlete FTP (watts) for converting watts targets to %FTP in the preview; 0 if unset.
// Read from a data attribute (like charts.js reads data-imperial) to keep Go
// template values out of the JS body.
const PLAN_FTP = parseInt(document.getElementById("plan-profile")?.dataset.ftp || "0", 10) || 0;

// Upper %FTP bound of each zone Z1–Z6 (Z7 is open-ended), derived from charts.js's
// fraction-of-FTP bounds so the preview buckets colors exactly like the power graph.
const PLAN_ZONE_BOUNDS = POWER_ZONE_BOUNDS.map((f) => f * 100);

// Representative %FTP for a zone label, used only to place zone-only targets on
// the profile (the backend stores no concrete watts for a zone — see model docs).
const PLAN_ZONE_PCT = { z1: 45, z2: 65, z3: 83, z4: 98, z5: 113, z6: 135, z7: 160, ss: 90 };

// planZoneColor returns the discrete zone color for a %FTP value, matching the
// hard-bucketed coloring of the workout power graph.
function planZoneColor(pct) {
  for (let i = 0; i < PLAN_ZONE_BOUNDS.length; i++) {
    if (pct < PLAN_ZONE_BOUNDS[i]) return POWER_ZONE_COLORS[i];
  }
  return POWER_ZONE_COLORS[6];
}

// planZoneBands splits a ramp's start→end %FTP span into sub-bands that each fall
// in a single zone, so a ramp steps through the same discrete colors the power
// graph shows (hard changes at zone boundaries) rather than a blended gradient.
// Works in either direction: a descending cooldown ramp returns bands ordered
// start→end too.
function planZoneBands(start, end) {
  if (start === end) return [[start, end]];
  const lo = Math.min(start, end), hi = Math.max(start, end);
  const cuts = PLAN_ZONE_BOUNDS.filter((b) => b > lo && b < hi);
  const pts = start < end ? [start, ...cuts, end] : [start, ...cuts.reverse(), end];
  const bands = [];
  for (let i = 0; i < pts.length - 1; i++) bands.push([pts[i], pts[i + 1]]);
  return bands;
}

function planHexA(hex, a) {
  const p = (i) => parseInt(hex.slice(i, i + 2), 16);
  return `rgba(${p(1)},${p(3)},${p(5)},${a})`;
}

// planTargetPct resolves a node's power target to {lo, hi} %FTP (lo==hi for a flat
// target; free=true when there's no usable target).
function planTargetPct(node) {
  if (node.target_pct_ftp_start != null && node.target_pct_ftp_end != null) {
    return { lo: +node.target_pct_ftp_start, hi: +node.target_pct_ftp_end };
  }
  if (node.target_pct_ftp != null) {
    return { lo: +node.target_pct_ftp, hi: +node.target_pct_ftp };
  }
  if (node.target_watts != null && PLAN_FTP > 0) {
    const p = (+node.target_watts / PLAN_FTP) * 100;
    return { lo: p, hi: p };
  }
  if (node.target_zone) {
    const p = PLAN_ZONE_PCT[String(node.target_zone).toLowerCase()];
    if (p != null) return { lo: p, hi: p };
  }
  return { lo: 0, hi: 0, free: true };
}

// planFlatten expands repeat groups into a flat list of timed segments.
function planFlatten(nodes, out, t) {
  for (const node of nodes) {
    if (Array.isArray(node.steps) && node.steps.length) {
      const reps = node.repeat && node.repeat > 1 ? node.repeat : 1;
      for (let r = 0; r < reps; r++) t = planFlatten(node.steps, out, t);
      continue;
    }
    const dur = +node.duration_secs || 0;
    if (dur <= 0) continue;
    const tgt = planTargetPct(node);
    out.push({ start: t, dur, lo: tgt.lo, hi: tgt.hi, free: !!tgt.free });
    t += dur;
  }
  return t;
}

function planFormatDuration(sec) {
  const h = Math.floor(sec / 3600), m = Math.round((sec % 3600) / 60);
  return h > 0 ? `${h}h ${m}m` : `${m}m`;
}

// planNiceStep rounds a rough axis step up to a clean 1/2/5 × 10ⁿ value so watt
// gridlines land on tidy numbers (50, 100, 250, …).
function planNiceStep(rough) {
  if (rough <= 0) return 1;
  const pow = Math.pow(10, Math.floor(Math.log10(rough)));
  const n = rough / pow;
  return (n < 1.5 ? 1 : n < 3 ? 2 : n < 7 ? 5 : 10) * pow;
}

// planDrawProfile renders the flattened segments as a zone-colored power profile,
// styled to match the workout charts (gridlines, %FTP + time axes, dashed FTP line,
// gradient area fills with a crisp colored top edge).
function planDrawProfile(canvas, segments, total) {
  const dpr = window.devicePixelRatio || 1;
  const cssW = canvas.clientWidth || 480, cssH = 150;
  canvas.width = cssW * dpr;
  canvas.height = cssH * dpr;
  const ctx = canvas.getContext("2d");
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, cssW, cssH);
  ctx.font = CHART_THEME.font;
  if (!segments.length || total <= 0) return;

  // Label the Y axis in watts when FTP is known, else fall back to %FTP.
  const hasW = PLAN_FTP > 0;
  // Plot area with gutters for axis labels (wider left gutter for watt numbers).
  const padL = hasW ? 40 : 32, padR = 8, padT = 10, padB = 18;
  const plotW = cssW - padL - padR, plotH = cssH - padT - padB;
  const baseY = padT + plotH;

  let maxPct = 120;
  for (const s of segments) maxPct = Math.max(maxPct, s.lo, s.hi);
  maxPct = Math.ceil((maxPct * 1.08) / 25) * 25; // round up to a clean 25% step

  const xFor = (sec) => padL + (sec / total) * plotW;
  const yFor = (pct) => padT + plotH - (pct / maxPct) * plotH;

  // Horizontal gridlines + labels (watts relative to FTP, or %FTP as fallback).
  ctx.textAlign = "right";
  ctx.textBaseline = "middle";
  const gridLine = (gy, label) => {
    gy = Math.round(gy) + 0.5;
    ctx.strokeStyle = CHART_THEME.grid;
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(padL, gy);
    ctx.lineTo(cssW - padR, gy);
    ctx.stroke();
    ctx.fillStyle = CHART_THEME.axis;
    ctx.fillText(label, padL - 5, gy);
  };
  if (hasW) {
    const maxW = (maxPct / 100) * PLAN_FTP;
    const stepW = planNiceStep(maxW / 4);
    for (let w = 0; w <= maxW + 0.5; w += stepW) gridLine(yFor((w / PLAN_FTP) * 100), String(Math.round(w)));
  } else {
    const step = maxPct > 175 ? 50 : 25;
    for (let gp = 0; gp <= maxPct + 0.5; gp += step) gridLine(yFor(gp), gp + "%");
  }

  // Segments: solid zone-colored fills (matching the power graph palette), split
  // into per-zone bands for ramps so colors change hard at zone boundaries.
  ctx.lineJoin = "round";
  ctx.lineCap = "round";
  for (const s of segments) {
    const x0 = xFor(s.start), x1 = Math.max(x0 + 1, xFor(s.start + s.dur));
    if (s.free) {
      ctx.fillStyle = planHexA("#94a3b8", 0.18);
      ctx.fillRect(x0, yFor(45), x1 - x0, baseY - yFor(45));
      continue;
    }
    const span = s.hi - s.lo;
    const xAt = (pct) => (span === 0 ? x0 : x0 + ((pct - s.lo) / span) * (x1 - x0));
    for (const [pa, pb] of planZoneBands(s.lo, s.hi)) {
      const bx0 = xAt(pa), bx1 = pb === s.hi ? x1 : xAt(pb);
      const color = planZoneColor((pa + pb) / 2); // midpoint sits unambiguously in one zone
      ctx.fillStyle = color;
      ctx.beginPath();
      ctx.moveTo(bx0, baseY);
      ctx.lineTo(bx0, yFor(pa));
      ctx.lineTo(bx1, yFor(pb)); // sloped top for ramps; flat when lo==hi
      ctx.lineTo(bx1, baseY);
      ctx.closePath();
      ctx.fill();
      // Crisp top edge in the same zone color, echoing the power line.
      ctx.strokeStyle = color;
      ctx.lineWidth = 1.5;
      ctx.beginPath();
      ctx.moveTo(bx0, yFor(pa));
      ctx.lineTo(bx1, yFor(pb));
      ctx.stroke();
    }
  }

  // Dashed FTP (100%) reference line, drawn over the blocks and labeled so the
  // watt scale is clearly anchored to threshold.
  const fy = Math.round(yFor(100)) + 0.5;
  ctx.save();
  ctx.setLineDash([4, 3]);
  ctx.strokeStyle = planHexA("#e2e8f0", 0.55);
  ctx.lineWidth = 1;
  ctx.beginPath();
  ctx.moveTo(padL, fy);
  ctx.lineTo(cssW - padR, fy);
  ctx.stroke();
  ctx.restore();
  ctx.fillStyle = planHexA("#e2e8f0", 0.6);
  ctx.textAlign = "right";
  ctx.textBaseline = "bottom";
  ctx.fillText(hasW ? `FTP ${PLAN_FTP}w` : "FTP", cssW - padR - 2, fy - 1);

  // Per-block target + duration labels (actual watts, or %FTP when FTP is
  // unknown) so the exact target is readable without eyeballing it against the
  // axis. Drawn last so nothing overlaps them; on blocks too narrow for the full
  // text the duration is dropped first, then the label is skipped entirely.
  const wattAt = (pct) => Math.round((pct / 100) * PLAN_FTP);
  ctx.textAlign = "center";
  ctx.textBaseline = "middle";
  for (const s of segments) {
    if (s.free) continue;
    const x0 = xFor(s.start), x1 = Math.max(x0 + 1, xFor(s.start + s.dur));
    const lo = hasW ? wattAt(s.lo) : Math.round(s.lo);
    const hi = hasW ? wattAt(s.hi) : Math.round(s.hi);
    const unit = hasW ? "w" : "%";
    const target = lo === hi ? `${lo}${unit}` : `${lo}-${hi}${unit}`;
    const fits = (text) => x1 - x0 >= ctx.measureText(text).width + 6;
    const full = `${target} · ${planFormatDuration(s.dur)}`;
    const label = fits(full) ? full : target;
    if (!fits(label)) continue; // too narrow even for the target alone
    const ly = Math.min(yFor(Math.max(s.lo, s.hi)) + 11, baseY - 7);
    ctx.save();
    ctx.fillStyle = "#fff";
    ctx.shadowColor = "rgba(0,0,0,0.65)";
    ctx.shadowBlur = 3;
    ctx.fillText(label, (x0 + x1) / 2, ly);
    ctx.restore();
  }

  // Time axis ticks along the bottom.
  ctx.strokeStyle = CHART_THEME.grid;
  ctx.fillStyle = CHART_THEME.axis;
  ctx.textAlign = "center";
  ctx.textBaseline = "top";
  const nTicks = Math.max(2, Math.min(6, Math.round(plotW / 90)));
  for (let i = 0; i <= nTicks; i++) {
    const t = (total / nTicks) * i;
    const tx = xFor(t);
    ctx.beginPath();
    ctx.moveTo(tx + 0.5, baseY);
    ctx.lineTo(tx + 0.5, baseY + 3);
    ctx.stroke();
    ctx.fillText(Math.round(t / 60) + "m", Math.min(cssW - padR, Math.max(padL, tx)), baseY + 5);
  }
}
