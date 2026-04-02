package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/fitbase/fitbase/internal/syncer"
)

// DropboxHandler is thin HTTP glue for Dropbox sync endpoints.
type DropboxHandler struct {
	syncMgr *syncer.Manager
	source  *syncer.DropboxSource
}

func NewDropboxHandler(sm *syncer.Manager, src *syncer.DropboxSource) *DropboxHandler {
	return &DropboxHandler{syncMgr: sm, source: src}
}

// Sync downloads new .fit files from the configured Dropbox folder and imports them.
// With ?stream=1 it sends SSE progress events; otherwise it fires in the background.
func (h *DropboxHandler) Sync(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("stream") == "1" {
		setupSSE(w)
		imported, skipped, failed := h.source.Sync(r.Context(), func(event string, data any) {
			writeSSE(w, event, data)
		})
		writeSSE(w, "done", map[string]any{"imported": imported, "skipped": skipped, "failed": failed})
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		imported, skipped, failed := h.source.Sync(ctx, nil)
		slog.Info("dropbox sync complete", "imported", imported, "skipped", skipped, "failed", failed)
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "sync started"})
}

// SetLongpoll enables or disables the background longpoll watcher.
func (h *DropboxHandler) SetLongpoll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if body.Enabled {
		if err := h.syncMgr.Enable("dropbox"); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		if err := h.syncMgr.Disable("dropbox"); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Disconnect removes the stored Dropbox token and settings.
func (h *DropboxHandler) Disconnect(w http.ResponseWriter, r *http.Request) {
	if err := h.syncMgr.Disable("dropbox"); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.source.Disconnect(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
