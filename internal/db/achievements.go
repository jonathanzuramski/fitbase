package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/fitbase/fitbase/internal/models"
)

// ── Achievements ──────────────────────────────────────────────────────────────
//
// Per-workout personal-best trophies, computed at import against every
// strictly earlier workout and stored permanently. A trophy of rank r means
// the ride beat all but r-1 earlier efforts in its category at the time it
// happened — later rides never demote it, matching how riders think about
// "that was a PR when I set it". The one exception is out-of-order imports
// and deletions, which rewrite history itself: those recompute the affected
// later workouts (see RefreshAchievements / RecomputeAchievementsAfter).

// Achievement kinds. Power-duration kinds are "power_<secs>" via
// PowerAchievementKind.
const (
	AchievementRouteTime    = "route_time"       // fastest moving time on this route
	AchievementRoutePower   = "route_power"      // best average power on this route
	AchievementLongestRide  = "longest_distance" // longest activity for this sport
	AchievementMostClimbing = "most_climbing"    // most elevation gain for this sport
)

// achievementPowerDurations are the effort lengths that earn power trophies —
// a subset of the fitness package's standard power-curve durations.
var achievementPowerDurations = []int{5, 60, 300, 1200, 3600}

// PowerAchievementKind returns the achievement kind for a power-duration best.
func PowerAchievementKind(durationSecs int) string {
	return fmt.Sprintf("power_%d", durationSecs)
}

// achievementQueryer is satisfied by both *DB and *sql.Tx, so the same
// computation serves live imports and the v7 backfill migration.
type achievementQueryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
	Exec(query string, args ...any) (sql.Result, error)
}

// computeAchievements derives workoutID's trophies from scratch, replacing any
// stored ones. Ranking is against strictly earlier workouts only (recorded_at
// is stored as UTC RFC3339 text, so string comparison is chronological), and a
// trophy is only awarded when at least one strictly worse earlier effort
// exists — the first ride in a category sets the baseline, it doesn't medal.
func computeAchievements(q achievementQueryer, workoutID string) error {
	var recordedAt, sport string
	var routeID sql.NullString
	var avgPower sql.NullFloat64
	var durationSecs int
	var distance, elevation float64
	err := q.QueryRow(`
		SELECT recorded_at, sport, route_id, avg_power_watts,
		       duration_secs, distance_meters, elevation_gain_meters
		FROM workouts WHERE id = ?`, workoutID).
		Scan(&recordedAt, &sport, &routeID, &avgPower, &durationSecs, &distance, &elevation)
	if err == sql.ErrNoRows {
		return nil // deleted mid-flight — nothing to compute
	}
	if err != nil {
		return err
	}

	if _, err := q.Exec(`DELETE FROM workout_achievements WHERE workout_id = ?`, workoutID); err != nil {
		return fmt.Errorf("clear achievements: %w", err)
	}

	count := func(query string, args ...any) (int, error) {
		var n int
		err := q.QueryRow(query, args...).Scan(&n)
		return n, err
	}
	award := func(kind string, better, worse, maxRank int, value float64) error {
		rank := better + 1
		if rank > maxRank || worse == 0 {
			return nil
		}
		_, err := q.Exec(`
			INSERT INTO workout_achievements (workout_id, kind, rank, value)
			VALUES (?, ?, ?, ?)`, workoutID, kind, rank, value)
		return err
	}

	// Route PRs: fastest moving time and best average power among earlier
	// rides on the same matched route.
	if routeID.Valid && durationSecs > 0 {
		better, err := count(`
			SELECT COUNT(*) FROM workouts
			WHERE route_id = ? AND recorded_at < ? AND duration_secs > 0 AND duration_secs < ?`,
			routeID.String, recordedAt, durationSecs)
		if err != nil {
			return err
		}
		worse, err := count(`
			SELECT COUNT(*) FROM workouts
			WHERE route_id = ? AND recorded_at < ? AND duration_secs > ?`,
			routeID.String, recordedAt, durationSecs)
		if err != nil {
			return err
		}
		if err := award(AchievementRouteTime, better, worse, 3, float64(durationSecs)); err != nil {
			return err
		}
	}
	if routeID.Valid && avgPower.Valid {
		better, err := count(`
			SELECT COUNT(*) FROM workouts
			WHERE route_id = ? AND recorded_at < ? AND avg_power_watts > ?`,
			routeID.String, recordedAt, avgPower.Float64)
		if err != nil {
			return err
		}
		worse, err := count(`
			SELECT COUNT(*) FROM workouts
			WHERE route_id = ? AND recorded_at < ?
			  AND avg_power_watts IS NOT NULL AND avg_power_watts < ?`,
			routeID.String, recordedAt, avgPower.Float64)
		if err != nil {
			return err
		}
		if err := award(AchievementRoutePower, better, worse, 3, avgPower.Float64); err != nil {
			return err
		}
	}

	// Duration power bests, ranked within the same sport so a ride's watts
	// never compete with a run's.
	for _, dur := range achievementPowerDurations {
		var watts int
		err := q.QueryRow(`
			SELECT watts FROM workout_power_curve
			WHERE workout_id = ? AND duration_secs = ?`, workoutID, dur).Scan(&watts)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return err
		}
		better, err := count(`
			SELECT COUNT(*) FROM workout_power_curve c
			JOIN workouts w ON w.id = c.workout_id
			WHERE c.duration_secs = ? AND w.sport = ? AND w.recorded_at < ? AND c.watts > ?`,
			dur, sport, recordedAt, watts)
		if err != nil {
			return err
		}
		worse, err := count(`
			SELECT COUNT(*) FROM workout_power_curve c
			JOIN workouts w ON w.id = c.workout_id
			WHERE c.duration_secs = ? AND w.sport = ? AND w.recorded_at < ? AND c.watts < ?`,
			dur, sport, recordedAt, watts)
		if err != nil {
			return err
		}
		if err := award(PowerAchievementKind(dur), better, worse, 3, float64(watts)); err != nil {
			return err
		}
	}

	// All-time volume records (rank 1 only), per sport.
	if distance > 0 {
		better, err := count(`
			SELECT COUNT(*) FROM workouts
			WHERE sport = ? AND recorded_at < ? AND distance_meters > ?`,
			sport, recordedAt, distance)
		if err != nil {
			return err
		}
		worse, err := count(`
			SELECT COUNT(*) FROM workouts
			WHERE sport = ? AND recorded_at < ? AND distance_meters > 0 AND distance_meters < ?`,
			sport, recordedAt, distance)
		if err != nil {
			return err
		}
		if err := award(AchievementLongestRide, better, worse, 1, distance); err != nil {
			return err
		}
	}
	if elevation > 0 {
		better, err := count(`
			SELECT COUNT(*) FROM workouts
			WHERE sport = ? AND recorded_at < ? AND elevation_gain_meters > ?`,
			sport, recordedAt, elevation)
		if err != nil {
			return err
		}
		worse, err := count(`
			SELECT COUNT(*) FROM workouts
			WHERE sport = ? AND recorded_at < ? AND elevation_gain_meters > 0 AND elevation_gain_meters < ?`,
			sport, recordedAt, elevation)
		if err != nil {
			return err
		}
		if err := award(AchievementMostClimbing, better, worse, 1, elevation); err != nil {
			return err
		}
	}
	return nil
}

