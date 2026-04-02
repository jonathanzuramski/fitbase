package syncer

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/dropbox"
	"github.com/fitbase/fitbase/internal/importer"
)

// concurrentDropboxDownloads is the number of parallel Dropbox downloads.
const concurrentDropboxDownloads = 4

// DropboxSource implements SyncSource for Dropbox FIT file sync.
type DropboxSource struct {
	db       *db.DB
	importer *importer.Importer
	cancel   context.CancelFunc
	mu       sync.Mutex
}

func NewDropboxSource(database *db.DB, imp *importer.Importer) *DropboxSource {
	return &DropboxSource{db: database, importer: imp}
}

// Sync downloads new .fit files from the configured Dropbox folder.
func (s *DropboxSource) Sync(ctx context.Context, onProgress func(event string, data any)) (imported, skipped, failed int) {
	token, err := s.db.GetIntegrationToken("dropbox")
	if err != nil || token == "" {
		return
	}
	folderPath, _, err := s.db.GetIntegrationCredentials("dropbox")
	if err != nil {
		return
	}

	client := dropbox.New(token)

	files, err := client.ListFITFiles(ctx, folderPath)
	if err != nil {
		slog.Error("dropbox sync: list files", "err", err)
		if onProgress != nil {
			onProgress("error", map[string]string{"error": "list files: " + err.Error()})
		}
		return
	}

	known, _ := s.importer.AllImportedFilenames()
	var pending []dropbox.FileMetadata
	for _, f := range files {
		if _, ok := known[f.Name]; !ok {
			pending = append(pending, f)
		}
	}

	if onProgress != nil {
		onProgress("start", map[string]any{"total": len(files), "pending": len(pending)})
	}

	var onFile func(string, int, int)
	if onProgress != nil {
		onFile = func(name string, done, total int) {
			onProgress("file", map[string]any{"name": name, "index": done, "total": total})
		}
	}

	alreadySkipped := len(files) - len(pending)
	imp, sk, fa := downloadDropboxFiles(ctx, client, pending, s.importer, onFile)

	// Save latest cursor so the longpoller starts from the right place.
	cursorCtx, cursorCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cursorCancel()
	if cursor, cursorErr := client.GetLatestCursor(cursorCtx, folderPath); cursorErr == nil {
		s.db.SetDropboxCursor(cursor) //nolint:errcheck
		// If longpoll is running, restart it with the fresh cursor.
		if s.Running() {
			s.startLongpoll(client, cursor)
		}
	}

	return imp, sk + alreadySkipped, fa
}

// StartAuto begins the Dropbox longpoll watcher.
func (s *DropboxSource) StartAuto() error {
	token, err := s.db.GetIntegrationToken("dropbox")
	if err != nil || token == "" {
		return nil
	}
	cursor, err := s.db.GetDropboxCursor()
	if err != nil || cursor == "" {
		// Get a fresh cursor from the current folder state.
		folderPath, _, _ := s.db.GetIntegrationCredentials("dropbox")
		client := dropbox.New(token)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cursor, err = client.GetLatestCursor(ctx, folderPath)
		if err != nil {
			return err
		}
		s.db.SetDropboxCursor(cursor) //nolint:errcheck
	}
	s.startLongpoll(dropbox.New(token), cursor)
	return nil
}

// StopAuto stops the Dropbox longpoll watcher.
func (s *DropboxSource) StopAuto() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
		slog.Info("dropbox longpoll stopped")
	}
}

// Running reports whether the longpoll watcher is active.
func (s *DropboxSource) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancel != nil
}

// Disconnect removes all stored Dropbox credentials and stops auto-sync.
func (s *DropboxSource) Disconnect() error {
	s.StopAuto()
	if err := s.db.DeleteIntegrationToken("dropbox"); err != nil {
		return err
	}
	return s.db.DeleteIntegrationCredentials("dropbox")
}

func (s *DropboxSource) startLongpoll(client *dropbox.Client, cursor string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go s.runLongpoll(ctx, client, cursor)
	slog.Info("dropbox longpoll started")
}

func (s *DropboxSource) runLongpoll(ctx context.Context, client *dropbox.Client, cursor string) {
	const pollTimeout = 90
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
			if saveErr := s.db.SetDropboxCursor(cursor); saveErr != nil {
				slog.Warn("dropbox: save cursor", "err", saveErr)
			}
			for _, f := range files {
				data, dlErr := client.Download(ctx, f.PathLower)
				if dlErr != nil {
					slog.Error("dropbox longpoll: download", "file", f.Name, "err", dlErr)
					continue
				}
				id, importErr := s.importer.ImportBytes(data, f.Name)
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

// downloadDropboxFiles downloads files concurrently and imports them sequentially.
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
