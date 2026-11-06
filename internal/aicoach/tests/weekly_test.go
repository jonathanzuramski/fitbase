package aicoach_test

import (
	"strings"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/aicoach"
)

// The window is the seven days starting tomorrow in the rider's own timezone:
// late on Sunday evening in Denver is already Monday in UTC, and planning from
// the UTC date would silently drop the rider's Monday.
func TestWeeklyPlanWindow_StartsTomorrowInRiderTimezone(t *testing.T) {
	denver, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	now := time.Date(2026, 11, 8, 22, 30, 0, 0, denver) // Sunday 22:30 MST = Monday 05:30 UTC
	start, end := aicoach.WeeklyPlanWindow(now)
	if got := start.Format("2006-01-02 Mon"); got != "2026-11-09 Mon" {
		t.Errorf("start = %s, want 2026-11-09 Mon", got)
	}
	if got := end.Format("2006-01-02 Mon"); got != "2026-11-15 Sun" {
		t.Errorf("end = %s, want 2026-11-15 Sun", got)
	}
}

func TestWeeklyPlanWindow_CrossesMonthEnd(t *testing.T) {
	start, end := aicoach.WeeklyPlanWindow(time.Date(2026, 12, 29, 9, 0, 0, 0, time.UTC))
	if start.Format("2006-01-02") != "2026-12-30" || end.Format("2006-01-02") != "2027-01-05" {
		t.Errorf("window = %s..%s, want 2026-12-30..2027-01-05", start.Format("2006-01-02"), end.Format("2006-01-02"))
	}
}

// The brief must pin the exact dates (propose_schedule rejects nothing in the
// future, so a wrong window would land silently) and must send the model to
// get_workout_intervals — grading sessions from averages is the failure mode
// this feature exists to avoid.
func TestWeeklyPlanBrief_PinsWindowAndIntervalGrading(t *testing.T) {
	start, end := aicoach.WeeklyPlanWindow(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	brief := aicoach.WeeklyPlanBrief(start, end)

	for _, want := range []string{
		"2026-11-06 to 2026-11-12",
		"Friday, 2026-11-06",
		aicoach.ToolGetWorkoutIntervals,
		aicoach.ToolProposeSchedule,
		aicoach.ToolListPlannedWorkouts,
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("brief is missing %q", want)
		}
	}
	if strings.Contains(brief, "%!") {
		t.Errorf("brief has a formatting error:\n%s", brief)
	}
}