// RefreshAchievements computes a newly imported workout's trophies. In the
// normal case (the new workout is the latest) that's all it does; when a
// backfilled file lands out of chronological order it also recomputes every
// later workout, whose standings the new arrival may have changed.
func (db *DB) RefreshAchievements(workoutID string) error {
	if err := computeAchievements(db, workoutID); err != nil {
		return err
	}
	var recordedAt string
	switch err := db.QueryRow(`SELECT recorded_at FROM workouts WHERE id = ?`, workoutID).Scan(&recordedAt); {
	case err == sql.ErrNoRows:
		return nil
	case err != nil:
		return err
	}
	return db.recomputeAchievementsAfter(recordedAt)
}

// RecomputeAchievementsAfter recomputes trophies for every workout recorded
// after t. Used when a workout is deleted: removing a record holder rewrites
// history, so displaced later efforts get their trophies promoted.
func (db *DB) RecomputeAchievementsAfter(t time.Time) error {
	return db.recomputeAchievementsAfter(t.UTC().Format(time.RFC3339))
}

func (db *DB) recomputeAchievementsAfter(recordedAt string) error {
	rows, err := db.Query(`SELECT id FROM workouts WHERE recorded_at > ? ORDER BY recorded_at ASC, id ASC`, recordedAt)
	if err != nil {
		return err
	}
	defer rows.Close() //nolint:errcheck
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := computeAchievements(db, id); err != nil {
			return fmt.Errorf("recompute achievements for %s: %w", id, err)
		}
	}
	return nil
}

// GetWorkoutAchievements returns a workout's trophies, best rank first.
func (db *DB) GetWorkoutAchievements(workoutID string) ([]models.Achievement, error) {
	rows, err := db.Query(`
		SELECT workout_id, kind, rank, value FROM workout_achievements
		WHERE workout_id = ? ORDER BY rank ASC, kind ASC`, workoutID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var out []models.Achievement
	for rows.Next() {
		var a models.Achievement
		if err := rows.Scan(&a.WorkoutID, &a.Kind, &a.Rank, &a.Value); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAchievementCounts returns how many trophies each of the given workouts
// holds, omitting workouts with none. Used for trophy markers in list views.
func (db *DB) GetAchievementCounts(workoutIDs []string) (map[string]int, error) {
	counts := make(map[string]int)
	if len(workoutIDs) == 0 {
		return counts, nil
	}
	placeholders := strings.Repeat("?,", len(workoutIDs)-1) + "?"
	args := make([]any, len(workoutIDs))
	for i, id := range workoutIDs {
		args[i] = id
	}
	rows, err := db.Query(`
		SELECT workout_id, COUNT(*) FROM workout_achievements
		WHERE workout_id IN (`+placeholders+`)
		GROUP BY workout_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		counts[id] = n
	}
	return counts, rows.Err()
}
