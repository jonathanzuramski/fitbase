package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/fitbase/fitbase/internal/models"
)

// querier abstracts *DB / *sql.Tx so the planned-workout insert core can run
// standalone or inside a transaction.
type querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

// createPlannedWorkout is the insert core shared by CreatePlannedWorkout and
// CommitPlannedDraft: it persists p on q, generating an id if one isn't
// supplied, and returns the stored row (with id and created_at populated).
// Intervals are serialized to JSON for storage.
func createPlannedWorkout(q querier, p models.PlannedWorkout) (models.PlannedWorkout, error) {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	if p.Sport == "" {
		p.Sport = "cycling"
	}
	if p.Source == "" {
		p.Source = "manual"
	}

	intervalsJSON := ""
	if len(p.Intervals) > 0 {
		js, err := json.Marshal(p.Intervals)
		if err != nil {
			return models.PlannedWorkout{}, fmt.Errorf("marshal intervals: %w", err)
		}
		intervalsJSON = string(js)
	}

	_, err := q.Exec(`
		INSERT INTO planned_workouts (
			id, planned_date, sport, title, description,
			duration_secs, tss, intensity_factor, intervals_json, source
		) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.PlannedDate.UTC().Format("2006-01-02"), p.Sport, p.Title, p.Description,
		p.DurationSecs, p.TSS, p.IntensityFactor, intervalsJSON, p.Source,
	)
	if err != nil {
		return models.PlannedWorkout{}, fmt.Errorf("insert planned workout: %w", err)
	}

	// Re-read so created_at is the server-assigned value.
	row := q.QueryRow(`
		SELECT id, planned_date, sport, title, description,
		       duration_secs, tss, intensity_factor, intervals_json, source, created_at
		FROM planned_workouts WHERE id = ?`, p.ID)
	saved, err := scanPlannedWorkout(row)
	if err == sql.ErrNoRows {
		return models.PlannedWorkout{}, fmt.Errorf("planned workout vanished after insert: %s", p.ID)
	}
	if err != nil {
		return models.PlannedWorkout{}, err
	}
	return saved, nil
}

// CreatePlannedWorkout persists p and returns the stored row.
func (db *DB) CreatePlannedWorkout(p models.PlannedWorkout) (models.PlannedWorkout, error) {
	return createPlannedWorkout(db, p)
}

// CommitPlannedDraft persists every workout in ps and removes the draft, all in
// one transaction: a mid-batch failure rolls everything back and leaves the
// draft intact, so a retry can never duplicate already-inserted workouts.
func (db *DB) CommitPlannedDraft(draftID string, ps []models.PlannedWorkout) ([]models.PlannedWorkout, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	// A new coach plan supersedes the old one: coach-sourced workouts already on
	// the dates this draft covers are replaced, so accepting a re-proposed week
	// (fixed titles, rescheduled sessions, …) can never duplicate entries.
	// Manual entries are the rider's own and are left untouched.
	seen := map[string]bool{}
	for _, p := range ps {
		date := p.PlannedDate.UTC().Format("2006-01-02")
		if seen[date] {
			continue
		}
		seen[date] = true
		if _, err := tx.Exec(
			`DELETE FROM planned_workouts WHERE source = 'coach' AND planned_date = ?`, date,
		); err != nil {
			return nil, err
		}
	}

	saved := make([]models.PlannedWorkout, 0, len(ps))
	for _, p := range ps {
		s, err := createPlannedWorkout(tx, p)
		if err != nil {
			return nil, err
		}
		saved = append(saved, s)
	}
	if _, err := tx.Exec(`DELETE FROM planned_workout_drafts WHERE id = ?`, draftID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return saved, nil
}

// GetPlannedWorkout returns the planned workout with the given id, or (nil, nil)
// if it doesn't exist.
func (db *DB) GetPlannedWorkout(id string) (*models.PlannedWorkout, error) {
	row := db.QueryRow(`
		SELECT id, planned_date, sport, title, description,
		       duration_secs, tss, intensity_factor, intervals_json, source, created_at
		FROM planned_workouts WHERE id = ?`, id)
	p, err := scanPlannedWorkout(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ListPlannedWorkoutsBetween returns planned workouts with planned_date in
// [start, end] inclusive, ordered by date ascending.
func (db *DB) ListPlannedWorkoutsBetween(start, end time.Time) ([]models.PlannedWorkout, error) {
	rows, err := db.Query(`
		SELECT id, planned_date, sport, title, description,
		       duration_secs, tss, intensity_factor, intervals_json, source, created_at
		FROM planned_workouts
		WHERE planned_date BETWEEN ? AND ?
		ORDER BY planned_date ASC, created_at ASC`,
		start.UTC().Format("2006-01-02"), end.UTC().Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	var out []models.PlannedWorkout
	for rows.Next() {
		p, err := scanPlannedWorkout(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePlannedWorkout removes the planned workout with the given id.
// Returns sql.ErrNoRows if nothing was deleted so callers can return 404.
func (db *DB) DeletePlannedWorkout(id string) error {
	res, err := db.Exec(`DELETE FROM planned_workouts WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SavePlannedDraft stores a proposed schedule and returns a generated id.
// The payload is opaque JSON — typically a {"workouts":[…]} object the coach
// tool built.
func (db *DB) SavePlannedDraft(payloadJSON string) (string, error) {
	// Only one draft is live at a time: a new proposal supersedes any earlier
	// undecided one, so the rider never sees two accept buttons for the same
	// week (accepting both used to double-book the calendar). This also sweeps
	// abandoned drafts (rider ignored the preview card, closed the tab, …).
	_, _ = db.Exec(`DELETE FROM planned_workout_drafts`)

	id := uuid.NewString()
	_, err := db.Exec(`
		INSERT INTO planned_workout_drafts (id, payload_json) VALUES (?, ?)
		ON CONFLICT(id) DO UPDATE SET payload_json = excluded.payload_json`,
		id, payloadJSON)
	if err != nil {
		return "", err
	}
	return id, nil
}

// GetPlannedDraft returns the payload JSON for a draft, or "" if absent.
func (db *DB) GetPlannedDraft(id string) (string, error) {
	var payload string
	err := db.QueryRow(`SELECT payload_json FROM planned_workout_drafts WHERE id = ?`, id).Scan(&payload)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return payload, err
}

// DeletePlannedDraft removes a draft (typically after commit or discard).
func (db *DB) DeletePlannedDraft(id string) error {
	_, err := db.Exec(`DELETE FROM planned_workout_drafts WHERE id = ?`, id)
	return err
}

// scanPlannedWorkout reads one row (from QueryRow or Rows). The intervals
// column is JSON; an empty string means "no structured intervals".
func scanPlannedWorkout(s interface{ Scan(...any) error }) (models.PlannedWorkout, error) {
	var p models.PlannedWorkout
	var dateStr, createdStr, intervalsJSON string
	err := s.Scan(
		&p.ID, &dateStr, &p.Sport, &p.Title, &p.Description,
		&p.DurationSecs, &p.TSS, &p.IntensityFactor, &intervalsJSON, &p.Source, &createdStr,
	)
	if err != nil {
		return p, err
	}
	// The modernc.org/sqlite driver returns DATE columns as RFC3339
	// ("2026-05-25T00:00:00Z"). Try the date-only layout first for cases
	// where the value was inserted by hand, then fall back. Returning the
	// error instead of swallowing it prevents a future format change from
	// silently leaving PlannedDate at the zero value (which then filters
	// the row out of every calendar view).
	if pd, err := parsePlannedDate(dateStr); err != nil {
		return p, fmt.Errorf("parse planned_date %q: %w", dateStr, err)
	} else {
		p.PlannedDate = pd
	}
	if t, err := time.Parse(time.RFC3339, createdStr); err == nil {
		p.CreatedAt = t
	}
	if intervalsJSON != "" {
		if err := json.Unmarshal([]byte(intervalsJSON), &p.Intervals); err != nil {
			return p, fmt.Errorf("decode intervals: %w", err)
		}
	}
	return p, nil
}

// parsePlannedDate accepts the formats SQLite + the driver might hand us back
// for a DATE column: bare "2006-01-02" or full RFC3339 with a zero-time
// component.
func parsePlannedDate(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02", time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("no layout matched")
}
