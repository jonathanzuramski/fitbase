package db

import (
	"database/sql"
	"strings"
	"time"

	"github.com/fitbase/fitbase/internal/models"
)

// Ride feedback (workout_feedback, migration v11) is the one thing about a
// ride the rider types in, so it must outlive the ride's derived rows. The
// table has no foreign key to workouts on purpose: an archive rebuild
// (ResetDerivedData) deletes every workout and re-imports it under the same
// ID (the file's hash), and a cascade would wipe the ratings with it.
// Deleting a ride on purpose removes its feedback (DeleteWorkout).

// GetWorkoutFeedback returns the rider's feedback on a ride; found=false when
// there is none.
func (db *DB) GetWorkoutFeedback(workoutID string) (models.WorkoutFeedback, bool, error) {
	f := models.WorkoutFeedback{WorkoutID: workoutID}
	var rpe sql.NullInt64
	var updated string
	err := db.QueryRow(`SELECT rpe, note, updated_at FROM workout_feedback WHERE workout_id = ?`, workoutID).
		Scan(&rpe, &f.Note, &updated)
	if err == sql.ErrNoRows {
		return f, false, nil
	}
	if err != nil {
		return f, false, err
	}
	if rpe.Valid {
		v := int(rpe.Int64)
		f.RPE = &v
	}
	f.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return f, true, nil
}

// SaveWorkoutFeedback stores the rider's feedback on a ride, replacing what
// was there; with no rating and an empty note it removes the row. The caller
// validates the RPE range and the note length.
func (db *DB) SaveWorkoutFeedback(workoutID string, rpe *int, note string) error {
	note = strings.TrimSpace(note)
	if rpe == nil && note == "" {
		_, err := db.Exec(`DELETE FROM workout_feedback WHERE workout_id = ?`, workoutID)
		return err
	}
	_, err := db.Exec(`
		INSERT INTO workout_feedback (workout_id, rpe, note, updated_at)
		VALUES (?, ?, ?, strftime('%Y-%m-%dT%H:%M:%SZ','now'))
		ON CONFLICT(workout_id) DO UPDATE SET
			rpe = excluded.rpe, note = excluded.note, updated_at = excluded.updated_at`,
		workoutID, rpe, note)
	return err
}

// FeedbackForWorkouts returns the feedback on each of the given rides that has
// any, by workout ID.
func (db *DB) FeedbackForWorkouts(ids []string) (map[string]models.WorkoutFeedback, error) {
	out := map[string]models.WorkoutFeedback{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.Query(`SELECT workout_id, rpe, note, updated_at FROM workout_feedback
		WHERE workout_id IN (?`+strings.Repeat(",?", len(ids)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	for rows.Next() {
		var f models.WorkoutFeedback
		var rpe sql.NullInt64
		var updated string
		if err := rows.Scan(&f.WorkoutID, &rpe, &f.Note, &updated); err != nil {
			return nil, err
		}
		if rpe.Valid {
			v := int(rpe.Int64)
			f.RPE = &v
		}
		f.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
		out[f.WorkoutID] = f
	}
	return out, rows.Err()
}
