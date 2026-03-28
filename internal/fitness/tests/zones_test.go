package fitness_test

import (
	"math"
	"testing"

	"github.com/fitbase/fitbase/internal/fitness"
)

// ── PowerZones ────────────────────────────────────────────────────────────────

func TestPowerZones_Count(t *testing.T) {
	zones := fitness.PowerZones(250)
	if len(zones) != 8 {
		t.Fatalf("expected 8 zones, got %d", len(zones))
	}
}

func TestPowerZones_Labels(t *testing.T) {
	zones := fitness.PowerZones(250)
	want := []string{"Z1", "Z2", "Z3", "Z4", "Z5", "Z6", "Z7", "SS"}
	for i, z := range zones {
		if z.Label != want[i] {
			t.Errorf("zone[%d] label: got %q want %q", i, z.Label, want[i])
		}
	}
}

func TestPowerZones_WattsCalculation(t *testing.T) {
	ftp := 250
	zones := fitness.PowerZones(ftp)

	tests := []struct {
		idx      int
		wantLow  int
		wantHigh int
	}{
		{0, 1, 137},   // Z1: 0–55% → 1–137w
		{1, 138, 187}, // Z2: 56–75% → 138–187w
		{2, 188, 225}, // Z3: 76–90% → 188–225w
		{3, 226, 262}, // Z4: 91–105% → 226–262w
		{4, 263, 300}, // Z5: 106–120% → 263–300w
		{5, 301, 375}, // Z6: 121–150% → 301–375w
		{6, 376, 0},   // Z7: 151%+ → open-ended
		{7, 210, 242}, // SS: 84–97% → 210–242w
	}

	for _, tt := range tests {
		z := zones[tt.idx]
		if z.WattsLow != tt.wantLow {
			t.Errorf("zone[%d] (%s) WattsLow: got %d want %d", tt.idx, z.Label, z.WattsLow, tt.wantLow)
		}
		if z.WattsHigh != tt.wantHigh {
			t.Errorf("zone[%d] (%s) WattsHigh: got %d want %d", tt.idx, z.Label, z.WattsHigh, tt.wantHigh)
		}
	}
}

func TestPowerZones_ZeroFTP(t *testing.T) {
	zones := fitness.PowerZones(0)
	for _, z := range zones {
		if z.WattsLow < 0 || z.WattsHigh < 0 {
			t.Errorf("zone %s has negative watts with zero FTP", z.Label)
		}
	}
}

func TestPowerZones_ScalesWithFTP(t *testing.T) {
	z200 := fitness.PowerZones(200)
	z400 := fitness.PowerZones(400)
	for i := range z200 {
		if z200[i].WattsHigh == 0 {
			continue // open-ended
		}
		ratio := float64(z400[i].WattsHigh) / float64(z200[i].WattsHigh)
		if math.Abs(ratio-2.0) > 0.05 {
			t.Errorf("zone %s: doubling FTP didn't roughly double watts (ratio=%.2f)", z200[i].Label, ratio)
		}
	}
}

// ── HRZones ──────────────────────────────────────────────────────────────────

func TestHRZones_Count(t *testing.T) {
	zones := fitness.HRZones(174)
	if len(zones) != 5 {
		t.Fatalf("expected 5 HR zones, got %d", len(zones))
	}
}

func TestHRZones_BPMBoundaries_174bpm(t *testing.T) {
	zones := fitness.HRZones(174)
	tests := []struct {
		idx     int
		bpmLow  int
		bpmHigh int
	}{
		{0, 0, 118},   // Z1: 0–68%  → floor(174*0.68) = 118
		{1, 119, 144}, // Z2: 69–83% → floor(174*0.83) = 144
		{2, 145, 163}, // Z3: 84–94% → floor(174*0.94) = 163
		{3, 164, 182}, // Z4: 95–105% → floor(174*1.05) = 182
		{4, 183, 0},   // Z5: 106%+ → open-ended
	}
	for _, tt := range tests {
		z := zones[tt.idx]
		if z.BPMLow != tt.bpmLow {
			t.Errorf("zone[%d] (%s) BPMLow: got %d want %d", tt.idx, z.Label, z.BPMLow, tt.bpmLow)
		}
		if z.BPMHigh != tt.bpmHigh {
			t.Errorf("zone[%d] (%s) BPMHigh: got %d want %d", tt.idx, z.Label, z.BPMHigh, tt.bpmHigh)
		}
	}
}

func TestHRZones_ZeroThreshold(t *testing.T) {
	zones := fitness.HRZones(0)
	for _, z := range zones {
		if z.BPMHigh != 0 {
			t.Errorf("zone %s: expected BPMHigh=0 when threshold=0, got %d", z.Label, z.BPMHigh)
		}
	}
}

func TestHRZones_Contiguous(t *testing.T) {
	zones := fitness.HRZones(180)
	for i := 1; i < len(zones); i++ {
		prev := zones[i-1]
		curr := zones[i]
		if prev.BPMHigh == 0 {
			continue
		}
		if curr.BPMLow != prev.BPMHigh+1 {
			t.Errorf("gap between zone[%d] and zone[%d]: %d→%d (expected %d)",
				i-1, i, prev.BPMHigh, curr.BPMLow, prev.BPMHigh+1)
		}
	}
}

// ── CustomHRZones ─────────────────────────────────────────────────────────────

func TestCustomHRZones_CountAndLabels(t *testing.T) {
	maxBPMs := [6]int{118, 144, 163, 182, 0, 0}
	zones := fitness.CustomHRZones(maxBPMs)

	if len(zones) != 5 {
		t.Fatalf("expected 5 HR zones, got %d", len(zones))
	}
	want := []string{"Z1", "Z2", "Z3", "Z4", "Z5"}
	for i, z := range zones {
		if z.Label != want[i] {
			t.Errorf("zone[%d] label: got %q want %q", i, z.Label, want[i])
		}
	}
}

func TestCustomHRZones_BPMBoundaries(t *testing.T) {
	maxBPMs := [6]int{118, 144, 163, 182, 0, 0}
	zones := fitness.CustomHRZones(maxBPMs)

	tests := []struct {
		idx     int
		bpmLow  int
		bpmHigh int
	}{
		{0, 0, 118},
		{1, 119, 144},
		{2, 145, 163},
		{3, 164, 182},
		{4, 183, 0}, // open-ended
	}
	for _, tt := range tests {
		z := zones[tt.idx]
		if z.BPMLow != tt.bpmLow {
			t.Errorf("zone[%d] (%s) BPMLow: got %d want %d", tt.idx, z.Label, z.BPMLow, tt.bpmLow)
		}
		if z.BPMHigh != tt.bpmHigh {
			t.Errorf("zone[%d] (%s) BPMHigh: got %d want %d", tt.idx, z.Label, z.BPMHigh, tt.bpmHigh)
		}
	}
}

func TestCustomHRZones_LastZoneOpenEnded(t *testing.T) {
	maxBPMs := [6]int{130, 140, 150, 160, 0, 0}
	zones := fitness.CustomHRZones(maxBPMs)
	last := zones[len(zones)-1]
	if last.BPMHigh != 0 {
		t.Errorf("last zone should be open-ended (BPMHigh=0), got %d", last.BPMHigh)
	}
}
