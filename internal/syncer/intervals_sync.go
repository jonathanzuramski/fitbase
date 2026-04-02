package syncer

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/importer"
	"github.com/fitbase/fitbase/internal/intervals"
)

// concurrentIntervalsDownloads is the number of parallel intervals.icu downloads.
// Kept low to avoid 429 rate limiting from intervals.icu.
const concurrentIntervalsDownloads = 2

const intervalsPollInterval = 1 * time.Minute
const defaultSyncOldest = "2000-01-01"

// IntervalsSource implements SyncSource for intervals.icu activity sync.
type IntervalsSource struct {
	db       *db.DB
	importer *importer.Importer
	cancel   context.CancelFunc
	mu       sync.Mutex
}

func NewIntervalsSource(database *db.DB, imp *importer.Importer) *IntervalsSource {
	return &IntervalsSource{db: database, importer: imp}
}

func (s *IntervalsSource) client() (*intervals.Client, error) {
	athleteID, apiKey, err := s.db.GetIntegrationCredentials("intervals")
	if err != nil || athleteID == "" {
		return nil, fmt.Errorf("intervals.icu not connected")
	}
	return intervals.New(athleteID, apiKey), nil
}

// Sync pulls all activities from intervals.icu and imports their FIT files.
func (s *IntervalsSource) Sync(ctx context.Context, onProgress func(event string, data any)) (imported, skipped, failed int) {
	client, err := s.client()
	if err != nil {
		return
	}

	oldest, _ := s.db.GetSyncOldest("intervals")
	if oldest == "" {
		oldest = defaultSyncOldest
	}

	activities, err := client.ListActivities(ctx, oldest, "")
	if err != nil {
		slog.Error("intervals.icu sync: list activities", "err", err)
		if onProgress != nil {
			onProgress("error", map[string]string{"error": "list activities: " + err.Error()})
		}
		return
	}

	known, _ := s.importer.AllImportedFilenames()
	var pending []pendingIntervalsActivity
	for _, act := range activities {
		filename := fmt.Sprintf("intervals-%s.fit", act.ID)
		if _, ok := known[filename]; !ok {
			pending = append(pending, pendingIntervalsActivity{act.ID, filename})
		}
	}

	if onProgress != nil {
		onProgress("start", map[string]any{"total": len(activities), "pending": len(pending)})
	}

	var onFile func(string, int, int)
	if onProgress != nil {
		onFile = func(name string, done, total int) {
			onProgress("file", map[string]any{"name": name, "index": done, "total": total})
		}
	}

	alreadySkipped := len(activities) - len(pending)
	imp, sk, fa := downloadIntervalsFiles(ctx, client, pending, s.importer, onFile)
	return imp, sk + alreadySkipped, fa
}

// StartAuto begins the background intervals.icu poller.
func (s *IntervalsSource) StartAuto() error {
	client, err := s.client()
	if err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go s.poll(ctx, client)
	slog.Info("intervals.icu auto-sync started", "interval", intervalsPollInterval)
	return nil
}

// StopAuto stops the background poller.
func (s *IntervalsSource) StopAuto() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
		slog.Info("intervals.icu auto-sync stopped")
	}
}

// Running reports whether the background poller is active.
func (s *IntervalsSource) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancel != nil
}

// Disconnect removes stored intervals.icu credentials and stops auto-sync.
func (s *IntervalsSource) Disconnect() error {
	s.StopAuto()
	return s.db.DeleteIntegrationCredentials("intervals")
}

// Fetch downloads a single activity by its intervals.icu ID and imports it.
func (s *IntervalsSource) Fetch(ctx context.Context, activityID string) (workoutID string, status string, err error) {
	client, clientErr := s.client()
	if clientErr != nil {
		return "", "error", clientErr
	}

	data, dlErr := client.DownloadFIT(ctx, activityID)
	if dlErr != nil {
		return "", "download_failed", dlErr
	}

	filename := fmt.Sprintf("intervals-%s.fit", activityID)
	id, importErr := s.importer.ImportBytes(data, filename)
	if importErr != nil {
		return "", "import_failed", importErr
	}
	if id == "" {
		return "", "skipped", nil
	}
	return id, "imported", nil
}

func (s *IntervalsSource) poll(ctx context.Context, client *intervals.Client) {
	for {
		oldest, _ := s.db.GetSyncOldest("intervals")
		imported, skipped, failed := s.syncActivities(ctx, client, oldest)
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

func (s *IntervalsSource) syncActivities(ctx context.Context, client *intervals.Client, oldest string) (imported, skipped, failed int) {
	if oldest == "" {
		oldest = defaultSyncOldest
	}
	activities, err := client.ListActivities(ctx, oldest, "")
	if err != nil {
		slog.Error("intervals.icu sync: list activities", "err", err)
		return
	}

	known, _ := s.importer.AllImportedFilenames()
	var pending []pendingIntervalsActivity
	for _, act := range activities {
		filename := fmt.Sprintf("intervals-%s.fit", act.ID)
		if _, ok := known[filename]; !ok {
			pending = append(pending, pendingIntervalsActivity{act.ID, filename})
		}
	}

	imp, sk, fa := downloadIntervalsFiles(ctx, client, pending, s.importer, nil)
	return imp, sk + len(activities) - len(pending), fa
}

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
