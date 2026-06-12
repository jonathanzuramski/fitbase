// fitbase plan modal — wires up the calendar's quick-add planned-workout dialog:
// open/close, the structured-intervals JSON editor with its live profile preview
// (rendered by plan-profile.js, which must be loaded first), submit, and delete.

(() => {
  const dialog = document.getElementById("plan-dialog");
  const form = document.getElementById("plan-form");
  const dateInput = document.getElementById("plan-date");
  const formError = document.getElementById("plan-error");
  const submitBtn = document.getElementById("plan-submit");
  const durationInput = document.getElementById("plan-duration");
  const useIntervals = document.getElementById("plan-use-intervals");
  const intervalsSection = document.getElementById("plan-intervals-section");
  const intervalsJSON = document.getElementById("plan-intervals-json");
  const profileCanvas = document.getElementById("plan-profile");
  const profileMeta = document.getElementById("plan-profile-meta");

  // A worked example inserted when the user enables structured intervals, so the
  // JSON shape (flat target, ramp, and a repeat group) is self-explanatory.
  const EXAMPLE_INTERVALS_JSON = JSON.stringify([
    { kind: "warmup", duration_secs: 600, target_pct_ftp_start: 45, target_pct_ftp_end: 65 },
    {
      repeat: 4, steps: [
        { kind: "work", duration_secs: 300, target_pct_ftp: 105 },
        { kind: "recovery", duration_secs: 180, target_pct_ftp: 55 }
      ]
    },
    { kind: "cooldown", duration_secs: 300, target_pct_ftp_start: 55, target_pct_ftp_end: 40 }
  ], null, 2);

  // Holds the last successful parse so submit can reuse it without re-parsing.
  let intervalsState = { ok: false, intervals: [], total: 0 };

  // ---- dialog open/close (with a fallback for browsers without <dialog>:
  // open non-modally, leaving the rest of the page interactive) ----

  function openDialog() {
    if (typeof dialog.showModal === "function") dialog.showModal();
    else dialog.setAttribute("open", "");
  }

  function closeDialog() {
    if (typeof dialog.close === "function") dialog.close();
    else dialog.removeAttribute("open");
  }

  // ---- structured-intervals editor + live preview ----

  // autosizeJSON grows the textarea to fit its content so the example (and any
  // edits) show in full without an inner scrollbar.
  function autosizeJSON() {
    intervalsJSON.style.height = "auto";
    intervalsJSON.style.height = intervalsJSON.scrollHeight + "px";
  }

  // setPreviewError clears the profile and shows a message under it ("" for the
  // blank state); intervalsState is invalidated either way so submit can't use
  // stale segments.
  function setPreviewError(message) {
    intervalsState = { ok: false, intervals: [], total: 0, error: message };
    planDrawProfile(profileCanvas, [], 0);
    profileMeta.textContent = message;
    profileMeta.classList.toggle("plan-profile-err", message !== "");
  }

  // parseAndRenderIntervals reads the JSON textarea, draws the profile preview,
  // and (on success) auto-fills the read-only duration from the summed segments.
  function parseAndRenderIntervals() {
    autosizeJSON();
    const raw = intervalsJSON.value.trim();
    if (!raw) return setPreviewError("");
    let parsed;
    try {
      parsed = JSON.parse(raw);
    } catch (err) {
      return setPreviewError("Invalid JSON: " + err.message);
    }
    if (!Array.isArray(parsed)) return setPreviewError("Intervals must be a JSON array.");

    const segments = [];
    const total = planFlatten(parsed, segments, 0);
    intervalsState = { ok: true, intervals: parsed, total };
    profileMeta.classList.remove("plan-profile-err");
    planDrawProfile(profileCanvas, segments, total);
    profileMeta.textContent = total > 0
      ? `Total ${planFormatDuration(total)} · ${segments.length} segment${segments.length === 1 ? "" : "s"}`
      : "No timed segments yet.";
    if (total > 0) durationInput.value = Math.round(total / 60);
  }

  // setIntervalsMode toggles the structured-intervals UI. When on, duration is
  // derived (read-only); when off, the manual minute field is used as before.
  function setIntervalsMode(on) {
    intervalsSection.hidden = !on;
    durationInput.readOnly = on;
    dialog.classList.toggle("modal-wide", on); // widen first so the canvas measures correctly
    if (on) {
      if (!intervalsJSON.value.trim()) intervalsJSON.value = EXAMPLE_INTERVALS_JSON;
      parseAndRenderIntervals();
    } else {
      intervalsState = { ok: false, intervals: [], total: 0 };
    }
  }

  useIntervals.addEventListener("change", () => setIntervalsMode(useIntervals.checked));
  intervalsJSON.addEventListener("input", parseAndRenderIntervals);

  // Tab inserts two spaces instead of moving focus, so JSON can be indented in place.
  intervalsJSON.addEventListener("keydown", (e) => {
    if (e.key !== "Tab" || e.shiftKey) return;
    e.preventDefault();
    const el = intervalsJSON, s = el.selectionStart, en = el.selectionEnd;
    el.value = el.value.slice(0, s) + "  " + el.value.slice(en);
    el.selectionStart = el.selectionEnd = s + 2;
    parseAndRenderIntervals();
  });

  // On blur, pretty-print valid JSON (2-space indent) so the layout stays tidy
  // without manual formatting. Invalid JSON is left untouched for the user to fix.
  intervalsJSON.addEventListener("blur", () => {
    const raw = intervalsJSON.value.trim();
    if (!raw) return;
    try {
      intervalsJSON.value = JSON.stringify(JSON.parse(raw), null, 2);
    } catch (_) {
      return; // leave as-is; the error is already shown under the chart
    }
    parseAndRenderIntervals();
  });

  // Keep the canvas sharp if the viewport changes while the editor is open.
  window.addEventListener("resize", () => {
    if (useIntervals.checked && dialog.open) parseAndRenderIntervals();
  });

  // ---- opening and closing ----

  // Each day cell's "+" button opens the dialog reset to a blank form for that
  // date, with the structured-intervals UI collapsed to its default.
  document.querySelectorAll(".cal-add-btn").forEach((btn) => {
    btn.addEventListener("click", (e) => {
      e.stopPropagation();
      form.reset();
      dateInput.value = btn.dataset.date;
      formError.hidden = true;
      useIntervals.checked = false;
      intervalsJSON.value = "";
      setIntervalsMode(false);
      openDialog();
    });
  });

  document.getElementById("plan-cancel").addEventListener("click", closeDialog); // × in the header
  document.getElementById("plan-cancel-btn").addEventListener("click", closeDialog); // Cancel in the footer

  // ---- submit ----

  // showFormError surfaces a message above the buttons and re-enables submit so
  // the user can fix the problem and retry.
  function showFormError(message) {
    formError.textContent = message;
    formError.hidden = false;
    submitBtn.disabled = false;
  }

  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    formError.hidden = true;
    submitBtn.disabled = true; // stays disabled on success until the reload lands

    const fd = new FormData(form);
    const ifRaw = (fd.get("intensity_factor") || "").toString().trim();
    const body = {
      date: fd.get("date"),
      sport: fd.get("sport") || "cycling",
      title: (fd.get("title") || "").toString().trim(),
      description: (fd.get("description") || "").toString().trim(),
      duration_secs: parseInt(fd.get("duration_mins"), 10) * 60,
    };
    if (ifRaw) body.intensity_factor = parseFloat(ifRaw);

    // Structured intervals: re-parse on submit so a bad edit can't slip through,
    // and let the server derive the authoritative duration from the segments.
    if (useIntervals.checked) {
      parseAndRenderIntervals();
      if (!intervalsState.ok) {
        return showFormError(intervalsState.error || "Fix the intervals JSON before saving.");
      }
      if (intervalsState.total <= 0) {
        return showFormError("Intervals must contain at least one step with duration_secs > 0.");
      }
      body.intervals = intervalsState.intervals;
      body.duration_secs = intervalsState.total;
    }

    try {
      const res = await fetch("/api/planned-workouts", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!res.ok) {
        const j = await res.json().catch(() => ({}));
        throw new Error(j.error || "failed to add");
      }
      closeDialog();
      window.location.reload(); // server-rendered grid; reload keeps state correct
    } catch (err) {
      showFormError(err.message);
    }
  });

  // ---- delete ----

  // Delete a planned workout in place.
  document.querySelectorAll(".cal-planned-del").forEach((btn) => {
    btn.addEventListener("click", async (e) => {
      e.preventDefault();
      e.stopPropagation();
      if (!confirm("Remove this planned workout?")) return;
      const id = btn.dataset.id;
      try {
        const res = await fetch("/api/planned-workouts/" + encodeURIComponent(id), { method: "DELETE" });
        if (!res.ok && res.status !== 204) throw new Error("delete failed");
        btn.closest(".cal-planned").remove();
      } catch (_) {
        alert("Could not remove the planned workout.");
      }
    });
  });
})();
