package main

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fitbase/fitbase/internal/api"
	"github.com/fitbase/fitbase/internal/config"
	"github.com/fitbase/fitbase/internal/crypto"
	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/gdrive"
	"github.com/fitbase/fitbase/internal/importer"
	"github.com/fitbase/fitbase/internal/syncer"
	"github.com/fitbase/fitbase/internal/web"
)

func main() {
	cfg := config.Load()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	key, err := crypto.LoadOrCreateKey(cfg.KeyPath)
	if err != nil {
		slog.Error("failed to load master key", "path", cfg.KeyPath, "err", err)
		os.Exit(1)
	}
	slog.Info("master key loaded", "path", cfg.KeyPath)

	database, err := db.Open(cfg.DBPath, key)
	if err != nil {
		slog.Error("failed to open database", "err", err)
		os.Exit(1)
	}
	defer func() {
		if err := database.Close(); err != nil {
			slog.Error("failed to close database", "err", err)
		}
	}()
	slog.Info("database ready", "path", cfg.DBPath)

	imp := importer.NewImporter(database, cfg.ArchiveDir)

	// If the DB is empty but the archive has files, reimport everything.
	// This handles the case where the user deleted the DB and restarted.
	if n, _ := database.CountWorkouts(); n == 0 {
		imported, errCount := imp.ReimportArchive()
		if imported > 0 {
			slog.Info("reimported workouts from archive", "imported", imported, "errors", errCount)
		}
	}

	watcher, err := importer.NewWatcher(cfg.WatchDir, imp)
	if err != nil {
		slog.Error("failed to start watcher", "err", err)
		os.Exit(1)
	}
	watcher.Start()
	defer watcher.Stop()
	slog.Info("watching for FIT files", "dir", cfg.WatchDir)

	handler := api.NewHandler(database, imp)
	gdriveHandler := api.NewGDriveHandler(database, imp)

	// Sync sources and manager — sources are created here, registered with the
	// manager for mutual exclusivity, then passed to their HTTP handlers.
	dropboxSource := syncer.NewDropboxSource(database, imp)
	intervalsSource := syncer.NewIntervalsSource(database, imp)

	syncMgr := syncer.NewManager(database)
	syncMgr.Register("dropbox", dropboxSource)
	syncMgr.Register("intervals", intervalsSource)
	syncMgr.RestoreAll()

	dropboxHandler := api.NewDropboxHandler(syncMgr, dropboxSource)
	intervalsHandler := api.NewIntervalsHandler(syncMgr, intervalsSource)

	initGDrive(database, imp)

	var webFS fs.FS = web.FS
	if cfg.Dev {
		webFS = os.DirFS("internal/web")
		slog.Info("dev mode: templates and static served from disk")
	}

	tmplHandler := web.NewTemplateHandler(database, cfg.Dev, webFS)

	staticSub, err := fs.Sub(webFS, "static")
	if err != nil {
		slog.Error("failed to create static sub-fs", "err", err)
		os.Exit(1)
	}

	router := api.NewRouter(handler, dropboxHandler, intervalsHandler, gdriveHandler, http.FS(staticSub), tmplHandler)

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%s", cfg.Port),
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		slog.Info("fitbase running", "addr", fmt.Sprintf("http://localhost:%s", cfg.Port))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("shutdown error", "err", err)
	}
}

// initGDrive re-attaches a stored Google Drive token on startup so backup
// resumes automatically without requiring the user to re-connect.
func initGDrive(database *db.DB, imp *importer.Importer) {
	tokenJSON, err := database.GetIntegrationToken("gdrive")
	if err != nil || tokenJSON == "" {
		return
	}
	token, err := gdrive.TokenFromJSON(tokenJSON)
	if err != nil {
		slog.Warn("gdrive: invalid stored token", "err", err)
		return
	}
	clientID, clientSecret, err := database.GetIntegrationCredentials("gdrive")
	if err != nil || clientID == "" {
		return
	}
	client, err := gdrive.New(context.Background(), token, clientID, clientSecret, "http://localhost")
	if err != nil {
		slog.Warn("gdrive: failed to restore client", "err", err)
		return
	}
	imp.SetDrive(client)
	slog.Info("google drive backup active")
}
