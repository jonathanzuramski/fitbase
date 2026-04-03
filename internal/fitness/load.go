package fitness

import (
	"math"
	"time"

	"github.com/fitbase/fitbase/internal/models"
)

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
