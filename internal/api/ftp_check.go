package api

import (
	"encoding/json"
	"net/http"
	"time"
)

// maxFTPWatts matches the settings form's bound.
const maxFTPWatts = 600

// GET /api/athlete/ftp-check — how the FTP compares with the best efforts of
// the last 90 days (models.FTPCheck).
func (h *Handler) GetFTPCheck(w http.ResponseWriter, r *http.Request) {
	check, err := h.db.FTPCheck(time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	writeJSON(w, http.StatusOK, check)
}

// PUT /api/athlete/ftp — set the FTP alone, effective now; the rest of the
// profile is untouched. Returns the FTP check against the new value.
func (h *Handler) SetFTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FTPWatts int `json:"ftp_watts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.FTPWatts <= 0 || body.FTPWatts > maxFTPWatts {
		writeError(w, http.StatusBadRequest, "ftp_watts must be between 1 and 600")
		return
	}
	if err := h.db.SetFTP(body.FTPWatts); err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	h.GetFTPCheck(w, r)
}

// POST /api/athlete/ftp-check/dismiss — keep the current FTP despite the
// check. The prompt stays hidden for 30 days, or until the FTP or the
// verdict changes.
func (h *Handler) DismissFTPCheck(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	check, err := h.db.FTPCheck(now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if !check.Actionable() {
		writeError(w, http.StatusConflict, "the FTP check has nothing to dismiss")
		return
	}
	if err := h.db.DismissFTPCheck(check, now); err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	check.Dismissed = true
	writeJSON(w, http.StatusOK, check)
}
