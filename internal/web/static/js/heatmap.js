// ── Heat-map constants ───────────────────────────────────────────────────────

const HEAT_COLOR = "#ff3c00";

// Bounds are framed around recent activity so the map opens on where you've
// been lately rather than zooming out to fit years of history.
const RECENT_WINDOW_DAYS = 14;
const BOUNDS_PAD_RATIO   = 0.5;   // fraction of span added as padding each side
const MIN_SPAN_DEG       = 0.01;  // fallback span when all points coincide
const HIT_WIDTH          = 12;    // px width of the invisible click/hover target
const GLOW_BLUR          = 4;

const METERS_PER_MILE = 1609.34;
const METERS_PER_KM   = 1000;
const FEET_PER_METER  = 3.28084;

const EMPTY_MSG = "No outdoor activities with GPS data yet.";
const EMPTY_CSS = "display:flex;align-items:center;justify-content:center;color:var(--text-muted);font-size:13px";

const LINE_LAYOUT = { "line-cap": "round", "line-join": "round" };

// MapLibre GL's "interpolate" expression is a powerful way to have style properties smoothly adjust with zoom level. 
// Here we use it to have the heatmap lines get thicker and more opaque as you zoom in, and thinner and fainter as you zoom out.
// The breakpoints and values are tuned by eye to look good on typical data.
const GLOW_WIDTH = ["interpolate", ["linear"], ["zoom"], 7, 0.01, 10, 3, 13, 5];
const LINE_WIDTH = ["interpolate", ["linear"], ["zoom"], 7, 0.05, 10, 2, 13, 1.5];

// Single source of truth for the two visible track layers. `base` is the
// resting look; on hover the hovered workout gets `hover` while every other
// track drops to `dim`. The layer definitions, the hover handler, and every
// reset path all derive from this — change a number here and nowhere else.
const TRACK_STYLE = {
  "tracks-glow": {
    base:  { "line-opacity": 0.11, "line-width": GLOW_WIDTH },
    hover: { "line-opacity": 0.35, "line-width": 12 },
    dim:   { "line-opacity": 0.04, "line-width": 5 },
  },
  "tracks-line": {
    base:  { "line-opacity": 0.5,  "line-width": LINE_WIDTH },
    hover: { "line-opacity": 0.95, "line-width": 2.5 },
    dim:   { "line-opacity": 0.15, "line-width": 1.2 },
  },
};

// ── Pure helpers ─────────────────────────────────────────────────────────────

function showEmpty(el) {
  el.textContent = EMPTY_MSG;
  el.style.cssText = EMPTY_CSS;
}

function escHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => (
    { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]
  ));
}

// Frame to activity in the last RECENT_WINDOW_DAYS, falling back to all tracks
// when there's been nothing recent. Returns expanded [[w,s],[e,n]] or null.
function computeBounds(valid) {
  const cutoff = new Date();
  cutoff.setDate(cutoff.getDate() - RECENT_WINDOW_DAYS);
  const recent = valid.filter((t) => new Date(t.date) >= cutoff);
  const src = recent.length > 0 ? recent : valid;

  let minLng = Infinity, maxLng = -Infinity, minLat = Infinity, maxLat = -Infinity;
  for (const t of src) {
    for (const [lng, lat] of t.coords) {
      if (lng === 0 && lat === 0) continue;
      if (lng < minLng) minLng = lng;
      if (lng > maxLng) maxLng = lng;
      if (lat < minLat) minLat = lat;
      if (lat > maxLat) maxLat = lat;
    }
  }
  if (minLng === Infinity) return null;

  const lngSpan = (maxLng - minLng) || MIN_SPAN_DEG;
  const latSpan = (maxLat - minLat) || MIN_SPAN_DEG;
  return [
    [minLng - lngSpan * BOUNDS_PAD_RATIO, minLat - latSpan * BOUNDS_PAD_RATIO],
    [maxLng + lngSpan * BOUNDS_PAD_RATIO, maxLat + latSpan * BOUNDS_PAD_RATIO],
  ];
}

