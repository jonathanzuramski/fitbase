// Ride page interactions over the stacked ride charts (charts.js
// renderWorkoutCharts, which syncs their crosshair):
//   - one readout of every channel under the crosshair, instead of a legend
//     per chart
//   - drag across any chart to measure a stretch: time, avg/NP power, %FTP,
//     HR, cadence, speed; zoom every chart to it, or clear it
//   - selecting a row of the intervals table selects that effort
//   - the route map follows the crosshair and highlights the selection
function initRidePage({ ride, map, streams, ftp }) {
  if (!ride || ride.charts.length === 0) return;
  const { charts, timestamps: ts } = ride;
  const t0 = ts[0];
  const tEnd = ts[ts.length - 1];

  // A panel whose channel has no data drew no chart; drop its row.
  document.querySelectorAll(".ride-panel").forEach((p) => {
    if (!p.querySelector(".uplot")) p.hidden = true;
  });

  const readout = document.getElementById("ride-readout");
  const selBar = document.getElementById("ride-selection");
  const selStats = document.getElementById("ride-selection-stats");
  const resetBtn = document.getElementById("ride-reset");
  const speedUnit = IMPERIAL ? "mph" : "km/h";
  const altUnit = IMPERIAL ? "ft" : "m";

  const clock = (secs) => {
    secs = Math.max(0, Math.round(secs));
    const h = Math.floor(secs / 3600);
    const m = Math.floor((secs % 3600) / 60);
    const s = secs % 60;
    return (h ? h + ":" + String(m).padStart(2, "0") : String(m)) + ":" + String(s).padStart(2, "0");
  };

  // ── Readout ──────────────────────────────────────────────────────────
  let pendingIdx = null;
  let readoutQueued = false;
  function showReadout() {
    readoutQueued = false;
    const i = pendingIdx;
    if (i == null) {
      readout.textContent = "";
      moveMarker(null);
      return;
    }
    const parts = [clock(ts[i] - t0)];
    const p = ride.power[i];
    if (p != null) {
      const zi = getPowerZoneIndex(p, ftp);
      parts.push(p + " W" + (zi >= 0 ? " " + POWER_ZONE_NAMES[zi].split(" ")[0] : ""));
    }
    if (ride.hr[i] != null) parts.push(ride.hr[i] + " bpm");
    if (ride.cadence[i] != null) parts.push(ride.cadence[i] + " rpm");
    if (ride.speed[i] != null) parts.push(ride.speed[i].toFixed(1) + " " + speedUnit);
    if (ride.altitude[i] != null) parts.push(Math.round(ride.altitude[i]) + " " + altUnit);
    readout.textContent = parts.join(" · ");
    moveMarker(i);
  }
  for (const u of charts) {
    (u.hooks.setCursor ||= []).push((u) => {
      pendingIdx = u.cursor.idx;
      if (!readoutQueued) {
        readoutQueued = true;
        requestAnimationFrame(showReadout);
      }
    });
  }

  // ── Selection ────────────────────────────────────────────────────────
  let selection = null; // [tStart, tEnd] in chart time
  let applying = false;

  function idxAt(t) {
    // First index at or after t (timestamps are sorted).
    let lo = 0;
    let hi = ts.length - 1;
    while (lo < hi) {
      const mid = (lo + hi) >> 1;
      if (ts[mid] < t) lo = mid + 1;
      else hi = mid;
    }
    return lo;
  }

  function drawSelection() {
    applying = true;
    for (const u of charts) {
      if (!selection) {
        u.setSelect({ left: 0, top: 0, width: 0, height: 0 }, false);
        continue;
      }
      const left = u.valToPos(selection[0], "x");
      const right = u.valToPos(selection[1], "x");
      u.setSelect({ left, top: 0, width: Math.max(0, right - left), height: u.over.clientHeight }, false);
    }
    applying = false;
  }

  function select(tStart, tStop) {
    if (tStop - tStart < 1) return clearSelection();
    selection = [tStart, tStop];
    drawSelection();
    showStats();
    highlightRoute();
  }

  function clearSelection() {
    selection = null;
    drawSelection();
    selBar.hidden = true;
    highlightRoute();
    document.querySelectorAll(".interval-row.active").forEach((r) => r.classList.remove("active"));
  }

  // Cursor sync forwards a drag's mouseup to every chart, so each fires its
  // setSelect hook; only the chart the drag started on speaks for it (the
  // others may report an empty box, which would read as "clear").
  let dragSource = null;
  for (const u of charts) {
    u.over.addEventListener("mousedown", () => (dragSource = u));
    (u.hooks.setSelect ||= []).push((u) => {
      if (applying || u !== dragSource) return;
      const { left, width } = u.select;
      if (width < 2) return clearSelection();
      document.querySelectorAll(".interval-row.active").forEach((r) => r.classList.remove("active"));
      select(u.posToVal(left, "x"), u.posToVal(left + width, "x"));
    });
  }

  function mean(arr, i0, i1, keep = (v) => v != null) {
    let sum = 0;
    let n = 0;
    for (let i = i0; i <= i1; i++) {
      const v = arr[i];
      if (keep(v)) {
        sum += v;
        n++;
      }
    }
    return n ? sum / n : null;
  }

  // Normalized power over the stretch: 30 s rolling average (by time, so
  // gaps don't stretch the window), fourth-power mean, fourth root.
  function normalizedPower(i0, i1) {
    if (ts[i1] - ts[i0] < 60) return null;
    const rolled = [];
    let sum = 0;
    let lo = i0;
    for (let i = i0; i <= i1; i++) {
      sum += ride.power[i] ?? 0;
      while (ts[i] - ts[lo] >= 30) sum -= ride.power[lo++] ?? 0;
      if (ts[i] - ts[i0] >= 29) rolled.push(sum / (i - lo + 1));
    }
    if (!rolled.length) return null;
    return Math.pow(rolled.reduce((a, r) => a + r ** 4, 0) / rolled.length, 0.25);
  }

  function showStats() {
    const i0 = idxAt(selection[0]);
    const i1 = Math.max(i0, idxAt(selection[1]) - 1);
    const parts = [];
    const p = mean(ride.power, i0, i1);
    if (p != null) {
      let s = Math.round(p) + " W avg";
      const np = normalizedPower(i0, i1);
      if (np) s += " · " + Math.round(np) + " NP";
      if (ftp > 0) s += " · " + Math.round((p / ftp) * 100) + "% FTP";
      parts.push(s);
    }
    const hr = mean(ride.hr, i0, i1);
    if (hr != null) {
      let max = 0;
      for (let i = i0; i <= i1; i++) max = Math.max(max, ride.hr[i] ?? 0);
      parts.push(Math.round(hr) + " bpm (max " + max + ")");
    }
    const cad = mean(ride.cadence, i0, i1, (v) => v != null && v > 0);
    if (cad != null) parts.push(Math.round(cad) + " rpm");
    const spd = mean(ride.speed, i0, i1);
    if (spd != null) parts.push(spd.toFixed(1) + " " + speedUnit);
    const dur = document.createElement("strong");
    dur.textContent = clock(ts[i1] - ts[i0]);
    selStats.replaceChildren(dur, ...parts.map((s) => " · " + s));
    selBar.hidden = false;
  }

  // ── Zoom ─────────────────────────────────────────────────────────────
  function zoom(min, max) {
    for (const u of charts) u.setScale("x", { min, max });
    resetBtn.hidden = min <= t0 && max >= tEnd;
    drawSelection();
  }
  document.getElementById("ride-zoom").addEventListener("click", () => {
    if (!selection) return;
    const [a, b] = selection;
    clearSelection();
    zoom(a, b);
  });
  document.getElementById("ride-clear").addEventListener("click", clearSelection);
  resetBtn.addEventListener("click", () => zoom(t0, tEnd));
  // uPlot's own double-click resets just the chart clicked; reset them all.
  for (const u of charts) u.over.addEventListener("dblclick", () => zoom(t0, tEnd));

  // ── Intervals table ──────────────────────────────────────────────────
  const chartsCard = document.getElementById("ride-charts");
  function selectRow(row) {
    const start = t0 + Number(row.dataset.start);
    const stop = t0 + Number(row.dataset.end);
    const [min, max] = [charts[0].scales.x.min, charts[0].scales.x.max];
    if (start < min || stop > max) zoom(t0, tEnd);
    document.querySelectorAll(".interval-row.active").forEach((r) => r.classList.remove("active"));
    row.classList.add("active");
    select(start, stop);
    const box = chartsCard.getBoundingClientRect();
    if (box.top < 0 || box.bottom > window.innerHeight) {
      chartsCard.scrollIntoView({ behavior: "smooth", block: "nearest" });
    }
  }
  document.querySelectorAll(".interval-row").forEach((row) => {
    row.addEventListener("click", () => selectRow(row));
    row.addEventListener("keydown", (e) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        selectRow(row);
      }
    });
  });

  // ── Route map ────────────────────────────────────────────────────────
  let marker = null;
  function moveMarker(i) {
    if (!map) return;
    const s = i == null ? null : streams[i];
    if (!s || s.lat == null || s.lng == null) {
      if (marker) marker.getElement().style.display = "none";
      return;
    }
    if (!marker) {
      const dot = document.createElement("div");
      dot.className = "ride-map-cursor";
      marker = new maplibregl.Marker({ element: dot }).setLngLat([s.lng, s.lat]).addTo(map);
    }
    marker.getElement().style.display = "";
    marker.setLngLat([s.lng, s.lat]);
  }

  function highlightRoute() {
    if (!map) return;
    const apply = () => {
      const coords = [];
      if (selection) {
        const i0 = idxAt(selection[0]);
        const i1 = idxAt(selection[1]);
        for (let i = i0; i <= i1 && i < streams.length; i++) {
          if (streams[i].lat != null && streams[i].lng != null) coords.push([streams[i].lng, streams[i].lat]);
        }
      }
      const data = { type: "Feature", geometry: { type: "LineString", coordinates: coords.length > 1 ? coords : [] } };
      const src = map.getSource("ride-selection");
      if (src) {
        src.setData(data);
        return;
      }
      map.addSource("ride-selection", { type: "geojson", data });
      map.addLayer({
        id: "ride-selection",
        type: "line",
        source: "ride-selection",
        layout: { "line-cap": "round", "line-join": "round" },
        paint: { "line-color": "#fde047", "line-width": 7, "line-opacity": 0.85 },
      });
    };
    if (map.isStyleLoaded()) apply();
    else map.once("load", apply);
  }
}

