package fitness_test

import (
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/fitness"
	"github.com/fitbase/fitbase/internal/models"
)

// makeStreams builds a 1Hz stream from a slice of watts.
// Negative values produce a nil PowerWatts (gap in power data).
func makeStreams(watts []int) []models.Stream {
	base := time.Unix(1_000_000, 0).UTC()
	s := make([]models.Stream, len(watts))
	for i, w := range watts {
		s[i].Timestamp = base.Add(time.Duration(i) * time.Second)
		if w >= 0 {
			v := w
			s[i].PowerWatts = &v
		}
	}
	return s
}

// repeat returns n copies of v.
func repeat(v, n int) []int {
	s := make([]int, n)
	for i := range s {
		s[i] = v
	}
	return s
}

// concat joins slices.
func concat(slices ...[]int) []int {
	var out []int
	for _, s := range slices {
		out = append(out, s...)
	}
	return out
}

// ── Core invariants ──────────────────────────────────────────────────────────

// The 1-second best must equal the peak single-sample power, not the average
// of two adjacent samples. This catches the > vs >= shrink-condition bug.
func TestPowerCurve_1sPower_IsPeak(t *testing.T) {
	watts := concat(repeat(200, 30), []int{500}, repeat(200, 30))
	curve := fitness.ComputePowerCurve(makeStreams(watts))
	if curve[1] != 500 {
		t.Errorf("1s best: got %dW, want 500W (peak single sample)", curve[1])
	}
}

// Constant power ride: every standard duration should equal that constant.
func TestPowerCurve_ConstantPower(t *testing.T) {
	curve := fitness.ComputePowerCurve(makeStreams(repeat(300, 600)))
	for _, dur := range []int{1, 5, 10, 30, 60, 300} {
		if curve[dur] != 300 {
			t.Errorf("dur=%ds: got %dW, want 300W", dur, curve[dur])
		}
	}
}

// The best window can appear anywhere in the ride, not only at the start.
func TestPowerCurve_BestWindowPosition(t *testing.T) {
	watts := concat(repeat(200, 30), repeat(400, 30))
	curve := fitness.ComputePowerCurve(makeStreams(watts))
	if curve[30] != 400 {
		t.Errorf("30s best: got %dW, want 400W (second half)", curve[30])
	}
}

// 0W coasting must be included in the window average. A 2-minute window
// with 1 minute at 600W and 1 minute at 0W must average to 300W, not 600W.
// This catches the null-exclusion bug that caused 2min > 1min.
func TestPowerCurve_CoastingDilutesAverage(t *testing.T) {
	watts := concat(repeat(600, 60), repeat(0, 60))
	curve := fitness.ComputePowerCurve(makeStreams(watts))

	if curve[60] != 600 {
		t.Errorf("1min best: got %dW, want 600W", curve[60])
	}
	if curve[120] != 300 {
		t.Errorf("2min best: got %dW, want 300W (600W sprint + 60s coasting)", curve[120])
	}
}

// The power curve must be monotonically non-increasing: a longer effort can
// never have a higher average than a shorter one.
func TestPowerCurve_Monotonic(t *testing.T) {
	// 2-hour ride with varied power — many opportunities for inversions.
	watts := make([]int, 7200)
	for i := range watts {
		watts[i] = 150 + (i*37+i*i*13)%251
	}
	curve := fitness.ComputePowerCurve(makeStreams(watts))

	durations := []int{1, 5, 10, 30, 60, 120, 300, 600, 1200, 1800, 2700, 3600}
	for i := 0; i < len(durations)-1; i++ {
		d1, d2 := durations[i], durations[i+1]
		w1, ok1 := curve[d1]
		w2, ok2 := curve[d2]
		if ok1 && ok2 && w1 < w2 {
			t.Errorf("not monotonic: power[%ds]=%dW < power[%ds]=%dW", d1, w1, d2, w2)
		}
	}
}

// A ride of exactly N samples (1Hz) must produce a power curve entry for
// the N-second duration. This catches the rideSecs off-by-one guard bug.
func TestPowerCurve_ExactRideDurationIncluded(t *testing.T) {
	for _, dur := range []int{60, 120, 300} {
		curve := fitness.ComputePowerCurve(makeStreams(repeat(250, dur)))
		if _, ok := curve[dur]; !ok {
			t.Errorf("dur=%ds: ride with exactly %d samples should have a %ds curve entry", dur, dur, dur)
		}
		if curve[dur] != 250 {
			t.Errorf("dur=%ds: got %dW, want 250W", dur, curve[dur])
		}
	}
}

// Null power gaps (e.g. power meter dropout) must not inflate averages.
// The window average divides by total samples including the gap seconds.
func TestPowerCurve_NullGapCountsAsZero(t *testing.T) {
	// 10s: 5s at 400W, 5s with no power signal (nil), treated as 0W
	watts := concat(repeat(400, 5), repeat(-1, 5)) // -1 → nil PowerWatts
	curve := fitness.ComputePowerCurve(makeStreams(watts))

	// 5s best = 400W (the all-power window)
	if curve[5] != 400 {
		t.Errorf("5s best: got %dW, want 400W", curve[5])
	}
	// 10s best = (400*5 + 0*5) / 10 = 200W
	if curve[10] != 200 {
		t.Errorf("10s best: got %dW, want 200W (null gaps count as 0W)", curve[10])
	}
}

// Rides with no power data must return nil.
func TestPowerCurve_NoPowerData(t *testing.T) {
	watts := repeat(-1, 120)
	curve := fitness.ComputePowerCurve(makeStreams(watts))
	if curve != nil {
		t.Errorf("expected nil for stream with no power data, got %v", curve)
	}
}
