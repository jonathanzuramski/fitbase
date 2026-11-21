package fitness_test

import (
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/fitness"
)

func TestIntensitySplit(t *testing.T) {
	// 60 minutes easy, 30 tempo/threshold, 6 hard.
	easy, middle, hard, ok := fitness.IntensitySplit([7]int{1200, 2400, 1200, 600, 240, 120, 0})
	if !ok || easy != 62.5 || middle != 31.3 || hard != 6.3 {
		t.Errorf("IntensitySplit = %v/%v/%v ok=%v, want 62.5/31.3/6.3", easy, middle, hard, ok)
	}
	if _, _, _, ok := fitness.IntensitySplit([7]int{}); ok {
		t.Error("no zone time should report no split")
	}
}

func TestIFBand(t *testing.T) {
	cases := map[float64]int{0.5: 0, 0.65: 1, 0.74: 1, 0.75: 2, 0.849: 2, 0.85: 3, 0.95: 4, 1.1: 4}
	for v, want := range cases {
		if got := fitness.IFBand(v); got != want {
			t.Errorf("IFBand(%v) = %d, want %d", v, got, want)
		}
	}
}

// The reference sits at the median, and only rides near it are averaged:
// a 250 W ride says nothing about heart rate at 180 W.
func TestMatchedMean(t *testing.T) {
	samples := []fitness.MatchSample{
		{X: 175, Y: 140}, {X: 182, Y: 144}, {X: 186, Y: 146}, {X: 250, Y: 170}, {X: 120, Y: 118},
	}
	ref, ok := fitness.MatchReference(samples, 5)
	if !ok || ref != 180 {
		t.Fatalf("MatchReference = %v, %v; want 180 (median 182 to the nearest 5 W)", ref, ok)
	}
	y, x, n := fitness.MatchedMean(samples, ref, fitness.MatchPowerTolerance)
	if n != 3 || !approx(y, 430.0/3) || !approx(x, 181) {
		t.Errorf("MatchedMean = %v at %v from %d rides, want %v at 181 from 3", y, x, n, 430.0/3)
	}
	if _, _, n := fitness.MatchedMean(samples, 0, fitness.MatchPowerTolerance); n != 0 {
		t.Error("no reference should match nothing")
	}
	if _, ok := fitness.MatchReference(nil, 5); ok {
		t.Error("no samples should give no reference")
	}
}

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestDayStreaks(t *testing.T) {
	today := day(2026, 11, 21) // Saturday
	days := []time.Time{
		// a five-day run in October
		day(2026, 10, 1), day(2026, 10, 2), day(2026, 10, 3), day(2026, 10, 4), day(2026, 10, 5),
		// the current run: Thursday and Friday, nothing yet today
		day(2026, 11, 19), day(2026, 11, 20), day(2026, 11, 20), // twice on one day counts once
	}
	s := fitness.DayStreaks(days, today)
	if s.CurrentDays != 2 || s.LongestDays != 5 || !s.LongestEnd.Equal(day(2026, 10, 5)) {
		t.Errorf("days: current %d, longest %d ending %s; want 2, 5 ending 2026-10-05",
			s.CurrentDays, s.LongestDays, s.LongestEnd.Format("2006-01-02"))
	}
	// Weeks: Sep 28 and Oct 5 (Oct 5 is a Monday) run two; this week alone is one.
	if s.CurrentWeeks != 1 || s.LongestWeeks != 2 {
		t.Errorf("weeks: current %d, longest %d; want 1, 2", s.CurrentWeeks, s.LongestWeeks)
	}

	// Two days without a ride end the run.
	if s := fitness.DayStreaks([]time.Time{day(2026, 11, 18)}, today); s.CurrentDays != 0 || s.LongestDays != 1 {
		t.Errorf("a ride three days ago: current %d, longest %d; want 0, 1", s.CurrentDays, s.LongestDays)
	}
	if s := fitness.DayStreaks(nil, today); s != (fitness.Streaks{}) {
		t.Errorf("no rides: %+v, want nothing", s)
	}
}
