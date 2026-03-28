package importer

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/fitness"
	fitparser "github.com/fitbase/fitbase/internal/fitparser"
	"github.com/fitbase/fitbase/internal/models"
	"github.com/fitbase/fitbase/internal/route"
	"github.com/fsnotify/fsnotify"
)

// DriveUploader is the subset of gdrive.Client used by the importer for backup.
type DriveUploader interface {
	Upload(ctx context.Context, workoutID, year, month string, data []byte) error
}

// Importer handles FIT file import, archiving, and optional Drive backup.
type Importer struct {
	db         *db.DB
	archiveDir string
	mu         sync.RWMutex
	drive      DriveUploader // nil if Google Drive backup is not configured
}

func NewImporter(database *db.DB, archiveDir string) *Importer {
	return &Importer{db: database, archiveDir: archiveDir}
}

// SetDrive attaches (or clears) a Drive backup client. Thread-safe.
func (imp *Importer) SetDrive(d DriveUploader) {
	imp.mu.Lock()
	imp.drive = d
	imp.mu.Unlock()
}

// DriveConnected reports whether a Drive backup client is active.
func (imp *Importer) DriveConnected() bool {
	imp.mu.RLock()
	defer imp.mu.RUnlock()
	return imp.drive != nil
}

// Import parses, stores, and archives a FIT file. Returns the workout ID.
// Safe to call multiple times — duplicate files are skipped.
func (imp *Importer) Import(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}

	sum := sha256.Sum256(data)
	hash := fmt.Sprintf("%x", sum[:])

	already, err := imp.db.IsImported(hash)
	if err != nil {
		return "", err
	}
	if already {
		return "", nil // silently skip
	}

	data, err = decompressIfNeeded(data, filepath.Base(path))
	if err != nil {
		return "", fmt.Errorf("decompress %s: %w", path, err)
	}

	athlete, err := imp.db.GetAthlete()
	if err != nil {
		return "", fmt.Errorf("get athlete: %w", err)
	}

	result, err := fitparser.Parse(
		bytes.NewReader(data),
		filepath.Base(path),
		athlete.FTPWatts,
		athlete.ThresholdHR,
		athlete.RestingHR,
	)

	if errors.Is(err, fitparser.ErrSkipped) {
		slog.Debug("skipping non-cardio activity", "path", path)
		_ = imp.db.MarkImported(hash, filepath.Base(path))
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}

	exists, err := imp.db.WorkoutExists(result.ID)
	if err != nil {
		return "", err
	}
	if !exists {
		// Activity-level dedup: same ride from a different source has different
		// bytes (and therefore a different ID) but identical timestamp + sport.
		if dupID, _ := imp.db.FindDuplicateWorkout(result.Workout.RecordedAt, result.Workout.Sport, result.Workout.DurationSecs); dupID != "" {
			slog.Info("skipping duplicate activity from different source",
				"new_id", result.ID, "existing_id", dupID, "path", path)
			_ = imp.db.MarkImported(hash, filepath.Base(path))
			return "", nil
		}

		if err := imp.db.InsertWorkout(&result.Workout, result.Streams); err != nil {
			return "", fmt.Errorf("store workout: %w", err)
		}
		if curve := fitness.ComputePowerCurve(result.Streams); curve != nil {
			if err := imp.db.InsertPowerCurve(result.ID, curve); err != nil {
				slog.Warn("power curve insert failed", "id", result.ID, "err", err)
			}
		}
		{
			var pz []models.PowerZone
			if athlete.FTPWatts > 0 {
				pz = fitness.PowerZones(athlete.FTPWatts)[:7]
			}
			var hz []models.HRZone
			if athlete.ThresholdHR > 0 {
				hz = fitness.ResolveHRZones(athlete)
			}
			pw, hr := fitness.ComputeZoneTimes(result.Streams, pz, hz)
			if err := imp.db.InsertZoneTimes(result.ID, pw, hr); err != nil {
				slog.Warn("zone times insert failed", "id", result.ID, "err", err)
			}
		}
		if routeID := imp.matchOrCreateRoute(result.Streams); routeID != "" {
			if err := imp.db.SetWorkoutRouteID(result.ID, routeID); err != nil {
				slog.Warn("route assignment failed", "id", result.ID, "err", err)
			}
		}
		if err := imp.archive(data, &result.Workout); err != nil {
			// Log but don't fail the import — data is already in the DB.
			slog.Warn("archive failed", "id", result.ID, "err", err)
		} else {
			if err := os.Remove(path); err != nil {
				slog.Warn("failed to delete watch file after import", "path", path, "err", err)
			}
		}
		imp.mu.RLock()
		hasDrive := imp.drive != nil
		imp.mu.RUnlock()
		if hasDrive {
			go imp.uploadToDrive(data, &result.Workout)
		}
	}

	if err := imp.db.MarkImported(hash, filepath.Base(path)); err != nil {
		slog.Warn("failed to mark file as imported", "hash", hash, "err", err)
	}

	slog.Info("imported workout",
		"id", result.ID,
		"sport", result.Workout.Sport,
		"date", result.Workout.RecordedAt.Format("2006-01-02"),
		"duration_mins", result.Workout.DurationSecs/60,
	)

	return result.ID, nil
}

