package web

import (
	"math"
	"net/http"

	"github.com/fitbase/fitbase/internal/fitness"
	"github.com/fitbase/fitbase/internal/models"
)

// workoutView is the template data for the workout detail page.
type workoutView struct {
	Workout     *models.Workout
	Streams     []models.Stream
	Imperial    bool
	WorkKJ      float64
	Calories    float64
	EFTP        float64
	EFTPRounded int
	// ShowEFTP: the ride's eFTP (95% of its best 20 minutes) only says
	// something about FTP when the ride was hard; from an easy ride it is
	// just a low number next to the real FTP.
	ShowEFTP    bool
	MaxCadence  *int
	MaxPower1s  *int
	VI          float64
	EF          float64
	Fitness     models.FitnessPoint
	FTP         int
	FTPIndoor   bool // FTP came from the indoor line (it differs from the general one)
	ThresholdHR int

	AllTimeCurve map[int]models.AllTimeBest
	WorkoutCurve map[int]int
	FTPDetected  bool
	FTPOld       int

	RouteHistory     []models.Workout
	RouteName        string
	RouteBestTimeID  string
	RouteBestPowerID string
	Achievements     []models.Achievement

	// Ride is what the ride was: kind, headline, intervals, planned workout.
	Ride rideSummary

	PowerZoneSecs   *[7]int
	HRZoneSecs      *[5]int
	PowerZoneRanges []string
	HRZoneRanges    []string
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
	fitnessOnDay, _ := th.db.GetFitnessOnDate(workout.RecordedAt, workout.RecordedAt.Location())

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
	ftpAtTime := th.db.GetFTPForRide(workout.RecordedAt, workout.IsIndoor)

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

	// FTP detection: flag a ride whose eFTP clearly beats the current FTP,
	// on the same bar as the dashboard's FTP check (a watt or two over is
	// noise, not a new FTP).
	var ftpDetected bool
	var ftpOld int
	if fitness.EstimateRaisesFTP(eftpF, athlete.FTPWatts) {
		ftpDetected = true
		ftpOld = athlete.FTPWatts
	}

	// Route history: load sibling workouts if this workout has a route.
	var routeHistory []models.Workout
	var routeName string
	if workout.RouteID != nil {
		routeName, routeHistory, _ = th.db.GetRouteHistory(*workout.RouteID)
	}

	// Current record holders across the route's whole history, for the trophy
	// markers in the table. (Unlike the badges — which are frozen at import
	// time — these always reflect the standings as of today.)
	var routeBestTimeID, routeBestPowerID string
	var bestTime int
	var bestPower float64
	for _, rw := range routeHistory {
		if rw.DurationSecs > 0 && (routeBestTimeID == "" || rw.DurationSecs < bestTime) {
			routeBestTimeID, bestTime = rw.ID, rw.DurationSecs
		}
		if rw.AvgPowerWatts != nil && (routeBestPowerID == "" || *rw.AvgPowerWatts > bestPower) {
			routeBestPowerID, bestPower = rw.ID, *rw.AvgPowerWatts
		}
	}

	achievements, _ := th.db.GetWorkoutAchievements(workout.ID)

	// Non-fatal: without them the page just has no header line or table.
	shape, _ := th.db.RideShape(workout.ID, ftpAtTime)
	day := trainingDate(workout)
	planned, _ := th.db.ListPlannedWorkoutsBetween(day, day)

	renderTemplate(w, th.templates().workout, "base", workoutView{
		Workout:     workout,
		Streams:     streams,
		Imperial:    th.isImperial(),
		WorkKJ:      sm.WorkKJ,
		Calories:    sm.Calories,
		EFTP:        eftpF,
		EFTPRounded: int(math.Round(eftpF)),
		ShowEFTP:    eftpF > 0 && eftpF >= 0.95*float64(athlete.FTPWatts),
		MaxCadence:  sm.MaxCadence,
		MaxPower1s:  sm.MaxPower1s,
		VI:          viF,
		EF:          efF,
		Fitness:     fitnessOnDay,
		FTP:         ftpAtTime,
		FTPIndoor:   workout.IsIndoor && ftpAtTime != th.db.GetFTPAtDate(workout.RecordedAt),
		ThresholdHR: athlete.ThresholdHR,

		AllTimeCurve: allTimeCurve,
		WorkoutCurve: workoutCurve,
		FTPDetected:  ftpDetected,
		FTPOld:       ftpOld,

		RouteHistory:     routeHistory,
		RouteName:        routeName,
		RouteBestTimeID:  routeBestTimeID,
		RouteBestPowerID: routeBestPowerID,
		Achievements:     achievements,

		Ride: summarizeRide(shape, ftpAtTime, workout.IntensityFactor, planned),

		PowerZoneSecs:   powerZoneSecs,
		HRZoneSecs:      hrZoneSecs,
		PowerZoneRanges: fitness.PowerZoneRangeLabels(ftpAtTime),
		HRZoneRanges:    fitness.HRZoneRangeLabels(hrZones, athlete.ThresholdHR),
	})
}
