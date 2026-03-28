package fitness

import (
	"math"
	"testing"
	"time"
)

const tolerance = 1e-9

func approx(a, b float64) bool {
	return math.Abs(a-b) < tolerance
}

var epoch = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

func tssMap(start time.Time, values []float64) map[string]float64 {
	m := make(map[string]float64, len(values))
	for i, v := range values {
		m[start.AddDate(0, 0, i).Format("2006-01-02")] = v
	}
	return m
}

// TestZeroTSS — no training should leave CTL, ATL, and TSB all at zero.
func TestZeroTSS(t *testing.T) {
	points := ComputeLoad(map[string]float64{}, epoch, 10, 0)
	for _, p := range points {
		if p.Fitness != 0 || p.Fatigue != 0 || p.Form != 0 {
			t.Errorf("expected all zeros, got CTL=%.6f ATL=%.6f TSB=%.6f on %s", p.Fitness, p.Fatigue, p.Form, p.Date.Format("2006-01-02"))
		}
	}
}

// TestTSBIsCTLMinusATL — TSB must always equal CTL - ATL regardless of input.
func TestTSBIsCTLMinusATL(t *testing.T) {
	tssByDay := tssMap(epoch, []float64{100, 50, 200, 0, 150, 80, 30})
	points := ComputeLoad(tssByDay, epoch, len(tssByDay), 0)
	for _, p := range points {
		if !approx(p.Form, p.Fitness-p.Fatigue) {
			t.Errorf("TSB (%.9f) != CTL-ATL (%.9f) on %s", p.Form, p.Fitness-p.Fatigue, p.Date.Format("2006-01-02"))
		}
	}
}

// TestConstantTSSConverges — with sustained daily TSS the EMAs converge toward that value.
func TestConstantTSSConverges(t *testing.T) {
	const dailyTSS = 100.0
	tssByDay := tssMap(epoch, make([]float64, 365))
	for k := range tssByDay {
		tssByDay[k] = dailyTSS
	}
	points := ComputeLoad(tssByDay, epoch, 365, 0)
	last := points[len(points)-1]

	// After a full year of constant 100 TSS, both EMAs should be very close to 100.
	if math.Abs(last.Fitness-dailyTSS) > 1.0 {
		t.Errorf("CTL did not converge: got %.4f, want ~%.4f", last.Fitness, dailyTSS)
	}
	if math.Abs(last.Fatigue-dailyTSS) > 0.01 {
		t.Errorf("ATL did not converge: got %.4f, want ~%.4f", last.Fatigue, dailyTSS)
	}
	// When CTL ≈ ATL, TSB ≈ 0.
	if math.Abs(last.Form) > 1.0 {
		t.Errorf("TSB should be ~0 when CTL≈ATL, got %.4f", last.Form)
	}
}

// TestATLRisesAndFallsFasterThanCTL — ATL (7-day) should react faster than CTL (42-day).
func TestATLRisesAndFallsFasterThanCTL(t *testing.T) {
	// Spike of heavy training then complete rest.
	tss := make([]float64, 30)
	for i := 0; i < 7; i++ {
		tss[i] = 200
	}
	// days 7-29 are rest (zero)
	points := ComputeLoad(tssMap(epoch, tss), epoch, 30, 0)

	// After the spike, ATL should be higher than CTL (more acute stress).
	afterSpike := points[6]
	if afterSpike.Fatigue <= afterSpike.Fitness {
		t.Errorf("after training spike ATL (%.4f) should exceed CTL (%.4f)", afterSpike.Fatigue, afterSpike.Fitness)
	}

	// During rest, ATL should decay faster — after 14 days of rest ATL < CTL.
	afterRest := points[20]
	if afterRest.Fatigue >= afterRest.Fitness {
		t.Errorf("after extended rest ATL (%.4f) should be below CTL (%.4f)", afterRest.Fatigue, afterRest.Fitness)
	}
}

// TestSkipWarmup — skip parameter should omit the first N points from output.
func TestSkipWarmup(t *testing.T) {
	const total = 10
	const skip = 3
	points := ComputeLoad(tssMap(epoch, make([]float64, total)), epoch, total, skip)
	if len(points) != total-skip {
		t.Errorf("expected %d points after skip=%d, got %d", total-skip, skip, len(points))
	}
	if !points[0].Date.Equal(epoch.AddDate(0, 0, skip)) {
		t.Errorf("first point date should be %s, got %s", epoch.AddDate(0, 0, skip).Format("2006-01-02"), points[0].Date.Format("2006-01-02"))
	}
}

// TestSingleDayManual — hand-verify one step of the EMA formula.
func TestSingleDayManual(t *testing.T) {
	const ctlDecay = 1.0 / 42.0
	const atlDecay = 1.0 / 7.0
	const tss = 150.0

	// Starting from zero, after one day:
	wantCTL := ctlDecay * tss
	wantATL := atlDecay * tss
	wantTSB := wantCTL - wantATL

	points := ComputeLoad(tssMap(epoch, []float64{tss}), epoch, 1, 0)
	if len(points) != 1 {
		t.Fatalf("expected 1 point, got %d", len(points))
	}
	p := points[0]
	if !approx(p.Fitness, wantCTL) {
		t.Errorf("CTL: got %.9f, want %.9f", p.Fitness, wantCTL)
	}
	if !approx(p.Fatigue, wantATL) {
		t.Errorf("ATL: got %.9f, want %.9f", p.Fatigue, wantATL)
	}
	if !approx(p.Form, wantTSB) {
		t.Errorf("TSB: got %.9f, want %.9f", p.Form, wantTSB)
	}
}