// ArchivePath returns the path where a workout's FIT file is stored.
// Layout: {archiveDir}/{year}/{month}/{YYYY-MM-DD}T{HH-MM-SS}_{workoutID}.fit
func (imp *Importer) ArchivePath(workout *models.Workout) string {
	t := workout.RecordedAt.UTC()
	filename := t.Format("2006-01-02T15-04-05") + "-" + workout.Sport + ".fit"
	return filepath.Join(
		imp.archiveDir,
		t.Format("2006"),
		t.Format("01"),
		filename,
	)
}

func (imp *Importer) archive(data []byte, workout *models.Workout) error {
	dest := imp.ArchivePath(workout)
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return fmt.Errorf("create archive dir: %w", err)
	}
	if err := os.WriteFile(dest, data, 0644); err != nil {
		return fmt.Errorf("write archive: %w", err)
	}
	slog.Info("archived fit file", "path", dest)
	return nil
}

// IsFilenameImported reports whether a file with the given filename has been imported.
// Used by integrations to skip downloading files that are already in the DB.
func (imp *Importer) IsFilenameImported(filename string) (bool, error) {
	return imp.db.IsFilenameImported(filename)
}

// AllImportedFilenames returns the set of every imported filename for batch pre-checks.
func (imp *Importer) AllImportedFilenames() (map[string]struct{}, error) {
	return imp.db.AllImportedFilenames()
}

