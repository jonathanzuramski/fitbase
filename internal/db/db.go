package db

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fitbase/fitbase/internal/crypto"
	"github.com/fitbase/fitbase/internal/fitness"
	"github.com/fitbase/fitbase/internal/geo"
	"github.com/fitbase/fitbase/internal/models"
	"github.com/fitbase/fitbase/internal/timeutil"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

type DB struct {
	*sql.DB
	key  []byte // AES-256 key for encrypting OAuth tokens at rest
	path string // filesystem path of the database file (for snapshots)
}

// ── Setup ─────────────────────────────────────────────────────────────────────

// Open opens (or creates) the SQLite database at path, running the schema on first use.
// key must be a 32-byte AES-256 key used to encrypt OAuth tokens — use crypto.LoadOrCreateKey.
func Open(path string, key []byte) (*DB, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("db: master key must be 32 bytes, got %d", len(key))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	sqldb, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	sqldb.SetMaxOpenConns(1) // SQLite is single-writer

	// The migration ladder is the single source of truth for the schema. Every
	// database replays it from wherever its PRAGMA user_version left off and updates
	// the schema accordingly. See internal/db/migrate.go.
	if err := migrate(sqldb); err != nil {
		return nil, fmt.Errorf("migrate schema: %w", err)
	}

	return &DB{DB: sqldb, key: key, path: path}, nil
}

// SnapshotPreRebuild writes a consistent copy of the database to
// <path>.pre-rebuild (replacing any previous one) before an archive rebuild
// wipes derived data — a rebuild gone wrong is recoverable by stopping fitbase
// and restoring the snapshot over the database file.
func (db *DB) SnapshotPreRebuild() (string, error) {
	dest := db.path + ".pre-rebuild"
	// VACUUM INTO refuses to overwrite.
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("remove old snapshot: %w", err)
	}
	if _, err := db.Exec(`VACUUM INTO ?`, dest); err != nil {
		return "", fmt.Errorf("vacuum into %s: %w", dest, err)
	}
	return dest, nil
}

// ── Workouts ──────────────────────────────────────────────────────────────────

