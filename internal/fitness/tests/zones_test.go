package fitness_test

import (
	"math"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/fitness"
	"github.com/fitbase/fitbase/internal/models"
)

// powerStream builds samples one second apart, each carrying the given watts,
// so every sample contributes exactly 1s of zone time.
func powerStream(base time.Time, watts ...int) []models.Stream {
	s := make([]models.Stream, len(watts))
	for i, w := range watts {
		w := w
		s[i] = models.Stream{
			Timestamp:  base.Add(time.Duration(i) * time.Second),
			PowerWatts: &w,
		}
	}
	return s
}

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
		{7, 220, 235}, // SS: 88–94% → 220–235w
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

// ── SweetSpotBand ─────────────────────────────────────────────────────────────

func TestSweetSpotBand_MatchesZoneDefinition(t *testing.T) {
	low, high := fitness.SweetSpotBand(250)
	// 88–94% of FTP 250 → 220–235w.
	if low != 220 || high != 235 {
		t.Errorf("SweetSpotBand(250) = (%d, %d), want (220, 235)", low, high)
	}
	// The helper must agree with the SS entry in the PowerZones slice — that
	// equivalence is the whole reason the helper exists.
	ss := fitness.PowerZones(250)[fitness.SweetSpotIdx]
	if low != ss.WattsLow || high != ss.WattsHigh {
		t.Errorf("SweetSpotBand disagrees with PowerZones[SweetSpotIdx]: (%d,%d) vs (%d,%d)",
			low, high, ss.WattsLow, ss.WattsHigh)
	}
}

func TestSweetSpotBand_ZeroOrNegativeFTP(t *testing.T) {
	for _, ftp := range []int{0, -50} {
		low, high := fitness.SweetSpotBand(ftp)
		if low != 0 || high != 0 {
			t.Errorf("SweetSpotBand(%d) = (%d, %d), want (0, 0)", ftp, low, high)
		}
	}
}

func TestSweetSpotBand_ScalesWithFTP(t *testing.T) {
	l200, h200 := fitness.SweetSpotBand(200)
	l400, h400 := fitness.SweetSpotBand(400)
	if l400 != 2*l200 || h400 != 2*h200 {
		t.Errorf("doubling FTP should double the band: (%d,%d) → (%d,%d)", l200, h200, l400, h400)
	}
}

// ── ComputeZoneTimes ──────────────────────────────────────────────────────────

func TestComputeZoneTimes_SweetSpotOverlapsPartition(t *testing.T) {
	zones := fitness.PowerZones(250)[:7]
	ssLow, ssHigh := fitness.SweetSpotBand(250) // 220–235
	base := time.Date(2024, 3, 15, 8, 0, 0, 0, time.UTC)
	// 100→Z1, 200→Z3, 225→Z3+SS, 230→Z4+SS, 240→Z4
	streams := powerStream(base, 100, 200, 225, 230, 240)

	power, hr, ss := fitness.ComputeZoneTimes(streams, zones, nil, ssLow, ssHigh)

	wantPower := [7]int{1, 0, 2, 2, 0, 0, 0}
	if power != wantPower {
		t.Errorf("power = %v, want %v", power, wantPower)
	}
	if ss != 2 {
		t.Errorf("ss = %d, want 2 (225w and 230w fall in the 220–235 band)", ss)
	}
	// Core design claim: SS is counted in parallel and is NOT subtracted from the
	// partition. Every sample is still counted across the 7 zones (total == 5),
	// and two of them are ALSO counted in SS.
	total := 0
	for _, s := range power {
		total += s
	}
	if total != len(streams) {
		t.Errorf("partition total = %d, want %d — SS must not reduce zone counts", total, len(streams))
	}
	if hr != ([5]int{}) {
		t.Errorf("hr = %v, want all-zero with nil hrZones", hr)
	}
}

func TestComputeZoneTimes_SweetSpotBandInclusive(t *testing.T) {
	base := time.Date(2024, 3, 15, 8, 0, 0, 0, time.UTC)
	// Band 220–235; 219 below, 236 above, 220 and 235 exactly on the edges.
	streams := powerStream(base, 219, 220, 235, 236)
	_, _, ss := fitness.ComputeZoneTimes(streams, nil, nil, 220, 235)
	if ss != 2 {
		t.Errorf("ss = %d, want 2 (220 and 235 are inclusive bounds)", ss)
	}
}

func TestComputeZoneTimes_SweetSpotDisabledWhenBandZero(t *testing.T) {
	zones := fitness.PowerZones(250)[:7]
	base := time.Date(2024, 3, 15, 8, 0, 0, 0, time.UTC)
	streams := powerStream(base, 225, 230) // both in-band at FTP 250
	_, _, ss := fitness.ComputeZoneTimes(streams, zones, nil, 0, 0)
	if ss != 0 {
		t.Errorf("ss = %d, want 0 when the SS band is (0,0)", ss)
	}
}

func TestComputeZoneTimes_SSOnlyWithNilPartitionZones(t *testing.T) {
	// The backfill path calls ComputeZoneTimes(streams, nil, nil, low, high) to
	// recover only Sweet Spot time without recomputing the 7-zone partition.
	ssLow, ssHigh := fitness.SweetSpotBand(250)
	base := time.Date(2024, 3, 15, 8, 0, 0, 0, time.UTC)
	streams := powerStream(base, 100, 225, 230, 240)
	power, _, ss := fitness.ComputeZoneTimes(streams, nil, nil, ssLow, ssHigh)
	if power != ([7]int{}) {
		t.Errorf("power = %v, want all-zero with nil power zones", power)
	}
	if ss != 2 {
		t.Errorf("ss = %d, want 2", ss)
	}
}

func TestComputeZoneTimes_HeartRateZones(t *testing.T) {
	hrZones := fitness.HRZones(174) // Z1 ≤118, Z2 ≤144, Z3 ≤163, Z4 ≤182, Z5 open
	base := time.Date(2024, 3, 15, 8, 0, 0, 0, time.UTC)
	bpms := []int{100, 130, 150, 170, 190}
	streams := make([]models.Stream, len(bpms))
	for i, b := range bpms {
		b := b
		streams[i] = models.Stream{Timestamp: base.Add(time.Duration(i) * time.Second), HeartRateBPM: &b}
	}
	_, hr, ss := fitness.ComputeZoneTimes(streams, nil, hrZones, 0, 0)
	wantHR := [5]int{1, 1, 1, 1, 1}
	if hr != wantHR {
		t.Errorf("hr = %v, want %v", hr, wantHR)
	}
	if ss != 0 {
		t.Errorf("ss = %d, want 0 (no power data)", ss)
	}
}

func TestComputeZoneTimes_GapResetsDelta(t *testing.T) {
	// A >30s gap between samples is treated as 1s, not the raw difference, so a
	// recording pause can't inflate zone totals.
	base := time.Date(2024, 3, 15, 8, 0, 0, 0, time.UTC)
	w := 225
	streams := []models.Stream{
		{Timestamp: base, PowerWatts: &w},
		{Timestamp: base.Add(10 * time.Minute), PowerWatts: &w}, // 600s gap → clamped to 1s
	}
	_, _, ss := fitness.ComputeZoneTimes(streams, nil, nil, 220, 235)
	if ss != 2 {
		t.Errorf("ss = %d, want 2 (gap clamped to 1s per sample)", ss)
	}
}