// ImportBytes parses and stores a FIT file from raw bytes.
func (imp *Importer) ImportBytes(data []byte, filename string) (string, error) {
	sum := sha256.Sum256(data)
	hash := fmt.Sprintf("%x", sum[:])

	already, err := imp.db.IsImported(hash)
	if err != nil {
		return "", err
	}
	if already {
		return "", nil
	}

	data, err = decompressIfNeeded(data, filename)
	if err != nil {
		return "", fmt.Errorf("decompress %s: %w", filename, err)
	}

	athlete, err := imp.db.GetAthlete()
	if err != nil {
		return "", fmt.Errorf("get athlete: %w", err)
	}

	result, err := fitparser.Parse(bytes.NewReader(data), filename, athlete.FTPWatts, athlete.ThresholdHR, athlete.RestingHR)
	if errors.Is(err, fitparser.ErrSkipped) {
		slog.Debug("skipping non-cardio activity", "file", filename)
		_ = imp.db.MarkImported(hash, filename)
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", filename, err)
	}

	exists, err := imp.db.WorkoutExists(result.ID)
	if err != nil {
		return "", err
	}
	if !exists {
		if dupID, _ := imp.db.FindDuplicateWorkout(result.Workout.RecordedAt, result.Workout.Sport, result.Workout.DurationSecs); dupID != "" {
			slog.Info("skipping duplicate activity from different source",
				"new_id", result.ID, "existing_id", dupID, "file", filename)
			_ = imp.db.MarkImported(hash, filename)
			return "", nil
		}

		if err := imp.db.InsertWorkout(&result.Workout, result.Streams); err != nil {
			return "", fmt.Errorf("store workout: %w", err)
		}
		if curve := fitness.ComputePowerCurve(result.Streams); curve != nil {
			if err := imp.db.InsertPowerCurve(result.ID, curve); err != nil {
				slog.Warn("power curve insert failed", "id", result.ID, "err", err)
			}
		}
		{
			var pz []models.PowerZone
			if athlete.FTPWatts > 0 {
				pz = fitness.PowerZones(athlete.FTPWatts)[:7]
			}
			var hz []models.HRZone
			if athlete.ThresholdHR > 0 {
				hz = fitness.ResolveHRZones(athlete)
			}
			pw, hr := fitness.ComputeZoneTimes(result.Streams, pz, hz)
			if err := imp.db.InsertZoneTimes(result.ID, pw, hr); err != nil {
				slog.Warn("zone times insert failed", "id", result.ID, "err", err)
			}
		}
		if routeID := imp.matchOrCreateRoute(result.Streams); routeID != "" {
			if err := imp.db.SetWorkoutRouteID(result.ID, routeID); err != nil {
				slog.Warn("route assignment failed", "id", result.ID, "err", err)
			}
		}
		if err := imp.archive(data, &result.Workout); err != nil {
			slog.Warn("archive failed", "id", result.ID, "err", err)
		}
		imp.mu.RLock()
		hasDrive := imp.drive != nil
		imp.mu.RUnlock()
		if hasDrive {
			go imp.uploadToDrive(data, &result.Workout)
		}
	}

	if err := imp.db.MarkImported(hash, filename); err != nil {
		slog.Warn("failed to mark imported", "hash", hash, "err", err)
	}

	slog.Info("imported workout", "id", result.ID, "file", filename)
	return result.ID, nil
}

func (imp *Importer) uploadToDrive(data []byte, workout *models.Workout) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	imp.mu.RLock()
	d := imp.drive
	imp.mu.RUnlock()
	if d == nil {
		return
	}
	year := workout.RecordedAt.UTC().Format("2006")
	month := workout.RecordedAt.UTC().Format("01")
	if err := d.Upload(ctx, workout.ID, year, month, data); err != nil {
		slog.Error("google drive backup failed", "id", workout.ID, "err", err)
	}
}

// SyncArchiveToDrive uploads all locally archived FIT files to Drive.
// Safe to call repeatedly — skips files that already exist on Drive.
func (imp *Importer) SyncArchiveToDrive(ctx context.Context) (uploaded, failed int, err error) {
	return imp.SyncArchiveToDriveStream(ctx, nil)
}

