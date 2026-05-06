package syncer

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/fitness"
	"github.com/fitbase/fitbase/internal/importer"
	"github.com/fitbase/fitbase/internal/intervals"
)

// concurrentIntervalsDownloads is the number of parallel intervals.icu downloads.
// Sequential (1) avoids bursting the per-second rate limit on the FIT file endpoint.
const concurrentIntervalsDownloads = 1

const intervalsPollInterval = 1 * time.Minute
const intervalsStartupDelay = 30 * time.Second

// IntervalsSource implements SyncSource for intervals.icu activity sync.
type IntervalsSource struct {
	db       *db.DB
	importer *importer.Importer
	cancel   context.CancelFunc
	mu       sync.Mutex
	apiBase  string // empty means use the default production URL; set by overrideBase in tests
}

// overrideBase redirects all API calls to the given base URL. Used in tests only.
func (s *IntervalsSource) overrideBase(base string) { s.apiBase = base }

func NewIntervalsSource(database *db.DB, imp *importer.Importer) *IntervalsSource {
	return &IntervalsSource{db: database, importer: imp}
}

func (s *IntervalsSource) client() (*intervals.Client, error) {
	athleteID, apiKey, err := s.db.GetIntegrationCredentials("intervals")
	if err != nil || athleteID == "" {
		return nil, fmt.Errorf("intervals.icu not connected")
	}
	if s.apiBase != "" {
		return intervals.NewWithBase(athleteID, apiKey, s.apiBase), nil
	}
	return intervals.New(athleteID, apiKey), nil
}

// Sync pulls all activities from intervals.icu and imports their FIT files.
func (s *IntervalsSource) Sync(ctx context.Context, onProgress func(event string, data any)) (imported, skipped, failed int) {
	client, err := s.client()
	if err != nil {
		return
	}

	activities, err := client.ListActivities(ctx, "2000-01-01", "")
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
	select {
	case <-time.After(intervalsStartupDelay):
	case <-ctx.Done():
		return
	}
	for {
		imported, skipped, failed := s.syncActivities(ctx, client)
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

func (s *IntervalsSource) syncActivities(ctx context.Context, client *intervals.Client) (imported, skipped, failed int) {
	activities, err := client.ListActivities(ctx, "2000-01-01", "")
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

// SyncFTPHistory fetches all activities from intervals.icu, extracts per-activity FTP,
// writes detected FTP change points into the local ftp_history table (replacing all
// existing entries), then recomputes TSS and IF for every power-based workout using
// the now-accurate historical FTP.
func (s *IntervalsSource) SyncFTPHistory(ctx context.Context, onProgress func(event string, data any)) (updated int, err error) {
	client, err := s.client()
	if err != nil {
		return 0, err
	}

	activities, err := client.ListActivities(ctx, "2000-01-01", "")
	if err != nil {
		return 0, fmt.Errorf("list activities: %w", err)
	}

	// Sort oldest→newest so we detect change points in chronological order.
	sort.Slice(activities, func(i, j int) bool {
		return activities[i].StartDateLocal < activities[j].StartDateLocal
	})

	// Build FTP change points: each time icu_ftp differs from the previous value,
	// record the date and new FTP.
	type changePoint struct {
		at  time.Time
		ftp int
	}
	var changes []changePoint
	var lastFTP int
	for _, act := range activities {
		if act.IcuFTP == nil || *act.IcuFTP <= 0 {
			continue
		}
		ftp := *act.IcuFTP
		if ftp == lastFTP {
			continue
		}
		t, parseErr := time.Parse("2006-01-02T15:04:05", act.StartDateLocal)
		if parseErr != nil {
			// Try date-only fallback
			t, parseErr = time.Parse("2006-01-02", act.StartDateLocal[:10])
			if parseErr != nil {
				continue
			}
		}
		changes = append(changes, changePoint{at: t, ftp: ftp})
		lastFTP = ftp
	}

	if onProgress != nil {
		onProgress("ftpchanges", map[string]any{"count": len(changes)})
	}

	// Replace ftp_history with the intervals-derived change points.
	if err := s.db.ClearFTPHistory(); err != nil {
		return 0, fmt.Errorf("clear ftp history: %w", err)
	}
	for _, cp := range changes {
		if err := s.db.LogFTPChangeAt(cp.ftp, cp.at); err != nil {
			slog.Warn("intervals ftp sync: insert history entry", "ftp", cp.ftp, "at", cp.at, "err", err)
		}
	}

	// Recompute TSS/IF for every power-based workout using the now-accurate history.
	workouts, err := s.db.AllWorkoutsForTSSBackfill()
	if err != nil {
		return 0, fmt.Errorf("load workouts: %w", err)
	}

	if onProgress != nil {
		onProgress("recompute", map[string]any{"total": len(workouts)})
	}

	for _, w := range workouts {
		ftp := s.db.GetFTPAtDate(w.RecordedAt)
		if ftp <= 0 {
			continue
		}
		ftpF := float64(ftp)
		ifactor := fitness.IntensityFactor(w.NormalizedPower, ftpF)
		tss := fitness.PowerTSS(w.DurationSecs, w.NormalizedPower, ftpF)
		if err := s.db.UpdateWorkoutLoad(w.ID, tss, ifactor); err != nil {
			slog.Warn("intervals ftp sync: update workout load", "id", w.ID, "err", err)
			continue
		}
		updated++
	}

	return updated, nil
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
