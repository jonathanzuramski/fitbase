package models

import "time"

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
