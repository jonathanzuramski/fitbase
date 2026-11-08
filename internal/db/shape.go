package db

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/fitbase/fitbase/internal/fitness"
)

// Per-workout ride-shape cache (workout_shape, created by migration v8).
// Detecting a ride's efforts means fetching and resampling its full stream, so
// the result is computed once — lazily, by the first caller that needs it —
// and read from here afterwards. A row is only valid for the FTP and
// fitness.ShapeVersion it was computed with; anything else reads as a miss.

// GetWorkoutShape returns the cached shape of a workout computed against ftp
// by the current detector. found=false means no usable row: none yet, or one
// computed with a different FTP or detector version.
func (db *DB) GetWorkoutShape(workoutID string, ftp int) (shape fitness.RideShape, found bool, err error) {
	var js string
	err = db.QueryRow(`
		SELECT shape_json FROM workout_shape
		WHERE workout_id = ? AND ftp_watts = ? AND version = ?`,
		workoutID, ftp, fitness.ShapeVersion).Scan(&js)
	if err == sql.ErrNoRows {
		return fitness.RideShape{}, false, nil
	}
	if err != nil {
		return fitness.RideShape{}, false, err
	}
	if err := json.Unmarshal([]byte(js), &shape); err != nil {
		// A row this build can't read is as good as missing; the caller
		// recomputes and overwrites it.
		return fitness.RideShape{}, false, nil
	}
	return shape, true, nil
}

// SaveWorkoutShape caches a workout's shape as computed against ftp by the
// current detector, replacing any earlier row for that workout.
func (db *DB) SaveWorkoutShape(workoutID string, ftp int, shape fitness.RideShape) error {
	js, err := json.Marshal(shape)
	if err != nil {
		return fmt.Errorf("marshal ride shape: %w", err)
	}
	_, err = db.Exec(`
		INSERT INTO workout_shape (workout_id, ftp_watts, version, shape_json) VALUES (?, ?, ?, ?)
		ON CONFLICT(workout_id) DO UPDATE SET
			ftp_watts  = excluded.ftp_watts,
			version    = excluded.version,
			shape_json = excluded.shape_json`,
		workoutID, ftp, fitness.ShapeVersion, string(js))
	return err
}

// RideShape returns a workout's shape against ftp — from the workout_shape
// cache when a row for this FTP and detector version exists, otherwise
// computed from the streams and cached. A stream-read failure is returned
// without caching so the next call retries.
func (db *DB) RideShape(workoutID string, ftp int) (fitness.RideShape, error) {
	if s, ok, err := db.GetWorkoutShape(workoutID, ftp); err == nil && ok {
		return s, nil
	}
	streams, err := db.GetStreams(workoutID)
	if err != nil {
		return fitness.RideShape{}, err
	}
	s := fitness.AnalyzeShape(streams, ftp)
	_ = db.SaveWorkoutShape(workoutID, ftp, s) // best-effort: a miss just recomputes
	return s, nil
}
