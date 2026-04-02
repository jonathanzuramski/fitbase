package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/fitbase/fitbase/internal/syncer"
	"github.com/go-chi/chi/v5"
)

// IntervalsHandler is thin HTTP glue for intervals.icu sync endpoints.
type IntervalsHandler struct {
	syncMgr *syncer.Manager
	source  *syncer.IntervalsSource
}

func NewIntervalsHandler(sm *syncer.Manager, src *syncer.IntervalsSource) *IntervalsHandler {
	return &IntervalsHandler{syncMgr: sm, source: src}
}

// Sync pulls all activities from intervals.icu and imports their FIT files.
// With ?stream=1 it sends SSE progress events; otherwise it fires in the background.
func (h *IntervalsHandler) Sync(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("stream") == "1" {
		setupSSE(w)
		imported, skipped, failed := h.source.Sync(r.Context(), func(event string, data any) {
			writeSSE(w, event, data)
		})
		writeSSE(w, "done", map[string]any{"imported": imported, "skipped": skipped, "failed": failed})
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
		defer cancel()
		imported, skipped, failed := h.source.Sync(ctx, nil)
		slog.Info("intervals.icu sync complete", "imported", imported, "skipped", skipped, "failed", failed)
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "sync started"})
}

// Fetch downloads a single activity by its intervals.icu ID and attempts to import it.
func (h *IntervalsHandler) Fetch(w http.ResponseWriter, r *http.Request) {
	activityID := chi.URLParam(r, "id")

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	workoutID, status, err := h.source.Fetch(ctx, activityID)
	resp := map[string]any{"activity_id": activityID, "status": status}
	if err != nil {
		resp["error"] = err.Error()
	}
	if workoutID != "" {
		resp["workout_id"] = workoutID
	}
	if status == "skipped" {
		resp["reason"] = "already imported"
	}
	writeJSON(w, http.StatusOK, resp)
}

// Disconnect removes the stored intervals.icu credentials and stops auto-sync.
func (h *IntervalsHandler) Disconnect(w http.ResponseWriter, r *http.Request) {
	if err := h.syncMgr.Disable("intervals"); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.source.Disconnect(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// SetAutoSync enables or disables the background intervals.icu poller.
func (h *IntervalsHandler) SetAutoSync(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if body.Enabled {
		if err := h.syncMgr.Enable("intervals"); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		if err := h.syncMgr.Disable("intervals"); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
