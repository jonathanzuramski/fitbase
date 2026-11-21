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
		daysSince = calendarDaysBetween(lastDate.In(tz), today)
	} else {
		// No rides at all: nothing is settled, whatever the load model says
		// for an empty history (pages hide readiness without an AsOf).
		asOf = ""
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

// calendarDaysBetween counts the calendar days from a's date to b's (both in
// their own location). Dividing the elapsed time by 24 hours called a ride at
// 1 PM yesterday "today" until 1 PM.
func calendarDaysBetween(a, b time.Time) int {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	// UTC midnights, so a DST change can't make a day 23 or 25 hours.
	from := time.Date(ay, am, ad, 0, 0, 0, 0, time.UTC)
	to := time.Date(by, bm, bd, 0, 0, 0, 0, time.UTC)
	return int(to.Sub(from).Hours() / 24)
}
