package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/importer"
	"github.com/fitbase/fitbase/internal/intervals"
	"github.com/go-chi/chi/v5"
)

const intervalsIntegration = "intervals"

// concurrentIntervalsDownloads is the number of parallel intervals.icu downloads.
// Kept low to avoid 429 rate limiting from intervals.icu.
const concurrentIntervalsDownloads = 2

// intervalsPoller periodically syncs new activities from intervals.icu.
type intervalsPoller struct {
	db       *db.DB
	importer *importer.Importer
	cancel   context.CancelFunc
	mu       sync.Mutex
}

const intervalsPollInterval = 15 * time.Minute

func (p *intervalsPoller) start(athleteID, apiKey string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		p.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	go p.run(ctx, athleteID, apiKey)
	slog.Info("intervals.icu auto-sync started", "interval", intervalsPollInterval)
}

func (p *intervalsPoller) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
		slog.Info("intervals.icu auto-sync stopped")
	}
}

func (p *intervalsPoller) run(ctx context.Context, athleteID, apiKey string) {
	// Run an initial sync immediately, then poll on the interval.
	for {
		client := intervals.New(athleteID, apiKey)
		oldest, _ := p.db.GetSyncOldest("intervals")
		imported, skipped, failed := syncIntervalsActivities(ctx, client, p.db, p.importer, oldest)
		if imported > 0 || failed > 0 {
			slog.Info("intervals.icu auto-sync", "imported", imported, "skipped", skipped, "failed", failed)
		}
		select {
		case <-time.After(intervalsPollInterval):
		case <-ctx.Done():
			return
		}
	}
}

const defaultSyncOldest = "2000-01-01"

// syncIntervalsActivities is a standalone sync function usable by both the
// handler and the background poller. oldest is a YYYY-MM-DD lower bound
// ("" defaults to all time).
func syncIntervalsActivities(ctx context.Context, client *intervals.Client, database *db.DB, imp *importer.Importer, oldest string) (imported, skipped, failed int) {
	if oldest == "" {
		oldest = defaultSyncOldest
	}
	activities, err := client.ListActivities(ctx, oldest, "")
	if err != nil {
		slog.Error("intervals.icu sync: list activities", "err", err)
		return
	}

	known, _ := imp.AllImportedFilenames()
	var pending []pendingIntervalsActivity
	for _, act := range activities {
		filename := fmt.Sprintf("intervals-%s.fit", act.ID)
		if _, ok := known[filename]; !ok {
			pending = append(pending, pendingIntervalsActivity{act.ID, filename})
		}
	}

	imp2, sk, fa := downloadIntervalsFiles(ctx, client, pending, imp, nil)
	return imp2, sk + len(activities) - len(pending), fa
}

// IntervalsHandler manages intervals.icu activity sync (API-key based, no OAuth).
type IntervalsHandler struct {
	db       *db.DB
	importer *importer.Importer
	poller   *intervalsPoller
	syncMgr  *SyncManager
}

func NewIntervalsHandler(database *db.DB, importer *importer.Importer, sm *SyncManager) *IntervalsHandler {
	return &IntervalsHandler{
		db:       database,
		importer: importer,
		poller:   &intervalsPoller{db: database, importer: importer},
		syncMgr:  sm,
	}
}

// Sync pulls all activities from intervals.icu and imports their FIT files.
// With ?stream=1 it sends SSE progress events; otherwise it fires in the background.
func (h *IntervalsHandler) Sync(w http.ResponseWriter, r *http.Request) {
	athleteID, apiKey, err := h.db.GetIntegrationCredentials(intervalsIntegration)
	if err != nil || athleteID == "" {
		writeError(w, http.StatusUnauthorized, "intervals.icu not connected")
		return
	}

	client := intervals.New(athleteID, apiKey)

	if r.URL.Query().Get("stream") == "1" {
		h.syncStream(w, r, client)
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
		defer cancel()
		oldest, _ := h.db.GetSyncOldest("intervals")
		imported, skipped, failed := syncIntervalsActivities(ctx, client, h.db, h.importer, oldest)
		slog.Info("intervals.icu sync complete", "imported", imported, "skipped", skipped, "failed", failed)
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "sync started"})
}

