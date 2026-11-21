package db

import (
	"database/sql"

	"github.com/fitbase/fitbase/internal/fitness"
	"github.com/fitbase/fitbase/internal/models"
)

// Per-workout aerobic-decoupling cache (workout_decoupling, created by
// migration v4). A finished workout's streams never change, so the value is
// computed once — lazily, by the first coach call that needs it — and read
// from here ever after. A row with NULL decoupling_pct records "computed:
// not derivable from this ride's streams" so those rides aren't re-fetched
// either; no row means "not computed yet".

// GetWorkoutDecoupling returns the cached decoupling for a workout.
// found=false means nothing is cached; ok=false (with found=true) means the
// ride was already found not to support the calculation.
func (db *DB) GetWorkoutDecoupling(workoutID string) (val float64, ok, found bool, err error) {
	var v sql.NullFloat64
	err = db.QueryRow(`SELECT decoupling_pct FROM workout_decoupling WHERE workout_id = ?`, workoutID).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, false, false, nil
	}
	if err != nil {
		return 0, false, false, err
	}
	return v.Float64, v.Valid, true, nil
}

// SaveWorkoutDecoupling caches a computed decoupling value; pass nil to record
// that the workout's streams can't produce one.
func (db *DB) SaveWorkoutDecoupling(workoutID string, val *float64) error {
	_, err := db.Exec(`
		INSERT INTO workout_decoupling (workout_id, decoupling_pct) VALUES (?, ?)
		ON CONFLICT(workout_id) DO UPDATE SET decoupling_pct = excluded.decoupling_pct`,
		workoutID, val)
	return err
}

// AerobicDecoupling returns a workout's Pw:HR decoupling, from the cache or
// computed from its streams and cached. ok=false means the ride doesn't
// support it (no power and HR, too short). A stream-read failure isn't
// cached, so the next call retries.
func (db *DB) AerobicDecoupling(workoutID string) (float64, bool) {
	if val, ok, found, err := db.GetWorkoutDecoupling(workoutID); err == nil && found {
		return val, ok
	}
	streams, err := db.GetStreams(workoutID)
	if err != nil {
		return 0, false
	}
	dec, ok := fitness.AerobicDecoupling(streams)
	var cached *float64
	if ok {
		cached = &dec
	}
	_ = db.SaveWorkoutDecoupling(workoutID, cached) // best-effort
	return dec, ok
}

// DecouplingMinSecs is the shortest ride whose decoupling is cached ahead of
// time: shorter rides tell little about drift. Analytics reads only rides of
// 90 minutes or more, the coach an hour or more.
const DecouplingMinSecs = 60 * 60

// DecouplingCandidate reports whether a ride's decoupling is worth caching
// ahead of time: it has power and heart rate and is long enough to drift.
func DecouplingCandidate(w *models.Workout) bool {
	return w.AvgPowerWatts != nil && w.AvgHeartRate != nil && w.DurationSecs >= DecouplingMinSecs
}

// WorkoutIDsWithoutDecoupling returns the rides that are decoupling
// candidates (DecouplingCandidate) but have nothing cached yet, for the boot
// backfill. Aggregate views read decoupling only from the cache, never from
// streams, so this is what fills it for rides imported before it was cached
// at import.
func (db *DB) WorkoutIDsWithoutDecoupling() ([]string, error) {
	rows, err := db.Query(`
		SELECT w.id FROM workouts w
		WHERE w.avg_power_watts IS NOT NULL AND w.avg_heart_rate IS NOT NULL
		  AND w.duration_secs >= ?
		  AND NOT EXISTS (SELECT 1 FROM workout_decoupling d WHERE d.workout_id = w.id)
		ORDER BY w.recorded_at DESC`, DecouplingMinSecs)
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
