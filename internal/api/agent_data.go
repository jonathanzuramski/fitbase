package api

import (
	"math"
	"time"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/fitness"
	"github.com/fitbase/fitbase/internal/models"
)

// Reusable data-shaping cores for the agent-oriented API. Each function below
// is the single source of truth for one curated shape; the HTTP handlers in
// handlers.go and the AI coach's tool executor (coach_tools.go) both call
// these so the REST surface and the coach can never drift apart.

// efficiencyFactor is normalized power ÷ average HR — aerobic efficiency, where
// a rising value at matched intensity signals fitness gains. Returns false when
// the inputs aren't all present. The single definition of the formula; callers
// round to whatever precision their output contract needs.
func efficiencyFactor(np *float64, avgHR *int) (float64, bool) {
	if np == nil || avgHR == nil || *avgHR <= 0 {
		return 0, false
	}
	return *np / float64(*avgHR), true
}

// variabilityIndex is normalized power ÷ average power — 1.00 is perfectly
// steady, >1.05 surgey. Returns false when inputs aren't all present.
func variabilityIndex(np, avgP *float64) (float64, bool) {
	if np == nil || avgP == nil || *avgP <= 0 {
		return 0, false
	}
	return *np / *avgP, true
}

// readinessReport computes today's readiness snapshot: current Fitness/Fatigue/
// Form, days since last workout, 28-day CTL ramp rate, and a recommendation.
// Backs GET /api/athlete/readiness and the coach's readiness tool.
func readinessReport(database *db.DB) (models.ReadinessReport, error) {
	today := time.Now().UTC()

	fp, err := database.GetFitnessOnDate(today)
	if err != nil {
		return models.ReadinessReport{}, err
	}

	lastDate, err := database.GetLastWorkoutDate()
	if err != nil {
		return models.ReadinessReport{}, err
	}
	daysSince := 0
	if lastDate != nil {
		daysSince = int(today.Sub(*lastDate).Hours() / 24)
	}

	// Ramp rate: change in fitness (CTL) over the last 28 days.
	var rampRate float64
	if history, histErr := database.GetFitnessHistory(42); histErr == nil && len(history) >= 29 {
		rampRate = math.Round((history[len(history)-1].Fitness-history[len(history)-29].Fitness)*10) / 10
	}

	rec, detail := readinessRecommendation(fp.Form, rampRate, daysSince)
	return models.ReadinessReport{
		Date:                 today.Format("2006-01-02"),
		Fitness:              math.Round(fp.Fitness*10) / 10,
		Fatigue:              math.Round(fp.Fatigue*10) / 10,
		Form:                 math.Round(fp.Form*10) / 10,
		DaysSinceLastWorkout: daysSince,
		RampRate:             rampRate,
		Recommendation:       rec,
		RecommendationDetail: detail,
	}, nil
}

func readinessRecommendation(form, rampRate float64, daysSince int) (string, string) {
	switch {
	case daysSince > 7:
		return "Resume Training", "More than a week without training — fitness is declining. Ease back in with a light ride."
	case rampRate > 10:
		return "Ease Up", "Training load has increased sharply over the last 4 weeks. Prioritise sleep and monitor fatigue."
	case form > 15:
		return "Race Ready", "You're very fresh. Consider a peak effort or race — extended rest risks fitness loss."
	case form > 5:
		return "Go Ride", "Good form. Ready for a quality session or race effort."
	case form >= -10:
		return "Maintain", "Normal training load. Keep building consistency."
	case form >= -25:
		return "Training Block", "Productive fatigue — you're adapting. A recovery day will pay off soon."
	case form >= -40:
		return "Ease Up", "Heavy fatigue. Schedule a recovery ride or rest day before your next hard effort."
	default:
		return "Rest", "Significant overreach — rest now to avoid illness or injury."
	}
}

