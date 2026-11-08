package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fitbase/fitbase/internal/aicoach"
	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/models"
	"github.com/fitbase/fitbase/internal/timeutil"
)

// indexView is the template data for the dashboard page.
type indexView struct {
	Workouts       []models.Workout
	Fitness        []models.FitnessPoint
	Imperial       bool
	Page           int
	TotalPages     int
	HasPrev        bool
	HasNext        bool
	PrevPage       int
	NextPage       int
	ShowPagination bool
	FTPIsDefault   bool
	FTPCheck       models.FTPCheck // banner when the FTP looks low or high
	Readiness      models.ReadinessReport
	FTPWatts       int
	Sort           string
	Dir            string
	TypeFilter     string

	GoalViews       []sportGoalView
	ActiveGoalSport string
	YearSummary     db.PeriodSummary
	Today           todayCard
	Week            weekView
	// RideKinds says what each listed ride was (kind and headline), by ID.
	RideKinds map[string]rideSummary

	AIConfigured    bool
	AIProviderLabel string
	AIChatCapable   bool
	AIChatLabels    string
}

// weekDayBar is one bar of a goal card's week chart.
type weekDayBar struct {
	Label     string
	HeightPct float64
	IsToday   bool
}

// sportGoalView is the per-sport panel of the mileage-goals card.
type sportGoalView struct {
	Sport            string
	Active           bool
	WeeklyGoalMeters float64
	YearlyGoalMeters float64
	WeekMeters       float64
	WeekPct          float64
	WeekDays         [7]weekDayBar
	YearMeters       float64
	YearPct          float64
	YearPacePct      float64 // 0-100: where the "today" tick sits on the year bar
	AheadOfPace      bool
	PaceDiffMeters   float64
}

var dayLabels = [7]string{"M", "T", "W", "T", "F", "S", "S"}

func (th *templateHandler) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	// First-run: redirect to welcome screen until setup is complete.
	athlete, err := th.db.GetAthlete()
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	if !athlete.SetupComplete {
		http.Redirect(w, r, "/welcome", http.StatusSeeOther)
		return
	}

	sortKey := r.URL.Query().Get("sort")
	switch sortKey {
	case "date", "sport", "duration", "distance", "power", "np", "tss", "hr", "elev":
	default:
		sortKey = ""
	}
	sortDir := r.URL.Query().Get("dir")
	if sortDir != "asc" && sortDir != "desc" {
		sortDir = "desc"
	}

	typeFilter := r.URL.Query().Get("type")
	if typeFilter != "outdoor" && typeFilter != "indoor" {
		typeFilter = ""
	}

	const pageSize = 20
	page := 1
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 1 {
		page = p
	}
	offset := (page - 1) * pageSize

	total, err := th.db.CountWorkoutsFiltered(typeFilter)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	totalPages := (total + pageSize - 1) / pageSize
	if totalPages < 1 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
		offset = (page - 1) * pageSize
	}

	workouts, err := th.db.ListWorkouts(pageSize, offset, sortKey, sortDir, typeFilter)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	// Distance fallback for existing DB rows
	for i := range workouts {
		if workouts[i].DistanceMeters == 0 && workouts[i].AvgSpeedMPS > 0 {
			workouts[i].DistanceMeters = workouts[i].AvgSpeedMPS * float64(workouts[i].DurationSecs)
		}
	}

	// Resolve the viewer's timezone (tz cookie → profile fallback); it anchors
	// "today", "this week", and the end of the fitness chart.
	tz := timeutil.ViewerLocation(r, th.db.AthleteLocation())

	// Get User Fitness for fitness chart (4 day projection).
	fitnessHistory, err := th.db.GetFitnessHistoryForChart(90, 4, tz)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	hasFTPHistory, err := th.db.HasFTPHistory()
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	// Non-fatal: without it the dashboard just shows no FTP banner.
	ftpCheck, _ := th.db.FTPCheck(time.Now())
	readiness, _ := th.db.Readiness(tz) // non-fatal: no badge

	activeSport := r.URL.Query().Get("goal_sport")
	switch activeSport {
	case "cycling", "running", "swimming":
	default:
		activeSport = "cycling"
	}

	now := time.Now().In(tz)
	todayIdx := timeutil.DaysSinceMonday(now) // Mon=0 … Sun=6
	weekStart := timeutil.MondayOf(now)
	yearStart := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, tz)

	// Sidebar widgets degrade gracefully — a failed query renders an empty
	// widget rather than failing the whole dashboard, hence the ignored errors.
	yearSummary, _ := th.db.GetPeriodSummary(yearStart)
	week := th.buildWeek(weekStart, now)
	today := th.buildToday(now, week)
	rideKinds := map[string]rideSummary{}
	for i := range workouts {
		rideKinds[workouts[i].ID] = th.rideSummaryFor(&workouts[i], nil)
	}

	goalViews, err := th.buildGoalViews(now, tz, activeSport, todayIdx)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	aiSettings, _ := th.db.GetAISettings()

	renderTemplate(w, th.templates().index, "base", indexView{
		Workouts:       workouts,
		Fitness:        fitnessHistory,
		Imperial:       th.isImperial(),
		Page:           page,
		TotalPages:     totalPages,
		HasPrev:        page > 1,
		HasNext:        page < totalPages,
		PrevPage:       page - 1,
		NextPage:       page + 1,
		ShowPagination: totalPages > 1,
		FTPIsDefault:   !hasFTPHistory,
		FTPCheck:       ftpCheck,
		Readiness:      readiness,
		FTPWatts:       athlete.FTPWatts,
		Sort:           sortKey,
		Dir:            sortDir,
		TypeFilter:     typeFilter,

		GoalViews:       goalViews,
		ActiveGoalSport: activeSport,
		YearSummary:     yearSummary,
		Today:           today,
		Week:            week,
		RideKinds:       rideKinds,

		AIConfigured:    aiSettings.Provider != "" && aiSettings.APIKey != "",
		AIProviderLabel: aicoach.ProviderLabel(aiSettings.Provider),
		AIChatCapable:   aiSettings.Provider != "" && aiSettings.APIKey != "" && aicoach.SupportsChat(aiSettings.Provider),
		// Which providers could chat, for the "switch provider" hint — derived
		// from the registry so the copy never hardcodes a provider name.
		AIChatLabels: strings.Join(aicoach.ChatCapableLabels(), " or "),
	})
}

