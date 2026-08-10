package db

import "database/sql"

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
