package fitness_test

import (
	"math"
	"testing"

	"github.com/fitbase/fitbase/internal/fitness"
)

func TestNormalizedPower_FewValues_UsesAverage(t *testing.T) {
	watts := []float64{100, 200, 300} // < 30, falls back to average
	got := fitness.NormalizedPower(watts)
	want := 200.0
	if math.Abs(got-want) > 0.001 {
		t.Errorf("got %.4f want %.4f", got, want)
	}
}

func TestNormalizedPower_AllSame(t *testing.T) {
	watts := make([]float64, 100)
	for i := range watts {
		watts[i] = 250
	}
	got := fitness.NormalizedPower(watts)
	if math.Abs(got-250.0) > 0.1 {
		t.Errorf("constant power NP: got %.4f want 250.0", got)
	}
}

func TestNormalizedPower_IntervalsBiasedUp(t *testing.T) {
	// NP of intervals should be higher than average power.
	// Intervals must be longer than the 30s rolling window to produce variance.
	watts := make([]float64, 120)
	for i := range watts {
		if i < 60 {
			watts[i] = 400 // 60s hard
		} else {
			watts[i] = 100 // 60s easy
		}
	}
	avg := 250.0 // (60*400 + 60*100) / 120
	np := fitness.NormalizedPower(watts)
	if np <= avg {
		t.Errorf("expected NP (%.1f) > average (%.1f) for long intervals", np, avg)
	}
}

func TestNormalizedPower_ExactlyThirty(t *testing.T) {
	watts := make([]float64, 30)
	for i := range watts {
		watts[i] = 200
	}
	got := fitness.NormalizedPower(watts)
	if math.Abs(got-200.0) > 0.1 {
		t.Errorf("30 constant samples: got %.4f want 200.0", got)
	}
}
