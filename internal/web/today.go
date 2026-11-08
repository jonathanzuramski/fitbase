package web

import (
	"time"

	"github.com/fitbase/fitbase/internal/fitness"
	"github.com/fitbase/fitbase/internal/models"
)

// todayCard leads the Today page: what's planned for today and what has been
// ridden, beside readiness.
type todayCard struct {
	Date    time.Time
	Planned []models.PlannedWorkout
	Rides   []todayRide
}

// todayRide is a ride logged today, with what it was.
type todayRide struct {
	Workout  models.Workout
	Ride     rideSummary
	Feedback models.WorkoutFeedback // how it felt, or a nudge to say
}

// weekView is the "this week" strip: each day's planned load against what was
// ridden, and the week's totals.
type weekView struct {
	Days        [7]weekDay
	PlannedTSS  float64
	DoneTSS     float64
	PlannedSecs int
	DoneSecs    int
	// MaxTSS scales the day bars (the biggest planned or ridden day).
	MaxTSS float64
}

type weekDay struct {
	Label       string // Mon
	Date        time.Time
	IsToday     bool
	IsFuture    bool
	Planned     []models.PlannedWorkout
	PlannedTSS  float64
	PlannedSecs int
	DoneTSS     float64
	DoneSecs    int
	Rides       int
	RideID      string // the day's first ride, for the link
}

// DoneHeightPct and PlannedHeightPct size a day's bars against the week's
// biggest day.
func (d weekDay) DoneHeightPct(maxTSS float64) float64    { return pctOf(d.DoneTSS, maxTSS) }
func (d weekDay) PlannedHeightPct(maxTSS float64) float64 { return pctOf(d.PlannedTSS, maxTSS) }

func pctOf(v, max float64) float64 {
	if max <= 0 {
		return 0
	}
	return v / max * 100
}

// plannedTSS is a planned workout's TSS, estimated from its duration and IF
// when it has none of its own.
func plannedTSS(p models.PlannedWorkout) float64 {
	if p.TSS != nil {
		return *p.TSS
	}
	if p.IntensityFactor != nil {
		return fitness.EstimateTSS(p.DurationSecs, *p.IntensityFactor)
	}
	return 0
}

var weekdayLabels = [7]string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}

// buildWeek fills the week starting monday (in the viewer's timezone) from its
// planned workouts and rides; now marks today. Planned workouts are stored as
// UTC-midnight dates and rides by training day, so both key on the calendar
// date string.
func (th *templateHandler) buildWeek(monday, now time.Time) weekView {
	var wk weekView
	byDate := map[string]int{}
	for i := 0; i < 7; i++ {
		d := monday.AddDate(0, 0, i)
		key := d.Format("2006-01-02")
		byDate[key] = i
		wk.Days[i] = weekDay{
			Label:    weekdayLabels[i],
			Date:     d,
			IsToday:  key == now.Format("2006-01-02"),
			IsFuture: key > now.Format("2006-01-02"),
		}
	}
	first := time.Date(monday.Year(), monday.Month(), monday.Day(), 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 0, 6)

	// Degrade gracefully: a failed query leaves that half of the strip empty.
	if planned, err := th.db.ListPlannedWorkoutsBetween(first, last); err == nil {
		for _, p := range planned {
			i, ok := byDate[p.PlannedDate.Format("2006-01-02")]
			if !ok {
				continue
			}
			d := &wk.Days[i]
			d.Planned = append(d.Planned, p)
			d.PlannedTSS += plannedTSS(p)
			d.PlannedSecs += p.DurationSecs
		}
	}
	if rides, err := th.db.GetWorkoutsBetweenDays(first.Format("2006-01-02"), first.AddDate(0, 0, 7).Format("2006-01-02")); err == nil {
		for _, w := range rides {
			i, ok := byDate[w.TrainingDay]
			if !ok {
				continue
			}
			d := &wk.Days[i]
			if d.Rides == 0 {
				d.RideID = w.ID
			}
			d.Rides++
			d.DoneSecs += w.DurationSecs
			if w.TSS != nil {
				d.DoneTSS += *w.TSS
			}
		}
	}
	for _, d := range wk.Days {
		wk.PlannedTSS += d.PlannedTSS
		wk.PlannedSecs += d.PlannedSecs
		wk.DoneTSS += d.DoneTSS
		wk.DoneSecs += d.DoneSecs
		wk.MaxTSS = max(wk.MaxTSS, d.PlannedTSS, d.DoneTSS)
	}
	return wk
}

// buildToday gathers today's planned workouts and rides (with what each ride
// was) from the week already loaded.
func (th *templateHandler) buildToday(now time.Time, wk weekView) todayCard {
	card := todayCard{Date: now}
	for _, d := range wk.Days {
		if !d.IsToday {
			continue
		}
		card.Planned = d.Planned
		if d.Rides == 0 {
			break
		}
		day := d.Date.Format("2006-01-02")
		if rides, err := th.db.GetWorkoutsBetweenDays(day, d.Date.AddDate(0, 0, 1).Format("2006-01-02")); err == nil {
			for _, w := range rides {
				f, _, _ := th.db.GetWorkoutFeedback(w.ID)
				card.Rides = append(card.Rides, todayRide{Workout: w, Ride: th.rideSummaryFor(&w, nil), Feedback: f})
			}
		}
	}
	return card
}

// rideSummaryFor summarizes a stored ride against the FTP in effect on its
// day. Shapes are cached, so this is cheap after a ride's first view.
func (th *templateHandler) rideSummaryFor(w *models.Workout, planned []models.PlannedWorkout) rideSummary {
	if w.NormalizedPower == nil {
		return rideSummary{Planned: planned}
	}
	ftp := th.db.GetFTPForRide(w.RecordedAt, w.IsIndoor)
	shape, err := th.db.RideShape(w.ID, ftp)
	if err != nil {
		return rideSummary{Planned: planned}
	}
	return summarizeRide(shape, ftp, w.IntensityFactor, planned)
}
