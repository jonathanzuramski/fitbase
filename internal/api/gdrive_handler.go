package api

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/gdrive"
	"github.com/fitbase/fitbase/internal/importer"
)

const gdriveIntegration = "gdrive"

// GDriveHandler manages Google Drive OAuth and FIT file backup.
type GDriveHandler struct {
	db       *db.DB
	importer *importer.Importer
}

func NewGDriveHandler(database *db.DB, importer *importer.Importer) *GDriveHandler {
	return &GDriveHandler{db: database, importer: importer}
}

func (h *GDriveHandler) credentials() (clientID, clientSecret string, err error) {
	return h.db.GetIntegrationCredentials(gdriveIntegration)
}

// Connect starts the OAuth loopback flow: opens a browser URL and waits for the callback.
func (h *GDriveHandler) Connect(w http.ResponseWriter, r *http.Request) {
	clientID, clientSecret, err := h.credentials()
	if err != nil || clientID == "" {
		writeError(w, http.StatusBadRequest, "google drive credentials not configured")
		return
	}

	// Bind to a random local port for the callback.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot start callback server")
		return
	}

	port := ln.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://localhost:%d", port)
	state := fmt.Sprintf("%d", time.Now().UnixNano())
	authURL := gdrive.AuthURL(clientID, clientSecret, redirectURI, state)

	go h.runCallbackServer(ln, state, clientID, clientSecret, redirectURI)

	http.Redirect(w, r, authURL, http.StatusFound)
}

func (h *GDriveHandler) runCallbackServer(ln net.Listener, state, clientID, clientSecret, redirectURI string) {
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("state") != state {
				http.Error(w, "state mismatch", http.StatusBadRequest)
				return
			}
			code := r.URL.Query().Get("code")
			if code == "" {
				http.Error(w, "missing code", http.StatusBadRequest)
				return
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			token, err := gdrive.Exchange(ctx, code, clientID, clientSecret, redirectURI)
			if err != nil {
				slog.Error("gdrive: token exchange failed", "err", err)
				http.Error(w, "token exchange failed", http.StatusInternalServerError)
				return
			}

			tokenJSON, err := gdrive.TokenToJSON(token)
			if err != nil {
				slog.Error("gdrive: serialize token", "err", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}

			if err := h.db.SetIntegrationToken(gdriveIntegration, tokenJSON); err != nil {
				slog.Error("gdrive: store token", "err", err)
				http.Error(w, "database error", http.StatusInternalServerError)
				return
			}

			// Restore the Drive client in the importer now that we have a token.
			client, err := gdrive.New(context.Background(), token, clientID, clientSecret, redirectURI)
			if err == nil {
				h.importer.SetDrive(client)
				slog.Info("google drive connected")
			}

			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprint(w, `<!doctype html><html><body>
<p>Google Drive connected. You can close this tab.</p>
<script>setTimeout(()=>window.close(),1500)</script>
</body></html>`)
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	srv.Serve(ln) //nolint:errcheck
}

// Sync uploads all locally archived FIT files to Google Drive.
// With ?stream=1 it sends SSE progress events; otherwise it fires in the background.
func (h *GDriveHandler) Sync(w http.ResponseWriter, r *http.Request) {
	if !h.importer.DriveConnected() {
		writeError(w, http.StatusServiceUnavailable, "google drive not connected")
		return
	}

	if r.URL.Query().Get("stream") == "1" {
		setupSSE(w)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		started := false
		uploaded, failed, err := h.importer.SyncArchiveToDriveStream(ctx, func(name string, index, count int) {
			if !started {
				started = true
				writeSSE(w, "start", map[string]any{"total": count, "pending": count})
			}
			writeSSE(w, "file", map[string]any{"name": name, "index": index, "total": count})
		})
		if err != nil {
			writeSSE(w, "error", map[string]string{"error": err.Error()})
			return
		}
		if !started {
			writeSSE(w, "start", map[string]any{"total": 0, "pending": 0})
		}
		writeSSE(w, "done", map[string]any{"uploaded": uploaded, "failed": failed})
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		uploaded, failed, err := h.importer.SyncArchiveToDrive(ctx)
		if err != nil {
			slog.Error("gdrive sync failed", "err", err)
			return
		}
		slog.Info("gdrive sync complete", "uploaded", uploaded, "failed", failed)
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "sync started"})
}

// Disconnect removes the stored Drive token and detaches the client.
func (h *GDriveHandler) Disconnect(w http.ResponseWriter, r *http.Request) {
	if err := h.db.DeleteIntegrationToken(gdriveIntegration); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.importer.SetDrive(nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Restore re-imports FIT files from Google Drive into the local database.
func (h *GDriveHandler) Restore(w http.ResponseWriter, r *http.Request) {
	tokenJSON, err := h.db.GetIntegrationToken(gdriveIntegration)
	if err != nil || tokenJSON == "" {
		writeError(w, http.StatusUnauthorized, "google drive not connected")
		return
	}
	clientID, clientSecret, err := h.credentials()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	token, err := gdrive.TokenFromJSON(tokenJSON)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "invalid stored token")
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		client, err := gdrive.New(ctx, token, clientID, clientSecret, "")
		if err != nil {
			slog.Error("gdrive restore: create client", "err", err)
			return
		}

		files, err := client.ListFITFiles(ctx)
		if err != nil {
			slog.Error("gdrive restore: list files", "err", err)
			return
		}

		imported := 0
		for _, f := range files {
			data, err := client.Download(ctx, f.ID)
			if err != nil {
				slog.Error("gdrive restore: download", "file", f.Name, "err", err)
				continue
			}
			id, err := h.importer.ImportBytes(data, f.Name)
			if err != nil {
				slog.Error("gdrive restore: import", "file", f.Name, "err", err)
				continue
			}
			if id != "" {
				imported++
			}
		}
		slog.Info("gdrive restore complete", "imported", imported)
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{"status": "restore started"})
}
