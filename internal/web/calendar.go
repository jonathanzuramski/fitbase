package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fitbase/fitbase/internal/aicoach"
	"github.com/fitbase/fitbase/internal/models"
	"github.com/fitbase/fitbase/internal/timeutil"
)

// calendarView is the template data for the calendar page.
type calendarView struct {
	Imperial bool
	Calendar CalendarData
	FTP      int // used by the plan modal's power-profile preview

	// The weekly-plan panel needs a provider that can run tools; AIChatLabels
	// names the ones that can, for the "switch provider" hint.
	AIConfigured  bool
	AIChatCapable bool
	AIChatLabels  string
}

func (th *templateHandler) calendar(w http.ResponseWriter, r *http.Request) {
	athlete, err := th.db.GetAthlete()
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	// The calendar's "today" highlight and default month follow the viewer.
	tz := timeutil.ViewerLocation(r, th.db.AthleteLocation())

	now := time.Now().In(tz)
	year, month := now.Year(), now.Month()
	if y, err := strconv.Atoi(r.URL.Query().Get("year")); err == nil && y >= 2000 && y <= 2100 {
		year = y
	}
	if m, err := strconv.Atoi(r.URL.Query().Get("month")); err == nil && m >= 1 && m <= 12 {
		month = time.Month(m)
	}

	workouts, err := th.db.GetWorkoutsForMonth(year, month)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	first := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1)
	planned, err := th.db.ListPlannedWorkoutsBetween(first, last)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	// Non-fatal: without settings the panel just shows the setup hint.
	aiSettings, _ := th.db.GetAISettings()
	aiConfigured := aiSettings.Provider != "" && aiSettings.APIKey != ""

	renderTemplate(w, th.templates().calendar, "base", calendarView{
		Imperial:      th.isImperial(),
		Calendar:      buildCalendarData(year, month, workouts, planned, tz),
		FTP:           athlete.FTPWatts,
		AIConfigured:  aiConfigured,
		AIChatCapable: aiConfigured && aicoach.SupportsChat(aiSettings.Provider),
		AIChatLabels:  strings.Join(aicoach.ChatCapableLabels(), " or "),
	})
}

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

	// The phone day list labels every week's summary row with these
	// ("Week 46 · Nov 9 – 15"), so each week is visibly separated even when
	// nothing was ridden in it.
	WeekNum    int    // ISO week number of the week's Monday
	RangeLabel string // "Nov 9 – 15", or "Oct 26 – Nov 1" across months
	IsCurrent  bool   // the week containing today
	// Planned volume (in-month planned workouts) — shown for weeks with no
	// completed rides yet, so an upcoming week still says how big it is.
	PlannedDurSecs int
	PlannedTSS     float64
}

// weekRangeLabel formats a Monday–Sunday span compactly: "Nov 9 – 15" within
// one month, "Oct 26 – Nov 1" across two.
func weekRangeLabel(monday time.Time) string {
	sunday := monday.AddDate(0, 0, 6)
	if monday.Month() == sunday.Month() {
		return fmt.Sprintf("%s %d – %d", monday.Format("Jan"), monday.Day(), sunday.Day())
	}
	return fmt.Sprintf("%s %d – %s %d", monday.Format("Jan"), monday.Day(), sunday.Format("Jan"), sunday.Day())
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
		_, week.WeekNum = cursor.ISOWeek() // cursor is this week's Monday
		week.RangeLabel = weekRangeLabel(cursor)
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
			for _, cp := range day.Planned {
				week.PlannedDurSecs += cp.DurationSecs
				if cp.TSS != nil {
					week.PlannedTSS += *cp.TSS
				}
			}
			if day.IsToday {
				week.IsCurrent = true
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
