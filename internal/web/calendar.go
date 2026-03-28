package web

import (
	"fmt"
	"time"

	"github.com/fitbase/fitbase/internal/models"
)

// CalendarWorkout is a compact representation of a workout for the calendar grid.
type CalendarWorkout struct {
	ID           string
	Sport        string
	DurationSecs int
	DistanceM    float64
	TSS          *float64
}

// CalendarDay represents a single day cell in the calendar.
type CalendarDay struct {
	Date     time.Time
	InMonth  bool // false for padding days from adjacent months
	IsToday  bool
	Workouts []CalendarWorkout
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
func buildCalendarData(year int, month time.Month, workouts []models.Workout, tz *time.Location) CalendarData {
	now := time.Now().In(tz)
	todayY, todayM, todayD := now.Date()

	// Index workouts by day-of-month.
	byDay := map[int][]CalendarWorkout{}
	for _, w := range workouts {
		local := w.RecordedAt.In(tz)
		d := local.Day()
		byDay[d] = append(byDay[d], CalendarWorkout{
			ID:           w.ID,
			Sport:        w.Sport,
			DurationSecs: w.DurationSecs,
			DistanceM:    w.DistanceMeters,
			TSS:          w.TSS,
		})
	}

	// Find the Monday on or before the 1st of the month.
	first := time.Date(year, month, 1, 0, 0, 0, 0, tz)
	wd := first.Weekday()
	if wd == time.Sunday {
		wd = 7
	}
	gridStart := first.AddDate(0, 0, -(int(wd) - 1)) // back to Monday

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
				InMonth: inMonth,
				IsToday: cy == todayY && cm == todayM && cd == todayD,
			}
			if inMonth {
				day.Workouts = byDay[cd]
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
