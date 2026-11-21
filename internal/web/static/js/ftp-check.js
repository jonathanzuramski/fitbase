// FTP actions shared by the dashboard's FTP check banner, the settings note and
// the ride page's "new FTP detected" banner:
//   [data-ftp-set="215"]  sets the FTP alone (PUT /api/athlete/ftp), then reloads
//                         so zones and numbers redraw against it
//   [data-ftp-dismiss]    keeps the current FTP: hides the check's prompt for 30
//                         days (POST /api/athlete/ftp-check/dismiss)
document.addEventListener("click", async (e) => {
  const set = e.target.closest("[data-ftp-set]");
  const keep = e.target.closest("[data-ftp-dismiss]");
  const btn = set || keep;
  if (!btn) return;
  e.preventDefault();
  btn.disabled = true;
  try {
    const res = set
      ? await fetch("/api/athlete/ftp", {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ ftp_watts: Number(set.dataset.ftpSet) }),
        })
      : await fetch("/api/athlete/ftp-check/dismiss", { method: "POST" });
    if (!res.ok) {
      const body = await res.json().catch(() => ({}));
      throw new Error(body.error || res.statusText);
    }
    if (set) {
      location.reload();
      return;
    }
    const banner = keep.closest("[data-ftp-check]");
    if (banner) banner.hidden = true;
  } catch (err) {
    btn.disabled = false;
    alert("Couldn't update FTP: " + err.message);
  }
});
