package api

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/dropbox"
	"github.com/fitbase/fitbase/internal/importer"
)

const dropboxIntegration = "dropbox"

// concurrentDropboxDownloads is the number of parallel Dropbox downloads.
const concurrentDropboxDownloads = 4

// dropboxLongpoller watches for new files using Dropbox's list_folder/longpoll API.
type dropboxLongpoller struct {
	db       *db.DB
	importer *importer.Importer
	cancel   context.CancelFunc
	mu       sync.Mutex
}

func (l *dropboxLongpoller) start(client *dropbox.Client, cursor string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cancel != nil {
		l.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel
	go l.run(ctx, client, cursor)
	slog.Info("dropbox longpoll started")
}

func (l *dropboxLongpoller) stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cancel != nil {
		l.cancel()
		l.cancel = nil
		slog.Info("dropbox longpoll stopped")
	}
}

func (l *dropboxLongpoller) running() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cancel != nil
}

func (l *dropboxLongpoller) run(ctx context.Context, client *dropbox.Client, cursor string) {
	const pollTimeout = 90 // seconds; Dropbox max is 480
	for {
		if ctx.Err() != nil {
			return
		}

		changes, backoff, err := client.Longpoll(ctx, cursor, pollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Error("dropbox longpoll error", "err", err)
			select {
			case <-time.After(30 * time.Second):
			case <-ctx.Done():
				return
			}
			continue
		}

		if backoff > 0 {
			select {
			case <-time.After(time.Duration(backoff) * time.Second):
			case <-ctx.Done():
				return
			}
		}

		if !changes {
			continue
		}

		// Drain all pages of changes.
		for {
			files, newCursor, hasMore, err := client.ListFolderContinue(ctx, cursor)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				slog.Error("dropbox longpoll: list_folder/continue", "err", err)
				break
			}
			cursor = newCursor
			if saveErr := l.db.SetDropboxCursor(cursor); saveErr != nil {
				slog.Warn("dropbox: save cursor", "err", saveErr)
			}
			for _, f := range files {
				data, dlErr := client.Download(ctx, f.PathLower)
				if dlErr != nil {
					slog.Error("dropbox longpoll: download", "file", f.Name, "err", dlErr)
					continue
				}
				id, importErr := l.importer.ImportBytes(data, f.Name)
				if importErr != nil {
					slog.Error("dropbox longpoll: import", "file", f.Name, "err", importErr)
					continue
				}
				if id != "" {
					slog.Info("dropbox: auto-imported new file", "file", f.Name, "id", id)
				}
			}
			if !hasMore {
				break
			}
		}
	}
}

// DropboxHandler manages Dropbox FIT file sync (no OAuth — uses a direct access token).
type DropboxHandler struct {
	db         *db.DB
	importer   *importer.Importer
	longpoller *dropboxLongpoller
	syncMgr    *SyncManager
}

func NewDropboxHandler(database *db.DB, importer *importer.Importer, sm *SyncManager) *DropboxHandler {
	return &DropboxHandler{
		db:         database,
		importer:   importer,
		longpoller: &dropboxLongpoller{db: database, importer: importer},
		syncMgr:    sm,
	}
}

// StartLongpollIfEnabled restores the longpoll watcher on server startup.
func (h *DropboxHandler) StartLongpollIfEnabled(client *dropbox.Client, cursor string) {
	h.longpoller.start(client, cursor)
}

// StopLongpoll stops the background longpoll watcher (called by the Intervals handler for mutual exclusivity).
func (h *DropboxHandler) StopLongpoll() { h.longpoller.stop() }

// Sync downloads new .fit files from the configured Dropbox folder and imports them.
// With ?stream=1 it sends SSE progress events; otherwise it fires in the background.
func (h *DropboxHandler) Sync(w http.ResponseWriter, r *http.Request) {
	token, err := h.db.GetIntegrationToken(dropboxIntegration)
	if err != nil || token == "" {
		writeError(w, http.StatusUnauthorized, "dropbox not connected")
		return
	}
	folderPath, _, err := h.db.GetIntegrationCredentials(dropboxIntegration)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	client := dropbox.New(token)

	if r.URL.Query().Get("stream") == "1" {
		h.syncStream(w, r, client, folderPath)
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		imported, skipped, failed := h.syncFiles(ctx, client, folderPath, nil)
		slog.Info("dropbox sync complete", "imported", imported, "skipped", skipped, "failed", failed)
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "sync started"})
}

