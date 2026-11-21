package fitness

import (
	"math"
	"slices"
	"time"

	"github.com/fitbase/fitbase/internal/timeutil"
)

// The rides each aerobic marker is read from. Markers only compare across
// rides of a similar kind: EF from a sprint session against EF from a long
// easy ride says nothing about fitness.
const (
	// Steady rides: steady enough that their averages describe one
	// intensity, long enough for heart rate to settle. EF, HR at matched power
	// and power at matched HR all come from these.
	SteadyMinIF   = 0.55
	SteadyMaxIF   = 0.80
	SteadyMinSecs = 45 * 60

	// Long steady rides, where Pw:HR drift is meaningful.
	DecouplingMaxIF   = 0.80
	DecouplingMinSecs = 90 * 60

	// Matched markers compare rides near one power or one heart rate: within
	// 7.5% of the reference power (±14 W at 185 W) or 4% of the reference
	// heart rate (±5 bpm at 135).
	MatchPowerTolerance = 0.075
	MatchHRTolerance    = 0.04
)

// IsSteady reports whether a ride with intensity factor ifactor lasting secs
// counts as steady (see SteadyMinIF).
func IsSteady(ifactor float64, secs int) bool {
	return ifactor >= SteadyMinIF && ifactor <= SteadyMaxIF && secs >= SteadyMinSecs
}

// IsDecouplingRide reports whether a ride's decoupling describes durability:
// long and steady (see DecouplingMinSecs).
func IsDecouplingRide(ifactor float64, secs int) bool {
	return ifactor <= DecouplingMaxIF && secs >= DecouplingMinSecs
}

// IntensitySplit groups time in the seven power zones into easy (Z1–Z2),
// middle (Z3–Z4) and hard (Z5+), each as a percentage of the total, to one
// decimal. ok is false when there is no zone time at all.
func IntensitySplit(secs [7]int) (easy, middle, hard float64, ok bool) {
	var total int
	for _, s := range secs {
		total += s
	}
	if total <= 0 {
		return 0, 0, 0, false
	}
	pct := func(s int) float64 { return math.Round(float64(s)/float64(total)*1000) / 10 }
	return pct(secs[0] + secs[1]), pct(secs[2] + secs[3]), pct(secs[4] + secs[5] + secs[6]), true
}

// IFBandEdges split rides by intensity factor into five bands: under 0.65
// (recovery), 0.65–0.75 (endurance), 0.75–0.85 (tempo), 0.85–0.95 (threshold)
// and 0.95 or more (hard: races, intervals, short hard rides).
var IFBandEdges = [4]float64{0.65, 0.75, 0.85, 0.95}

// IFBand is the band (0–4) an intensity factor falls in.
func IFBand(ifactor float64) int {
	for i, edge := range IFBandEdges {
		if ifactor < edge {
			return i
		}
	}
	return len(IFBandEdges)
}

// MatchSample is one ride for a matched marker: X is what rides are matched
// on (power for HR at matched power), Y what is averaged (heart rate).
type MatchSample struct {
	X, Y float64
}

// MatchReference picks the value to match rides at: the median X of the
// samples, rounded to step (5 W, 1 bpm), so it sits where most riding is and
// both sides of a comparison have rides near it. ok is false with no samples.
func MatchReference(samples []MatchSample, step float64) (float64, bool) {
	if len(samples) == 0 {
		return 0, false
	}
	xs := make([]float64, len(samples))
	for i, s := range samples {
		xs[i] = s.X
	}
	slices.Sort(xs)
	mid := len(xs) / 2
	median := xs[mid]
	if len(xs)%2 == 0 {
		median = (xs[mid-1] + xs[mid]) / 2
	}
	return math.Round(median/step) * step, true
}

// MatchedMean averages Y over the samples whose X lies within tol (a
// fraction) of ref, and returns that mean, the mean X of the same samples
// (how close to ref they really were) and how many there were.
func MatchedMean(samples []MatchSample, ref, tol float64) (y, x float64, n int) {
	if ref <= 0 {
		return 0, 0, 0
	}
	for _, s := range samples {
		if math.Abs(s.X-ref) <= ref*tol {
			y += s.Y
			x += s.X
			n++
		}
	}
	if n == 0 {
		return 0, 0, 0
	}
	return y / float64(n), x / float64(n), n
}

// Streaks are runs of consecutive training days or weeks.
type Streaks struct {
	CurrentDays int
	LongestDays int
	LongestEnd  time.Time // last day of the longest run of days
	// Weeks with at least one workout, Monday to Sunday.
	CurrentWeeks int
	LongestWeeks int
}

// DayStreaks counts runs of consecutive days in days (calendar dates, any
// order, duplicates allowed). The current run ends today, or yesterday when
// today has nothing yet: a streak isn't broken before the day is over. Weeks
// are counted the same way, the current week counting once it has a workout.
func DayStreaks(days []time.Time, today time.Time) Streaks {
	var s Streaks
	if len(days) == 0 {
		return s
	}
	sorted := slices.Clone(days)
	slices.SortFunc(sorted, func(a, b time.Time) int { return a.Compare(b) })
	sorted = slices.CompactFunc(sorted, func(a, b time.Time) bool { return a.Equal(b) })

	run := 0
	var prev time.Time
	for i, d := range sorted {
		if i > 0 && d.Equal(prev.AddDate(0, 0, 1)) {
			run++
		} else {
			run = 1
		}
		if run >= s.LongestDays {
			s.LongestDays, s.LongestEnd = run, d
		}
		prev = d
	}
	last := sorted[len(sorted)-1]
	if last.Equal(today) || last.Equal(today.AddDate(0, 0, -1)) {
		s.CurrentDays = run
	}

	var weeks []time.Time
	for _, d := range sorted {
		m := timeutil.MondayOf(d)
		if len(weeks) == 0 || !weeks[len(weeks)-1].Equal(m) {
			weeks = append(weeks, m)
		}
	}
	run = 0
	for i, w := range weeks {
		if i > 0 && w.Equal(weeks[i-1].AddDate(0, 0, 7)) {
			run++
		} else {
			run = 1
		}
		s.LongestWeeks = max(s.LongestWeeks, run)
	}
	thisWeek := timeutil.MondayOf(today)
	if lastWeek := weeks[len(weeks)-1]; lastWeek.Equal(thisWeek) || lastWeek.Equal(thisWeek.AddDate(0, 0, -7)) {
		s.CurrentWeeks = run
	}
	return s
}
