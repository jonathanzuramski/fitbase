package web

import (
	"fmt"
	"time"

	"github.com/fitbase/fitbase/internal/models"
	"github.com/fitbase/fitbase/internal/timeutil"
)

// CalendarWorkout is a compact representation of a workout for the calendar grid.
type CalendarWorkout struct {
	ID           string
	Sport        string
	DurationSecs int
	DistanceM    float64
	TSS          *float64
}

// CalendarPlanned is a compact representation of a planned workout for the
// calendar grid. Source is "manual" or "coach" so the UI can flag the
// provenance of the entry.
type CalendarPlanned struct {
	ID           string
	Sport        string
	Title        string
	DurationSecs int
	TSS          *float64
	Source       string
}

// CalendarDay represents a single day cell in the calendar.
type CalendarDay struct {
	Date     time.Time
	DateISO  string // YYYY-MM-DD — used by the quick-add modal as a data attribute
	InMonth  bool   // false for padding days from adjacent months
	IsToday  bool
	Workouts []CalendarWorkout
	Planned  []CalendarPlanned
}

// CalendarWeek represents one row of the calendar grid.
type CalendarWeek struct {
	Days         [7]CalendarDay
	TotalTSS     float64
	TotalDurSecs int
	TotalDistM   float64
}

// CalendarData is the full month view passed to the template.
type CalendarData struct {
	Year       int
	Month      time.Month
	MonthLabel string // "March 2026"
	Weeks      []CalendarWeek
	PrevYear   int
	PrevMonth  int
	NextYear   int
	NextMonth  int
}

// buildCalendarData organises workouts into a monthly calendar grid.
// Weeks start on Monday. Padding days from adjacent months are included
// with InMonth=false so the grid is always rectangular.
func buildCalendarData(year int, month time.Month, workouts []models.Workout, planned []models.PlannedWorkout, tz *time.Location) CalendarData {
	now := time.Now().In(tz)
	todayY, todayM, todayD := now.Date()

	// Index workouts by their ride-local calendar day (training_day) — the
	// day the athlete experienced, regardless of viewer or profile timezone.
	byDay := map[string][]CalendarWorkout{}
	for _, w := range workouts {
		day := w.TrainingDay
		if day == "" {
			day = w.RecordedAt.In(tz).Format("2006-01-02")
		}
		byDay[day] = append(byDay[day], CalendarWorkout{
			ID:           w.ID,
			Sport:        w.Sport,
			DurationSecs: w.DurationSecs,
			DistanceM:    w.DistanceMeters,
			TSS:          w.TSS,
		})
	}

	// Index planned workouts by day-of-month. planned_date is a DATE; no
	// timezone conversion needed — match it directly against the grid day.
	plannedByDay := map[int][]CalendarPlanned{}
	for _, p := range planned {
		if p.PlannedDate.Year() != year || p.PlannedDate.Month() != month {
			continue
		}
		plannedByDay[p.PlannedDate.Day()] = append(plannedByDay[p.PlannedDate.Day()], CalendarPlanned{
			ID:           p.ID,
			Sport:        p.Sport,
			Title:        p.Title,
			DurationSecs: p.DurationSecs,
			TSS:          p.TSS,
			Source:       p.Source,
		})
	}

	// Find the Monday on or before the 1st of the month.
	first := time.Date(year, month, 1, 0, 0, 0, 0, tz)
	gridStart := timeutil.MondayOf(first)

	// Build weeks until we pass the last day of the month.
	last := first.AddDate(0, 1, -1) // last day of month
	var weeks []CalendarWeek
	cursor := gridStart
	for cursor.Before(last) || cursor.Equal(last) || cursor.Weekday() != time.Monday {
		var week CalendarWeek
		for i := 0; i < 7; i++ {
			cy, cm, cd := cursor.Date()
			inMonth := cm == month && cy == year
			day := CalendarDay{
				Date:    cursor,
				DateISO: cursor.Format("2006-01-02"),
				InMonth: inMonth,
				IsToday: cy == todayY && cm == todayM && cd == todayD,
			}
			if inMonth {
				day.Workouts = byDay[day.DateISO]
				day.Planned = plannedByDay[cd]
			}
			// Accumulate weekly totals for all days (including padding).
			for _, cw := range day.Workouts {
				week.TotalDurSecs += cw.DurationSecs
				week.TotalDistM += cw.DistanceM
				if cw.TSS != nil {
					week.TotalTSS += *cw.TSS
				}
			}
			week.Days[i] = day
			cursor = cursor.AddDate(0, 0, 1)
		}
		weeks = append(weeks, week)
		// Stop once we've filled a full week past the last day.
		if cursor.Month() != month && cursor.Weekday() == time.Monday {
			break
		}
	}

	prev := time.Date(year, month-1, 1, 0, 0, 0, 0, tz)
	next := time.Date(year, month+1, 1, 0, 0, 0, 0, tz)

	return CalendarData{
		Year:       year,
		Month:      month,
		MonthLabel: fmt.Sprintf("%s %d", month.String(), year),
		Weeks:      weeks,
		PrevYear:   prev.Year(),
		PrevMonth:  int(prev.Month()),
		NextYear:   next.Year(),
		NextMonth:  int(next.Month()),
	}
}
