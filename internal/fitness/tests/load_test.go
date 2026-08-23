package fitness_test

import (
	"math"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/fitness"
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
	points := fitness.ComputeLoad(map[string]float64{}, epoch, 10, 0)
	for _, p := range points {
		if p.Fitness != 0 || p.Fatigue != 0 || p.Form != 0 {
			t.Errorf("expected all zeros, got CTL=%.6f ATL=%.6f TSB=%.6f on %s", p.Fitness, p.Fatigue, p.Form, p.Date.Format("2006-01-02"))
		}
	}
}

// TestTSBIsCTLMinusATL — TSB must always equal CTL - ATL regardless of input.
func TestTSBIsCTLMinusATL(t *testing.T) {
	tssByDay := tssMap(epoch, []float64{100, 50, 200, 0, 150, 80, 30})
	points := fitness.ComputeLoad(tssByDay, epoch, len(tssByDay), 0)
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
	points := fitness.ComputeLoad(tssByDay, epoch, 365, 0)
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
	points := fitness.ComputeLoad(tssMap(epoch, tss), epoch, 30, 0)

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
	points := fitness.ComputeLoad(tssMap(epoch, make([]float64, total)), epoch, total, skip)
	if len(points) != total-skip {
		t.Errorf("expected %d points after skip=%d, got %d", total-skip, skip, len(points))
	}
	if !points[0].Date.Equal(epoch.AddDate(0, 0, skip)) {
		t.Errorf("first point date should be %s, got %s", epoch.AddDate(0, 0, skip).Format("2006-01-02"), points[0].Date.Format("2006-01-02"))
	}
}

// TestSingleDayManual — hand-verify one step of the EMA formula.
func TestSingleDayManual(t *testing.T) {
	ctlDecay := 1.0 - math.Exp(-1.0/42.0)
	atlDecay := 1.0 - math.Exp(-1.0/7.0)
	const tss = 150.0

	// Starting from zero, after one day:
	wantCTL := ctlDecay * tss
	wantATL := atlDecay * tss
	wantTSB := wantCTL - wantATL

	points := fitness.ComputeLoad(tssMap(epoch, []float64{tss}), epoch, 1, 0)
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

// TestSettled_TrimsTrailingProjections — Settled drops projected points from the
// end of the history only, and copes with empty and all-projected input.
func TestSettled_TrimsTrailingProjections(t *testing.T) {
	pts := fitness.ComputeLoad(tssMap(epoch, []float64{100, 100, 100, 100, 100}), epoch, 5, 0)
	pts[3].IsProjection = true
	pts[4].IsProjection = true

	settled := fitness.Settled(pts)
	if len(settled) != 3 {
		t.Fatalf("expected 3 settled points, got %d", len(settled))
	}
	if !settled[2].Date.Equal(epoch.AddDate(0, 0, 2)) {
		t.Errorf("last settled date: got %s, want %s", settled[2].Date.Format("2006-01-02"), epoch.AddDate(0, 0, 2).Format("2006-01-02"))
	}

	if got := fitness.Settled(nil); len(got) != 0 {
		t.Errorf("Settled(nil) should be empty, got %d points", len(got))
	}
	for i := range pts {
		pts[i].IsProjection = true
	}
	if got := fitness.Settled(pts); len(got) != 0 {
		t.Errorf("all-projected history should settle to nothing, got %d points", len(got))
	}
}

// TestCurrent_SkipsUnsettledToday — Current returns the last settled point, so
// an unlogged "today" (zero-TSS decay, flagged as a projection) never becomes
// the rider's current form.
func TestCurrent_SkipsUnsettledToday(t *testing.T) {
	pts := fitness.ComputeLoad(tssMap(epoch, []float64{100, 100, 100, 0}), epoch, 4, 0)
	pts[3].IsProjection = true // "today", nothing logged yet

	cur, ok := fitness.Current(pts)
	if !ok {
		t.Fatal("expected a current point")
	}
	if !cur.Date.Equal(pts[2].Date) {
		t.Errorf("current should be the last settled day %s, got %s", pts[2].Date.Format("2006-01-02"), cur.Date.Format("2006-01-02"))
	}
	// The unsettled point reads fresher than the true carried-in form — the
	// inflation Current exists to avoid.
	if pts[3].Form <= cur.Form {
		t.Errorf("zero-TSS day should inflate form: unsettled %.4f, settled %.4f", pts[3].Form, cur.Form)
	}

	if _, ok := fitness.Current(nil); ok {
		t.Error("Current(nil) should report no point")
	}
}

// TestRampRate_IgnoresProjectedToday — an unlogged today must not register as a
// day of detraining in the ramp rate, and a window the settled history can't
// span yields 0.
func TestRampRate_IgnoresProjectedToday(t *testing.T) {
	vals := make([]float64, 11)
	for i := 0; i < 10; i++ {
		vals[i] = 100
	}
	pts := fitness.ComputeLoad(tssMap(epoch, vals), epoch, 11, 0) // day 10 = zero-TSS "today"

	want := fitness.RampRate(pts[:10], 7)
	if want == 0 {
		t.Fatal("test setup: expected a non-zero ramp over 7 days of loading")
	}
	pts[10].IsProjection = true
	if got := fitness.RampRate(pts, 7); got != want {
		t.Errorf("ramp with projected today: got %.1f, want %.1f (same as without it)", got, want)
	}
	if got := fitness.RampRate(pts, 10); got != 0 {
		t.Errorf("ramp over a window the settled history can't span should be 0, got %.1f", got)
	}
}