// SyncArchiveToDriveStream is like SyncArchiveToDrive but calls onFile before each upload
// so callers can stream progress. onFile receives the filename and 1-based index out of total.
func (imp *Importer) SyncArchiveToDriveStream(ctx context.Context, onFile func(name string, index, total int)) (uploaded, failed int, err error) {
	imp.mu.RLock()
	d := imp.drive
	imp.mu.RUnlock()
	if d == nil {
		return 0, 0, fmt.Errorf("google drive not connected")
	}

	type entry struct{ path, workoutID, year, month string }
	var entries []entry
	walkErr := filepath.WalkDir(imp.archiveDir, func(path string, e fs.DirEntry, werr error) error {
		if werr != nil || e.IsDir() || !isFIT(e.Name()) {
			return nil
		}
		rel, rerr := filepath.Rel(imp.archiveDir, path)
		if rerr != nil {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != 3 {
			return nil
		}
		entries = append(entries, entry{path, strings.TrimSuffix(parts[2], ".fit"), parts[0], parts[1]})
		return nil
	})
	if walkErr != nil {
		return 0, 0, walkErr
	}

	for i, e := range entries {
		if onFile != nil {
			onFile(filepath.Base(e.path), i+1, len(entries))
		}
		fileData, readErr := os.ReadFile(e.path)
		if readErr != nil {
			slog.Error("sync: read archive file", "path", e.path, "err", readErr)
			failed++
			continue
		}
		if uploadErr := d.Upload(ctx, e.workoutID, e.year, e.month, fileData); uploadErr != nil {
			slog.Error("sync: drive upload", "path", e.path, "err", uploadErr)
			failed++
			continue
		}
		uploaded++
	}
	return
}

// BackfillPowerCurves computes and stores power curves for workouts that
// were imported before this feature existed. Safe to call repeatedly.
func (imp *Importer) BackfillPowerCurves() {
	ids, err := imp.db.WorkoutIDsWithoutPowerCurve()
	if err != nil {
		slog.Error("backfill: query workouts without power curve", "err", err)
		return
	}
	if len(ids) == 0 {
		return
	}
	slog.Info("backfilling power curves", "count", len(ids))
	done := 0
	for _, id := range ids {
		streams, err := imp.db.GetStreams(id)
		if err != nil {
			slog.Warn("backfill: get streams", "id", id, "err", err)
			continue
		}
		curve := fitness.ComputePowerCurve(streams)
		if curve == nil {
			continue
		}
		if err := imp.db.InsertPowerCurve(id, curve); err != nil {
			slog.Warn("backfill: insert power curve", "id", id, "err", err)
			continue
		}
		done++
	}
	if done > 0 {
		slog.Info("power curve backfill complete", "backfilled", done)
	}
}

// matchOrCreateRoute computes the GPS cell fingerprint for a workout's streams
// and either matches it to an existing route or creates a new one.
func (imp *Importer) matchOrCreateRoute(streams []models.Stream) string {
	cells := route.ComputeCells(streams)
	if cells == nil {
		return ""
	}

	dbCandidates, err := imp.db.GetAllRouteCandidates()
	if err != nil {
		slog.Warn("route matching: load candidates", "err", err)
		return ""
	}

	candidates := make([]route.Candidate, len(dbCandidates))
	for i, c := range dbCandidates {
		candidates[i] = route.Candidate{ID: c.ID, Cells: route.CellsFromString(c.Cells)}
	}

	if matchID, _ := route.MatchRoute(cells, candidates, 0.85); matchID != "" {
		return matchID
	}

	id := route.CellSetID(cells)
	if err := imp.db.InsertRoute(id, route.CellsToString(cells), len(cells)); err != nil {
		slog.Warn("route creation failed", "err", err)
		return ""
	}
	return id
}

// BackfillRoutes assigns routes to workouts that have GPS data but no route_id.
func (imp *Importer) BackfillRoutes() {
	ids, err := imp.db.WorkoutIDsWithoutRoute()
	if err != nil {
		slog.Error("backfill: query workouts without route", "err", err)
		return
	}
	if len(ids) == 0 {
		return
	}
	slog.Info("backfilling route assignments", "count", len(ids))
	done := 0
	for _, id := range ids {
		streams, err := imp.db.GetStreams(id)
		if err != nil {
			slog.Warn("backfill: get streams for route", "id", id, "err", err)
			continue
		}
		routeID := imp.matchOrCreateRoute(streams)
		if routeID == "" {
			continue
		}
		if err := imp.db.SetWorkoutRouteID(id, routeID); err != nil {
			slog.Warn("backfill: set route_id", "id", id, "err", err)
			continue
		}
		done++
	}
	if done > 0 {
		slog.Info("route backfill complete", "assigned", done)
	}
}

// ImportDir scans a directory and imports all unprocessed FIT files.
func (imp *Importer) ImportDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read dir %s: %w", dir, err)
	}

	for _, e := range entries {
		if e.IsDir() || !isFIT(e.Name()) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if _, err := imp.Import(path); err != nil {
			slog.Error("import failed", "file", e.Name(), "err", err)
		}
	}
	return nil
}