function buildFeatures(valid) {
  return valid.map((t) => ({
    type: "Feature",
    geometry: { type: "LineString", coordinates: t.coords },
    properties: {
      workout_id:            t.workout_id,
      sport:                 t.sport,
      date:                  t.date,
      distance_meters:       t.distance_meters || 0,
      duration_secs:         t.duration_secs || 0,
      elevation_gain_meters: t.elevation_gain_meters || 0,
    },
  }));
}

function applyBaseStyle(map) {
  for (const [layer, s] of Object.entries(TRACK_STYLE)) {
    for (const [prop, val] of Object.entries(s.base)) {
      map.setPaintProperty(layer, prop, val);
    }
  }
}

function applyHoverStyle(map, workoutId) {
  for (const [layer, s] of Object.entries(TRACK_STYLE)) {
    for (const prop of Object.keys(s.base)) {
      map.setPaintProperty(layer, prop, [
        "case",
        ["==", ["get", "workout_id"], workoutId],
        s.hover[prop],
        s.dim[prop],
      ]);
    }
  }
}

// ── Entry point ──────────────────────────────────────────────────────────────

function renderHeatMap(containerId, tracks, opts) {
  opts = opts || {};
  if (typeof maplibregl === "undefined") return;
  const el = document.getElementById(containerId);
  if (!el) return;

  // Re-entrant: tear down any prior map/listeners on this container.
  if (el.__heatmapMap) { el.__heatmapMap.remove(); el.__heatmapMap = null; }

  const valid = tracks.filter((t) => t.coords && t.coords.length >= 2);
  if (!valid.length) { showEmpty(el); return; }

  const bounds = computeBounds(valid);
  if (!bounds) { showEmpty(el); return; }

  const map = new maplibregl.Map({
    container: el,
    style: "https://tiles.openfreemap.org/styles/liberty",
    bounds: bounds,
    fitBoundsOptions: { padding: 48, maxZoom: 13 },
    scrollZoom: true,
    touchZoomRotate: true,
    attributionControl: false,
  });
  el.__heatmapMap = map;

  map.addControl(new maplibregl.NavigationControl(), "top-right");
  map.addControl(new maplibregl.AttributionControl({ compact: true }), "bottom-right");

  const infoEl  = opts.infoPanel ? document.getElementById(opts.infoPanel) : null;
  const dotEl   = infoEl ? document.getElementById("heatmap-info-dot")   : null;
  const sportEl = infoEl ? document.getElementById("heatmap-info-sport") : null;
  const dateEl  = infoEl ? document.getElementById("heatmap-info-date")  : null;
  const statsEl = infoEl ? document.getElementById("heatmap-info-stats") : null;

  function fmtDist(m) {
    return opts.imperial
      ? (m / METERS_PER_MILE).toFixed(1) + " mi"
      : (m / METERS_PER_KM).toFixed(1) + " km";
  }
  function fmtElev(m) {
    return opts.imperial
      ? Math.round(m * FEET_PER_METER) + " ft"
      : Math.round(m) + " m";
  }
  function fmtDuration(secs) {
    const h = Math.floor(secs / 3600);
    const m = Math.floor((secs % 3600) / 60);
    return h > 0 ? h + "h " + m + "m" : m + "m";
  }

  const filtersEl = opts.filters ? document.getElementById(opts.filters) : null;
  let hovered = null;

  if (filtersEl) {
    filtersEl.innerHTML = "";
    const sports = [...new Set(valid.map((t) => t.sport))].sort();
    for (const sport of ["all", ...sports]) {
      const btn = document.createElement("button");
      btn.className = "heatmap-filter" + (sport === "all" ? " active" : "");
      btn.dataset.sport = sport;
      btn.textContent = sport;
      filtersEl.appendChild(btn);
    }
    filtersEl.addEventListener("click", (e) => {
      const btn = e.target.closest(".heatmap-filter");
      if (!btn) return;
      // The style may not have finished loading on a very fast click.
      if (!map.getLayer("tracks-line")) return;
      const sport = btn.dataset.sport;
      filtersEl.querySelectorAll(".heatmap-filter")
        .forEach((b) => b.classList.toggle("active", b === btn));
      const f = sport === "all" ? null : ["==", ["get", "sport"], sport];
      map.setFilter("tracks-hit", f);
      map.setFilter("tracks-glow", f);
      map.setFilter("tracks-line", f);
      hovered = null;
      if (infoEl) infoEl.style.display = "none";
      applyBaseStyle(map);
    });
  }

  map.on("load", () => {
    map.addSource("tracks", {
      type: "geojson",
      data: { type: "FeatureCollection", features: buildFeatures(valid) },
    });

    map.addLayer({
      id: "tracks-hit",
      type: "line",
      source: "tracks",
      layout: LINE_LAYOUT,
      paint: { "line-color": "transparent", "line-width": HIT_WIDTH },
    });

    map.addLayer({
      id: "tracks-glow",
      type: "line",
      source: "tracks",
      layout: LINE_LAYOUT,
      paint: {
        "line-color": HEAT_COLOR,
        "line-blur":  GLOW_BLUR,
        ...TRACK_STYLE["tracks-glow"].base,
      },
    });

    map.addLayer({
      id: "tracks-line",
      type: "line",
      source: "tracks",
      layout: LINE_LAYOUT,
      paint: {
        "line-color": HEAT_COLOR,
        ...TRACK_STYLE["tracks-line"].base,
      },
    });

    map.on("mousemove", "tracks-hit", (e) => {
      if (!e.features.length) return;
      const props = e.features[0].properties;
      if (props.workout_id === hovered) return;
      hovered = props.workout_id;
      map.getCanvas().style.cursor = "pointer";
      applyHoverStyle(map, hovered);

      if (infoEl) {
        if (dotEl)   dotEl.style.background = HEAT_COLOR;
        if (sportEl) sportEl.textContent = props.sport || "";
        if (dateEl)  dateEl.textContent = props.date || "";
        if (statsEl) {
          const parts = [];
          if (props.distance_meters > 0)       parts.push(fmtDist(props.distance_meters));
          if (props.duration_secs > 0)         parts.push(fmtDuration(props.duration_secs));
          if (props.elevation_gain_meters > 0) parts.push("↑ " + fmtElev(props.elevation_gain_meters));
          statsEl.textContent = parts.join("  ·  ");
        }
        infoEl.style.display = "block";
      }
    });

    map.on("mouseleave", "tracks-hit", () => {
      hovered = null;
      map.getCanvas().style.cursor = "";
      applyBaseStyle(map);
      if (infoEl) infoEl.style.display = "none";
    });

    let activePopup = null;

    map.on("click", "tracks-hit", (e) => {
      if (activePopup) { activePopup.remove(); activePopup = null; }

      const seen = new Set();
      const unique = map.queryRenderedFeatures(e.point, { layers: ["tracks-hit"] }).filter((f) => {
        if (seen.has(f.properties.workout_id)) return false;
        seen.add(f.properties.workout_id);
        return true;
      });

      if (!unique.length) return;

      if (unique.length === 1) {
        window.location.href = "/workouts/" + encodeURIComponent(unique[0].properties.workout_id);
        return;
      }

      const items = unique.map((f) => {
        const p = f.properties;
        return `<a class="heatmap-popup-item" href="/workouts/${encodeURIComponent(p.workout_id)}">` +
          `<span class="heatmap-popup-dot" style="background:${HEAT_COLOR}"></span>` +
          `<span class="heatmap-popup-body">` +
          `<span class="heatmap-popup-sport">${escHtml(p.sport)}</span>` +
          `<span class="heatmap-popup-meta">${escHtml(p.date)}&nbsp;&nbsp;${escHtml(fmtDist(p.distance_meters))}&nbsp;&nbsp;${escHtml(fmtDuration(p.duration_secs || 0))}</span>` +

          `<span class="heatmap-popup-sport">${p.sport}</span>` +
          `<span class="heatmap-popup-meta">${p.date}&nbsp;&nbsp;${fmtDist(p.distance_meters)}&nbsp;&nbsp;${fmtDuration(p.duration_secs || 0)}</span>` +
          `</span></a>`;
      }).join("");

      activePopup = new maplibregl.Popup({ closeButton: true, closeOnClick: true, maxWidth: "260px", className: "heatmap-popup-wrap" })
        .setLngLat(e.lngLat)
        .setHTML(`<div class="heatmap-popup"><div class="heatmap-popup-title">${unique.length} workouts here</div>${items}</div>`)
        .addTo(map);

      activePopup.on("close", () => { activePopup = null; });
    });
  });
}
