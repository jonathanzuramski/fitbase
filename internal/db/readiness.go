package db

import (
	"math"
	"time"

	"github.com/fitbase/fitbase/internal/fitness"
	"github.com/fitbase/fitbase/internal/models"
)

// Readiness is the rider's state going into today in tz: settled fitness,
// fatigue and form, days since the last ride, the 28-day ramp rate, and the
// recommendation (fitness.Recommend). Shared by GET /api/athlete/readiness,
// the coach's readiness tool and the server-rendered pages.
//
// "Today" is the last settled day (see GetFitnessOnDate): until a ride with
// TSS is logged today, form is yesterday's, the freshness carried into today.
func (db *DB) Readiness(tz *time.Location) (models.ReadinessReport, error) {
	today := time.Now().In(tz)

	fp, err := db.GetFitnessOnDate(today, tz)
	if err != nil {
		return models.ReadinessReport{}, err
	}
	asOf := ""
	if !fp.Date.IsZero() {
		asOf = fp.Date.Format("2006-01-02")
	}

	lastDate, err := db.GetLastWorkoutDate()
	if err != nil {
		return models.ReadinessReport{}, err
	}
	daysSince := 0
	if lastDate != nil {
		daysSince = int(today.Sub(*lastDate).Hours() / 24)
	}

	var rampRate float64
	if history, histErr := db.GetFitnessHistory(42, tz); histErr == nil {
		rampRate = fitness.RampRate(history, 28)
	}

	label, detail, tone := fitness.Recommend(fp.Form, rampRate, daysSince)
	return models.ReadinessReport{
		Date:                 today.Format("2006-01-02"),
		AsOf:                 asOf,
		Fitness:              math.Round(fp.Fitness*10) / 10,
		Fatigue:              math.Round(fp.Fatigue*10) / 10,
		Form:                 math.Round(fp.Form*10) / 10,
		DaysSinceLastWorkout: daysSince,
		RampRate:             rampRate,
		Recommendation:       label,
		RecommendationDetail: detail,
		RecommendationTone:   tone,
	}, nil
}