func (h *DropboxHandler) syncStream(w http.ResponseWriter, r *http.Request, client *dropbox.Client, folderPath string) {
	setupSSE(w)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	files, err := client.ListFITFiles(ctx, folderPath)
	if err != nil {
		writeSSE(w, "error", map[string]string{"error": "list files: " + err.Error()})
		return
	}

	// One query to get all already-imported filenames, then filter in memory.
	known, _ := h.importer.AllImportedFilenames()
	var pending []dropbox.FileMetadata
	for _, f := range files {
		if _, ok := known[f.Name]; !ok {
			pending = append(pending, f)
		}
	}

	writeSSE(w, "start", map[string]any{"total": len(files), "pending": len(pending)})

	alreadySkipped := len(files) - len(pending)
	imported, skipped, failed := downloadDropboxFiles(ctx, client, pending, h.importer, func(name string, done, total int) {
		writeSSE(w, "file", map[string]any{"name": name, "index": done, "total": total})
	})

	writeSSE(w, "done", map[string]any{
		"imported": imported,
		"skipped":  skipped + alreadySkipped,
		"failed":   failed,
	})

	// Save latest cursor after sync so the longpoller starts from the right place.
	latestCtx, latestCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer latestCancel()
	if cursor, cursorErr := client.GetLatestCursor(latestCtx, folderPath); cursorErr == nil {
		h.db.SetDropboxCursor(cursor) //nolint:errcheck
		// If longpoll is running, restart it with the fresh cursor.
		if h.longpoller.running() {
			h.longpoller.start(client, cursor)
		}
	}
}

// syncFiles lists the folder, pre-filters already-imported files, then downloads
// and imports new ones concurrently. onFile is called per completed download (nil for background).
func (h *DropboxHandler) syncFiles(ctx context.Context, client *dropbox.Client, folderPath string, onFile func(string, int, int)) (imported, skipped, failed int) {
	files, err := client.ListFITFiles(ctx, folderPath)
	if err != nil {
		slog.Error("dropbox sync: list files", "err", err)
		return
	}

	known, _ := h.importer.AllImportedFilenames()
	var pending []dropbox.FileMetadata
	for _, f := range files {
		if _, ok := known[f.Name]; !ok {
			pending = append(pending, f)
		}
	}

	imp, sk, fa := downloadDropboxFiles(ctx, client, pending, h.importer, onFile)
	return imp, sk + len(files) - len(pending), fa
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

	if !body.Enabled {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	// Enable: ensure we have a cursor to start from.
	token, _ := h.db.GetIntegrationToken(dropboxIntegration)
	folderPath, _, _ := h.db.GetIntegrationCredentials(dropboxIntegration)
	client := dropbox.New(token)

	cursor, _ := h.db.GetDropboxCursor()
	if cursor == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var err error
		cursor, err = client.GetLatestCursor(ctx, folderPath)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to get cursor: "+err.Error())
			return
		}
		h.db.SetDropboxCursor(cursor) //nolint:errcheck
	}

	h.longpoller.start(client, cursor)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Disconnect removes the stored Dropbox token and settings.
func (h *DropboxHandler) Disconnect(w http.ResponseWriter, r *http.Request) {
	h.longpoller.stop()
	if err := h.db.DeleteIntegrationToken(dropboxIntegration); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.db.DeleteIntegrationCredentials(dropboxIntegration); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// downloadDropboxFiles downloads files concurrently and imports them sequentially.
// Downloads run in a pool of concurrentDropboxDownloads workers; the import loop
// stays single-threaded to avoid SQLite write contention.
// onFile is called after each file is processed (nil for background syncs).
func downloadDropboxFiles(ctx context.Context, client *dropbox.Client, files []dropbox.FileMetadata, importer *importer.Importer, onFile func(name string, done, total int)) (imported, skipped, failed int) {
	if len(files) == 0 {
		return
	}

	type result struct {
		name string
		data []byte
		err  error
	}

	ch := make(chan result, len(files))
	sem := make(chan struct{}, concurrentDropboxDownloads)
	var wg sync.WaitGroup

	for _, f := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func(file dropbox.FileMetadata) {
			defer wg.Done()
			defer func() { <-sem }()
			data, err := client.Download(ctx, file.PathLower)
			ch <- result{file.Name, data, err}
		}(f)
	}
	go func() { wg.Wait(); close(ch) }()

	done := 0
	for r := range ch {
		done++
		if onFile != nil {
			onFile(r.name, done, len(files))
		}
		if r.err != nil {
			slog.Error("dropbox: download failed", "file", r.name, "err", r.err)
			failed++
			continue
		}
		id, err := importer.ImportBytes(r.data, r.name)
		if err != nil {
			slog.Error("dropbox: import failed", "file", r.name, "err", err)
			failed++
		} else if id != "" {
			imported++
		} else {
			skipped++
		}
	}
	return
}