func (h *IntervalsHandler) syncStream(w http.ResponseWriter, r *http.Request, client *intervals.Client) {
	setupSSE(w)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	oldest, _ := h.db.GetSyncOldest("intervals")
	if oldest == "" {
		oldest = defaultSyncOldest
	}
	activities, err := client.ListActivities(ctx, oldest, "")
	if err != nil {
		writeSSE(w, "error", map[string]string{"error": "list activities: " + err.Error()})
		return
	}

	// One query to get all already-imported filenames, then filter in memory.
	known, _ := h.importer.AllImportedFilenames()
	var pending []pendingIntervalsActivity
	for _, act := range activities {
		filename := fmt.Sprintf("intervals-%s.fit", act.ID)
		if _, ok := known[filename]; !ok {
			pending = append(pending, pendingIntervalsActivity{act.ID, filename})
		}
	}

	writeSSE(w, "start", map[string]any{"total": len(activities), "pending": len(pending)})

	alreadySkipped := len(activities) - len(pending)
	imported, skipped, failed := downloadIntervalsFiles(ctx, client, pending, h.importer, func(name string, done, total int) {
		writeSSE(w, "file", map[string]any{"name": name, "index": done, "total": total})
	})

	writeSSE(w, "done", map[string]any{
		"imported": imported,
		"skipped":  skipped + alreadySkipped,
		"failed":   failed,
	})
}

// Fetch downloads a single activity by its intervals.icu ID and attempts to import it.
// Returns JSON describing what happened, including the full error if it fails.
// Useful for debugging activities that fail during bulk sync.
func (h *IntervalsHandler) Fetch(w http.ResponseWriter, r *http.Request) {
	athleteID, apiKey, err := h.db.GetIntegrationCredentials(intervalsIntegration)
	if err != nil || athleteID == "" {
		writeError(w, http.StatusUnauthorized, "intervals.icu not connected")
		return
	}

	activityID := chi.URLParam(r, "id")
	client := intervals.New(athleteID, apiKey)

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	data, err := client.DownloadFIT(ctx, activityID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"activity_id": activityID, "status": "download_failed", "error": err.Error()})
		return
	}

	filename := fmt.Sprintf("intervals-%s.fit", activityID)
	id, err := h.importer.ImportBytes(data, filename)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"activity_id": activityID, "status": "import_failed", "error": err.Error()})
		return
	}
	if id == "" {
		writeJSON(w, http.StatusOK, map[string]any{"activity_id": activityID, "status": "skipped", "reason": "already imported"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"activity_id": activityID, "status": "imported", "workout_id": id})
}

// Disconnect removes the stored intervals.icu credentials and stops auto-sync.
func (h *IntervalsHandler) Disconnect(w http.ResponseWriter, r *http.Request) {
	h.syncMgr.Disable("intervals") //nolint:errcheck
	if err := h.db.DeleteIntegrationCredentials(intervalsIntegration); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// SetAutoSync enables or disables the background intervals.icu poller.
// When enabling, the SyncManager disables all other sync sources.
func (h *IntervalsHandler) SetAutoSync(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if !body.Enabled {
		if err := h.syncMgr.Disable("intervals"); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	if err := h.syncMgr.Enable("intervals"); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	athleteID, apiKey, err := h.db.GetIntegrationCredentials(intervalsIntegration)
	if err != nil || athleteID == "" {
		writeError(w, http.StatusBadRequest, "intervals.icu not connected")
		return
	}

	h.poller.start(athleteID, apiKey)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// StartAutoSyncIfEnabled restores the intervals poller on server startup if it was enabled.
func (h *IntervalsHandler) StartAutoSyncIfEnabled() {
	enabled, err := h.db.GetAutoSync("intervals")
	if err != nil || !enabled {
		return
	}
	athleteID, apiKey, err := h.db.GetIntegrationCredentials(intervalsIntegration)
	if err != nil || athleteID == "" {
		return
	}
	h.poller.start(athleteID, apiKey)
}

// StopAutoSync stops the background poller. Used as the registered stop function in SyncManager.
func (h *IntervalsHandler) StopAutoSync() { h.poller.stop() }

type pendingIntervalsActivity struct {
	id       string
	filename string
}

// downloadIntervalsFiles downloads FIT files concurrently and imports them sequentially.
func downloadIntervalsFiles(ctx context.Context, client *intervals.Client, files []pendingIntervalsActivity, importer *importer.Importer, onFile func(name string, done, total int)) (imported, skipped, failed int) {
	if len(files) == 0 {
		return
	}

	type result struct {
		filename string
		data     []byte
		err      error
	}

	ch := make(chan result, len(files))
	sem := make(chan struct{}, concurrentIntervalsDownloads)
	var wg sync.WaitGroup

	for _, f := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func(act pendingIntervalsActivity) {
			defer wg.Done()
			defer func() { <-sem }()
			data, err := client.DownloadFIT(ctx, act.id)
			ch <- result{act.filename, data, err}
		}(f)
	}
	go func() { wg.Wait(); close(ch) }()

	done := 0
	for r := range ch {
		done++
		if onFile != nil {
			onFile(r.filename, done, len(files))
		}
		if r.err != nil {
			slog.Error("intervals: download FIT failed", "file", r.filename, "err", r.err)
			failed++
			continue
		}
		id, err := importer.ImportBytes(r.data, r.filename)
		if err != nil {
			slog.Error("intervals: import failed", "file", r.filename, "err", err)
			failed++
		} else if id != "" {
			imported++
		} else {
			skipped++
		}
	}
	return
}
