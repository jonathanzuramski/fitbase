package fitness

import (
	"math"
	"time"

	"github.com/fitbase/fitbase/internal/models"
)

// RampRate is the change in CTL (fitness) over the trailing N days, rounded
// to 1 decimal. A +CTL ramp >8/week (≈ +32/4wk) is the conventional
// over-training warning line; <0 is detraining. Trailing projected points are
// ignored so an unlogged today doesn't register as a day of detraining.
// Returns 0 when the settled history is too short to span the window.
func RampRate(history []models.FitnessPoint, days int) float64 {
	history = Settled(history)
	if days <= 0 || len(history) < days+1 {
		return 0
	}
	delta := history[len(history)-1].Fitness - history[len(history)-1-days].Fitness
	return math.Round(delta*10) / 10
}

// ClassifyWeeklyLoad returns a Coggan load category label for a week's total TSS.
func ClassifyWeeklyLoad(tss float64) string {
	switch {
	case tss < 150:
		return "Low"
	case tss < 300:
		return "Medium"
	case tss <= 450:
		return "High"
	default:
		return "Very High"
	}
}

// Settled returns history with its trailing projected points removed.
//
// A projected point is a day whose load isn't final: the forecast days after
// today, and today itself until a workout with TSS has been logged for it.
// Anything that reports "current" fitness should read the last settled point
// rather than the last point — an unlogged today is zero-TSS decay, and since
// ATL decays ~6x faster than CTL that decay inflates form by several points.
func Settled(history []models.FitnessPoint) []models.FitnessPoint {
	end := len(history)
	for end > 0 && history[end-1].IsProjection {
		end--
	}
	return history[:end]
}

// Current returns the most recent settled point — the state the rider carries
// into today — and false when the history has no settled point at all.
func Current(history []models.FitnessPoint) (models.FitnessPoint, bool) {
	settled := Settled(history)
	if len(settled) == 0 {
		return models.FitnessPoint{}, false
	}
	return settled[len(settled)-1], true
}

// ComputeLoad computes daily Fitness/Fatigue/Form from a map of daily TSS values using
// exponential moving averages. It iterates totalDays starting from start, but
// only appends points for days at index >= skip (the warmup period).
func ComputeLoad(tssByDay map[string]float64, start time.Time, totalDays int, skip int) []models.FitnessPoint {
	fitnessDecay := 1.0 - math.Exp(-1.0/42.0)
	fatigueDecay := 1.0 - math.Exp(-1.0/7.0)
	var fitness, fatigue float64
	var points []models.FitnessPoint

	for i := 0; i < totalDays; i++ {
		d := start.AddDate(0, 0, i)
		tss := tssByDay[d.Format("2006-01-02")]

		fitness = fitness + fitnessDecay*(tss-fitness)
		fatigue = fatigue + fatigueDecay*(tss-fatigue)

		if i >= skip {
			points = append(points, models.FitnessPoint{
				Date:    d,
				Fitness: fitness,
				Fatigue: fatigue,
				Form:    fitness - fatigue,
			})
		}
	}
	return points
}