// buildGoalViews assembles the mileage-goals card: one panel per sport with
// weekly/yearly progress, the week's per-day bars, and year-pace status.
func (th *templateHandler) buildGoalViews(now time.Time, tz *time.Location, activeSport string, todayIdx int) ([]sportGoalView, error) {
	allGoals, err := th.db.GetAllMileageGoals()
	if err != nil {
		return nil, err
	}

	dayOfYear := now.YearDay()
	daysInYear := 365
	if now.Year()%4 == 0 && (now.Year()%100 != 0 || now.Year()%400 == 0) {
		daysInYear = 366
	}
	yearPacePct := float64(dayOfYear) / float64(daysInYear) * 100

	clamp := func(v float64) float64 {
		if v > 100 {
			return 100
		}
		return v
	}

	var views []sportGoalView
	for _, s := range []string{"cycling", "running", "swimming"} {
		g := allGoals[s]
		g.Sport = s
		prog, _ := th.db.GetMileageProgress(s, tz)

		maxDay := 0.0
		for _, d := range prog.WeekDayMeters {
			if d > maxDay {
				maxDay = d
			}
		}
		var days [7]weekDayBar
		for i := 0; i < 7; i++ {
			pct := 0.0
			if maxDay > 0 {
				pct = prog.WeekDayMeters[i] / maxDay * 100
			}
			days[i] = weekDayBar{Label: dayLabels[i], HeightPct: pct, IsToday: i == todayIdx}
		}

		weekPct := 0.0
		if g.WeeklyMeters > 0 {
			weekPct = clamp(prog.WeekMeters / g.WeeklyMeters * 100)
		}
		yearPct := 0.0
		if g.YearlyMeters > 0 {
			yearPct = clamp(prog.YearMeters / g.YearlyMeters * 100)
		}

		aheadOfPace := false
		paceDiff := 0.0
		if g.YearlyMeters > 0 {
			expected := g.YearlyMeters * float64(dayOfYear) / float64(daysInYear)
			diff := prog.YearMeters - expected
			if diff >= 0 {
				aheadOfPace = true
				paceDiff = diff
			} else {
				paceDiff = -diff
			}
		}

		views = append(views, sportGoalView{
			Sport:            s,
			Active:           s == activeSport,
			WeeklyGoalMeters: g.WeeklyMeters,
			YearlyGoalMeters: g.YearlyMeters,
			WeekMeters:       prog.WeekMeters,
			WeekPct:          weekPct,
			WeekDays:         days,
			YearMeters:       prog.YearMeters,
			YearPct:          yearPct,
			YearPacePct:      yearPacePct,
			AheadOfPace:      aheadOfPace,
			PaceDiffMeters:   paceDiff,
		})
	}
	return views, nil
}
