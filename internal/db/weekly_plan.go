package db

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/fitbase/fitbase/internal/models"
)

// MetaWeeklyPlanAuto is "1" when the rider has turned on the Sunday-night
// auto-plan from the calendar page.
const MetaWeeklyPlanAuto = "weekly_plan_auto"

const weeklyPlanCols = `id, created_at, trigger, window_start, window_end, status,
	request, review_md, workouts_json, error`

func scanWeeklyPlan(s interface{ Scan(...any) error }) (models.WeeklyPlan, error) {
	var p models.WeeklyPlan
	var created string
	err := s.Scan(&p.ID, &created, &p.Trigger, &p.WindowStart, &p.WindowEnd, &p.Status,
		&p.Request, &p.ReviewMD, &p.WorkoutsJSON, &p.Error)
	if err != nil {
		return p, err
	}
	if p.CreatedAt, err = time.Parse(time.RFC3339, created); err != nil {
		return p, fmt.Errorf("parse weekly plan created_at %q: %w", created, err)
	}
	return p, nil
}

// SaveWeeklyPlan inserts a plan, assigning an id and created_at when unset,
// and returns the stored row.
func (db *DB) SaveWeeklyPlan(p models.WeeklyPlan) (models.WeeklyPlan, error) {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	_, err := db.Exec(`
		INSERT INTO weekly_plans (`+weeklyPlanCols+`)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.CreatedAt.UTC().Format(time.RFC3339), p.Trigger, p.WindowStart, p.WindowEnd,
		p.Status, p.Request, p.ReviewMD, p.WorkoutsJSON, p.Error)
	if err != nil {
		return models.WeeklyPlan{}, fmt.Errorf("insert weekly plan: %w", err)
	}
	return db.mustGetWeeklyPlan(p.ID)
}

func (db *DB) mustGetWeeklyPlan(id string) (models.WeeklyPlan, error) {
	p, err := db.GetWeeklyPlan(id)
	if err != nil {
		return models.WeeklyPlan{}, err
	}
	if p == nil {
		return models.WeeklyPlan{}, fmt.Errorf("weekly plan vanished after insert: %s", id)
	}
	return *p, nil
}

// GetWeeklyPlan returns the plan with the given id, or (nil, nil) if absent.
func (db *DB) GetWeeklyPlan(id string) (*models.WeeklyPlan, error) {
	p, err := scanWeeklyPlan(db.QueryRow(`SELECT `+weeklyPlanCols+` FROM weekly_plans WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// LatestWeeklyPlan returns the most recently generated plan that didn't fail,
// or (nil, nil) when there is none — what the calendar panel shows.
func (db *DB) LatestWeeklyPlan() (*models.WeeklyPlan, error) {
	p, err := scanWeeklyPlan(db.QueryRow(`
		SELECT `+weeklyPlanCols+` FROM weekly_plans
		WHERE status != ?
		ORDER BY created_at DESC, rowid DESC LIMIT 1`, models.WeeklyPlanFailed))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// SetWeeklyPlanStatus updates a plan's status. Returns sql.ErrNoRows when no
// plan has the id.
func (db *DB) SetWeeklyPlanStatus(id, status string) error {
	res, err := db.Exec(`UPDATE weekly_plans SET status = ? WHERE id = ?`, status, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// AcceptWeeklyPlan adds a plan's workouts to the calendar and marks it
// accepted, in one transaction — the same replace-coach-entries rule as a
// draft commit, so re-accepting a revised week never double-books.
func (db *DB) AcceptWeeklyPlan(id string, ps []models.PlannedWorkout) ([]models.PlannedWorkout, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	saved, err := replaceCoachWorkoutsTx(tx, ps)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE weekly_plans SET status = ? WHERE id = ?`, models.WeeklyPlanAccepted, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return saved, nil
}

// WeeklyPlanCovers reports whether a plan that didn't fail already covers the
// week starting monday (YYYY-MM-DD): its window starts on or after that day.
// The scheduler uses this to skip a week the rider already planned by hand.
func (db *DB) WeeklyPlanCovers(monday string) (bool, error) {
	var n int
	err := db.QueryRow(`
		SELECT COUNT(*) FROM weekly_plans
		WHERE status != ? AND window_start >= ?`, models.WeeklyPlanFailed, monday).Scan(&n)
	return n > 0, err
}

// ScheduledFailuresSince counts failed scheduled runs created at or after
// since and returns the newest one's time (zero when none), so the scheduler
// can back off instead of retrying an outage every tick.
func (db *DB) ScheduledFailuresSince(since time.Time) (int, time.Time, error) {
	var n int
	var last sql.NullString
	err := db.QueryRow(`
		SELECT COUNT(*), MAX(created_at) FROM weekly_plans
		WHERE trigger = ? AND status = ? AND created_at >= ?`,
		models.WeeklyPlanScheduled, models.WeeklyPlanFailed, since.UTC().Format(time.RFC3339)).Scan(&n, &last)
	if err != nil || !last.Valid {
		return n, time.Time{}, err
	}
	t, err := time.Parse(time.RFC3339, last.String)
	return n, t, err
}

// Weekly-plan preferences live in meta: the rider's training days and
// optional weekly time budget.
const (
	metaWeeklyPlanDays  = "weekly_plan_days"  // comma-separated mon..sun
	metaWeeklyPlanHours = "weekly_plan_hours" // decimal hours
)

// GetWeeklyPlanPrefs returns the rider's weekly-plan availability, defaulting
// to every day and no time budget.
func (db *DB) GetWeeklyPlanPrefs() (models.WeeklyPlanPrefs, error) {
	p := models.DefaultWeeklyPlanPrefs()
	days, err := db.GetMeta(metaWeeklyPlanDays)
	if err != nil {
		return p, err
	}
	if days != "" {
		p.Days = strings.Split(days, ",")
	}
	hours, err := db.GetMeta(metaWeeklyPlanHours)
	if err != nil {
		return p, err
	}
	if hours != "" {
		if h, err := strconv.ParseFloat(hours, 64); err == nil {
			p.WeeklyHours = &h
		}
	}
	return p, nil
}

// SaveWeeklyPlanPrefs validates and stores the rider's availability.
func (db *DB) SaveWeeklyPlanPrefs(p models.WeeklyPlanPrefs) error {
	if err := p.Validate(); err != nil {
		return err
	}
	p = p.Normalized()
	if err := db.SetMeta(metaWeeklyPlanDays, strings.Join(p.Days, ",")); err != nil {
		return err
	}
	if p.WeeklyHours == nil {
		return db.DeleteMeta(metaWeeklyPlanHours)
	}
	return db.SetMeta(metaWeeklyPlanHours, strconv.FormatFloat(*p.WeeklyHours, 'f', -1, 64))
}
