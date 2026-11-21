// "How did it feel?" on the ride page: a 1–10 rating (click again to clear)
// and a note, saved to PUT /api/workouts/{id}/feedback as they change. The
// coach reads both when it reviews rides and plans the week.
(function () {
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
})();
