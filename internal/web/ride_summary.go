package web

import (
	"fmt"
	"math"
	"time"

	"github.com/fitbase/fitbase/internal/fitness"
	"github.com/fitbase/fitbase/internal/models"
)

// rideSummary is the ride page's account of what the ride was: its kind, the
// one-line headline the coach also reads, the detected efforts grouped by set
// for the intervals table, and any workout planned for the ride's day.
type rideSummary struct {
	Kind      string // intervals, mixed, endurance, tempo, recovery; "" without power and FTP
	KindLabel string
	Headline  string
	Groups    []intervalGroup
	Planned   []models.PlannedWorkout
}

// intervalGroup is one block of the intervals table: a set ("Set 1 · 5×4:00 @
// 104% FTP, held power across reps") or the efforts outside any set.
type intervalGroup struct {
	Title string
	Rows  []intervalRow
}

// intervalRow is one detected effort. StartSecs/EndSecs are elapsed seconds
// from the ride's first record, which the page uses to select the effort on
// the charts.
type intervalRow struct {
	N          int
	StartSecs  int
	EndSecs    int
	Start      string // h:mm:ss
	Duration   string
	AvgWatts   int
	PctFTP     int
	NPWatts    int // 0 when the effort is too short for NP
	MaxWatts   int
	AvgHR      int // 0 without HR
	MaxHR      int
	AvgCadence int
	Recovery   string // "2:00 @ 121 W" to the next effort, "" after the last
}

// Ride kinds for steady rides, by intensity factor (Coggan's bands).
const (
	recoveryBelowIF  = 0.56
	enduranceBelowIF = 0.76
)

// mixedEffortShare is how much of a ride its efforts must fill before it
// counts as mixed riding. Below it, a couple of surges on an endurance ride
// don't change what the ride was; the headline still names them.
const mixedEffortShare = 0.10

// summarizeRide builds the summary from the ride's shape (against the FTP in
// effect on its day) and its intensity factor.
func summarizeRide(shape fitness.RideShape, ftp int, intensity *float64, planned []models.PlannedWorkout) rideSummary {
	s := rideSummary{Planned: planned}
	if !shape.HasPower || ftp <= 0 {
		return s
	}

	var effortSecs int
	for _, e := range shape.Efforts {
		effortSecs += e.DurationSecs
	}
	mixed := float64(effortSecs) >= mixedEffortShare*float64(shape.ElapsedSecs)

	switch {
	case len(shape.Sets) > 0:
		s.Kind, s.KindLabel = "intervals", "Intervals"
	case len(shape.Efforts) > 0 && mixed:
		s.Kind, s.KindLabel = "mixed", "Mixed"
	case intensity != nil && *intensity < recoveryBelowIF:
		s.Kind, s.KindLabel = "recovery", "Recovery"
	case intensity != nil && *intensity < enduranceBelowIF:
		s.Kind, s.KindLabel = "endurance", "Endurance"
	default:
		s.Kind, s.KindLabel = "tempo", "Tempo"
	}

	if len(shape.Efforts) == 0 {
		// The coach's "steady — no efforts at or above 85% FTP" reads as a
		// detector note; say what the ride was instead.
		if intensity != nil {
			s.Headline = fmt.Sprintf("Steady riding at %d%% of FTP, no efforts at or above %d%%.",
				int(math.Round(*intensity*100)), fitness.EffortStartPctFTP)
		}
		return s
	}
	s.Headline = fitness.ShapeHeadline(shape, ftp)
	if s.Kind != "intervals" && s.Kind != "mixed" && intensity != nil {
		s.Headline = fmt.Sprintf("Mostly steady at %d%% of FTP; %s", int(math.Round(*intensity*100)), s.Headline)
	}

	row := func(i int) intervalRow {
		e := shape.Efforts[i]
		r := intervalRow{
			N:          i + 1,
			StartSecs:  e.StartSecs,
			EndSecs:    e.StartSecs + e.DurationSecs,
			Start:      fitness.FormatClock(e.StartSecs),
			Duration:   fitness.FormatDur(e.DurationSecs),
			AvgWatts:   int(math.Round(e.AvgWatts)),
			PctFTP:     int(math.Round(e.AvgWatts / float64(ftp) * 100)),
			NPWatts:    int(math.Round(e.NPWatts)),
			MaxWatts:   e.MaxWatts,
			AvgHR:      int(math.Round(e.AvgHR)),
			MaxHR:      e.MaxHR,
			AvgCadence: int(math.Round(e.AvgCadence)),
		}
		if i < len(shape.Efforts)-1 && e.RecoverySecs > 0 {
			r.Recovery = fmt.Sprintf("%s @ %d W", fitness.FormatDur(e.RecoverySecs), int(math.Round(e.RecoveryAvgWatts)))
		}
		return r
	}

	inSet := make([]bool, len(shape.Efforts))
	for n, set := range shape.Sets {
		g := intervalGroup{Title: fmt.Sprintf("Set %d · %s", n+1,
			fitness.SetHeadline(shape.Efforts[set.First:set.First+set.Count], ftp))}
		for i := set.First; i < set.First+set.Count; i++ {
			inSet[i] = true
			g.Rows = append(g.Rows, row(i))
		}
		s.Groups = append(s.Groups, g)
	}
	other := intervalGroup{Title: "Efforts"}
	if len(shape.Sets) > 0 {
		other.Title = "Other efforts"
	}
	for i := range shape.Efforts {
		if !inSet[i] {
			other.Rows = append(other.Rows, row(i))
		}
	}
	if len(other.Rows) > 0 {
		s.Groups = append(s.Groups, other)
	}
	// Sets and loose efforts each read in ride order; sets come first.
	return s
}

// trainingDate is the ride's training day as a UTC midnight date, the form
// planned workouts are stored in.
func trainingDate(w *models.Workout) time.Time {
	if d, err := time.Parse("2006-01-02", w.TrainingDay); err == nil {
		return d
	}
	y, m, d := w.RecordedAt.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