// "How did it feel?": a 1–10 rating (click again to clear) and a note, saved
// to PUT /api/workouts/{id}/feedback as they change.
function initRideFeedback() {
  const card = document.getElementById("feel");
  if (!card) return;
  const id = card.dataset.workout;
  const note = document.getElementById("feel-note");
  const status = document.getElementById("feel-status");
  const buttons = [...card.querySelectorAll(".rpe-btn")];
  let rpe = buttons.find((b) => b.classList.contains("selected"))?.dataset.rpe ?? null;
  let saved = note.value;
  let timer = null;

  async function save() {
    clearTimeout(timer);
    status.textContent = "saving…";
    try {
      const res = await fetch("/api/workouts/" + encodeURIComponent(id) + "/feedback", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ rpe: rpe == null ? null : Number(rpe), note: note.value }),
      });
      if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error || res.statusText);
      saved = note.value;
      status.textContent = "saved";
    } catch (err) {
      status.textContent = "couldn't save: " + err.message;
    }
  }

  buttons.forEach((b) => {
    b.addEventListener("click", () => {
      rpe = rpe === b.dataset.rpe ? null : b.dataset.rpe;
      buttons.forEach((x) => {
        const on = x.dataset.rpe === rpe;
        x.classList.toggle("selected", on);
        x.setAttribute("aria-checked", on ? "true" : "false");
      });
      save();
    });
  });
  note.addEventListener("input", () => {
    clearTimeout(timer);
    status.textContent = "";
    timer = setTimeout(() => note.value !== saved && save(), 1200);
  });
  note.addEventListener("blur", () => note.value !== saved && save());
}