// powerCurveReport returns all-time best power at the six standard durations
// with W/kg and %FTP. Backs GET /api/athlete/power-curve and the coach's
// power-curve tool.
func powerCurveReport(database *db.DB) (models.PowerCurveReport, error) {
	curve, err := database.GetAllTimePowerCurve()
	if err != nil {
		return models.PowerCurveReport{}, err
	}
	athlete, err := database.GetAthlete()
	if err != nil {
		return models.PowerCurveReport{}, err
	}

	displayDurations := []struct {
		secs  int
		label string
	}{
		{5, "5s"}, {30, "30s"}, {60, "1min"}, {300, "5min"}, {1200, "20min"}, {3600, "60min"},
	}

	entries := make([]models.PowerCurveEntry, 0, len(displayDurations))
	for _, d := range displayDurations {
		best, ok := curve[d.secs]
		if !ok {
			continue
		}
		entry := models.PowerCurveEntry{
			DurationSecs:  d.secs,
			DurationLabel: d.label,
			Watts:         best.Watts,
			WorkoutID:     best.WorkoutID,
		}
		if athlete.WeightKG > 0 {
			entry.WattsPerKG = math.Round(float64(best.Watts)/athlete.WeightKG*100) / 100
		}
		if athlete.FTPWatts > 0 {
			entry.PctFTP = math.Round(float64(best.Watts)/float64(athlete.FTPWatts)*1000) / 10
		}
		entries = append(entries, entry)
	}
	return models.PowerCurveReport{
		Entries:  entries,
		FTPWatts: athlete.FTPWatts,
		WeightKG: athlete.WeightKG,
	}, nil
}

// workoutAnalysis computes per-workout zone distribution, VI/EF, and 90-day
// context. Returns (nil, nil) when no workout has the given id. Backs
// GET /api/workouts/{id}/analysis and the coach's workout-detail tool.
func workoutAnalysis(database *db.DB, id string) (*models.WorkoutAnalysis, error) {
	workout, err := database.GetWorkout(id)
	if err != nil {
		return nil, err
	}
	if workout == nil {
		return nil, nil
	}

	athlete, err := database.GetAthlete()
	if err != nil {
		return nil, err
	}

	powerZoneDefs := fitness.PowerZones(athlete.FTPWatts)
	hrZoneDefs := fitness.ResolveHRZones(athlete)

	powerSecs, hrSecs, err := database.GetZoneTimes(id)
	if err != nil {
		return nil, err
	}

	analysis := &models.WorkoutAnalysis{
		WorkoutID:  id,
		PowerZones: []models.ZoneBreakdown{},
		HRZones:    []models.ZoneBreakdown{},
	}

	if powerSecs != nil {
		total := 0
		for _, s := range powerSecs {
			total += s
		}
		for i, z := range powerZoneDefs[:7] {
			pct := 0.0
			if total > 0 {
				pct = math.Round(float64(powerSecs[i])/float64(total)*1000) / 10
			}
			analysis.PowerZones = append(analysis.PowerZones, models.ZoneBreakdown{
				Label:     z.Label,
				Name:      z.Name,
				Seconds:   powerSecs[i],
				PctTime:   pct,
				WattsLow:  z.WattsLow,
				WattsHigh: z.WattsHigh,
			})
		}
	}

	if hrSecs != nil {
		total := 0
		for _, s := range hrSecs {
			total += s
		}
		for i, z := range hrZoneDefs {
			if i >= len(hrSecs) {
				break
			}
			pct := 0.0
			if total > 0 {
				pct = math.Round(float64(hrSecs[i])/float64(total)*1000) / 10
			}
			analysis.HRZones = append(analysis.HRZones, models.ZoneBreakdown{
				Label:   z.Label,
				Name:    z.Name,
				Seconds: hrSecs[i],
				PctTime: pct,
				BPMLow:  z.BPMLow,
				BPMHigh: z.BPMHigh,
			})
		}
	}

	if vi, ok := variabilityIndex(workout.NormalizedPower, workout.AvgPowerWatts); ok {
		r := math.Round(vi*1000) / 1000
		analysis.VariabilityIndex = &r
	}
	if ef, ok := efficiencyFactor(workout.NormalizedPower, workout.AvgHeartRate); ok {
		r := math.Round(ef*1000) / 1000
		analysis.EfficiencyFactor = &r
	}

	if avgs, err := database.Get90DayAverages(workout.Sport); err == nil && avgs != nil {
		analysis.AvgNP90Day = avgs.AvgNP
		analysis.AvgHR90Day = avgs.AvgHR
		analysis.AvgTSS90Day = avgs.AvgTSS
		analysis.AvgIF90Day = avgs.AvgIF
		analysis.AvgDuration90Day = avgs.AvgDurationSecs
	}

	return analysis, nil
}
