package web

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fitbase/fitbase/internal/aicoach"
	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/dropbox"
	"github.com/fitbase/fitbase/internal/fitness"
	"github.com/fitbase/fitbase/internal/intervals"
	"github.com/fitbase/fitbase/internal/models"
	"github.com/fitbase/fitbase/internal/timeutil"
)

type pageTemplates struct {
	index     *template.Template
	workout   *template.Template
	settings  *template.Template
	welcome   *template.Template
	calendar  *template.Template
	heatmap   *template.Template
	importing *template.Template
}

// fsys is used just so that updates in local dev are picked up
// and rendered without restarting the go binary every time.
func loadTemplatesFrom(fsys fs.FS) *pageTemplates {
	parse := func(files ...string) *template.Template {
		return template.Must(
			template.New("").Funcs(FuncMap).ParseFS(fsys, files...),
		)
	}
	return &pageTemplates{
		index:     parse("templates/base.html", "templates/index.html"),
		workout:   parse("templates/base.html", "templates/workout.html"),
		settings:  parse("templates/base.html", "templates/settings.html"),
		welcome:   parse("templates/welcome.html"),
		calendar:  parse("templates/base.html", "templates/calendar.html"),
		heatmap:   parse("templates/base.html", "templates/heatmap.html"),
		importing: parse("templates/importing.html"),
	}
}

// templateHandler serves server-rendered UI pages.
type templateHandler struct {
	tmpls *pageTemplates
	db    *db.DB
	dev   bool
	webFS fs.FS // only used when dev=true to re-parse templates per request
}

func (th *templateHandler) templates() *pageTemplates {
	if th.dev {
		return loadTemplatesFrom(th.webFS)
	}
	return th.tmpls
}