// InsertWorkout persists a workout and its streams in a single transaction.
func (db *DB) InsertWorkout(w *models.Workout, streams []models.Stream) error {
	// Pre-compute downsampled GPS coords for outdoor workouts so the heatmap
	// and card thumbnails never need to JOIN against workout_streams.
	var routeCoords *string
	var routeCoordsV *int
	if !w.IsIndoor {
		pts := make([][2]float64, 0, len(streams))
		for _, s := range streams {
			if s.Lat == nil || s.Lng == nil || (*s.Lat == 0 && *s.Lng == 0) {
				continue
			}
			pts = append(pts, [2]float64{*s.Lng, *s.Lat})
		}
		coords := simplifyCoords(pts, 500)
		js, _ := json.Marshal(coords)
		s := string(js)
		routeCoords = &s
		v := routeCoordsVersion
		routeCoordsV = &v
	}

	// Resolve the ride's local-time offset if the FIT file didn't declare one:
	// outdoor rides from the start coordinate's timezone (historically correct,
	// DST included), otherwise the athlete profile timezone at the ride's date.
	// Stamped once here so nothing downstream ever does per-query timezone math.
	if w.UTCOffsetSecs == nil {
		off, resolved := 0, false
		if !w.IsIndoor && w.StartLat != nil && w.StartLng != nil {
			if secs, ok := geo.UTCOffsetAt(*w.StartLat, *w.StartLng, w.RecordedAt); ok {
				off, resolved = secs, true
			}
		}
		if !resolved {
			_, off = w.RecordedAt.In(db.athleteLocation()).Zone()
		}
		w.UTCOffsetSecs = &off
	}
	if w.County == "" && !w.IsIndoor && w.StartLat != nil && w.StartLng != nil {
		if county, state, ok := geo.CountyState(*w.StartLat, *w.StartLng); ok {
			w.County, w.State = county, state
		}
	}
	// Calendar day the ride happened, in its own timezone.
	w.TrainingDay = w.RecordedAt.In(time.FixedZone("", *w.UTCOffsetSecs)).Format("2006-01-02")

	// Empty place strings persist as NULL so "unknown" is one value, not two.
	var county, state any
	if w.County != "" {
		county, state = w.County, w.State
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	_, err = tx.Exec(`
		INSERT INTO workouts (
			id, filename, recorded_at, sport, duration_secs, elapsed_secs, distance_meters,
			elevation_gain_meters, avg_power_watts, max_power_watts, normalized_power,
			avg_heart_rate, max_heart_rate, avg_cadence, avg_speed_mps,
			tss, intensity_factor, is_indoor, route_coords, route_coords_v, training_day,
			utc_offset_secs, start_lat, start_lng, county, state, geo_v
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		w.ID, w.Filename, w.RecordedAt.UTC().Format(time.RFC3339),
		w.Sport, w.DurationSecs, w.ElapsedSecs, w.DistanceMeters, w.ElevationGainMeters,
		w.AvgPowerWatts, w.MaxPowerWatts, w.NormalizedPower,
		w.AvgHeartRate, w.MaxHeartRate, w.AvgCadenceRPM, w.AvgSpeedMPS,
		w.TSS, w.IntensityFactor, w.IsIndoor, routeCoords, routeCoordsV, w.TrainingDay,
		w.UTCOffsetSecs, w.StartLat, w.StartLng, county, state, geoVersion,
	)
	if err != nil {
		return fmt.Errorf("insert workout: %w", err)
	}

	stmt, err := tx.Prepare(`
		INSERT OR IGNORE INTO workout_streams (
			workout_id, timestamp, power_watts, heart_rate_bpm, cadence_rpm,
			speed_mps, altitude_meters, lat, lng, distance_meters
		) VALUES (?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return fmt.Errorf("prepare stream insert: %w", err)
	}
	defer stmt.Close() //nolint:errcheck

	for _, s := range streams {
		_, err = stmt.Exec(
			w.ID, s.Timestamp.UTC().Format(time.RFC3339),
			s.PowerWatts, s.HeartRateBPM, s.CadenceRPM,
			s.SpeedMPS, s.AltitudeMeters, s.Lat, s.Lng, s.DistanceMeters,
		)
		if err != nil {
			return fmt.Errorf("insert stream: %w", err)
		}
	}

	return tx.Commit()
}

// GetWorkout retrieves a single workout by ID.
func (db *DB) GetWorkout(id string) (*models.Workout, error) {
	row := db.QueryRow(`SELECT `+workoutCols+` FROM workouts WHERE id = ?`, id)

	w, err := scanWorkout(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &w, err
}

// sortColumns maps safe client-supplied sort keys to DB column names.
var sortColumns = map[string]string{
	"date":     "recorded_at",
	"sport":    "sport",
	"duration": "duration_secs",
	"distance": "distance_meters",
	"power":    "avg_power_watts",
	"np":       "normalized_power",
	"tss":      "tss",
	"hr":       "avg_heart_rate",
	"elev":     "elevation_gain_meters",
}

// ListWorkouts returns workouts with configurable sort order and optional type filter.
// sortKey must be a key in sortColumns (unknown keys fall back to recorded_at).
// sortDir must be "asc" or "desc" (anything else defaults to "desc").
// typeFilter: "outdoor" | "indoor" | "" (all).
func (db *DB) ListWorkouts(limit, offset int, sortKey, sortDir, typeFilter string) ([]models.Workout, error) {
	col, ok := sortColumns[sortKey]
	if !ok {
		col = "recorded_at"
	}
	dir := "DESC"
	if sortDir == "asc" {
		dir = "ASC"
	}

	where := ""
	switch typeFilter {
	case "outdoor":
		where = "WHERE is_indoor = 0"
	case "indoor":
		where = "WHERE is_indoor = 1"
	}

	// NULLS LAST keeps activities without that metric at the bottom regardless of direction.
	q := `SELECT ` + workoutCols + `
		FROM workouts ` + where + `
		ORDER BY ` + col + ` IS NULL, ` + col + ` ` + dir + `
		LIMIT ? OFFSET ?`

	rows, err := db.Query(q, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	var workouts []models.Workout
	for rows.Next() {
		w, err := scanWorkout(rows)
		if err != nil {
			return nil, err
		}
		workouts = append(workouts, w)
	}
	return workouts, rows.Err()
}

// GetWorkoutsForMonth returns all workouts whose training_day falls within
// the given calendar month, ordered chronologically. Months are ride-local
// calendar months — no timezone parameter needed, because each workout
// already knows which day it happened on.
func (db *DB) GetWorkoutsForMonth(year int, month time.Month) ([]models.Workout, error) {
	start := fmt.Sprintf("%04d-%02d-01", year, month)
	next := time.Date(year, month+1, 1, 0, 0, 0, 0, time.UTC)
	end := next.Format("2006-01-02")

	rows, err := db.Query(`
		SELECT `+workoutCols+`
		FROM workouts
		WHERE training_day >= ? AND training_day < ?
		ORDER BY training_day ASC, recorded_at ASC`, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	var workouts []models.Workout
	for rows.Next() {
		w, err := scanWorkout(rows)
		if err != nil {
			return nil, err
		}
		workouts = append(workouts, w)
	}
	return workouts, rows.Err()
}

// CountWorkoutsFiltered returns total count respecting an optional type filter.
func (db *DB) CountWorkoutsFiltered(typeFilter string) (int, error) {
	where := ""
	switch typeFilter {
	case "outdoor":
		where = "WHERE is_indoor = 0"
	case "indoor":
		where = "WHERE is_indoor = 1"
	}
	var n int
	err := db.QueryRow("SELECT COUNT(*) FROM workouts " + where).Scan(&n)
	return n, err
}

// CountWorkouts returns the total number of workouts.
func (db *DB) CountWorkouts() (int, error) {
	var n int
	err := db.QueryRow("SELECT COUNT(*) FROM workouts").Scan(&n)
	return n, err
}

// GetStreams returns all time-series records for a workout.
func (db *DB) GetStreams(workoutID string) ([]models.Stream, error) {
	rows, err := db.Query(`
		SELECT timestamp, power_watts, heart_rate_bpm, cadence_rpm,
		       speed_mps, altitude_meters, lat, lng, distance_meters
		FROM workout_streams
		WHERE workout_id = ?
		ORDER BY timestamp ASC`, workoutID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	var streams []models.Stream
	for rows.Next() {
		var s models.Stream
		var ts string
		err := rows.Scan(
			&ts, &s.PowerWatts, &s.HeartRateBPM, &s.CadenceRPM,
			&s.SpeedMPS, &s.AltitudeMeters, &s.Lat, &s.Lng, &s.DistanceMeters,
		)
		if err != nil {
			return nil, err
		}
		if s.Timestamp, err = time.Parse(time.RFC3339, ts); err != nil {
			return nil, fmt.Errorf("parse stream timestamp %q: %w", ts, err)
		}
		streams = append(streams, s)
	}
	return streams, rows.Err()
}

// WorkoutExists returns true if the workout ID is already in the DB.
func (db *DB) WorkoutExists(id string) (bool, error) {
	var exists bool
	err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM workouts WHERE id = ?)", id).Scan(&exists)
	return exists, err
}

// FindDuplicateWorkout checks whether an activity with the same sport and a
// recorded_at timestamp within ±60 seconds already exists. This catches the
// same ride arriving from different sources (e.g. Dropbox and intervals.icu)
// where the raw bytes differ but the activity is identical.
// Returns the existing workout ID if found, or "" if no duplicate.
func (db *DB) FindDuplicateWorkout(recordedAt time.Time, sport string, durationSecs int) (string, error) {
	ts := recordedAt.UTC().Format(time.RFC3339)
	var id string
	err := db.QueryRow(`
		SELECT id FROM workouts
		WHERE sport = ?
		  AND ABS(CAST(strftime('%s', recorded_at) AS INTEGER) - CAST(strftime('%s', ?) AS INTEGER)) <= 60
		  AND ABS(duration_secs - ?) <= MAX(? * 0.05, 10)
		LIMIT 1`, sport, ts, durationSecs, durationSecs).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}

// DeleteWorkout removes a workout, its streams (cascades via FK), and every
// imported_files entry recorded for its file — including hash aliases — so the
// same file can be deliberately re-imported. Deleting is an explicit user
// action; making the ledger forget the file is what makes it reversible.
func (db *DB) DeleteWorkout(id string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	// Ledger first: the filename lookup needs the workout row to still exist.
	if _, err := tx.Exec(`
		DELETE FROM imported_files WHERE hash IN (
			SELECT hash FROM imported_files
			WHERE filename = (SELECT filename FROM workouts WHERE id = ?))`, id); err != nil {
		return fmt.Errorf("clear import ledger: %w", err)
	}
	res, err := tx.Exec("DELETE FROM workouts WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

// DeleteAllWorkouts removes every workout (streams and power curves cascade) and
// clears imported_files so the same files can be re-imported if needed.
func (db *DB) DeleteAllWorkouts() error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for _, table := range []string{
		"workout_streams",
		"workout_power_curve",
		"workout_zone_times",
		"workouts",
		"imported_files",
	} {
		if _, err := tx.Exec("DELETE FROM " + table); err != nil {
			return fmt.Errorf("delete %s: %w", table, err)
		}
	}
	return tx.Commit()
}

// ResetDerivedData clears everything that is derived from the archived FIT files
// — workouts, streams, power curves, zone times, routes, and the imported-file
// ledger — so it can be rebuilt from scratch by re-importing the archive. The
// athlete profile, FTP history, integration tokens/credentials, mileage goals,
// AI settings, and planned workouts are all preserved, because the reimport
// reproduces correct derived metrics from them.
func (db *DB) ResetDerivedData() error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	// workouts before routes so the route_id FK so we aren't causing SQLite to do a per-row cascade.
	for _, table := range []string{
		"workout_streams",
		"workout_power_curve",
		"workout_zone_times",
		"workouts",
		"routes",
		"imported_files",
	} {
		if _, err := tx.Exec("DELETE FROM " + table); err != nil {
			return fmt.Errorf("reset %s: %w", table, err)
		}
	}
	return tx.Commit()
}

// AllWorkoutArchiveRefs returns the id, recorded time, and sport of every
// workout — the fields needed to compute its expected archive path. Used by the
// pre-rebuild coverage check to find workouts missing from the archive.
func (db *DB) AllWorkoutArchiveRefs() ([]models.Workout, error) {
	rows, err := db.Query(`SELECT id, recorded_at, sport FROM workouts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var out []models.Workout
	for rows.Next() {
		var w models.Workout
		var rec string
		if err := rows.Scan(&w.ID, &rec, &w.Sport); err != nil {
			return nil, err
		}
		if w.RecordedAt, err = time.Parse(time.RFC3339, rec); err != nil {
			return nil, fmt.Errorf("parse recorded_at %q: %w", rec, err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// ── Import tracking ───────────────────────────────────────────────────────────

// MarkImported records a file hash so it won't be re-imported.
func (db *DB) MarkImported(hash, filename string) error {
	_, err := db.Exec(
		"INSERT OR IGNORE INTO imported_files (hash, filename) VALUES (?,?)",
		hash, filename,
	)
	return err
}

// IsImported returns true if the file hash has been seen before.
func (db *DB) IsImported(hash string) (bool, error) {
	var exists bool
	err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM imported_files WHERE hash = ?)", hash).Scan(&exists)
	return exists, err
}

// IsFilenameImported reports whether a file with the given filename has been imported.
// Used by integrations to avoid re-downloading already-imported files.
func (db *DB) IsFilenameImported(filename string) (bool, error) {
	var exists bool
	err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM imported_files WHERE filename = ?)", filename).Scan(&exists)
	return exists, err
}

// AllImportedFilenames returns the set of every filename that has been imported.
// Used by integrations to batch-check a whole folder listing at once.
func (db *DB) AllImportedFilenames() (map[string]struct{}, error) {
	rows, err := db.Query("SELECT filename FROM imported_files")
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	m := make(map[string]struct{})
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		m[s] = struct{}{}
	}
	return m, rows.Err()
}

// ── Power curves ──────────────────────────────────────────────────────────────

// InsertPowerCurve stores best-effort watts for standard durations for a workout.
// Replaces any existing data for that workout (safe to call repeatedly).
func (db *DB) InsertPowerCurve(workoutID string, curve map[int]int) error {
	if len(curve) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec("DELETE FROM workout_power_curve WHERE workout_id = ?", workoutID); err != nil {
		return err
	}
	stmt, err := tx.Prepare("INSERT INTO workout_power_curve (workout_id, duration_secs, watts) VALUES (?,?,?)")
	if err != nil {
		return err
	}
	defer stmt.Close() //nolint:errcheck
	for dur, w := range curve {
		if _, err := stmt.Exec(workoutID, dur, w); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetWorkoutPowerCurve returns the stored best watts per duration for a single workout.
func (db *DB) GetWorkoutPowerCurve(workoutID string) (map[int]int, error) {
	rows, err := db.Query(`SELECT duration_secs, watts FROM workout_power_curve WHERE workout_id = ?`, workoutID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	result := map[int]int{}
	for rows.Next() {
		var dur, w int
		if err := rows.Scan(&dur, &w); err != nil {
			return nil, err
		}
		result[dur] = w
	}
	return result, rows.Err()
}

// GetAllTimePowerCurve returns the best watts and source workout per duration across all workouts.
// Returns a map of duration_secs → AllTimeBest.
func (db *DB) GetAllTimePowerCurve() (map[int]models.AllTimeBest, error) {
	rows, err := db.Query(`
		SELECT wpc.duration_secs, wpc.watts, wpc.workout_id
		FROM workout_power_curve wpc
		INNER JOIN (
			SELECT duration_secs, MAX(watts) AS max_watts
			FROM workout_power_curve
			GROUP BY duration_secs
		) best ON wpc.duration_secs = best.duration_secs AND wpc.watts = best.max_watts
		GROUP BY wpc.duration_secs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	result := map[int]models.AllTimeBest{}
	for rows.Next() {
		var dur int
		var b models.AllTimeBest
		if err := rows.Scan(&dur, &b.Watts, &b.WorkoutID); err != nil {
			return nil, err
		}
		result[dur] = b
	}
	return result, rows.Err()
}

// GetPowerCurveSince returns the best watts per standard duration across
// workouts recorded on or after `since` — the recent-form counterpart to
// GetAllTimePowerCurve, used by the coach to compare current ability against
// all-time bests.
func (db *DB) GetPowerCurveSince(since time.Time) (map[int]models.AllTimeBest, error) {
	rows, err := db.Query(`
		SELECT wpc.duration_secs, wpc.watts, wpc.workout_id
		FROM workout_power_curve wpc
		INNER JOIN (
			SELECT wpc2.duration_secs, MAX(wpc2.watts) AS max_watts
			FROM workout_power_curve wpc2
			INNER JOIN workouts w2 ON w2.id = wpc2.workout_id
			WHERE w2.recorded_at >= ?
			GROUP BY wpc2.duration_secs
		) best ON wpc.duration_secs = best.duration_secs AND wpc.watts = best.max_watts
		INNER JOIN workouts w ON w.id = wpc.workout_id AND w.recorded_at >= ?
		GROUP BY wpc.duration_secs`,
		since.UTC().Format(time.RFC3339), since.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	result := map[int]models.AllTimeBest{}
	for rows.Next() {
		var dur int
		var b models.AllTimeBest
		if err := rows.Scan(&dur, &b.Watts, &b.WorkoutID); err != nil {
			return nil, err
		}
		result[dur] = b
	}
	return result, rows.Err()
}

// ── Zone times ────────────────────────────────────────────────────────────────

// InsertZoneTimes stores pre-computed zone seconds for a workout. ss is the
// Sweet Spot reference band (88–94% FTP) — a parallel counter, not a 7-zone
// bucket. Pass -1 to leave ss_secs NULL (e.g. when FTP was unknown).
func (db *DB) InsertZoneTimes(workoutID string, power [7]int, hr [5]int, ss int) error {
	ps, _ := json.Marshal(power)
	hs, _ := json.Marshal(hr)
	var ssArg any
	if ss >= 0 {
		ssArg = ss
	}
	_, err := db.Exec(`
		INSERT OR REPLACE INTO workout_zone_times (workout_id, power_secs, hr_secs, ss_secs)
		VALUES (?, ?, ?, ?)`, workoutID, string(ps), string(hs), ssArg)
	return err
}

// GetZoneTimes returns the stored zone seconds for a workout. The third return
// is Sweet Spot seconds, nil if not yet computed for this workout (NULL in DB).
// Returns (nil, nil, nil, nil) if no row exists at all.
func (db *DB) GetZoneTimes(workoutID string) (*[7]int, *[5]int, *int, error) {
	var ps, hs string
	var ss sql.NullInt64
	err := db.QueryRow(`SELECT power_secs, hr_secs, ss_secs FROM workout_zone_times WHERE workout_id = ?`, workoutID).Scan(&ps, &hs, &ss)
	if err == sql.ErrNoRows {
		return nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, err
	}
	var power [7]int
	var hr [5]int
	json.Unmarshal([]byte(ps), &power) //nolint:errcheck
	json.Unmarshal([]byte(hs), &hr)    //nolint:errcheck
	var ssPtr *int
	if ss.Valid {
		v := int(ss.Int64)
		ssPtr = &v
	}
	return &power, &hr, ssPtr, nil
}

// WorkoutIDsWithoutSSZone returns IDs of workouts whose zone-times row exists
// but predates the ss_secs column (NULL). Used for one-time startup backfill.
func (db *DB) WorkoutIDsWithoutSSZone() ([]string, error) {
	rows, err := db.Query(`
		SELECT workout_id FROM workout_zone_times
		WHERE ss_secs IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SetSSZoneSecs updates only the ss_secs column for a workout. Used by the
// backfill path so it doesn't have to re-shape the 7-zone partition.
func (db *DB) SetSSZoneSecs(workoutID string, ss int) error {
	_, err := db.Exec(`UPDATE workout_zone_times SET ss_secs = ? WHERE workout_id = ?`, ss, workoutID)
	return err
}

// WorkoutIDsWithoutPowerCurve returns IDs of workouts that have power data
// but no entry in workout_power_curve (for backfill on startup).
func (db *DB) WorkoutIDsWithoutPowerCurve() ([]string, error) {
	rows, err := db.Query(`
		SELECT id FROM workouts
		WHERE avg_power_watts IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM workout_power_curve wpc WHERE wpc.workout_id = workouts.id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ── Routes ────────────────────────────────────────────────────────────────────

// RouteCandidate is a lightweight route representation for matching.
type RouteCandidate struct {
	ID    string
	Cells string
}

// GetAllRouteCandidates returns all routes (id + cells) for matching.
func (db *DB) GetAllRouteCandidates() ([]RouteCandidate, error) {
	rows, err := db.Query("SELECT id, cells FROM routes")
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var candidates []RouteCandidate
	for rows.Next() {
		var c RouteCandidate
		if err := rows.Scan(&c.ID, &c.Cells); err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}
	return candidates, rows.Err()
}

// InsertRoute stores a new route.
func (db *DB) InsertRoute(id, cells string, cellCount int) error {
	_, err := db.Exec(`INSERT OR IGNORE INTO routes (id, cells, cell_count) VALUES (?,?,?)`,
		id, cells, cellCount)
	return err
}

// SetWorkoutRouteID assigns a route to a workout.
func (db *DB) SetWorkoutRouteID(workoutID, routeID string) error {
	_, err := db.Exec("UPDATE workouts SET route_id = ? WHERE id = ?", routeID, workoutID)
	return err
}

// GetRouteHistory returns the route name and all workouts sharing that route,
// ordered by date descending. Returns ("", nil, nil) if the route doesn't exist.
func (db *DB) GetRouteHistory(routeID string) (string, []models.Workout, error) {
	var name string
	err := db.QueryRow("SELECT name FROM routes WHERE id = ?", routeID).Scan(&name)
	if err == sql.ErrNoRows {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}

	rows, err := db.Query(`
		SELECT `+workoutCols+`
		FROM workouts WHERE route_id = ?
		ORDER BY recorded_at DESC`, routeID)
	if err != nil {
		return name, nil, err
	}
	defer rows.Close() //nolint:errcheck
	var workouts []models.Workout
	for rows.Next() {
		w, err := scanWorkout(rows)
		if err != nil {
			return name, nil, err
		}
		workouts = append(workouts, w)
	}
	return name, workouts, rows.Err()
}

// WorkoutIDsWithoutRoute returns IDs of non-indoor workouts with GPS data but no route_id.
func (db *DB) WorkoutIDsWithoutRoute() ([]string, error) {
	rows, err := db.Query(`
		SELECT DISTINCT w.id FROM workouts w
		JOIN workout_streams ws ON ws.workout_id = w.id
		WHERE w.route_id IS NULL AND w.is_indoor = 0 AND ws.lat IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ── Athlete ───────────────────────────────────────────────────────────────────

// GetAthlete returns the athlete profile.
func (db *DB) GetAthlete() (*models.Athlete, error) {
	var a models.Athlete
	var updatedAt string
	var setupComplete int
	err := db.QueryRow(`
		SELECT ftp_watts, weight_kg, threshold_hr, max_hr,
		       resting_hr, age, location, language, timezone, units, setup_complete,
		       hr_zones_json, updated_at
		FROM athlete WHERE id = 1`).
		Scan(&a.FTPWatts, &a.WeightKG, &a.ThresholdHR, &a.MaxHR,
			&a.RestingHR, &a.Age, &a.Location, &a.Language, &a.Timezone, &a.Units, &setupComplete,
			&a.HRZonesJSON, &updatedAt)
	if err != nil {
		return nil, err
	}
	a.SetupComplete = setupComplete == 1
	if a.UpdatedAt, err = time.Parse(time.RFC3339, updatedAt); err != nil {
		return nil, fmt.Errorf("parse updated_at %q: %w", updatedAt, err)
	}
	return &a, nil
}

// UpdateAthlete saves all athlete profile fields and logs an FTP history entry
// if FTPWatts > 0.
func (db *DB) UpdateAthlete(a *models.Athlete) error {
	_, err := db.Exec(`
		UPDATE athlete SET
			ftp_watts=?, weight_kg=?, threshold_hr=?, max_hr=?, resting_hr=?,
			age=?, location=?, language=?, timezone=?, units=?,
			updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')
		WHERE id=1`,
		a.FTPWatts, a.WeightKG, a.ThresholdHR, a.MaxHR, a.RestingHR,
		a.Age, a.Location, a.Language, a.Timezone, a.Units,
	)
	if err != nil {
		return err
	}
	if a.FTPWatts > 0 {
		return db.LogFTPChange(a.FTPWatts)
	}
	return nil
}

// SaveWelcomeProfile saves the first-run profile and marks setup as complete.
func (db *DB) SaveWelcomeProfile(a *models.Athlete) error {
	if err := db.UpdateAthlete(a); err != nil {
		return err
	}
	_, err := db.Exec("UPDATE athlete SET setup_complete=1 WHERE id=1")
	return err
}

// UpdateAthleteUnits persists the unit preference ("imperial" or "metric").
func (db *DB) UpdateAthleteUnits(units string) error {
	_, err := db.Exec("UPDATE athlete SET units=? WHERE id=1", units)
	return err
}

// MarkSetupComplete marks the welcome flow as done without saving any profile data.
func (db *DB) MarkSetupComplete() error {
	_, err := db.Exec("UPDATE athlete SET setup_complete=1 WHERE id=1")
	return err
}

// SetCustomHRZones stores 4 upper BPM bounds as JSON (Z1–Z4 max; Z5 is open-ended).
func (db *DB) SetCustomHRZones(zonesJSON string) error {
	_, err := db.Exec("UPDATE athlete SET hr_zones_json=? WHERE id=1", zonesJSON)
	return err
}

// ClearCustomHRZones removes any custom HR zone overrides, reverting to calculated zones.
func (db *DB) ClearCustomHRZones() error {
	_, err := db.Exec("UPDATE athlete SET hr_zones_json='' WHERE id=1")
	return err
}

// ── FTP history ───────────────────────────────────────────────────────────────

// LogFTPChange records a new FTP value in the history table, effective now.
func (db *DB) LogFTPChange(ftp int) error {
	_, err := db.Exec(
		"INSERT INTO ftp_history (ftp_watts, effective_from) VALUES (?, strftime('%Y-%m-%dT%H:%M:%SZ','now'))",
		ftp,
	)
	return err
}

// HasFTPHistory reports whether the athlete has ever saved an FTP value.
// Empty history means the FTP is still the seeded default.
func (db *DB) HasFTPHistory() (bool, error) {
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM ftp_history").Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// GetFTPAtDate returns the FTP that was active at time t.
// Falls back to current athlete FTP if no history entry predates t.
func (db *DB) GetFTPAtDate(t time.Time) int {
	var ftp int
	err := db.QueryRow(`
		SELECT ftp_watts FROM ftp_history
		WHERE effective_from <= ?
		ORDER BY effective_from DESC
		LIMIT 1`, t.UTC().Format(time.RFC3339)).Scan(&ftp)
	if err != nil || ftp <= 0 {
		_ = db.QueryRow("SELECT ftp_watts FROM athlete WHERE id=1").Scan(&ftp)
	}
	return ftp
}

// LogFTPChangeAt records an FTP value in the history table with an explicit effective date.
func (db *DB) LogFTPChangeAt(ftp int, at time.Time) error {
	_, err := db.Exec(
		"INSERT INTO ftp_history (ftp_watts, effective_from) VALUES (?, ?)",
		ftp, at.UTC().Format(time.RFC3339),
	)
	return err
}

// ClearFTPHistory deletes all rows from the FTP history table.
func (db *DB) ClearFTPHistory() error {
	_, err := db.Exec("DELETE FROM ftp_history")
	return err
}

// FTPHistoryEntry is one row of the ftp_history table.
type FTPHistoryEntry struct {
	ID            int64
	FTPWatts      int
	EffectiveFrom time.Time
}

// AllFTPHistory returns every FTP history entry, newest first.
func (db *DB) AllFTPHistory() ([]FTPHistoryEntry, error) {
	rows, err := db.Query(`
		SELECT id, ftp_watts, effective_from
		FROM ftp_history
		ORDER BY effective_from DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var out []FTPHistoryEntry
	for rows.Next() {
		var e FTPHistoryEntry
		var eff string
		if err := rows.Scan(&e.ID, &e.FTPWatts, &eff); err != nil {
			return nil, err
		}
		e.EffectiveFrom, _ = time.Parse(time.RFC3339, eff)
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeleteFTPHistoryEntry removes a single FTP history row by id.
func (db *DB) DeleteFTPHistoryEntry(id int64) error {
	_, err := db.Exec("DELETE FROM ftp_history WHERE id = ?", id)
	return err
}

// RecomputePowerLoad recomputes TSS and intensity factor for every workout that
// has normalized power, using GetFTPAtDate for each workout's recorded date.
// onProgress, if non-nil, is called once with (0, total) up front and once per
// processed workout as (done, total).
func (db *DB) RecomputePowerLoad(onProgress func(done, total int)) (updated int, err error) {
	workouts, err := db.AllWorkoutsForTSSBackfill()
	if err != nil {
		return 0, fmt.Errorf("load workouts: %w", err)
	}
	if onProgress != nil {
		onProgress(0, len(workouts))
	}
	for i, wk := range workouts {
		ftp := db.GetFTPAtDate(wk.RecordedAt)
		if ftp <= 0 {
			if onProgress != nil {
				onProgress(i+1, len(workouts))
			}
			continue
		}
		ftpF := float64(ftp)
		ifactor := fitness.IntensityFactor(wk.NormalizedPower, ftpF)
		tss := fitness.PowerTSS(wk.DurationSecs, wk.NormalizedPower, ftpF)
		if err := db.UpdateWorkoutLoad(wk.ID, tss, ifactor); err != nil {
			slog.Warn("recompute power load: update workout", "id", wk.ID, "err", err)
		} else {
			updated++
		}
		if onProgress != nil {
			onProgress(i+1, len(workouts))
		}
	}
	return updated, nil
}

// WorkoutTSSRow holds the fields needed to recompute TSS for a workout.
type WorkoutTSSRow struct {
	ID              string
	RecordedAt      time.Time
	DurationSecs    int
	NormalizedPower float64
}

// AllWorkoutsForTSSBackfill returns all workouts that have normalized power stored,
// which is sufficient to recompute TSS and intensity factor from FTP alone.
func (db *DB) AllWorkoutsForTSSBackfill() ([]WorkoutTSSRow, error) {
	rows, err := db.Query(`
		SELECT id, recorded_at, duration_secs, normalized_power
		FROM workouts
		WHERE normalized_power IS NOT NULL AND normalized_power > 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var out []WorkoutTSSRow
	for rows.Next() {
		var r WorkoutTSSRow
		var recordedAt string
		if err := rows.Scan(&r.ID, &recordedAt, &r.DurationSecs, &r.NormalizedPower); err != nil {
			return nil, err
		}
		r.RecordedAt, _ = time.Parse(time.RFC3339, recordedAt)
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateWorkoutLoad writes recomputed TSS and intensity factor back to a workout row.
func (db *DB) UpdateWorkoutLoad(id string, tss, intensityFactor float64) error {
	_, err := db.Exec(
		"UPDATE workouts SET tss=?, intensity_factor=? WHERE id=?",
		tss, intensityFactor, id,
	)
	return err
}

// ── Training load ─────────────────────────────────────────────────────────────

// AthleteLocation returns the athlete's configured timezone, falling back to
// UTC. Exported so callers outside this package (the AI coach's "today" and
// goal-progress weeks) resolve the timezone through the same policy as the
// rest of the app.
func (db *DB) AthleteLocation() *time.Location {
	return db.athleteLocation()
}

// athleteLocation returns the athlete's configured timezone, falling back to UTC.
// Used to anchor "today" and per-day TSS grouping to the user's local calendar
// instead of UTC, so a workout finished at 11pm local lands on the day the user
// thinks it does.
func (db *DB) athleteLocation() *time.Location {
	var tz string
	_ = db.QueryRow("SELECT timezone FROM athlete WHERE id=1").Scan(&tz)
	if tz == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC
	}
	return loc
}

// GetFitnessOnDate returns the Fitness/Fatigue/Form values as of a specific date.
// Uses the same 270-day lookback as the dashboard chart (90-day display + 180-day
// warmup) so the EMA starting conditions are identical and the values match.
// "Today" and the target are interpreted in the athlete's timezone.
func (db *DB) GetFitnessOnDate(date time.Time) (models.FitnessPoint, error) {
	tz := db.athleteLocation()
	target := timeutil.LocalMidnight(date.In(tz))
	today := timeutil.LocalMidnight(time.Now().In(tz))
	daysAgo := max(int(today.Sub(target).Hours()/24), 0)
	// Request daysAgo+90 days so the total lookback matches the chart (270 days).
	// The target date lands at index 90 from the oldest point returned.
	points, err := db.getFitnessHistory(daysAgo+90, 0)
	if err != nil {
		return models.FitnessPoint{}, err
	}
	if len(points) == 0 {
		return models.FitnessPoint{}, nil
	}
	// points is oldest-first; index 90 is the target date.
	if len(points) <= 90 {
		return points[0], nil
	}
	return points[90], nil
}

// GetFitnessHistory returns daily Fitness/Fatigue/Form for the last n days.
func (db *DB) GetFitnessHistory(days int) ([]models.FitnessPoint, error) {
	return db.getFitnessHistory(days, 0)
}

// GetFitnessHistoryForChart returns fitness history plus projected days assuming zero TSS.
func (db *DB) GetFitnessHistoryForChart(days, projection int) ([]models.FitnessPoint, error) {
	return db.getFitnessHistory(days, projection)
}

func (db *DB) getFitnessHistory(days, projection int) ([]models.FitnessPoint, error) {
	// Warm up the EMA with 180 days of history before the display window.
	// Fitness has a 42-day constant; starting from 0 at day -90 causes ~12% error
	// and visible ramp-up distortion for the first several weeks of the chart.
	const warmup = 180
	totalDays := days + warmup

	tz := db.athleteLocation()
	today := timeutil.LocalMidnight(time.Now().In(tz))
	start := today.AddDate(0, 0, -totalDays)

	// training_day was computed at insert time using the athlete's local
	// timezone, so we can group/filter by it directly without per-query math.
	rows, err := db.Query(`
		SELECT training_day AS day, COALESCE(SUM(tss), 0) AS daily_tss
		FROM workouts
		WHERE training_day >= ?
		  AND training_day IS NOT NULL
		  AND tss IS NOT NULL
		GROUP BY training_day
		ORDER BY training_day ASC`, start.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	tssByDay := map[string]float64{}
	for rows.Next() {
		var day string
		var tss float64
		if err := rows.Scan(&day, &tss); err != nil {
			return nil, err
		}
		tssByDay[day] = tss
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Walk the full range (warmup + display + projection) computing the EMA.
	// Only return points after the warmup period to avoid ramp-up distortion.
	// The +1 includes today: days=0 → [today], days=1 → [yesterday, today], etc.
	// Projection days extend past today with zero TSS for chart forecasting.
	points := fitness.ComputeLoad(tssByDay, start, totalDays+1+projection, warmup)
	// Mark trailing `projection` points as forecast so the chart can render
	// them differently without doing its own clock math.
	for i := len(points) - projection; i < len(points); i++ {
		points[i].IsProjection = true
	}
	return points, nil
}

// GetLastWorkoutDate returns the recorded_at of the most recent workout, or nil if none.
func (db *DB) GetLastWorkoutDate() (*time.Time, error) {
	var s string
	err := db.QueryRow(`SELECT recorded_at FROM workouts ORDER BY recorded_at DESC LIMIT 1`).Scan(&s)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// GetWeeklyBreakdown returns per-ISO-week training totals for the last n weeks,
// including the current (partial) week. Weeks with no workouts are omitted.
// Results are ordered oldest first. Weeks are reckoned from training_day, so a
// ride belongs to the week the athlete experienced it in, not the UTC week.
func (db *DB) GetWeeklyBreakdown(weeks int) ([]models.WeeklyLoad, error) {
	tz := db.athleteLocation()
	start := timeutil.MondayOf(time.Now().In(tz)).AddDate(0, 0, -((weeks - 1) * 7))

	rows, err := db.Query(`
		SELECT id, training_day, COALESCE(tss, 0), duration_secs, distance_meters, elevation_gain_meters
		FROM workouts
		WHERE training_day >= ? AND training_day IS NOT NULL
		ORDER BY training_day ASC, recorded_at ASC`,
		start.Format("2006-01-02"),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	// Aggregate per ISO week in Go: rows arrive ordered by training_day, so each
	// week's workouts are contiguous and a label change opens a new bucket.
	var result []models.WeeklyLoad
	for rows.Next() {
		var ref models.WorkoutRef
		if err := rows.Scan(&ref.ID, &ref.TrainingDay, &ref.TSS, &ref.DurationSecs, &ref.DistanceMeters, &ref.ElevationGainMeters); err != nil {
			return nil, err
		}
		day, err := time.ParseInLocation("2006-01-02", ref.TrainingDay, tz)
		if err != nil {
			continue // malformed training_day: skip rather than fail the report
		}
		week := timeutil.ISOWeekLabel(day)
		if len(result) == 0 || result[len(result)-1].Week != week {
			result = append(result, models.WeeklyLoad{Week: week})
		}
		wl := &result[len(result)-1]
		wl.TSS += ref.TSS
		wl.DurationSecs += ref.DurationSecs
		wl.DistanceMeters += ref.DistanceMeters
		wl.ElevationGainMeters += ref.ElevationGainMeters
		wl.WorkoutCount++
		wl.WorkoutRefs = append(wl.WorkoutRefs, ref)
	}
	for i := range result {
		result[i].LoadType = fitness.ClassifyWeeklyLoad(result[i].TSS)
	}
	return result, rows.Err()
}

// NinetyDayAverages holds sport-scoped 90-day averages for workout analysis comparisons.
type NinetyDayAverages struct {
	AvgNP           *float64
	AvgHR           *float64
	AvgTSS          *float64
	AvgIF           *float64
	AvgDurationSecs *float64
}

// Get90DayAverages returns average power/HR/TSS metrics for a sport over the last 90 days.
// Only workouts with power data are included. Returns nil if no qualifying workouts exist.
func (db *DB) Get90DayAverages(sport string) (*NinetyDayAverages, error) {
	// The window is cut on training_day — SQLite's date('now') is UTC-now,
	// which would shift the boundary by a day for athletes west of Greenwich.
	cutoff := timeutil.LocalMidnight(time.Now().In(db.athleteLocation())).
		AddDate(0, 0, -90).Format("2006-01-02")
	var a NinetyDayAverages
	err := db.QueryRow(`
		SELECT
			AVG(NULLIF(normalized_power, 0)),
			AVG(NULLIF(avg_heart_rate, 0)),
			AVG(NULLIF(tss, 0)),
			AVG(NULLIF(intensity_factor, 0)),
			AVG(duration_secs)
		FROM workouts
		WHERE training_day >= ?
		  AND sport = ?
		  AND avg_power_watts IS NOT NULL`, cutoff, sport).Scan(
		&a.AvgNP, &a.AvgHR, &a.AvgTSS, &a.AvgIF, &a.AvgDurationSecs)
	if err != nil {
		return nil, err
	}
	if a.AvgNP == nil && a.AvgHR == nil {
		return nil, nil
	}
	return &a, nil
}

// ── Integrations ──────────────────────────────────────────────────────────────

// GetIntegrationToken returns the decrypted OAuth token JSON for an integration, or "" if not connected.
func (db *DB) GetIntegrationToken(name string) (string, error) {
	var stored string
	err := db.QueryRow("SELECT token_json FROM integrations WHERE name = ?", name).Scan(&stored)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	plain, err := crypto.Decrypt(db.key, stored)
	if err != nil {
		return "", fmt.Errorf("decrypt token for %q: %w", name, err)
	}
	return string(plain), nil
}

// SetIntegrationToken encrypts and stores the OAuth token JSON for an integration.
func (db *DB) SetIntegrationToken(name, tokenJSON string) error {
	encrypted, err := crypto.Encrypt(db.key, []byte(tokenJSON))
	if err != nil {
		return fmt.Errorf("encrypt token for %q: %w", name, err)
	}
	_, err = db.Exec(`
		INSERT INTO integrations (name, token_json) VALUES (?, ?)
		ON CONFLICT(name) DO UPDATE SET token_json=excluded.token_json,
		                                updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')`,
		name, encrypted,
	)
	return err
}

// DeleteIntegrationToken removes a stored integration token.
func (db *DB) DeleteIntegrationToken(name string) error {
	_, err := db.Exec("DELETE FROM integrations WHERE name = ?", name)
	return err
}

// GetIntegrationCredentials returns the decrypted client ID and secret for an integration.
// Returns empty strings (not an error) if no credentials have been saved yet.
func (db *DB) GetIntegrationCredentials(name string) (clientID, clientSecret string, err error) {
	var encID, encSecret string
	err = db.QueryRow("SELECT client_id, client_secret FROM integration_credentials WHERE name = ?", name).
		Scan(&encID, &encSecret)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	if encID == "" {
		return "", "", nil
	}
	idBytes, err := crypto.Decrypt(db.key, encID)
	if err != nil {
		return "", "", fmt.Errorf("decrypt client_id for %q: %w", name, err)
	}
	secretBytes, err := crypto.Decrypt(db.key, encSecret)
	if err != nil {
		return "", "", fmt.Errorf("decrypt client_secret for %q: %w", name, err)
	}
	return string(idBytes), string(secretBytes), nil
}

// SetIntegrationCredentials encrypts and stores the OAuth app credentials for an integration.
func (db *DB) SetIntegrationCredentials(name, clientID, clientSecret string) error {
	encID, err := crypto.Encrypt(db.key, []byte(clientID))
	if err != nil {
		return fmt.Errorf("encrypt client_id for %q: %w", name, err)
	}
	encSecret, err := crypto.Encrypt(db.key, []byte(clientSecret))
	if err != nil {
		return fmt.Errorf("encrypt client_secret for %q: %w", name, err)
	}
	_, err = db.Exec(`
		INSERT INTO integration_credentials (name, client_id, client_secret) VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			client_id=excluded.client_id,
			client_secret=excluded.client_secret,
			updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')`,
		name, encID, encSecret,
	)
	return err
}

// DeleteIntegrationCredentials removes stored app credentials for an integration.
func (db *DB) DeleteIntegrationCredentials(name string) error {
	_, err := db.Exec("DELETE FROM integration_credentials WHERE name = ?", name)
	return err
}

// GetDropboxCursor returns the saved Dropbox list_folder cursor, or "" if none.
func (db *DB) GetDropboxCursor() (string, error) {
	var cursor string
	err := db.QueryRow("SELECT cursor FROM integrations WHERE name = 'dropbox'").Scan(&cursor)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return cursor, err
}

// SetDropboxCursor saves the Dropbox list_folder cursor.
func (db *DB) SetDropboxCursor(cursor string) error {
	_, err := db.Exec("UPDATE integrations SET cursor = ? WHERE name = 'dropbox'", cursor)
	return err
}

// GetAutoSync returns whether auto-sync is enabled for the named integration.
func (db *DB) GetAutoSync(name string) (bool, error) {
	var v int
	err := db.QueryRow("SELECT longpoll FROM integrations WHERE name = ?", name).Scan(&v)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return v == 1, err
}

// SetAutoSync enables or disables auto-sync for the named integration.
func (db *DB) SetAutoSync(name string, enabled bool) error {
	v := 0
	if enabled {
		v = 1
	}
	_, err := db.Exec(`
		INSERT INTO integrations (name, token_json, longpoll) VALUES (?, '', ?)
		ON CONFLICT(name) DO UPDATE SET longpoll=excluded.longpoll`,
		name, v)
	return err
}

// ── Period summary ────────────────────────────────────────────────────────────

// PeriodSummary holds aggregate training totals across all sports for a date range.
type PeriodSummary struct {
	DurationSecs int
	TSS          float64
}

// GetWeekActivityDays returns a 7-element array (Mon=0 … Sun=6) where each
// entry is true if any workout was recorded on that day of the ISO week.
func (db *DB) GetWeekActivityDays(weekStart time.Time, tz *time.Location) ([7]bool, error) {
	var days [7]bool
	weekEnd := weekStart.AddDate(0, 0, 7)
	rows, err := db.Query(`
		SELECT DISTINCT training_day FROM workouts
		WHERE training_day >= ? AND training_day < ? AND training_day IS NOT NULL`,
		weekStart.Format("2006-01-02"),
		weekEnd.Format("2006-01-02"),
	)
	if err != nil {
		return days, err
	}
	defer rows.Close() //nolint:errcheck
	for rows.Next() {
		var dayStr string
		if err := rows.Scan(&dayStr); err != nil {
			return days, err
		}
		t, err := time.ParseInLocation("2006-01-02", dayStr, tz)
		if err != nil {
			continue
		}
		days[timeutil.DaysSinceMonday(t)] = true
	}
	return days, rows.Err()
}

// GetPeriodSummary returns the total duration and TSS across all sports on or
// after the calendar day of `since` (callers pass local midnights — week or
// year starts). Cut on training_day so rides count toward the day the athlete
// experienced, not the UTC day.
func (db *DB) GetPeriodSummary(since time.Time) (PeriodSummary, error) {
	var s PeriodSummary
	err := db.QueryRow(`
		SELECT COALESCE(SUM(duration_secs), 0),
		       COALESCE(SUM(tss), 0)
		FROM workouts
		WHERE training_day >= ?`, since.Format("2006-01-02"),
	).Scan(&s.DurationSecs, &s.TSS)
	return s, err
}

// ── Mileage goals ─────────────────────────────────────────────────────────────

// MileageGoal holds the weekly/yearly targets for a single sport.
type MileageGoal struct {
	Sport        string
	WeeklyMeters float64
	YearlyMeters float64
}

// GetAllMileageGoals returns the stored goals for all three supported sports.
func (db *DB) GetAllMileageGoals() (map[string]MileageGoal, error) {
	rows, err := db.Query(`SELECT sport, weekly_meters, yearly_meters FROM mileage_goals`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	goals := map[string]MileageGoal{}
	for rows.Next() {
		var g MileageGoal
		if err := rows.Scan(&g.Sport, &g.WeeklyMeters, &g.YearlyMeters); err != nil {
			return nil, err
		}
		goals[g.Sport] = g
	}
	return goals, rows.Err()
}

// SaveMileageGoal upserts the weekly/yearly goal for a sport.
func (db *DB) SaveMileageGoal(sport string, weeklyMeters, yearlyMeters float64) error {
	_, err := db.Exec(`
		INSERT INTO mileage_goals (sport, weekly_meters, yearly_meters)
		VALUES (?, ?, ?)
		ON CONFLICT(sport) DO UPDATE SET
			weekly_meters = excluded.weekly_meters,
			yearly_meters = excluded.yearly_meters,
			updated_at    = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')`,
		sport, weeklyMeters, yearlyMeters,
	)
	return err
}

// MileageProgress holds week and year distance totals for one sport, plus a
// per-day breakdown of the current ISO week for the bar chart.
type MileageProgress struct {
	WeekMeters    float64
	WeekDayMeters [7]float64 // Mon=0 … Sun=6
	YearMeters    float64
}

// GetMileageProgress returns distance totals for the given sport for the
// current ISO week (with per-day breakdown) and year-to-date.
func (db *DB) GetMileageProgress(sport string, tz *time.Location) (MileageProgress, error) {
	var p MileageProgress
	now := time.Now().In(tz)

	weekStart := timeutil.MondayOf(now)
	weekEnd := weekStart.AddDate(0, 0, 7)
	yearStart := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, tz)

	rows, err := db.Query(`
		SELECT training_day AS day,
		       COALESCE(SUM(distance_meters), 0)
		FROM workouts
		WHERE sport = ? AND training_day >= ? AND training_day < ? AND training_day IS NOT NULL
		GROUP BY training_day`,
		sport,
		weekStart.Format("2006-01-02"),
		weekEnd.Format("2006-01-02"),
	)
	if err != nil {
		return p, err
	}
	defer rows.Close() //nolint:errcheck

	for rows.Next() {
		var dayStr string
		var dist float64
		if err := rows.Scan(&dayStr, &dist); err != nil {
			return p, err
		}
		t, err := time.ParseInLocation("2006-01-02", dayStr, tz)
		if err != nil {
			continue
		}
		p.WeekDayMeters[timeutil.DaysSinceMonday(t)] = dist
		p.WeekMeters += dist
	}
	if err := rows.Err(); err != nil {
		return p, err
	}

	err = db.QueryRow(
		`SELECT COALESCE(SUM(distance_meters), 0) FROM workouts WHERE sport = ? AND training_day >= ?`,
		sport, yearStart.Format("2006-01-02"),
	).Scan(&p.YearMeters)
	return p, err
}

// ── Route tracks ─────────────────────────────────────────────────────────────

// routeCoordsVersion is the format version of the route_coords JSON, stamped
// on each row at insert. If the simplification changes, bump this AND append a
// migration that re-converges rows below the new version (see the migrations
// doc in migrate.go). NULL/absent = legacy uniform downsample (pre-RDP).
const routeCoordsVersion = 2

// geoVersion is the version of the geo lookup data (county boundaries +
// timezone shapes) stamped on each row's geo_v at insert. If the embedded
// datasets change meaningfully, bump this AND append a migration that
// re-converges rows below the new version (migration v5 is the template).
const geoVersion = 1

// WorkoutRouteTrack is a simplified GPS track for a single workout.
type WorkoutRouteTrack struct {
	WorkoutID           string
	Sport               string
	Date                time.Time
	DistanceMeters      float64
	DurationSecs        int
	ElevationGainMeters float64
	Coords              [][2]float64 // GeoJSON order: [lng, lat]
}

// simplifyCoords reduces GPS coords using Ramer-Douglas-Peucker, which preserves
// corners and road curves while aggressively dropping redundant points on straight
// sections. Falls back to uniform downsampling only if RDP still exceeds maxPts.
func simplifyCoords(pts [][2]float64, maxPts int) [][2]float64 {
	if len(pts) < 2 {
		return [][2]float64{}
	}
	// ~5 m tolerance in degree space; keeps all meaningful bends in the road.
	simplified := rdpSimplify(pts, 0.00005)
	if len(simplified) <= maxPts {
		return simplified
	}
	// Safety cap: uniformly thin the already-simplified set. The step is
	// rounded up so the sampled count can never exceed maxPts.
	step := (len(simplified) + maxPts - 1) / maxPts
	out := make([][2]float64, 0, maxPts)
	for i := 0; i < len(simplified); i += step {
		out = append(out, simplified[i])
	}
	// Always keep the true final point, without breaching the cap.
	if last := simplified[len(simplified)-1]; out[len(out)-1] != last {
		if len(out) < maxPts {
			out = append(out, last)
		} else {
			out[len(out)-1] = last
		}
	}
	return out
}

// rdpSimplify is the recursive Ramer-Douglas-Peucker simplification algorithm.
func rdpSimplify(pts [][2]float64, epsilon float64) [][2]float64 {
	if len(pts) < 3 {
		// Return a copy: callers must never receive a slice that aliases the
		// input backing array (see the combine step below for why).
		return append([][2]float64(nil), pts...)
	}
	maxDist, maxIdx := 0.0, 0
	first, last := pts[0], pts[len(pts)-1]
	for i := 1; i < len(pts)-1; i++ {
		if d := rdpPerpDist(pts[i], first, last); d > maxDist {
			maxDist, maxIdx = d, i
		}
	}
	if maxDist > epsilon {
		left := rdpSimplify(pts[:maxIdx+1], epsilon)
		right := rdpSimplify(pts[maxIdx:], epsilon)
		// Build a fresh slice. `append(left[:len(left)-1], right...)` would
		// write into a backing array that can still alias the shared input
		// (pts[:maxIdx+1] keeps the original capacity), corrupting points the
		// sibling recursion has not read yet.
		out := make([][2]float64, 0, len(left)-1+len(right))
		out = append(out, left[:len(left)-1]...)
		out = append(out, right...)
		return out
	}
	return [][2]float64{first, last}
}

// rdpPerpDist returns the perpendicular distance from point p to the line a→b.
func rdpPerpDist(p, a, b [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	if dx == 0 && dy == 0 {
		return math.Sqrt((p[0]-a[0])*(p[0]-a[0]) + (p[1]-a[1])*(p[1]-a[1]))
	}
	return math.Abs(dy*p[0]-dx*p[1]+b[0]*a[1]-b[1]*a[0]) / math.Sqrt(dx*dx+dy*dy)
}

// GetWorkoutRouteTracks returns pre-computed GPS tracks for the given workout IDs.
// Pass nil ids to return tracks for the 500 most recent outdoor workouts (heatmap mode).
// Coords are read directly from the route_coords column — no stream JOIN needed.
func (db *DB) GetWorkoutRouteTracks(ids []string) ([]WorkoutRouteTrack, error) {
	var rows *sql.Rows
	var err error
	if len(ids) == 0 {
		rows, err = db.Query(`
			SELECT id, sport, recorded_at, distance_meters, duration_secs, elevation_gain_meters, route_coords
			FROM workouts
			WHERE is_indoor = 0
			  AND route_coords IS NOT NULL
			  AND route_coords != '[]'
			ORDER BY recorded_at DESC LIMIT 500`)
	} else {
		ph := strings.Join(strings.Fields(strings.Repeat("? ", len(ids))), ",")
		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}
		rows, err = db.Query(fmt.Sprintf(`
			SELECT id, sport, recorded_at, distance_meters, duration_secs, elevation_gain_meters, route_coords
			FROM workouts
			WHERE id IN (%s)
			  AND route_coords IS NOT NULL
			  AND route_coords != '[]'`, ph), args...)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	var out []WorkoutRouteTrack
	for rows.Next() {
		var id, sport, recAt, coordsJSON string
		var distM, elevM float64
		var durSecs int
		if err := rows.Scan(&id, &sport, &recAt, &distM, &durSecs, &elevM, &coordsJSON); err != nil {
			return nil, err
		}
		var coords [][2]float64
		if err := json.Unmarshal([]byte(coordsJSON), &coords); err != nil || len(coords) < 2 {
			continue
		}
		t, _ := time.Parse(time.RFC3339, recAt)
		out = append(out, WorkoutRouteTrack{
			WorkoutID:           id,
			Sport:               sport,
			Date:                t,
			DistanceMeters:      distM,
			DurationSecs:        durSecs,
			ElevationGainMeters: elevM,
			Coords:              coords,
		})
	}
	return out, rows.Err()
}

// ── Helpers ───────────────────────────────────────────────────────────────────

type scanner interface {
	Scan(dest ...any) error
}

// workoutCols is the canonical workouts column list — every full-row SELECT
// uses it, and scanWorkout scans exactly these columns in this order. Keep
// the two in lockstep.
const workoutCols = `id, filename, recorded_at, sport, duration_secs, elapsed_secs, distance_meters,
	elevation_gain_meters, avg_power_watts, max_power_watts, normalized_power,
	avg_heart_rate, max_heart_rate, avg_cadence, avg_speed_mps,
	tss, intensity_factor, is_indoor, route_id, created_at,
	training_day, utc_offset_secs, start_lat, start_lng, county, state`

func scanWorkout(s scanner) (models.Workout, error) {
	var w models.Workout
	var recordedAt, createdAt string
	var trainingDay, county, state sql.NullString
	var offsetSecs sql.NullInt64
	err := s.Scan(
		&w.ID, &w.Filename, &recordedAt, &w.Sport,
		&w.DurationSecs, &w.ElapsedSecs, &w.DistanceMeters, &w.ElevationGainMeters,
		&w.AvgPowerWatts, &w.MaxPowerWatts, &w.NormalizedPower,
		&w.AvgHeartRate, &w.MaxHeartRate, &w.AvgCadenceRPM, &w.AvgSpeedMPS,
		&w.TSS, &w.IntensityFactor, &w.IsIndoor, &w.RouteID, &createdAt,
		&trainingDay, &offsetSecs, &w.StartLat, &w.StartLng, &county, &state,
	)
	if err != nil {
		return w, err
	}
	w.TrainingDay = trainingDay.String
	w.County, w.State = county.String, state.String
	if w.RecordedAt, err = time.Parse(time.RFC3339, recordedAt); err != nil {
		return w, fmt.Errorf("parse recorded_at %q: %w", recordedAt, err)
	}
	if offsetSecs.Valid {
		v := int(offsetSecs.Int64)
		w.UTCOffsetSecs = &v
		// Re-home the instant into the ride's own timezone: it still compares
		// equal as an instant, but Format and JSON now yield the wall-clock
		// time the athlete actually saw.
		w.RecordedAt = w.RecordedAt.In(time.FixedZone("", v))
	}
	if w.CreatedAt, err = time.Parse(time.RFC3339, createdAt); err != nil {
		return w, fmt.Errorf("parse created_at %q: %w", createdAt, err)
	}
	return w, nil
}
