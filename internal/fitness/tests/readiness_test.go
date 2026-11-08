package fitness_test

import (
	"testing"

	"github.com/fitbase/fitbase/internal/fitness"
)

func TestRecommend(t *testing.T) {
	cases := []struct {
		form, ramp float64
		daysSince  int
		label      string
		tone       string
	}{
		{13.5, -14, 0, "Go Ride", fitness.ToneGo},
		{16, 0, 1, "Race Ready", fitness.ToneGo},
		{0, 2, 1, "Maintain", fitness.ToneTrain},
		{-10, 2, 1, "Maintain", fitness.ToneTrain}, // boundary is inclusive
		{-20, 4, 1, "Training Block", fitness.ToneTrain},
		{-30, 4, 1, "Ease Up", fitness.ToneEase},
		{-45, 4, 1, "Rest", fitness.ToneRest},
		// The checks that the old dashboard-only (form-only) badge missed:
		{20, -5, 8, "Resume Training", fitness.ToneEase}, // fresh, but only because of a week off
		{0, 12, 1, "Ease Up", fitness.ToneEase},          // a sharp ramp outranks neutral form
	}
	for _, c := range cases {
		label, detail, tone := fitness.Recommend(c.form, c.ramp, c.daysSince)
		if label != c.label || tone != c.tone || detail == "" {
			t.Errorf("Recommend(%v, %v, %d) = %q/%q, want %q/%q", c.form, c.ramp, c.daysSince, label, tone, c.label, c.tone)
		}
	}
}
