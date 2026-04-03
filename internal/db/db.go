package db

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fitbase/fitbase/internal/crypto"
	"github.com/fitbase/fitbase/internal/fitness"
	"github.com/fitbase/fitbase/internal/models"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

type DB struct {
	*sql.DB
	key []byte // AES-256 key for encrypting OAuth tokens at rest
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

	if _, err := sqldb.Exec(schema); err != nil {
		return nil, fmt.Errorf("run schema: %w", err)
	}

	return &DB{sqldb, key}, nil
}

// ── Workouts ──────────────────────────────────────────────────────────────────

// InsertWorkout persists a workout and its streams in a single transaction.
func (db *DB) InsertWorkout(w *models.Workout, streams []models.Stream) error {
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
			tss, intensity_factor, is_indoor
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		w.ID, w.Filename, w.RecordedAt.UTC().Format(time.RFC3339),
		w.Sport, w.DurationSecs, w.ElapsedSecs, w.DistanceMeters, w.ElevationGainMeters,
		w.AvgPowerWatts, w.MaxPowerWatts, w.NormalizedPower,
		w.AvgHeartRate, w.MaxHeartRate, w.AvgCadenceRPM, w.AvgSpeedMPS,
		w.TSS, w.IntensityFactor, w.IsIndoor,
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
	row := db.QueryRow(`
		SELECT id, filename, recorded_at, sport, duration_secs, elapsed_secs, distance_meters,
		       elevation_gain_meters, avg_power_watts, max_power_watts, normalized_power,
		       avg_heart_rate, max_heart_rate, avg_cadence, avg_speed_mps,
		       tss, intensity_factor, is_indoor, route_id, created_at
		FROM workouts WHERE id = ?`, id)

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
	q := `SELECT id, filename, recorded_at, sport, duration_secs, elapsed_secs, distance_meters,
		       elevation_gain_meters, avg_power_watts, max_power_watts, normalized_power,
		       avg_heart_rate, max_heart_rate, avg_cadence, avg_speed_mps,
		       tss, intensity_factor, is_indoor, route_id, created_at
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

// GetWorkoutsForMonth returns all workouts whose recorded_at falls within the
// given month in the provided timezone, ordered chronologically.
func (db *DB) GetWorkoutsForMonth(year int, month time.Month, tz *time.Location) ([]models.Workout, error) {
	start := time.Date(year, month, 1, 0, 0, 0, 0, tz).UTC().Format(time.RFC3339)
	end := time.Date(year, month+1, 1, 0, 0, 0, 0, tz).UTC().Format(time.RFC3339)

	rows, err := db.Query(`
		SELECT id, filename, recorded_at, sport, duration_secs, elapsed_secs, distance_meters,
		       elevation_gain_meters, avg_power_watts, max_power_watts, normalized_power,
		       avg_heart_rate, max_heart_rate, avg_cadence, avg_speed_mps,
		       tss, intensity_factor, is_indoor, route_id, created_at
		FROM workouts
		WHERE recorded_at >= ? AND recorded_at < ?
		ORDER BY recorded_at ASC`, start, end)
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

// DeleteWorkout removes a workout and all its streams (cascades via FK).
func (db *DB) DeleteWorkout(id string) error {
	res, err := db.Exec("DELETE FROM workouts WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
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

// ── Zone times ────────────────────────────────────────────────────────────────

// InsertZoneTimes stores pre-computed zone seconds for a workout.
func (db *DB) InsertZoneTimes(workoutID string, power [7]int, hr [5]int) error {
	ps, _ := json.Marshal(power)
	hs, _ := json.Marshal(hr)
	_, err := db.Exec(`
		INSERT OR REPLACE INTO workout_zone_times (workout_id, power_secs, hr_secs)
		VALUES (?, ?, ?)`, workoutID, string(ps), string(hs))
	return err
}

// GetZoneTimes returns the stored zone seconds for a workout.
// Returns (nil, nil, nil) if no data exists yet.
func (db *DB) GetZoneTimes(workoutID string) (*[7]int, *[5]int, error) {
	var ps, hs string
	err := db.QueryRow(`SELECT power_secs, hr_secs FROM workout_zone_times WHERE workout_id = ?`, workoutID).Scan(&ps, &hs)
	if err == sql.ErrNoRows {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var power [7]int
	var hr [5]int
	json.Unmarshal([]byte(ps), &power) //nolint:errcheck
	json.Unmarshal([]byte(hs), &hr)    //nolint:errcheck
	return &power, &hr, nil
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
		SELECT id, filename, recorded_at, sport, duration_secs, elapsed_secs, distance_meters,
		       elevation_gain_meters, avg_power_watts, max_power_watts, normalized_power,
		       avg_heart_rate, max_heart_rate, avg_cadence, avg_speed_mps,
		       tss, intensity_factor, is_indoor, route_id, created_at
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

// ── Training load ─────────────────────────────────────────────────────────────

// GetFitnessOnDate returns the Fitness/Fatigue/Form values as of a specific date,
// looking back 180 days to build an accurate exponential moving average.
func (db *DB) GetFitnessOnDate(date time.Time) (models.FitnessPoint, error) {
	target := date.UTC().Truncate(24 * time.Hour)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	// How many days from target to today, so we can slice the shared history.
	daysAgo := max(int(today.Sub(target).Hours()/24), 0)
	points, err := db.getFitnessHistory(daysAgo, 0)
	if err != nil {
		return models.FitnessPoint{}, err
	}
	if len(points) == 0 {
		return models.FitnessPoint{}, nil
	}
	return points[0], nil
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

	rows, err := db.Query(`
		SELECT date(recorded_at) as day, COALESCE(SUM(tss), 0) as daily_tss
		FROM workouts
		WHERE recorded_at >= date('now', ? || ' days')
		  AND tss IS NOT NULL
		GROUP BY day
		ORDER BY day ASC`, fmt.Sprintf("-%d", totalDays))
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
	start := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -totalDays)
	return fitness.ComputeLoad(tssByDay, start, totalDays+1+projection, warmup), nil
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

// GetWeeklyBreakdown returns per-ISO-week training totals for the last n weeks.
// Weeks with no workouts are omitted. Results are ordered oldest first.
func (db *DB) GetWeeklyBreakdown(weeks int) ([]models.WeeklyLoad, error) {
	rows, err := db.Query(`
		SELECT
			strftime('%G-W%V', recorded_at)  AS week,
			COALESCE(SUM(tss), 0)            AS total_tss,
			SUM(duration_secs)               AS total_duration_secs,
			SUM(distance_meters)             AS total_distance_meters,
			SUM(elevation_gain_meters)       AS total_elevation_meters,
			COUNT(*)                         AS workout_count
		FROM workouts
		WHERE recorded_at >= date('now', ? || ' days')
		GROUP BY week
		ORDER BY week ASC`, fmt.Sprintf("-%d", weeks*7))
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var result []models.WeeklyLoad
	for rows.Next() {
		var wl models.WeeklyLoad
		if err := rows.Scan(&wl.Week, &wl.TSS, &wl.DurationSecs, &wl.DistanceMeters, &wl.ElevationGainMeters, &wl.WorkoutCount); err != nil {
			return nil, err
		}
		wl.LoadType = fitness.ClassifyWeeklyLoad(wl.TSS)
		result = append(result, wl)
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
	var a NinetyDayAverages
	err := db.QueryRow(`
		SELECT
			AVG(NULLIF(normalized_power, 0)),
			AVG(NULLIF(avg_heart_rate, 0)),
			AVG(NULLIF(tss, 0)),
			AVG(NULLIF(intensity_factor, 0)),
			AVG(duration_secs)
		FROM workouts
		WHERE recorded_at >= date('now', '-90 days')
		  AND sport = ?
		  AND avg_power_watts IS NOT NULL`, sport).Scan(
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

// GetSyncOldest returns the configured "oldest" date (YYYY-MM-DD) for an integration's sync range.
// Returns "" if no range is set (meaning sync all time).
func (db *DB) GetSyncOldest(name string) (string, error) {
	var v string
	err := db.QueryRow("SELECT sync_oldest FROM integrations WHERE name = ?", name).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// SetSyncOldest stores the "oldest" date for an integration's sync range.
// Pass "" to clear (sync all time).
func (db *DB) SetSyncOldest(name, oldest string) error {
	_, err := db.Exec(`
		INSERT INTO integrations (name, token_json, sync_oldest) VALUES (?, '', ?)
		ON CONFLICT(name) DO UPDATE SET sync_oldest=excluded.sync_oldest`,
		name, oldest)
	return err
}

// ── Helpers ───────────────────────────────────────────────────────────────────

type scanner interface {
	Scan(dest ...any) error
}

func scanWorkout(s scanner) (models.Workout, error) {
	var w models.Workout
	var recordedAt, createdAt string
	err := s.Scan(
		&w.ID, &w.Filename, &recordedAt, &w.Sport,
		&w.DurationSecs, &w.ElapsedSecs, &w.DistanceMeters, &w.ElevationGainMeters,
		&w.AvgPowerWatts, &w.MaxPowerWatts, &w.NormalizedPower,
		&w.AvgHeartRate, &w.MaxHeartRate, &w.AvgCadenceRPM, &w.AvgSpeedMPS,
		&w.TSS, &w.IntensityFactor, &w.IsIndoor, &w.RouteID, &createdAt,
	)
	if err != nil {
		return w, err
	}
	if w.RecordedAt, err = time.Parse(time.RFC3339, recordedAt); err != nil {
		return w, fmt.Errorf("parse recorded_at %q: %w", recordedAt, err)
	}
	if w.CreatedAt, err = time.Parse(time.RFC3339, createdAt); err != nil {
		return w, fmt.Errorf("parse created_at %q: %w", createdAt, err)
	}
	return w, nil
}
