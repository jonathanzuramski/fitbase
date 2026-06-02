package models_test

import (
	"testing"

	"github.com/fitbase/fitbase/internal/models"
)

func ptr(v int) *int { return &v }

// A 5×(3min @115% / 3min @50%) set inside a warmup/cooldown sandwich is the
// canonical structured workout the repeat-group model exists to express.
func TestPlannedIntervalTotalSecs(t *testing.T) {
	w := []models.PlannedInterval{
		{Kind: "warmup", DurationSecs: 600, TargetPctFTPLow: ptr(50), TargetPctFTPHigh: ptr(75)},
		{Repeat: 5, Steps: []models.PlannedInterval{
			{Kind: "work", DurationSecs: 180, TargetPctFTP: ptr(115)},
			{Kind: "recovery", DurationSecs: 180, TargetPctFTP: ptr(50)},
		}},
		{Kind: "cooldown", DurationSecs: 600},
	}
	total := 0
	for _, iv := range w {
		total += iv.TotalSecs()
	}
	// 600 + 5×(180+180) + 600 = 3000
	if total != 3000 {
		t.Errorf("TotalSecs sum = %d, want 3000", total)
	}

	// Repeat on a bare leaf multiplies that single step.
	leaf := models.PlannedInterval{DurationSecs: 60, Repeat: 4}
	if leaf.TotalSecs() != 240 {
		t.Errorf("leaf repeat TotalSecs = %d, want 240", leaf.TotalSecs())
	}
	// Repeat 0 means "once", not "zero".
	once := models.PlannedInterval{DurationSecs: 60}
	if once.TotalSecs() != 60 {
		t.Errorf("zero-repeat leaf TotalSecs = %d, want 60", once.TotalSecs())
	}
}

func TestPlannedIntervalValidate(t *testing.T) {
	cases := []struct {
		name    string
		iv      models.PlannedInterval
		wantErr bool
	}{
		{"flat target ok", models.PlannedInterval{DurationSecs: 600, TargetPctFTP: ptr(88)}, false},
		{"ramp ok", models.PlannedInterval{DurationSecs: 600, TargetPctFTPLow: ptr(50), TargetPctFTPHigh: ptr(75)}, false},
		{"no target ok (free ride)", models.PlannedInterval{DurationSecs: 600}, false},
		{"group ok", models.PlannedInterval{Repeat: 3, Steps: []models.PlannedInterval{
			{DurationSecs: 60, TargetPctFTP: ptr(120)},
			{DurationSecs: 60, TargetPctFTP: ptr(50)},
		}}, false},

		{"leaf needs duration", models.PlannedInterval{TargetPctFTP: ptr(88)}, true},
		{"pct out of range", models.PlannedInterval{DurationSecs: 600, TargetPctFTP: ptr(250)}, true},
		{"half a ramp", models.PlannedInterval{DurationSecs: 600, TargetPctFTPLow: ptr(50)}, true},
		{"ramp low > high", models.PlannedInterval{DurationSecs: 600, TargetPctFTPLow: ptr(80), TargetPctFTPHigh: ptr(60)}, true},
		{"flat target and ramp together", models.PlannedInterval{DurationSecs: 600, TargetPctFTP: ptr(90), TargetPctFTPLow: ptr(50), TargetPctFTPHigh: ptr(75)}, true},
		{"group carrying leaf fields", models.PlannedInterval{DurationSecs: 600, Steps: []models.PlannedInterval{{DurationSecs: 60}}}, true},
		{"negative watts", models.PlannedInterval{DurationSecs: 600, TargetWatts: ptr(-5)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.iv.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// Nesting beyond the depth cap must be rejected rather than recursed forever.
func TestPlannedIntervalDepthLimit(t *testing.T) {
	leaf := models.PlannedInterval{DurationSecs: 60, TargetPctFTP: ptr(100)}
	g1 := models.PlannedInterval{Repeat: 2, Steps: []models.PlannedInterval{leaf}}
	g2 := models.PlannedInterval{Repeat: 2, Steps: []models.PlannedInterval{g1}}
	g3 := models.PlannedInterval{Repeat: 2, Steps: []models.PlannedInterval{g2}}
	if err := g3.Validate(); err == nil {
		t.Error("expected depth-limit error for 4-level nesting, got nil")
	}
}