// NewTemplateHandler creates the http.Handler for all server-rendered pages.
func NewTemplateHandler(database *db.DB, dev bool, webFS fs.FS) http.Handler {
	th := &templateHandler{
		tmpls: loadTemplatesFrom(webFS),
		db:    database,
		dev:   dev,
		webFS: webFS,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", th.index)
	mux.HandleFunc("/workouts/{id}", th.workout)
	mux.HandleFunc("GET /settings", th.settings)
	mux.HandleFunc("POST /settings/units", th.setUnits)
	mux.HandleFunc("POST /settings/athlete", th.updateAthlete)
	mux.HandleFunc("POST /settings/hr-zones", th.saveHRZones)
	mux.HandleFunc("POST /settings/hr-zones/reset", th.resetHRZones)
	mux.HandleFunc("POST /settings/ftp-history", th.addFTPHistory)
	mux.HandleFunc("POST /settings/ftp-history/delete", th.deleteFTPHistory)
	mux.HandleFunc("POST /settings/integrations/dropbox/credentials", th.saveDropboxCredentials)
	mux.HandleFunc("POST /settings/integrations/intervals/credentials", th.saveIntervalsCredentials)
	mux.HandleFunc("POST /settings/integrations/gdrive/credentials", th.saveIntegrationCredentials("gdrive"))
	mux.HandleFunc("POST /goals/mileage", th.saveMileageGoal)
	mux.HandleFunc("GET /heatmap", th.heatmap)
	mux.HandleFunc("POST /settings/ai", th.saveAISettings)
	mux.HandleFunc("GET /calendar", th.calendar)
	mux.HandleFunc("GET /importing", th.importing)
	mux.HandleFunc("GET /welcome", th.welcomeGet)
	mux.HandleFunc("POST /welcome", th.welcomePost)
	mux.HandleFunc("GET /welcome/skip", th.welcomeSkip)
	return mux
}

// renderTemplate executes a template into a buffer first. On success the
// buffered HTML is written to w; on failure a 500 is returned so the client
// never sees a partial page with a 200 status.
func renderTemplate(w http.ResponseWriter, t *template.Template, name string, data any) {
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		slog.Error("render template", "name", name, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

// isImperial reads the units preference from the athlete profile.
// Defaults to imperial if the profile is unavailable.
func (th *templateHandler) isImperial() bool {
	a, err := th.db.GetAthlete()
	if err != nil || a == nil {
		return true
	}
	return a.Units != "metric"
}

func (th *templateHandler) setUnits(w http.ResponseWriter, r *http.Request) {
	units := r.FormValue("units")
	if units != "imperial" && units != "metric" {
		units = "imperial"
	}
	if err := th.db.UpdateAthleteUnits(units); err != nil {
		slog.Error("update units", "err", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	ref := r.Header.Get("Referer")
	if ref == "" {
		ref = "/"
	}
	http.Redirect(w, r, ref, http.StatusSeeOther)
}

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
	user_fitness, err := th.db.GetFitnessHistoryForChart(90, 4, tz)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	hasFTPHistory, err := th.db.HasFTPHistory()
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	// Load mileage goals and build pre-computed view models for all three sports.
	allGoals, err := th.db.GetAllMileageGoals()
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	type weekDayBar struct {
		Label     string
		HeightPct float64
		IsToday   bool
	}
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

	weekSummary, _ := th.db.GetPeriodSummary(weekStart)
	yearSummary, _ := th.db.GetPeriodSummary(yearStart)
	todayFitness, _ := th.db.GetFitnessOnDate(now, tz)

	// Streak widget: which days this week had any workout.
	activityDays, _ := th.db.GetWeekActivityDays(weekStart, tz)
	type streakDay struct {
		Label   string
		Active  bool
		IsToday bool
	}
	dayLabels := [7]string{"M", "T", "W", "T", "F", "S", "S"}
	var streakDays [7]streakDay
	activeCount := 0
	for i := 0; i < 7; i++ {
		streakDays[i] = streakDay{Label: dayLabels[i], Active: activityDays[i], IsToday: i == todayIdx}
		if activityDays[i] {
			activeCount++
		}
	}

	dayOfYear := now.YearDay()
	daysInYear := 365
	if now.Year()%4 == 0 && (now.Year()%100 != 0 || now.Year()%400 == 0) {
		daysInYear = 366
	}
	yearPacePct := float64(dayOfYear) / float64(daysInYear) * 100

	var goalViews []sportGoalView
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

		clamp := func(v float64) float64 {
			if v > 100 {
				return 100
			}
			return v
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

		goalViews = append(goalViews, sportGoalView{
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

	aiSettings, _ := th.db.GetAISettings()

	renderTemplate(w, th.templates().index, "base", map[string]any{
		"Workouts":        workouts,
		"Fitness":         user_fitness,
		"Imperial":        th.isImperial(),
		"Page":            page,
		"TotalPages":      totalPages,
		"HasPrev":         page > 1,
		"HasNext":         page < totalPages,
		"PrevPage":        page - 1,
		"NextPage":        page + 1,
		"ShowPagination":  totalPages > 1,
		"FTPIsDefault":    !hasFTPHistory,
		"FTPWatts":        athlete.FTPWatts,
		"Sort":            sortKey,
		"Dir":             sortDir,
		"TypeFilter":      typeFilter,
		"GoalViews":       goalViews,
		"ActiveGoalSport": activeSport,
		"WeekSummary":     weekSummary,
		"YearSummary":     yearSummary,
		"TodayFitness":    todayFitness,
		"StreakDays":      streakDays,
		"StreakActive":    activeCount,
		"AIConfigured":    aiSettings.Provider != "" && aiSettings.APIKey != "",
		"AIProviderLabel": aicoach.ProviderLabel(aiSettings.Provider),
		"AIChatCapable":   aiSettings.Provider != "" && aiSettings.APIKey != "" && aicoach.SupportsChat(aiSettings.Provider),
		// Which providers could chat, for the "switch provider" hint — derived
		// from the registry so the copy never hardcodes a provider name.
		"AIChatLabels": strings.Join(aicoach.ChatCapableLabels(), " or "),
	})
}

func (th *templateHandler) workout(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	workout, err := th.db.GetWorkout(id)
	if err != nil || workout == nil {
		http.NotFound(w, r)
		return
	}

	streams, err := th.db.GetStreams(id)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	// Distance fallback for existing DB rows (same logic as parser)
	if workout.DistanceMeters == 0 && workout.AvgSpeedMPS > 0 {
		workout.DistanceMeters = workout.AvgSpeedMPS * float64(workout.DurationSecs)
	}

	sm := fitness.ComputeStreamMetrics(streams)

	// Variability Index = NP / AvgPower — pass as float64 (0 = not available)
	var viF float64
	if workout.AvgPowerWatts != nil && workout.NormalizedPower != nil && *workout.AvgPowerWatts > 0 {
		viF = *workout.NormalizedPower / *workout.AvgPowerWatts
	}
	// Efficiency Factor = NP / AvgHR
	var efF float64
	if workout.NormalizedPower != nil && workout.AvgHeartRate != nil && *workout.AvgHeartRate > 0 {
		efF = *workout.NormalizedPower / float64(*workout.AvgHeartRate)
	}
	// eFTP — dereference to float64 so printf works in template
	var eftpF float64
	if sm.EFTP != nil {
		eftpF = *sm.EFTP
	}

	// Anchor on the ride's own timezone (RecordedAt was re-homed at scan time),
	// so "fitness on the day of this ride" means the ride's training day.
	user_fitness, _ := th.db.GetFitnessOnDate(workout.RecordedAt, workout.RecordedAt.Location())

	athlete, err := th.db.GetAthlete()
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	allTimeCurve, _ := th.db.GetAllTimePowerCurve()

	workoutCurve, _ := th.db.GetWorkoutPowerCurve(workout.ID)
	if len(workoutCurve) == 0 && workout.AvgPowerWatts != nil {
		// Fallback for workouts imported before power curves were stored.
		workoutCurve = fitness.ComputePowerCurve(streams)
	}

	// FTP at ride time — used for zone coloring and metric display.
	ftpAtTime := th.db.GetFTPAtDate(workout.RecordedAt)

	// Zone times — fetched from DB (computed at import); fall back to on-the-fly.
	powerZoneSecs, hrZoneSecs, _, _ := th.db.GetZoneTimes(workout.ID)
	if powerZoneSecs == nil || hrZoneSecs == nil {
		pz := fitness.PowerZones(ftpAtTime)[:7]
		hz := fitness.ResolveHRZones(athlete)
		ssLow, ssHigh := fitness.SweetSpotBand(ftpAtTime)
		pw, hr, _ := fitness.ComputeZoneTimes(streams, pz, hz, ssLow, ssHigh)
		powerZoneSecs, hrZoneSecs = &pw, &hr
	}

	hrZones := fitness.ResolveHRZones(athlete)

	// FTP detection: flag if eFTP from this workout exceeds current FTP.
	var ftpDetected bool
	var ftpOld int
	if eftpF > 0 && int(eftpF) > athlete.FTPWatts {
		ftpDetected = true
		ftpOld = athlete.FTPWatts
	}

	// Route history: load sibling workouts if this workout has a route.
	var routeHistory []models.Workout
	var routeName string
	if workout.RouteID != nil {
		routeName, routeHistory, _ = th.db.GetRouteHistory(*workout.RouteID)
	}

	renderTemplate(w, th.templates().workout, "base", map[string]any{
		"Workout":         workout,
		"Streams":         streams,
		"Imperial":        th.isImperial(),
		"WorkKJ":          sm.WorkKJ,
		"Calories":        sm.Calories,
		"EFTP":            eftpF,
		"EFTPRounded":     int(eftpF),
		"MaxCadence":      sm.MaxCadence,
		"MaxPower1s":      sm.MaxPower1s,
		"VI":              viF,
		"EF":              efF,
		"Fitness":         user_fitness,
		"FTP":             ftpAtTime,
		"ThresholdHR":     athlete.ThresholdHR,
		"AllTimeCurve":    allTimeCurve,
		"WorkoutCurve":    workoutCurve,
		"FTPDetected":     ftpDetected,
		"FTPOld":          ftpOld,
		"WeightKG":        athlete.WeightKG,
		"RouteHistory":    routeHistory,
		"RouteName":       routeName,
		"PowerZoneSecs":   powerZoneSecs,
		"HRZoneSecs":      hrZoneSecs,
		"PowerZoneRanges": fitness.PowerZoneRangeLabels(ftpAtTime),
		"HRZoneRanges":    fitness.HRZoneRangeLabels(hrZones, athlete.ThresholdHR),
	})
}

func (th *templateHandler) settings(w http.ResponseWriter, r *http.Request) {
	athlete, err := th.db.GetAthlete()
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	imperial := th.isImperial()
	weightDisplay := athlete.WeightKG
	if imperial {
		weightDisplay = athlete.WeightKG * 2.20462
	}

	dropboxToken, _ := th.db.GetIntegrationToken("dropbox")
	dropboxConnected := dropboxToken != ""
	dropboxFolder, _, _ := th.db.GetIntegrationCredentials("dropbox")

	intervalsAthleteID, _, _ := th.db.GetIntegrationCredentials("intervals")
	intervalsConnected := intervalsAthleteID != ""

	gdriveClientID, _, _ := th.db.GetIntegrationCredentials("gdrive")
	gdriveConfigured := gdriveClientID != ""

	ftpHistory, _ := th.db.AllFTPHistory()
	gdriveConnected := false
	if gdriveConfigured {
		if token, err := th.db.GetIntegrationToken("gdrive"); err == nil {
			gdriveConnected = token != ""
		}
	}

	aiSettings, _ := th.db.GetAISettings()

	renderTemplate(w, th.templates().settings, "base", map[string]any{
		"Athlete":          athlete,
		"WeightDisplay":    weightDisplay,
		"Imperial":         imperial,
		"PowerZones":       fitness.PowerZones(athlete.FTPWatts),
		"HRZones":          resolveHRZones(athlete),
		"HRZonesCustom":    athlete.HRZonesJSON != "",
		"DropboxConnected": dropboxConnected,
		"DropboxFolder":    dropboxFolder,
		"DropboxLongpoll": func() bool {
			v, _ := th.db.GetAutoSync("dropbox")
			return v
		}(),
		"IntervalsConnected": intervalsConnected,
		"IntervalsAthleteID": intervalsAthleteID,
		"IntervalsAutoSync": func() bool {
			v, _ := th.db.GetAutoSync("intervals")
			return v
		}(),
		"GDriveConfigured": gdriveConfigured,
		"GDriveConnected":  gdriveConnected,
		"GDriveClientID":   gdriveClientID,
		"FTPHistory":       ftpHistory,
		"Today":            time.Now().Format("2006-01-02"),
		"AIProvider":       aiSettings.Provider,
		"AIModel":          aiSettings.Model,
		"AIConfigured":     aiSettings.Provider != "" && aiSettings.APIKey != "",
		"AIProviders":      aicoach.AllInfo(),
	})
}

func (th *templateHandler) updateAthlete(w http.ResponseWriter, r *http.Request) {
	ftp, err := strconv.Atoi(r.FormValue("ftp_watts"))
	if err != nil || ftp <= 0 {
		http.Error(w, "invalid ftp_watts", http.StatusBadRequest)
		return
	}
	weightRaw, err := strconv.ParseFloat(r.FormValue("weight"), 64)
	if err != nil || weightRaw <= 0 {
		http.Error(w, "invalid weight", http.StatusBadRequest)
		return
	}
	weightKG := weightRaw
	if th.isImperial() {
		weightKG = weightRaw / 2.20462
	}
	thresholdHR, _ := strconv.Atoi(r.FormValue("threshold_hr"))
	maxHR, _ := strconv.Atoi(r.FormValue("max_hr"))
	if thresholdHR < 0 {
		thresholdHR = 0
	}
	if maxHR < 0 {
		maxHR = 0
	}

	// Read current profile to preserve fields not on this form (age, location, etc.)
	a, err := th.db.GetAthlete()
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	a.FTPWatts = ftp
	a.WeightKG = weightKG
	a.ThresholdHR = thresholdHR
	a.MaxHR = maxHR

	if err := th.db.UpdateAthlete(a); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// importing serves the standalone first-run import screen. The API router
// redirects all page requests here while a startup archive reimport is active;
// the page polls /api/import/status and returns to the app when it finishes.
func (th *templateHandler) importing(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, th.templates().importing, "importing", nil)
}

func (th *templateHandler) welcomeGet(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, th.templates().welcome, "welcome", map[string]any{})
}

func (th *templateHandler) welcomePost(w http.ResponseWriter, r *http.Request) {
	units := r.FormValue("units")
	if units != "imperial" && units != "metric" {
		units = "imperial"
	}

	weightRaw, err := strconv.ParseFloat(r.FormValue("weight"), 64)
	if err != nil || weightRaw < 0 {
		weightRaw = 0
	}
	weightKG := weightRaw
	if units == "imperial" && weightKG > 0 {
		weightKG = weightRaw / 2.20462
	}

	tz := r.FormValue("timezone")
	if _, err := time.LoadLocation(tz); err != nil {
		tz = "UTC"
	}

	lang := r.FormValue("language")
	if lang == "" {
		lang = "en"
	}

	age, _ := strconv.Atoi(r.FormValue("age"))
	restingHR, _ := strconv.Atoi(r.FormValue("resting_hr"))
	thresholdHR, _ := strconv.Atoi(r.FormValue("threshold_hr"))
	maxHR, _ := strconv.Atoi(r.FormValue("max_hr"))
	ftpWatts, _ := strconv.Atoi(r.FormValue("ftp_watts"))
	if ftpWatts <= 0 {
		ftpWatts = 0 // leave at DB default (250) if not provided
	}

	a := &models.Athlete{
		Age: age, Location: r.FormValue("location"),
		Language: lang, Timezone: tz, Units: units,
		WeightKG: weightKG, RestingHR: restingHR,
		ThresholdHR: thresholdHR, MaxHR: maxHR, FTPWatts: ftpWatts,
	}
	if err := th.db.SaveWelcomeProfile(a); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (th *templateHandler) welcomeSkip(w http.ResponseWriter, r *http.Request) {
	if err := th.db.MarkSetupComplete(); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// addFTPHistory inserts a manually-entered FTP change point. The user is
// expected to click "recompute TSS" afterward to apply the change to existing
// workouts — recompute is intentionally not auto-fired since it walks every
// power workout.
func (th *templateHandler) addFTPHistory(w http.ResponseWriter, r *http.Request) {
	ftp, err := strconv.Atoi(r.FormValue("ftp_watts"))
	if err != nil || ftp <= 0 || ftp > 600 {
		http.Error(w, "invalid ftp_watts", http.StatusBadRequest)
		return
	}
	dateStr := r.FormValue("effective_from")
	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		http.Error(w, "invalid effective_from (expect YYYY-MM-DD)", http.StatusBadRequest)
		return
	}
	if err := th.db.LogFTPChangeAt(ftp, t); err != nil {
		slog.Error("add ftp history", "err", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings#ftp-history", http.StatusSeeOther)
}

func (th *templateHandler) deleteFTPHistory(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := th.db.DeleteFTPHistoryEntry(id); err != nil {
		slog.Error("delete ftp history", "err", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings#ftp-history", http.StatusSeeOther)
}

func (th *templateHandler) saveMileageGoal(w http.ResponseWriter, r *http.Request) {
	sport := r.FormValue("sport")
	switch sport {
	case "cycling", "running", "swimming":
	default:
		http.Error(w, "invalid sport", http.StatusBadRequest)
		return
	}

	weeklyDisplay, err := strconv.ParseFloat(r.FormValue("weekly"), 64)
	if err != nil || weeklyDisplay < 0 {
		weeklyDisplay = 0
	}
	yearlyDisplay, err := strconv.ParseFloat(r.FormValue("yearly"), 64)
	if err != nil || yearlyDisplay < 0 {
		yearlyDisplay = 0
	}

	var weeklyMeters, yearlyMeters float64
	if th.isImperial() {
		weeklyMeters = weeklyDisplay * 1609.344
		yearlyMeters = yearlyDisplay * 1609.344
	} else {
		weeklyMeters = weeklyDisplay * 1000
		yearlyMeters = yearlyDisplay * 1000
	}

	if err := th.db.SaveMileageGoal(sport, weeklyMeters, yearlyMeters); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/?goal_sport="+sport, http.StatusSeeOther) // nosemgrep: go.lang.security.injection.open-redirect.open-redirect
}

func resolveHRZones(a *models.Athlete) []models.HRZone {
	return fitness.ResolveHRZones(a)
}

func (th *templateHandler) saveHRZones(w http.ResponseWriter, r *http.Request) {
	var maxBPMs [6]int
	// Z1–Z4 upper bounds; Z5 is always open-ended (Coggan 5-zone model)
	fields := []string{"z1_max", "z2_max", "z3_max", "z4_max"}
	for i, f := range fields {
		v, err := strconv.Atoi(r.FormValue(f))
		if err != nil || v <= 0 {
			http.Error(w, "invalid value for "+f, http.StatusBadRequest)
			return
		}
		maxBPMs[i] = v
	}
	// Validate zones are strictly ascending
	for i := 1; i < 4; i++ {
		if maxBPMs[i] <= maxBPMs[i-1] {
			http.Error(w, "zone boundaries must be strictly ascending", http.StatusBadRequest)
			return
		}
	}
	zonesJSON := fmt.Sprintf("[%d,%d,%d,%d,0,0]",
		maxBPMs[0], maxBPMs[1], maxBPMs[2], maxBPMs[3])
	if err := th.db.SetCustomHRZones(zonesJSON); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (th *templateHandler) resetHRZones(w http.ResponseWriter, r *http.Request) {
	if err := th.db.ClearCustomHRZones(); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// saveIntegrationCredentials returns a handler that saves the client ID and secret
// for the named integration and redirects back to settings.
func (th *templateHandler) saveIntegrationCredentials(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clientID := r.FormValue("client_id")
		clientSecret := r.FormValue("client_secret")
		if clientID == "" || clientSecret == "" {
			http.Error(w, "client_id and client_secret are required", http.StatusBadRequest)
			return
		}
		if err := th.db.SetIntegrationCredentials(name, clientID, clientSecret); err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
	}
}

// saveDropboxCredentials stores the Dropbox access token and folder path.
// It validates the token before saving.
func (th *templateHandler) saveDropboxCredentials(w http.ResponseWriter, r *http.Request) {
	accessToken := r.FormValue("access_token")
	folderPath := r.FormValue("folder_path")
	if accessToken == "" {
		http.Error(w, "access_token is required", http.StatusBadRequest)
		return
	}
	if folderPath == "" {
		folderPath = "/Apps/WahooFitness"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := dropbox.New(accessToken).ValidateToken(ctx); err != nil {
		http.Error(w, "invalid Dropbox token: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := th.db.SetIntegrationToken("dropbox", accessToken); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	if err := th.db.SetIntegrationCredentials("dropbox", folderPath, ""); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// saveIntervalsCredentials stores the intervals.icu athlete ID and API key.
// It validates the credentials before saving.
func (th *templateHandler) saveIntervalsCredentials(w http.ResponseWriter, r *http.Request) {
	athleteID := r.FormValue("athlete_id")
	apiKey := r.FormValue("api_key")
	if athleteID == "" || apiKey == "" {
		http.Error(w, "athlete_id and api_key are required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := intervals.New(athleteID, apiKey).ValidateCredentials(ctx); err != nil {
		http.Error(w, "invalid intervals.icu credentials: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := th.db.SetIntegrationCredentials("intervals", athleteID, apiKey); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (th *templateHandler) heatmap(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, th.templates().heatmap, "base", map[string]any{
		"Imperial": th.isImperial(),
	})
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

	cal := buildCalendarData(year, month, workouts, planned, tz)

	renderTemplate(w, th.templates().calendar, "base", map[string]any{
		"Imperial": th.isImperial(),
		"Calendar": cal,
		"FTP":      athlete.FTPWatts, // used by the plan modal's power-profile preview
	})
}

// saveAISettings stores the selected AI provider, model, and API key.
func (th *templateHandler) saveAISettings(w http.ResponseWriter, r *http.Request) {
	provider := r.FormValue("provider")
	model := r.FormValue("model")
	apiKey := r.FormValue("api_key")

	if _, ok := aicoach.Get(provider); !ok {
		http.Error(w, "invalid provider", http.StatusBadRequest)
		return
	}
	if model == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}
	// A blank key from the "update settings" form means "keep the existing key" —
	// the user is just changing the model. That only holds within one provider:
	// a key for provider A won't authenticate against provider B, so a provider
	// switch always requires a fresh key.
	if apiKey == "" {
		existing, err := th.db.GetAISettings()
		if err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		if existing.APIKey == "" {
			http.Error(w, "api_key is required", http.StatusBadRequest)
			return
		}
		if existing.Provider != provider {
			http.Error(w, "api_key is required when changing provider", http.StatusBadRequest)
			return
		}
		apiKey = existing.APIKey
	}

	if err := th.db.SaveAISettings(db.AISettings{
		Provider: provider,
		Model:    model,
		APIKey:   apiKey,
	}); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	// Clear any cached insights — they were generated with the previous config.
	_ = th.db.ClearCachedInsights()
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}
