function renderHeatMap(containerId, tracks, opts) {
  opts = opts || {};
  if (typeof maplibregl === "undefined") return;
  const el = document.getElementById(containerId);
  if (!el) return;

  const valid = tracks.filter((t) => t.coords && t.coords.length >= 2);
  if (!valid.length) {
    el.textContent = "No outdoor activities with GPS data yet.";
    el.style.cssText = "display:flex;align-items:center;justify-content:center;color:var(--text-muted);font-size:13px";
    return;
  }

  const cutoff = new Date();
  cutoff.setDate(cutoff.getDate() - 14);
  const boundsSource = valid.filter(t => new Date(t.date) >= cutoff);
  const bSource = boundsSource.length > 0 ? boundsSource : valid;

  let minLng = Infinity, maxLng = -Infinity, minLat = Infinity, maxLat = -Infinity;
  for (const t of bSource) {
    for (const [lng, lat] of t.coords) {
      if (lng === 0 && lat === 0) continue;
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

  const glowWidthBase = ["interpolate", ["linear"], ["zoom"], 7, 3, 10, 5, 13, 7, 16, 10];
  const lineWidthBase = ["interpolate", ["linear"], ["zoom"], 7, 0.5, 10, 0.9, 13, 1.5, 16, 2.0];

  const infoEl  = opts.infoPanel ? document.getElementById(opts.infoPanel) : null;
  const dotEl   = infoEl ? document.getElementById("heatmap-info-dot")   : null;
  const sportEl = infoEl ? document.getElementById("heatmap-info-sport") : null;
  const dateEl  = infoEl ? document.getElementById("heatmap-info-date")  : null;
  const statsEl = infoEl ? document.getElementById("heatmap-info-stats") : null;

  function fmtDist(m) {
    return opts.imperial ? (m / 1609.34).toFixed(1) + " mi" : (m / 1000).toFixed(1) + " km";
  }
  function fmtElev(m) {
    return opts.imperial ? Math.round(m * 3.28084) + " ft" : Math.round(m) + " m";
  }
  function fmtDuration(secs) {
    const h = Math.floor(secs / 3600);
    const m = Math.floor((secs % 3600) / 60);
    return h > 0 ? h + "h " + m + "m" : m + "m";
  }

  const filtersEl = opts.filters ? document.getElementById(opts.filters) : null;
  let hovered = null;

  if (filtersEl) {
    const sports = [...new Set(valid.map(t => t.sport))].sort();
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
      const sport = btn.dataset.sport;
      filtersEl.querySelectorAll(".heatmap-filter").forEach(b => b.classList.toggle("active", b === btn));
      const f = sport === "all" ? null : ["==", ["get", "sport"], sport];
      map.setFilter("tracks-hit", f);
      map.setFilter("tracks-glow", f);
      map.setFilter("tracks-line", f);
      hovered = null;
      if (infoEl) infoEl.style.display = "none";
      map.setPaintProperty("tracks-glow", "line-opacity", 0.11);
      map.setPaintProperty("tracks-glow", "line-width", glowWidthBase);
      map.setPaintProperty("tracks-line", "line-opacity", 0.38);
      map.setPaintProperty("tracks-line", "line-width", lineWidthBase);
    });
  }

  map.on("load", () => {
    const features = valid.map((t) => ({
      type: "Feature",
      geometry: { type: "LineString", coordinates: t.coords },
      properties: {
        workout_id:            t.workout_id,
        color:                 SPORT_COLORS[t.sport] || "#60a5fa",
        sport:                 t.sport,
        date:                  t.date,
        distance_meters:       t.distance_meters || 0,
        duration_secs:         t.duration_secs || 0,
        elevation_gain_meters: t.elevation_gain_meters || 0,
      },
    }));

    map.addSource("tracks", {
      type: "geojson",
      data: { type: "FeatureCollection", features },
    });

    map.addLayer({
      id: "tracks-hit",
      type: "line",
      source: "tracks",
      layout: { "line-cap": "round", "line-join": "round" },
      paint: { "line-color": "transparent", "line-width": 12 },
    });

    map.addLayer({
      id: "tracks-glow",
      type: "line",
      source: "tracks",
      layout: { "line-cap": "round", "line-join": "round" },
      paint: {
        "line-color":   ["get", "color"],
        "line-width":   glowWidthBase,
        "line-opacity": 0.11,
        "line-blur":    4,
      },
    });

    map.addLayer({
      id: "tracks-line",
      type: "line",
      source: "tracks",
      layout: { "line-cap": "round", "line-join": "round" },
      paint: {
        "line-color":   ["get", "color"],
        "line-width":   lineWidthBase,
        "line-opacity": 0.38,
      },
    });

    map.on("mousemove", "tracks-hit", (e) => {
      if (!e.features.length) return;
      const props = e.features[0].properties;
      if (props.workout_id === hovered) return;
      hovered = props.workout_id;
      map.getCanvas().style.cursor = "pointer";
      map.setPaintProperty("tracks-glow", "line-opacity", ["case", ["==", ["get", "workout_id"], hovered], 0.35, 0.04]);
      map.setPaintProperty("tracks-glow", "line-width",   ["case", ["==", ["get", "workout_id"], hovered], 12,   5  ]);
      map.setPaintProperty("tracks-line", "line-opacity", ["case", ["==", ["get", "workout_id"], hovered], 0.95, 0.15]);
      map.setPaintProperty("tracks-line", "line-width",   ["case", ["==", ["get", "workout_id"], hovered], 2.5,  1.2 ]);

      if (infoEl) {
        if (dotEl)   dotEl.style.background = props.color || "#60a5fa";
        if (sportEl) sportEl.textContent = props.sport || "";
        if (dateEl)  dateEl.textContent = props.date || "";
        if (statsEl) {
          const parts = [];
          if (props.distance_meters > 0)       parts.push(fmtDist(props.distance_meters));
          if (props.duration_secs > 0)          parts.push(fmtDuration(props.duration_secs));
          if (props.elevation_gain_meters > 0)  parts.push("↑ " + fmtElev(props.elevation_gain_meters));
          statsEl.textContent = parts.join("  ·  ");
        }
        infoEl.style.display = "block";
      }
    });

    map.on("mouseleave", "tracks-hit", () => {
      hovered = null;
      map.getCanvas().style.cursor = "";
      map.setPaintProperty("tracks-glow", "line-opacity", 0.11);
      map.setPaintProperty("tracks-glow", "line-width",   glowWidthBase);
      map.setPaintProperty("tracks-line", "line-opacity", 0.38);
      map.setPaintProperty("tracks-line", "line-width",   lineWidthBase);
      if (infoEl) infoEl.style.display = "none";
    });

    let activePopup = null;

    map.on("click", "tracks-hit", (e) => {
      if (activePopup) { activePopup.remove(); activePopup = null; }

      const seen = new Set();
      const unique = map.queryRenderedFeatures(e.point, { layers: ["tracks-hit"] }).filter(f => {
        if (seen.has(f.properties.workout_id)) return false;
        seen.add(f.properties.workout_id);
        return true;
      });

      if (!unique.length) return;

      if (unique.length === 1) {
        window.location.href = "/workouts/" + unique[0].properties.workout_id;
        return;
      }

      const items = unique.map(f => {
        const p = f.properties;
        return `<a class="heatmap-popup-item" href="/workouts/${p.workout_id}">` +
          `<span class="heatmap-popup-dot" style="background:${p.color}"></span>` +
          `<span class="heatmap-popup-body">` +
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