// ReimportArchive walks the archive directory and re-imports every .fit file
// using a worker pool so FIT parsing happens in parallel.
// Useful after deleting the database — the archive has all the originals.
// Returns the number of workouts imported and the number of errors.
func (imp *Importer) ReimportArchive() (imported, errCount int) {
	var paths []string
	walkErr := filepath.WalkDir(imp.archiveDir, func(path string, e fs.DirEntry, werr error) error {
		if werr != nil || e.IsDir() || !isFIT(e.Name()) {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if walkErr != nil {
		slog.Error("reimport: walk archive", "dir", imp.archiveDir, "err", walkErr)
		return
	}
	if len(paths) == 0 {
		return
	}

	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	if workers > len(paths) {
		workers = len(paths)
	}

	type result struct {
		imported bool
		err      error
	}

	work := make(chan string, len(paths))
	for _, p := range paths {
		work <- p
	}
	close(work)

	results := make(chan result, len(paths))
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range work {
				data, err := os.ReadFile(path)
				if err != nil {
					slog.Error("reimport: read file", "path", path, "err", err)
					results <- result{err: err}
					continue
				}
				id, err := imp.ImportBytes(data, filepath.Base(path))
				if err != nil {
					slog.Error("reimport: import file", "path", path, "err", err)
					results <- result{err: err}
					continue
				}
				results <- result{imported: id != ""}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	for r := range results {
		if r.err != nil {
			errCount++
		} else if r.imported {
			imported++
		}
	}
	return
}

// Watcher watches a directory and imports FIT files as they appear.
type Watcher struct {
	dir      string
	importer *Importer
	watcher  *fsnotify.Watcher
	done     chan struct{}
}

func NewWatcher(dir string, importer *Importer) (*Watcher, error) {
	// create directory for the watcher
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create watch dir: %w", err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("new watcher: %w", err)
	}
	// add directory to the watcher
	if err := watcher.Add(dir); err != nil {
		_ = watcher.Close()
		return nil, fmt.Errorf("watch dir: %w", err)
	}

	return &Watcher{
		dir:      dir,
		importer: importer,
		watcher:  watcher,
		done:     make(chan struct{}),
	}, nil
}

// Start begins watching in a goroutine. Call Stop to shut down.
func (watcher *Watcher) Start() {
	// Initial scan on startup to catch files dropped while server was down
	if err := watcher.importer.ImportDir(watcher.dir); err != nil {
		slog.Error("initial scan failed", "dir", watcher.dir, "err", err)
	}

	// Backfill power curves and route assignments for existing workouts.
	go watcher.importer.BackfillPowerCurves()
	go watcher.importer.BackfillRoutes()

	go func() {
		// Wait 2s after the last write event before importing — devices often
		// write files in chunks, so we hold off until writes settle.
		importTimer := time.NewTimer(0)
		<-importTimer.C
		// GoLang please add sets to stdlib pwease :')
		pending := map[string]struct{}{}

		// for with no condition is equivalent to a infinite loop
		for {
			select {
			case event, ok := <-watcher.watcher.Events:
				if !ok {
					return
				}
				if event.Has(fsnotify.Create) || event.Has(fsnotify.Write) {
					if isFIT(event.Name) {
						// fsnotify the event.Name is the filepath
						pending[event.Name] = struct{}{}
						importTimer.Reset(2 * time.Second)
					}
				}

			// timer notifies after 2 seconds of no fsnotify.create or write events
			case <-importTimer.C:
				for path := range pending {
					// import our file now that there is no file activity causing
					// use to import a half written file.
					if _, err := watcher.importer.Import(path); err != nil {
						slog.Error("import failed", "file", path, "err", err)
					}
					delete(pending, path)
				}

			case err, ok := <-watcher.watcher.Errors:
				if !ok {
					return
				}
				slog.Error("watcher error", "err", err)

			case <-watcher.done:
				return
			}
		}
	}()
}

func (w *Watcher) Stop() {
	close(w.done)
	_ = w.watcher.Close()
}

func isFIT(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".fit") || strings.HasSuffix(lower, ".fit.gz")
}

// decompressIfNeeded decompresses gzip data when the filename ends in .gz.
func decompressIfNeeded(data []byte, filename string) ([]byte, error) {
	if !strings.HasSuffix(strings.ToLower(filename), ".gz") {
		return data, nil
	}
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decompress: %w", err)
	}
	defer gr.Close() //nolint:errcheck
	return io.ReadAll(gr)
}
