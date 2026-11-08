package models

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

// Weekly plan triggers and statuses.
const (
	WeeklyPlanManual    = "manual"    // the rider pressed "plan my week"
	WeeklyPlanScheduled = "scheduled" // the Sunday-night auto-plan

	WeeklyPlanReady     = "ready"     // generated, waiting for the rider
	WeeklyPlanAccepted  = "accepted"  // its workouts were added to the calendar
	WeeklyPlanDiscarded = "discarded" // the rider dismissed it
	WeeklyPlanFailed    = "failed"    // generation errored; Error says why
)

// WeeklyPlan is one coach-generated weekly review and proposed schedule.
//
// It owns its proposed workouts (WorkoutsJSON, the propose_schedule payload)
// rather than pointing at a planned_workout_drafts row: drafts are single-slot
// and swept by the next proposal anywhere in the app, and a plan generated on
// Sunday night must still be acceptable when the rider opens the calendar on
// Monday.
type WeeklyPlan struct {
	ID          string    `json:"id"`
	CreatedAt   time.Time `json:"created_at"`
	Trigger     string    `json:"trigger"`
	WindowStart string    `json:"window_start"` // YYYY-MM-DD, rider-local
	WindowEnd   string    `json:"window_end"`
	Status      string    `json:"status"`
	// Request is the rider's message the plan answered, including any
	// constraints or revision notes — replayed when the plan is revised.
	Request      string `json:"request"`
	ReviewMD     string `json:"review_md"`
	WorkoutsJSON string `json:"-"`
	Error        string `json:"error,omitempty"`
}

// WeekdayKeys are the day tokens WeeklyPlanPrefs.Days uses, Monday first (the
// order the calendar shows them).
var WeekdayKeys = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// WeekdayKey returns the WeekdayKeys token for a time.Weekday.
func WeekdayKey(wd time.Weekday) string {
	return WeekdayKeys[(int(wd)+6)%7]
}

// MaxWeeklyHours bounds the weekly time budget a rider can set.
const MaxWeeklyHours = 40

// WeeklyPlanPrefs is the rider's standing availability for weekly plans: the
// days they can train and, optionally, how much time per week. Set on the
// calendar page and used by every plan run — including the Sunday-night
// auto-plan, which has no one there to type constraints.
type WeeklyPlanPrefs struct {
	Days        []string `json:"days"`         // WeekdayKeys tokens; every day when unset
	WeeklyHours *float64 `json:"weekly_hours"` // nil = the coach chooses the volume
}

// DefaultWeeklyPlanPrefs is every day available, no time budget.
func DefaultWeeklyPlanPrefs() WeeklyPlanPrefs {
	return WeeklyPlanPrefs{Days: append([]string(nil), WeekdayKeys...)}
}

// Validate rejects an empty or unknown day list and an out-of-range budget.
func (p WeeklyPlanPrefs) Validate() error {
	if len(p.Days) == 0 {
		return errors.New("pick at least one training day")
	}
	seen := map[string]bool{}
	for _, d := range p.Days {
		if !slices.Contains(WeekdayKeys, d) {
			return fmt.Errorf("unknown day %q (use mon, tue, wed, thu, fri, sat, sun)", d)
		}
		if seen[d] {
			return fmt.Errorf("day %q listed twice", d)
		}
		seen[d] = true
	}
	if h := p.WeeklyHours; h != nil && (*h < 1 || *h > MaxWeeklyHours) {
		return fmt.Errorf("hours per week must be between 1 and %d", MaxWeeklyHours)
	}
	return nil
}

// Allows reports whether the rider trains on wd.
func (p WeeklyPlanPrefs) Allows(wd time.Weekday) bool {
	return slices.Contains(p.Days, WeekdayKey(wd))
}

// Normalized returns the days in calendar order (Monday first).
func (p WeeklyPlanPrefs) Normalized() WeeklyPlanPrefs {
	out := WeeklyPlanPrefs{WeeklyHours: p.WeeklyHours}
	for _, k := range WeekdayKeys {
		if slices.Contains(p.Days, k) {
			out.Days = append(out.Days, k)
		}
	}
	return out
}
