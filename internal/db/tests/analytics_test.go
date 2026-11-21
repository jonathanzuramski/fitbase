package db_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/models"
	"github.com/fitbase/fitbase/internal/timeutil"
)

// ── Fixtures ──────────────────────────────────────────────────────────────────

func f64(v float64) *float64 { return &v }
func iptr(v int) *int        { return &v }

// aRide is a one-hour outdoor endurance ride at 08:00 UTC on day: 180 W
// average, 190 W NP, 140 bpm, 88 rpm, IF 0.76, TSS 58, 30 km and 300 m.
// Options change it.
func aRide(id, day string, opts ...func(*models.Workout)) *models.Workout {
	d, err := time.Parse("2006-01-02", day)
	if err != nil {
		panic(err)
	}
	w := &models.Workout{
		ID: id, Filename: id + ".fit", Sport: "cycling",
		RecordedAt:   d.Add(8 * time.Hour),
		DurationSecs: 3600, DistanceMeters: 30000, ElevationGainMeters: 300,
		AvgPowerWatts: f64(180), NormalizedPower: f64(190),
		AvgHeartRate: iptr(140), AvgCadenceRPM: iptr(88),
		TSS: f64(58), IntensityFactor: f64(0.76),
		UTCOffsetSecs: iptr(0), CreatedAt: time.Now().UTC(),
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

func indoor(w *models.Workout) { w.IsIndoor = true }

func noPower(w *models.Workout) {
	w.AvgPowerWatts, w.NormalizedPower, w.IntensityFactor = nil, nil, nil
}

func noHR(w *models.Workout) { w.AvgHeartRate = nil }

func lasting(secs int) func(*models.Workout) {
	return func(w *models.Workout) { w.DurationSecs = secs }
}

func power(np float64, hr int) func(*models.Workout) {
	return func(w *models.Workout) {
		w.NormalizedPower, w.AvgPowerWatts, w.AvgHeartRate = f64(np), f64(np-8), iptr(hr)
	}
}

func sport(s string) func(*models.Workout) { return func(w *models.Workout) { w.Sport = s } }

func insertRides(t *testing.T, d *db.DB, rides ...*models.Workout) {
	t.Helper()
	for _, w := range rides {
		if err := d.InsertWorkout(w, nil); err != nil {
			t.Fatalf("insert %s: %v", w.ID, err)
		}
	}
}

func period(first, last string) timeutil.Period {
	a, _ := timeutil.ParseDay(first)
	b, _ := timeutil.ParseDay(last)
	return timeutil.Period{Start: a, End: b.AddDate(0, 0, 1)}
}

// november and october 2026, whole.
func novemberVsOctober() timeutil.Comparison {
	return timeutil.Comparison{
		Kind: timeutil.PeriodMonth, Against: timeutil.AgainstPrevious,
		A: period("2026-11-01", "2026-11-30"), B: period("2026-10-01", "2026-10-31"),
	}
}

func rowOf(t *testing.T, c models.PeriodComparison, key string) *models.ComparisonRow {
	t.Helper()
	for i := range c.Rows {
		if c.Rows[i].Key == key {
			return &c.Rows[i]
		}
	}
	return nil
}

func val(p *float64) float64 {
	if p == nil {
		return math.NaN()
	}
	return *p
}

// ── Period comparison ─────────────────────────────────────────────────────────

func TestComparePeriods_Volume(t *testing.T) {
	d := newTestDB(t)
	insertRides(t, d,
		aRide("nov1", "2026-11-02"),
		aRide("nov2", "2026-11-02", lasting(1800)), // same day: one active day
		aRide("nov3", "2026-11-20"),
		aRide("oct1", "2026-10-10"),
		aRide("sep1", "2026-09-30"), // neither period
	)
	c, err := d.ComparePeriods(novemberVsOctober(), "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Scope != "cycling" {
		t.Errorf("default scope = %q, want cycling for a rider with rides", c.Scope)
	}
	a, b := c.A, c.B
	if a.Label != "November 2026" || a.Activities != 3 || a.ActiveDays != 2 || a.DurationSecs != 9000 || a.DistanceMeters != 90000 {
		t.Errorf("A = %q: %d activities on %d days, %d s, %.0f m; want November 2026: 3 on 2, 9000 s, 90000 m",
			a.Label, a.Activities, a.ActiveDays, a.DurationSecs, a.DistanceMeters)
	}
	if b.Activities != 1 || b.TSS != 58 {
		t.Errorf("B: %d activities, %.1f TSS; want 1, 58", b.Activities, b.TSS)
	}
	// kJ = average power × moving time: 180 W for 2.5 h.
	if val(a.WorkKJ) != 1620 {
		t.Errorf("A work = %v kJ, want 1620", val(a.WorkKJ))
	}
	row := rowOf(t, c, "duration_secs")
	if row == nil || val(row.Delta) != 5400 || val(row.DeltaPct) != 150 || row.Better != "" {
		t.Errorf("time row = %+v, want +5400 s, +150%%, and no better/worse: more time isn't better or worse", row)
	}
	// The best 20 minutes needs stored curves; none here, so no power rows.
	if rowOf(t, c, "best_1200") != nil {
		t.Error("a best-effort row with no curves")
	}
}

// A period is its training days in the rider's timezone, not UTC days: a ride
// at 23:30 in New York on October 31 is November 1 in UTC, and October's.
func TestComparePeriods_TrainingDayNotUTC(t *testing.T) {
	d := newTestDB(t)
	late := aRide("halloween", "2026-11-01", func(w *models.Workout) {
		w.RecordedAt = time.Date(2026, 11, 1, 3, 30, 0, 0, time.UTC)
		w.UTCOffsetSecs = iptr(-4 * 3600)
	})
	insertRides(t, d, late)
	c, err := d.ComparePeriods(novemberVsOctober(), "")
	if err != nil {
		t.Fatal(err)
	}
	if c.A.Activities != 0 || c.B.Activities != 1 {
		t.Errorf("November %d, October %d; want the ride in October, the day it was ridden", c.A.Activities, c.B.Activities)
	}
}

// Running watts aren't cycling watts: a run with a power meter counts toward
// volume, never toward NP, IF, kJ, power zones or best efforts.
func TestComparePeriods_PowerIsCyclingOnly(t *testing.T) {
	d := newTestDB(t)
	run := aRide("stryd", "2026-11-05", sport("running"), power(300, 160), func(w *models.Workout) {
		w.IntensityFactor = f64(1.2)
	})
	insertRides(t, d, aRide("ride", "2026-11-04"), run, aRide("octride", "2026-10-04"))
	if err := d.InsertPowerCurve("stryd", map[int]int{5: 600, 1200: 320}); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertPowerCurve("ride", map[int]int{5: 900, 1200: 230}); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertZoneTimes("stryd", [7]int{0, 0, 0, 0, 3600, 0, 0}, [5]int{}, 0); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertZoneTimes("ride", [7]int{0, 3600, 0, 0, 0, 0, 0}, [5]int{}, 0); err != nil {
		t.Fatal(err)
	}

	c, err := d.ComparePeriods(novemberVsOctober(), models.ScopeAll)
	if err != nil {
		t.Fatal(err)
	}
	a := c.A
	if a.Activities != 2 || a.PowerRides != 1 {
		t.Errorf("all sports: %d activities, %d power rides; want 2 and 1 (the run's watts don't count)", a.Activities, a.PowerRides)
	}
	if val(a.AvgNPWatts) != 190 || val(a.AvgIF) != 0.76 || val(a.WorkKJ) != 648 {
		t.Errorf("NP %v, IF %v, kJ %v; want the ride's alone: 190, 0.76, 648", val(a.AvgNPWatts), val(a.AvgIF), val(a.WorkKJ))
	}
	if a.PowerZoneSecs[4] != 0 || a.Split == nil || a.Split.EasyPct != 100 {
		t.Errorf("power zones %v, split %+v; want the ride's endurance hour alone", a.PowerZoneSecs, a.Split)
	}
	if row := rowOf(t, c, "best_1200"); row == nil || val(row.A) != 230 {
		t.Errorf("best 20 min row = %+v, want 230 W from the ride, not the run's 320", row)
	}
	if len(a.Sports) != 2 {
		t.Errorf("sport split = %+v, want cycling and running", a.Sports)
	}

	run2, err := d.ComparePeriods(novemberVsOctober(), "running")
	if err != nil {
		t.Fatal(err)
	}
	if run2.A.Activities != 1 || run2.A.PowerRides != 0 || len(run2.A.Bests) != 0 || run2.A.WorkKJ != nil {
		t.Errorf("running scope: %d activities, %d power rides, bests %v, kJ %v; want 1 and no power numbers",
			run2.A.Activities, run2.A.PowerRides, run2.A.Bests, run2.A.WorkKJ)
	}
	if len(run2.A.Sports) != 2 {
		t.Error("the sport split shows every sport whatever the scope")
	}
}

// EF and the matched markers come from steady rides, indoor and outdoor kept
// apart, and say which way is better.
func TestComparePeriods_AerobicMarkers(t *testing.T) {
	d := newTestDB(t)
	insertRides(t, d,
		// November: 185–190 W at 138–140 bpm outdoors, and one indoor ride.
		aRide("n1", "2026-11-03", power(185, 138)),
		aRide("n2", "2026-11-10", power(190, 140)),
		aRide("n3", "2026-11-12", power(160, 150), indoor),
		// October: the same power cost more heart rate.
		aRide("o1", "2026-10-03", power(185, 146)),
		aRide("o2", "2026-10-10", power(188, 148)),
		// Not steady: an interval session (IF 0.95) and a short spin.
		aRide("o3", "2026-10-12", power(240, 165), func(w *models.Workout) { w.IntensityFactor = f64(0.95) }),
		aRide("o4", "2026-10-13", lasting(1800)),
	)
	c, err := d.ComparePeriods(novemberVsOctober(), "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Reference.PowerOutdoorWatts == nil || *c.Reference.PowerOutdoorWatts != 185 {
		t.Fatalf("outdoor reference = %v, want 185 W (the median steady NP)", c.Reference.PowerOutdoorWatts)
	}
	hr := rowOf(t, c, "hr_at_power_outdoor")
	if hr == nil || hr.Label != "HR at 185 W · outdoor" || val(hr.A) != 139 || val(hr.B) != 147 || hr.Better != "lower" {
		t.Fatalf("HR at power = %+v, want 139 against 147 bpm, lower better", hr)
	}
	if *hr.RidesA != 2 || *hr.RidesB != 2 {
		t.Errorf("HR at power from %d and %d rides, want 2 and 2 (the interval session isn't steady)", *hr.RidesA, *hr.RidesB)
	}
	ef := rowOf(t, c, "ef_outdoor")
	if ef == nil || ef.Better != "higher" || val(ef.Delta) <= 0 {
		t.Errorf("outdoor EF = %+v, want higher in November and higher better", ef)
	}
	// Indoors only November has a ride: a row with nothing to compare.
	in := rowOf(t, c, "ef_indoor")
	if in == nil || in.A == nil || in.B != nil || in.Delta != nil {
		t.Errorf("indoor EF = %+v, want November's value alone and no change", in)
	}
	if p := rowOf(t, c, "power_at_hr_outdoor"); p == nil || p.Better != "higher" {
		t.Errorf("power at HR = %+v, want a row where higher is better", p)
	}
	if avg := rowOf(t, c, "avg_hr_bpm"); avg == nil || avg.Better != "" {
		t.Errorf("average HR = %+v, want no better/worse: it depends what was ridden", avg)
	}
}

// Decoupling is read from the cache only: a long ride whose decoupling isn't
// cached is left out rather than its streams read inside the request.
func TestComparePeriods_DecouplingFromCacheOnly(t *testing.T) {
	d := newTestDB(t)
	long := aRide("long", "2026-11-08", lasting(2*3600))
	start := long.RecordedAt
	var streams []models.Stream
	for i := 0; i < 2*3600; i++ {
		p, h := 190, 135
		if i >= 3600 {
			h = 145
		}
		streams = append(streams, models.Stream{Timestamp: start.Add(time.Duration(i) * time.Second), PowerWatts: &p, HeartRateBPM: &h})
	}
	if err := d.InsertWorkout(long, streams); err != nil {
		t.Fatal(err)
	}
	insertRides(t, d, aRide("cached", "2026-11-09", lasting(2*3600)))
	if err := d.SaveWorkoutDecoupling("cached", f64(3.2)); err != nil {
		t.Fatal(err)
	}
	c, err := d.ComparePeriods(novemberVsOctober(), "")
	if err != nil {
		t.Fatal(err)
	}
	if c.A.DecouplingPct == nil || c.A.DecouplingPct.Value != 3.2 || c.A.DecouplingPct.Rides != 1 {
		t.Errorf("decoupling = %+v, want 3.2%% from the one cached ride", c.A.DecouplingPct)
	}
	if _, _, found, _ := d.GetWorkoutDecoupling("long"); found {
		t.Error("the comparison computed decoupling from streams; it must read the cache only")
	}
}

// A rider with no power meter and no heart-rate strap gets volume, and no
// rows the data can't support.
func TestComparePeriods_NoPowerNoHR(t *testing.T) {
	d := newTestDB(t)
	insertRides(t, d, aRide("a", "2026-11-03", noPower, noHR), aRide("b", "2026-10-03", noPower, noHR))
	c, err := d.ComparePeriods(novemberVsOctober(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"work_kj", "avg_if", "avg_np_watts", "ef_outdoor", "avg_hr_bpm", "hr_at_power_outdoor", "best_5", "avg_rpe", "easy_pct"} {
		if row := rowOf(t, c, key); row != nil {
			t.Errorf("row %s = %+v; the rides have no power or heart rate", key, row)
		}
	}
	if row := rowOf(t, c, "distance_meters"); row == nil || val(row.A) != 30000 {
		t.Errorf("distance row = %+v, want 30000 m", row)
	}
}

func TestComparePeriods_EmptyDB(t *testing.T) {
	d := newTestDB(t)
	c, err := d.ComparePeriods(novemberVsOctober(), "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Scope != models.ScopeAll || len(c.Scopes) != 0 || c.A.Activities != 0 {
		t.Errorf("empty: scope %q, scopes %v, %d activities; want all, none, 0", c.Scope, c.Scopes, c.A.Activities)
	}
	if row := rowOf(t, c, "duration_secs"); row == nil || val(row.Delta) != 0 || row.DeltaPct != nil {
		t.Errorf("time row = %+v, want 0 against 0 with no percentage", row)
	}
}

func TestComparePeriods_RPE(t *testing.T) {
	d := newTestDB(t)
	insertRides(t, d, aRide("a", "2026-11-03"), aRide("b", "2026-11-04"), aRide("c", "2026-10-04"))
	for id, rpe := range map[string]int{"a": 6, "b": 8, "c": 5} {
		if err := d.SaveWorkoutFeedback(id, iptr(rpe), ""); err != nil {
			t.Fatal(err)
		}
	}
	c, err := d.ComparePeriods(novemberVsOctober(), "")
	if err != nil {
		t.Fatal(err)
	}
	row := rowOf(t, c, "avg_rpe")
	if row == nil || val(row.A) != 7 || val(row.B) != 5 || *row.RidesA != 2 || row.Better != "" {
		t.Errorf("RPE row = %+v, want 7 (2 rated) against 5, no better/worse", row)
	}
	if len(c.A.RPE) != 2 || c.A.RPE[0].TSS == nil {
		t.Errorf("RPE points = %+v, want two with their TSS", c.A.RPE)
	}
}

func TestComparePeriods_InvalidScope(t *testing.T) {
	d := newTestDB(t)
	if _, err := d.ComparePeriods(novemberVsOctober(), "curling"); !errors.Is(err, db.ErrInvalidParam) {
		t.Errorf("scope curling: %v, want ErrInvalidParam", err)
	}
}

// ── Trends ────────────────────────────────────────────────────────────────────

func dayStr(t time.Time) string { return t.Format("2006-01-02") }

func bucketByStart(tr models.AnalyticsTrends, start string) *models.TrendBucket {
	for i := range tr.Buckets {
		if tr.Buckets[i].Start == start {
			return &tr.Buckets[i]
		}
	}
	return nil
}

func TestAnalyticsTrends_Markers(t *testing.T) {
	d := newTestDB(t)
	now := time.Now().UTC()
	today := timeutil.DateOf(now)
	thisMonth := timeutil.Date(today.Year(), today.Month(), 1)
	lastMonth := thisMonth.AddDate(0, -1, 0)
	inLast := dayStr(lastMonth.AddDate(0, 0, 9))
	insertRides(t, d,
		aRide("out", inLast, power(185, 140)),
		aRide("in", inLast, power(170, 145), indoor),
		aRide("run", inLast, sport("running"), power(300, 160)),
		aRide("hard", dayStr(lastMonth.AddDate(0, 0, 10)), func(w *models.Workout) { w.IntensityFactor = f64(0.98) }),
	)
	if err := d.InsertPowerCurve("out", map[int]int{5: 800, 300: 280, 1200: 240}); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertPowerCurve("run", map[int]int{5: 1000, 300: 400, 1200: 330}); err != nil {
		t.Fatal(err)
	}

	tr, err := d.AnalyticsTrends("1y", now)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Bucket != "month" || len(tr.Buckets) != 12 {
		t.Fatalf("1y: %d %s buckets, want 12 months", len(tr.Buckets), tr.Bucket)
	}
	b := bucketByStart(tr, dayStr(lastMonth))
	if b == nil {
		t.Fatalf("no bucket for %s", dayStr(lastMonth))
	}
	// Indoors and outdoors kept apart.
	if b.EF.Outdoor == nil || b.EF.Indoor == nil || b.EF.Outdoor.Rides != 1 || b.EF.Indoor.Rides != 1 {
		t.Errorf("EF = %+v, want one outdoor and one indoor ride, apart", b.EF)
	}
	if want := math.Round(185.0/140*1000) / 1000; b.EF.Outdoor.Value != want {
		t.Errorf("outdoor EF = %v, want %v", b.EF.Outdoor.Value, want)
	}
	// IF bands count cycling rides: the steady ride and the indoor one in
	// 0.75–0.85, the hard one at 0.95+, the run nowhere.
	if b.IFBands != [5]int{0, 0, 2, 0, 1} {
		t.Errorf("IF bands = %v, want [0 0 2 0 1]", b.IFBands)
	}
	// Best efforts by month are cycling-only.
	var month *models.BestEffortMonth
	for i := range tr.BestEfforts.Months {
		if tr.BestEfforts.Months[i].Month == lastMonth.Format("2006-01") {
			month = &tr.BestEfforts.Months[i]
		}
	}
	if month == nil || month.Watts[0] == nil || *month.Watts[0] != 800 || *month.Watts[3] != 240 || month.Watts[4] != nil {
		t.Errorf("best efforts %+v, want the ride's 800 W / 240 W, not the run's", month)
	}
	if len(tr.BestEfforts.Months) != 12 {
		t.Errorf("%d best-effort months, want 12", len(tr.BestEfforts.Months))
	}
	if len(tr.Rides) == 0 {
		t.Error("no per-ride points for the scatter charts")
	}
	for _, r := range tr.Rides {
		if r.ID == "run" && r.EF != nil {
			t.Error("the run has an EF point; EF is cycling-only")
		}
	}
}

// Trends bucket by training day: a ride late on the last evening of a month
// in the rider's timezone belongs to that month, though it is the next in UTC.
func TestAnalyticsTrends_TrainingDayBuckets(t *testing.T) {
	d := newTestDB(t)
	now := time.Now().UTC()
	today := timeutil.DateOf(now)
	thisMonth := timeutil.Date(today.Year(), today.Month(), 1)
	lastDayOfPrev := thisMonth.AddDate(0, -1, 0).AddDate(0, 1, -1) // e.g. Oct 31
	late := aRide("late", dayStr(thisMonth), func(w *models.Workout) {
		w.RecordedAt = thisMonth.Add(2 * time.Hour) // 02:00 UTC on the 1st
		w.UTCOffsetSecs = iptr(-6 * 3600)           // 20:00 the evening before, locally
		w.TSS = f64(77)
	})
	insertRides(t, d, late)
	tr, err := d.AnalyticsTrends("1y", now)
	if err != nil {
		t.Fatal(err)
	}
	prev := bucketByStart(tr, dayStr(timeutil.Date(lastDayOfPrev.Year(), lastDayOfPrev.Month(), 1)))
	cur := bucketByStart(tr, dayStr(thisMonth))
	if prev == nil || cur == nil || prev.TSS != 77 || cur.TSS != 0 {
		t.Errorf("previous month TSS %v, this month %v; want the ride in the month it was ridden", prev, cur)
	}
}

// The FTP estimate reads an indoor effort against the general line, as the
// FTP check does, and the indoor estimate reads indoor rides against the
// indoor line.
func TestAnalyticsTrends_FTPEstimateUsesIndoorLine(t *testing.T) {
	d := newTestDB(t)
	now := time.Now().UTC()
	today := timeutil.DateOf(now)
	if err := d.LogFTPChangeAt(250, today.AddDate(0, -6, 0)); err != nil {
		t.Fatal(err)
	}
	if err := d.LogFTPChangeAtFor(230, today.AddDate(0, -6, 1), true); err != nil {
		t.Fatal(err)
	}
	insertRides(t, d, aRide("trainer", dayStr(today.AddDate(0, 0, -3)), indoor))
	if err := d.InsertPowerCurve("trainer", map[int]int{1200: 238}); err != nil {
		t.Fatal(err)
	}
	tr, err := d.AnalyticsTrends("3m", now)
	if err != nil {
		t.Fatal(err)
	}
	last := func(pts []models.FTPPoint) int {
		if len(pts) == 0 {
			return 0
		}
		return pts[len(pts)-1].W
	}
	// 238 W on a 230 W indoor line is 259 W in general terms; 95% is 246.
	if got := last(tr.FTP.Estimate); got != 246 {
		t.Errorf("general estimate = %d W, want 246 (the indoor effort scaled to the general line)", got)
	}
	if got := last(tr.FTP.IndoorEstimate); got != 226 {
		t.Errorf("indoor estimate = %d W, want 226 (95%% of 238 against the indoor line)", got)
	}
	if last(tr.FTP.General) != 250 || last(tr.FTP.Indoor) != 230 {
		t.Errorf("lines end at %d / %d, want 250 general and 230 indoor", last(tr.FTP.General), last(tr.FTP.Indoor))
	}
}

func TestAnalyticsTrends_LoadAndConsistency(t *testing.T) {
	d := newTestDB(t)
	now := time.Now().UTC()
	today := timeutil.DateOf(now)
	monday := timeutil.MondayOf(today)
	insertRides(t, d,
		aRide("w1", dayStr(monday.AddDate(0, 0, -7))),
		aRide("w2", dayStr(monday.AddDate(0, 0, -6)), sport("running")),
		aRide("y", dayStr(today)), // this week, whatever day today is
	)
	if _, err := d.CreatePlannedWorkout(models.PlannedWorkout{
		PlannedDate: monday.AddDate(0, 0, -5), Sport: "cycling", Title: "planned",
		DurationSecs: 3600, TSS: f64(70), Source: "manual",
	}); err != nil {
		t.Fatal(err)
	}
	tr, err := d.AnalyticsTrends("3m", now)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Bucket != "week" || len(tr.LoadWeeks) != 13 {
		t.Fatalf("3m: %s buckets, %d load weeks; want weeks, 13", tr.Bucket, len(tr.LoadWeeks))
	}
	var lastWeek *models.LoadBucket
	for i := range tr.LoadWeeks {
		if tr.LoadWeeks[i].Start == dayStr(monday.AddDate(0, 0, -7)) {
			lastWeek = &tr.LoadWeeks[i]
		}
	}
	if lastWeek == nil || lastWeek.Activities != 2 || lastWeek.TSS != 116 || lastWeek.PlannedTSS != 70 {
		t.Errorf("last week's load = %+v, want 2 activities (the run counts), 116 TSS, 70 planned", lastWeek)
	}
	b := bucketByStart(tr, dayStr(monday.AddDate(0, 0, -7)))
	if b == nil || b.CyclingDays != 1 || b.OtherDays != 1 || b.Days != 7 {
		t.Errorf("last week = %+v, want 1 cycling day, 1 other, 7 days", b)
	}
	if tr.Consistency.CurrentStreakDays != 1 || tr.Consistency.LongestStreakDays != 2 {
		t.Errorf("streaks = %+v, want current 1 (today), longest 2", tr.Consistency)
	}
	if len(tr.Fitness) == 0 {
		t.Error("no fitness history")
	}
}

func TestAnalyticsTrends_EmptyAndInvalid(t *testing.T) {
	d := newTestDB(t)
	for _, r := range []string{"", "3m", "6m", "1y", "2y", "all"} {
		tr, err := d.AnalyticsTrends(r, time.Now())
		if err != nil {
			t.Fatalf("range %q on an empty database: %v", r, err)
		}
		if len(tr.Rides) != 0 || len(tr.FTP.Estimate) != 0 || tr.Consistency.LongestStreakDays != 0 {
			t.Errorf("range %q: data on an empty database: %+v", r, tr)
		}
	}
	if _, err := d.AnalyticsTrends("5y", time.Now()); !errors.Is(err, db.ErrInvalidParam) {
		t.Errorf("range 5y: %v, want ErrInvalidParam", err)
	}
}

// ── Year over year ────────────────────────────────────────────────────────────

func TestYearOverYear(t *testing.T) {
	d := newTestDB(t)
	now := time.Now().UTC()
	y := now.Year()
	day := func(year int, md string) string {
		return time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).Format("2006") + "-" + md
	}
	insertRides(t, d,
		aRide("a", day(y-1, "03-01")),
		aRide("b", day(y-1, "03-01"), lasting(1800)),
		aRide("c", day(y-2, "03-01"), sport("running")),
		aRide("old", day(y-7, "06-01")), // beyond the five years
	)
	yy, err := d.YearOverYear("", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(yy.Years) != 1 || yy.Years[0].Year != y-1 {
		t.Fatalf("cycling years = %+v, want last year alone", yy.Years)
	}
	days := yy.Years[0].Days
	if len(days) != 1 || days[0].DurationSecs != 5400 || days[0].DayOfYear != 61 {
		t.Errorf("days = %+v, want one day of 5400 s on day 61 (March 1 on a leap calendar)", days)
	}
	all, err := d.YearOverYear(models.ScopeAll, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Years) != 2 {
		t.Errorf("all sports: %d years, want 2 (the run's year too)", len(all.Years))
	}
}

// The indoor line is the FTP indoor rides are scored against
// (GetFTPForRide), so a newer general entry takes it over: after a profile
// change to 280 W, indoor rides are scored at 280, and the chart must not
// keep drawing the older 230 W indoor entry to today.
func TestAnalyticsTrends_IndoorLineFollowsGeneralOverride(t *testing.T) {
	d := newTestDB(t)
	now := time.Now().UTC()
	today := timeutil.DateOf(now)
	if err := d.LogFTPChangeAt(250, today.AddDate(0, -10, 0)); err != nil {
		t.Fatal(err)
	}
	if err := d.LogFTPChangeAtFor(230, today.AddDate(0, -9, 0), true); err != nil {
		t.Fatal(err)
	}
	override := today.AddDate(0, -4, 0)
	if err := d.LogFTPChangeAt(280, override); err != nil {
		t.Fatal(err)
	}
	tr, err := d.AnalyticsTrends("1y", now)
	if err != nil {
		t.Fatal(err)
	}
	in := tr.FTP.Indoor
	if len(in) < 3 {
		t.Fatalf("indoor line = %+v, want 230 W, then 280 W from the general change, held to now", in)
	}
	if in[0].W != 230 || in[len(in)-1].W != 280 {
		t.Errorf("indoor line runs %d W to %d W, want 230 W to 280 W", in[0].W, in[len(in)-1].W)
	}
	var stepAt int64
	for _, p := range in {
		if p.W == 280 {
			stepAt = p.T
			break
		}
	}
	if stepAt != override.Unix() {
		t.Errorf("indoor line steps to 280 W at %d, want the general change at %d", stepAt, override.Unix())
	}
	// Every indoor point agrees with how an indoor ride at that time is scored.
	for _, p := range in {
		if got := d.GetFTPForRide(time.Unix(p.T, 0), true); got != p.W {
			t.Errorf("indoor line says %d W at %s; an indoor ride then is scored at %d W", p.W, time.Unix(p.T, 0).UTC(), got)
		}
	}
}

// Cadence comes from a cadence sensor, not a power meter: a rider without
// power still gets a cadence trend. A run's cadence (steps) never counts.
func TestAnalyticsTrends_CadenceWithoutPower(t *testing.T) {
	d := newTestDB(t)
	now := time.Now().UTC()
	today := timeutil.DateOf(now)
	thisMonth := timeutil.Date(today.Year(), today.Month(), 1)
	lastMonth := thisMonth.AddDate(0, -1, 0)
	insertRides(t, d,
		aRide("sensor", dayStr(lastMonth.AddDate(0, 0, 3)), noPower, func(w *models.Workout) { w.AvgCadenceRPM = iptr(84) }),
		aRide("run", dayStr(lastMonth.AddDate(0, 0, 4)), sport("running"), noPower, func(w *models.Workout) { w.AvgCadenceRPM = iptr(170) }),
	)
	tr, err := d.AnalyticsTrends("1y", now)
	if err != nil {
		t.Fatal(err)
	}
	b := bucketByStart(tr, dayStr(lastMonth))
	if b == nil || b.CadenceRPM.Outdoor == nil || b.CadenceRPM.Outdoor.Value != 84 || b.CadenceRPM.Outdoor.Rides != 1 {
		t.Errorf("cadence = %+v, want 84 rpm from the one ride without power (the run's 170 doesn't count)", b)
	}
}

// A change is taken between the numbers shown (IF and EF to two decimals,
// RPE to one, watts and bpm whole), so the table never says 0.75 against
// 0.75 changed by +0.00.
func TestComparePeriods_DeltaBetweenShownNumbers(t *testing.T) {
	d := newTestDB(t)
	insertRides(t, d,
		aRide("n", "2026-11-03", func(w *models.Workout) { w.IntensityFactor = f64(0.754) }),
		aRide("o", "2026-10-03", func(w *models.Workout) { w.IntensityFactor = f64(0.751) }),
		aRide("o2", "2026-10-04", func(w *models.Workout) { w.IntensityFactor = f64(0.751) }),
	)
	for id, rpe := range map[string]int{"n": 6, "o": 6, "o2": 7} {
		if err := d.SaveWorkoutFeedback(id, iptr(rpe), ""); err != nil {
			t.Fatal(err)
		}
	}
	c, err := d.ComparePeriods(novemberVsOctober(), "")
	if err != nil {
		t.Fatal(err)
	}
	if row := rowOf(t, c, "avg_if"); row == nil || val(row.A) != 0.75 || val(row.B) != 0.75 || val(row.Delta) != 0 {
		t.Errorf("IF row = %+v, want 0.75 against 0.75, no change", row)
	}
	if row := rowOf(t, c, "avg_rpe"); row == nil || val(row.B) != 6.5 || val(row.Delta) != -0.5 {
		t.Errorf("RPE row = %+v, want 6 against 6.5, a change of -0.5", row)
	}
	if row := rowOf(t, c, "avg_np_watts"); row == nil || val(row.A) != math.Round(val(row.A)) {
		t.Errorf("NP row = %+v, want whole watts", row)
	}
}

// Decoupling is judged by its distance from zero: drifting -3% is worse
// than +2%, not an improvement. Small moves get no verdict at all.
func TestComparePeriods_Verdicts(t *testing.T) {
	d := newTestDB(t)
	insertRides(t, d,
		aRide("n", "2026-11-08", lasting(2*3600), power(185, 139)),
		aRide("o", "2026-10-08", lasting(2*3600), power(185, 147)),
	)
	if err := d.SaveWorkoutDecoupling("n", f64(-3)); err != nil {
		t.Fatal(err)
	}
	if err := d.SaveWorkoutDecoupling("o", f64(2)); err != nil {
		t.Fatal(err)
	}
	c, err := d.ComparePeriods(novemberVsOctober(), "")
	if err != nil {
		t.Fatal(err)
	}
	if row := rowOf(t, c, "decoupling_pct"); row == nil || row.Better != "closer_to_zero" || row.Verdict != "worse" {
		t.Errorf("decoupling row = %+v, want closer_to_zero and worse (-3%% drifts further than +2%%)", row)
	}
	if row := rowOf(t, c, "hr_at_power_outdoor"); row == nil || row.Verdict != "better" {
		t.Errorf("HR at power row = %+v, want better: 139 against 147 bpm", row)
	}
	if row := rowOf(t, c, "ef_outdoor"); row == nil || row.Verdict != "better" {
		t.Errorf("EF row = %+v, want better", row)
	}
	if row := rowOf(t, c, "duration_secs"); row == nil || row.Verdict != "" {
		t.Errorf("time row = %+v, want no verdict", row)
	}
}

func TestComparePeriods_SmallChangeHasNoVerdict(t *testing.T) {
	d := newTestDB(t)
	insertRides(t, d, aRide("n", "2026-11-08", power(185, 140)), aRide("o", "2026-10-08", power(185, 141)))
	c, err := d.ComparePeriods(novemberVsOctober(), "")
	if err != nil {
		t.Fatal(err)
	}
	if row := rowOf(t, c, "hr_at_power_outdoor"); row == nil || row.Delta == nil || row.Verdict != "" {
		t.Errorf("HR at power row = %+v, want a change (140 against 141) but no verdict: under 2%%", row)
	}
}
