package models_test

import (
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/models"
)

// ── ToSummary ─────────────────────────────────────────────────────────────────

func TestToSummary(t *testing.T) {
	avgPower := 210.0
	np := 225.0
	avgHR := 155
	tss := 78.5
	ifactor := 0.9

	w := models.Workout{
		ID:                  "abc123",
		RecordedAt:          time.Date(2024, 3, 15, 8, 0, 0, 0, time.UTC),
		Sport:               "cycling",
		DurationSecs:        3600,
		DistanceMeters:      36000,
		ElevationGainMeters: 500,
		AvgPowerWatts:       &avgPower,
		NormalizedPower:     &np,
		AvgHeartRate:        &avgHR,
		TSS:                 &tss,
		IntensityFactor:     &ifactor,
	}

	s := w.ToSummary()

	if s.ID != "abc123" {
		t.Errorf("ID: got %q want %q", s.ID, "abc123")
	}
	if s.Date != "2024-03-15" {
		t.Errorf("Date: got %q want %q", s.Date, "2024-03-15")
	}
	if s.Sport != "cycling" {
		t.Errorf("Sport: got %q", s.Sport)
	}
	if s.DurationMins != 60.0 {
		t.Errorf("DurationMins: got %.1f want 60.0", s.DurationMins)
	}
	if s.DistanceKM != 36.0 {
		t.Errorf("DistanceKM: got %.1f want 36.0", s.DistanceKM)
	}
	if s.AvgPowerWatts == nil || *s.AvgPowerWatts != 210.0 {
		t.Errorf("AvgPowerWatts: unexpected value")
	}
	if s.NormalizedPower == nil || *s.NormalizedPower != 225.0 {
		t.Errorf("NormalizedPower: unexpected value")
	}
	if s.AvgHeartRate == nil || *s.AvgHeartRate != 155 {
		t.Errorf("AvgHeartRate: unexpected value")
	}
	if s.TSS == nil || *s.TSS != 78.5 {
		t.Errorf("TSS: unexpected value")
	}
}

func TestToSummary_NilPointers(t *testing.T) {
	w := models.Workout{
		ID:           "xyz",
		RecordedAt:   time.Now(),
		Sport:        "running",
		DurationSecs: 1800,
	}
	s := w.ToSummary()
	if s.AvgPowerWatts != nil {
		t.Error("expected nil AvgPowerWatts")
	}
	if s.TSS != nil {
		t.Error("expected nil TSS")
	}
}
