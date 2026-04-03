package db_test

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/models"
)

// testKey is a fixed 32-byte key used only in tests.
var testKey = []byte("fitbase-test-key-do-not-use-prod")

func newTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"), testKey)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func sampleWorkout(id string) *models.Workout {
	avgPower := 200.0
	maxPower := 350.0
	np := 215.0
	avgHR := 155
	maxHR := 178
	avgCad := 90
	tss := 75.0
	ifac := 0.86
	return &models.Workout{
		ID:                  id,
		Filename:            id + ".fit",
		RecordedAt:          time.Date(2024, 3, 15, 8, 0, 0, 0, time.UTC),
		Sport:               "cycling",
		DurationSecs:        3600,
		DistanceMeters:      36000,
		ElevationGainMeters: 450,
		AvgPowerWatts:       &avgPower,
		MaxPowerWatts:       &maxPower,
		NormalizedPower:     &np,
		AvgHeartRate:        &avgHR,
		MaxHeartRate:        &maxHR,
		AvgCadenceRPM:       &avgCad,
		AvgSpeedMPS:         10.0,
		TSS:                 &tss,
		IntensityFactor:     &ifac,
		CreatedAt:           time.Now().UTC(),
	}
}

// ── InsertWorkout / GetWorkout ────────────────────────────────────────────────

func TestInsertAndGetWorkout(t *testing.T) {
	d := newTestDB(t)
	w := sampleWorkout("abc123def456789a")
	streams := []models.Stream{
		{
			Timestamp: w.RecordedAt.Add(time.Second),
		},
	}

	if err := d.InsertWorkout(w, streams); err != nil {
		t.Fatalf("InsertWorkout: %v", err)
	}

	got, err := d.GetWorkout(w.ID)
	if err != nil {
		t.Fatalf("GetWorkout: %v", err)
	}
	if got == nil {
		t.Fatal("GetWorkout returned nil")
	}

	if got.ID != w.ID {
		t.Errorf("ID: got %q want %q", got.ID, w.ID)
	}
	if got.Sport != w.Sport {
		t.Errorf("Sport: got %q want %q", got.Sport, w.Sport)
	}
	if got.DurationSecs != w.DurationSecs {
		t.Errorf("DurationSecs: got %d want %d", got.DurationSecs, w.DurationSecs)
	}
	if got.DistanceMeters != w.DistanceMeters {
		t.Errorf("DistanceMeters: got %.1f want %.1f", got.DistanceMeters, w.DistanceMeters)
	}
	if got.AvgPowerWatts == nil || *got.AvgPowerWatts != *w.AvgPowerWatts {
		t.Errorf("AvgPower: got %v want %v", got.AvgPowerWatts, w.AvgPowerWatts)
	}
	if got.TSS == nil || *got.TSS != *w.TSS {
		t.Errorf("TSS: got %v want %v", got.TSS, w.TSS)
	}
}

func TestGetWorkout_NotFound(t *testing.T) {
	d := newTestDB(t)
	got, err := d.GetWorkout("doesnotexist")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Error("expected nil for missing workout")
	}
}

// ── WorkoutExists ─────────────────────────────────────────────────────────────

func TestWorkoutExists(t *testing.T) {
	d := newTestDB(t)
	w := sampleWorkout("existstest123456")

	exists, err := d.WorkoutExists(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("should not exist before insert")
	}

	if err := d.InsertWorkout(w, nil); err != nil {
		t.Fatal(err)
	}

	exists, err = d.WorkoutExists(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Error("should exist after insert")
	}
}

// ── ListWorkouts ──────────────────────────────────────────────────────────────

func TestListWorkouts_Empty(t *testing.T) {
	d := newTestDB(t)
	workouts, err := d.ListWorkouts(10, 0, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(workouts) != 0 {
		t.Errorf("expected 0 workouts, got %d", len(workouts))
	}
}

func TestListWorkouts_OrderedByDateDesc(t *testing.T) {
	d := newTestDB(t)

	w1 := sampleWorkout("w1aaaaaaaaaaaaaa")
	w1.RecordedAt = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	w2 := sampleWorkout("w2bbbbbbbbbbbbbb")
	w2.RecordedAt = time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	w3 := sampleWorkout("w3cccccccccccccc")
	w3.RecordedAt = time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)

	for _, w := range []*models.Workout{w1, w2, w3} {
		if err := d.InsertWorkout(w, nil); err != nil {
			t.Fatal(err)
		}
	}

	list, err := d.ListWorkouts(10, 0, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 workouts, got %d", len(list))
	}
	if list[0].ID != w2.ID {
		t.Errorf("first should be newest (w2), got %s", list[0].ID)
	}
	if list[2].ID != w1.ID {
		t.Errorf("last should be oldest (w1), got %s", list[2].ID)
	}
}

func TestListWorkouts_LimitAndOffset(t *testing.T) {
	d := newTestDB(t)
	for i := 0; i < 5; i++ {
		id := []byte("workout000000000")
		id[7] = byte('0' + i)
		w := sampleWorkout(string(id))
		w.RecordedAt = time.Date(2024, 1, i+1, 0, 0, 0, 0, time.UTC)
		if err := d.InsertWorkout(w, nil); err != nil {
			t.Fatal(err)
		}
	}

	page1, err := d.ListWorkouts(2, 0, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 2 {
		t.Errorf("limit=2 offset=0: got %d want 2", len(page1))
	}

	page2, err := d.ListWorkouts(2, 2, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 2 {
		t.Errorf("limit=2 offset=2: got %d want 2", len(page2))
	}

	// Pages should not overlap
	if page1[0].ID == page2[0].ID {
		t.Error("pages should not overlap")
	}
}

// ── GetStreams ────────────────────────────────────────────────────────────────

func TestGetStreams(t *testing.T) {
	d := newTestDB(t)
	w := sampleWorkout("streamtest123456")

	p1, p2 := 200, 210
	spd1, spd2 := 10.5, 10.7
	alt1, alt2 := 100.0, 105.0
	lat, lng := 51.5074, -0.1278
	dist1, dist2 := 10.0, 20.0

	streams := []models.Stream{
		{
			Timestamp:      w.RecordedAt.Add(time.Second),
			PowerWatts:     &p1,
			SpeedMPS:       &spd1,
			AltitudeMeters: &alt1,
			Lat:            &lat,
			Lng:            &lng,
			DistanceMeters: &dist1,
		},
		{
			Timestamp:      w.RecordedAt.Add(2 * time.Second),
			PowerWatts:     &p2,
			SpeedMPS:       &spd2,
			AltitudeMeters: &alt2,
			DistanceMeters: &dist2,
		},
	}

	if err := d.InsertWorkout(w, streams); err != nil {
		t.Fatal(err)
	}

	got, err := d.GetStreams(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 streams, got %d", len(got))
	}
	if got[0].PowerWatts == nil || *got[0].PowerWatts != p1 {
		t.Errorf("stream[0] power: got %v want %d", got[0].PowerWatts, p1)
	}
	if got[0].Lat == nil || *got[0].Lat != lat {
		t.Errorf("stream[0] lat: got %v want %f", got[0].Lat, lat)
	}
	if got[1].Lat != nil {
		t.Error("stream[1] lat: expected nil (not set)")
	}
}

func TestGetStreams_Empty(t *testing.T) {
	d := newTestDB(t)
	w := sampleWorkout("nostreamsworkout")
	if err := d.InsertWorkout(w, nil); err != nil {
		t.Fatal(err)
	}
	streams, err := d.GetStreams(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(streams) != 0 {
		t.Errorf("expected 0 streams, got %d", len(streams))
	}
}

// ── IsImported / MarkImported ─────────────────────────────────────────────────

func TestMarkAndIsImported(t *testing.T) {
	d := newTestDB(t)
	hash := "abc123def456abc123def456abc123def456abc123def456abc123def456abc1"

	ok, err := d.IsImported(hash)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("should not be imported yet")
	}

	if err := d.MarkImported(hash, "test.fit"); err != nil {
		t.Fatal(err)
	}

	ok, err = d.IsImported(hash)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("should be marked as imported")
	}
}

func TestMarkImported_Idempotent(t *testing.T) {
	d := newTestDB(t)
	hash := "dedup000000000000000000000000000000000000000000000000000000000000"

	if err := d.MarkImported(hash, "a.fit"); err != nil {
		t.Fatal(err)
	}
	// Inserting same hash again should not error (INSERT OR IGNORE)
	if err := d.MarkImported(hash, "b.fit"); err != nil {
		t.Errorf("second MarkImported should be idempotent: %v", err)
	}
}

// ── GetAthlete / UpdateAthlete ────────────────────────────────────────────────

func TestGetAthlete_Defaults(t *testing.T) {
	d := newTestDB(t)
	a, err := d.GetAthlete()
	if err != nil {
		t.Fatal(err)
	}
	if a.FTPWatts != 250 {
		t.Errorf("default FTP: got %d want 250", a.FTPWatts)
	}
	if a.WeightKG != 70.0 {
		t.Errorf("default weight: got %.1f want 70.0", a.WeightKG)
	}
	if a.ThresholdHR != 0 {
		t.Errorf("default ThresholdHR: got %d want 0", a.ThresholdHR)
	}
	if a.MaxHR != 0 {
		t.Errorf("default MaxHR: got %d want 0", a.MaxHR)
	}
}

func TestUpdateAthlete(t *testing.T) {
	d := newTestDB(t)

	a := &models.Athlete{FTPWatts: 280, WeightKG: 68.5, ThresholdHR: 174, MaxHR: 192}
	if err := d.UpdateAthlete(a); err != nil {
		t.Fatalf("UpdateAthlete: %v", err)
	}

	a, err := d.GetAthlete()
	if err != nil {
		t.Fatal(err)
	}
	if a.FTPWatts != 280 {
		t.Errorf("FTP: got %d want 280", a.FTPWatts)
	}
	if a.WeightKG != 68.5 {
		t.Errorf("Weight: got %.1f want 68.5", a.WeightKG)
	}
	if a.ThresholdHR != 174 {
		t.Errorf("ThresholdHR: got %d want 174", a.ThresholdHR)
	}
	if a.MaxHR != 192 {
		t.Errorf("MaxHR: got %d want 192", a.MaxHR)
	}
}

// ── GetFitnessHistory ─────────────────────────────────────────────────────────

func TestGetFitnessHistory_Empty(t *testing.T) {
	d := newTestDB(t)
	points, err := d.GetFitnessHistory(30)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 31 {
		t.Errorf("expected 31 points, got %d", len(points))
	}
	// All CTL/ATL should start at 0
	for _, p := range points {
		if p.Fitness != 0 || p.Fatigue != 0 {
			t.Errorf("empty history should have CTL=0 ATL=0, got CTL=%.2f ATL=%.2f", p.Fitness, p.Fatigue)
			break
		}
	}
}

func TestGetFitnessHistory_WithWorkouts(t *testing.T) {
	d := newTestDB(t)

	// Insert a workout with TSS yesterday
	tss := 100.0
	w := sampleWorkout("fitnessworkout00")
	w.RecordedAt = time.Now().UTC().AddDate(0, 0, -1)
	w.TSS = &tss
	if err := d.InsertWorkout(w, nil); err != nil {
		t.Fatal(err)
	}

	points, err := d.GetFitnessHistory(7)
	if err != nil {
		t.Fatal(err)
	}
	// CTL and ATL should be non-zero by the end
	last := points[len(points)-1]
	if last.Fitness == 0 && last.Fatigue == 0 {
		t.Error("expected non-zero CTL/ATL after workout")
	}
	// TSB = CTL - ATL
	for _, p := range points {
		if p.Form != p.Fitness-p.Fatigue {
			t.Errorf("TSB should equal CTL-ATL: %.2f != %.2f - %.2f", p.Form, p.Fitness, p.Fatigue)
		}
	}
}

// TestFitnessOnDate_MatchesChart verifies that GetFitnessOnDate returns the same
// CTL/ATL/TSB values as the corresponding day in GetFitnessHistory. This ensures
// the workout detail page stats match the fitness chart for every date.
func TestFitnessOnDate_MatchesChart(t *testing.T) {
	d := newTestDB(t)

	// Insert workouts on several different days with varying TSS.
	today := time.Now().UTC().Truncate(24 * time.Hour)
	workouts := []struct {
		daysAgo int
		tss     float64
	}{
		{30, 120.0},
		{25, 80.0},
		{20, 150.0},
		{15, 60.0},
		{10, 200.0},
		{7, 90.0},
		{5, 110.0},
		{3, 75.0},
		{1, 130.0},
	}
	for i, wk := range workouts {
		tss := wk.tss
		w := sampleWorkout(fmt.Sprintf("fitnessmatch%04d", i))
		w.RecordedAt = today.AddDate(0, 0, -wk.daysAgo).Add(8 * time.Hour)
		w.TSS = &tss
		if err := d.InsertWorkout(w, nil); err != nil {
			t.Fatalf("insert workout %d: %v", i, err)
		}
	}

	// Get the full chart history covering all workout dates.
	const chartDays = 45
	history, err := d.GetFitnessHistory(chartDays)
	if err != nil {
		t.Fatalf("GetFitnessHistory: %v", err)
	}
	if len(history) != chartDays+1 {
		t.Fatalf("expected %d points, got %d", chartDays+1, len(history))
	}

	// Build a lookup by date string.
	chartByDate := make(map[string]models.FitnessPoint, len(history))
	for _, p := range history {
		chartByDate[p.Date.Format("2006-01-02")] = p
	}

	// For each point in the chart, GetFitnessOnDate must return identical values.
	for _, chartPt := range history {
		fp, err := d.GetFitnessOnDate(chartPt.Date)
		if err != nil {
			t.Fatalf("GetFitnessOnDate(%s): %v", chartPt.Date.Format("2006-01-02"), err)
		}

		dateStr := chartPt.Date.Format("2006-01-02")
		if fp.Fitness != chartPt.Fitness {
			t.Errorf("%s: CTL mismatch — GetFitnessOnDate=%.9f, chart=%.9f", dateStr, fp.Fitness, chartPt.Fitness)
		}
		if fp.Fatigue != chartPt.Fatigue {
			t.Errorf("%s: ATL mismatch — GetFitnessOnDate=%.9f, chart=%.9f", dateStr, fp.Fatigue, chartPt.Fatigue)
		}
		if fp.Form != chartPt.Form {
			t.Errorf("%s: TSB mismatch — GetFitnessOnDate=%.9f, chart=%.9f", dateStr, fp.Form, chartPt.Form)
		}
	}
}

// TestFitnessOnDate_MatchesChartForChart verifies that GetFitnessHistoryForChart
// (used by the dashboard) produces the same values as GetFitnessHistory for the
// overlapping date range, and that projected days decay correctly.
func TestFitnessOnDate_MatchesChartForChart(t *testing.T) {
	d := newTestDB(t)

	today := time.Now().UTC().Truncate(24 * time.Hour)
	// Insert a week of daily workouts so there's meaningful CTL/ATL to project.
	for i := 0; i < 7; i++ {
		tss := 100.0 + float64(i*10)
		w := sampleWorkout(fmt.Sprintf("chartproj%04d", i))
		w.RecordedAt = today.AddDate(0, 0, -(7 - i)).Add(9 * time.Hour)
		w.TSS = &tss
		if err := d.InsertWorkout(w, nil); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	const days = 30
	const projection = 4

	plain, err := d.GetFitnessHistory(days)
	if err != nil {
		t.Fatal(err)
	}
	withProj, err := d.GetFitnessHistoryForChart(days, projection)
	if err != nil {
		t.Fatal(err)
	}

	// Chart version should have extra projection days.
	if len(withProj) != len(plain)+projection {
		t.Fatalf("expected %d points (chart), got %d; plain had %d", len(plain)+projection, len(withProj), len(plain))
	}

	// The overlapping portion must be identical.
	for i := 0; i < len(plain); i++ {
		dateStr := plain[i].Date.Format("2006-01-02")
		if plain[i].Fitness != withProj[i].Fitness {
			t.Errorf("%s: CTL plain=%.9f chart=%.9f", dateStr, plain[i].Fitness, withProj[i].Fitness)
		}
		if plain[i].Fatigue != withProj[i].Fatigue {
			t.Errorf("%s: ATL plain=%.9f chart=%.9f", dateStr, plain[i].Fatigue, withProj[i].Fatigue)
		}
		if plain[i].Form != withProj[i].Form {
			t.Errorf("%s: TSB plain=%.9f chart=%.9f", dateStr, plain[i].Form, withProj[i].Form)
		}
	}

	// Projected days should show decaying fatigue (no new TSS).
	lastReal := withProj[len(plain)-1]
	for i := len(plain); i < len(withProj); i++ {
		proj := withProj[i]
		// Both CTL and ATL should decay toward zero (be less than previous real values).
		if proj.Fatigue >= lastReal.Fatigue {
			t.Errorf("projection day %s: ATL %.4f should be less than last real %.4f",
				proj.Date.Format("2006-01-02"), proj.Fatigue, lastReal.Fatigue)
		}
	}
}

// TestFitnessOnDate_MultipleWorkoutsSameDay verifies that multiple workouts on the
// same day have their TSS summed correctly in fitness calculations.
func TestFitnessOnDate_MultipleWorkoutsSameDay(t *testing.T) {
	d := newTestDB(t)

	today := time.Now().UTC().Truncate(24 * time.Hour)
	daysAgo := 5

	// Insert two workouts on the same day.
	tss1, tss2 := 60.0, 80.0
	w1 := sampleWorkout("multifit0001abc")
	w1.RecordedAt = today.AddDate(0, 0, -daysAgo).Add(8 * time.Hour)
	w1.TSS = &tss1

	w2 := sampleWorkout("multifit0002abc")
	w2.RecordedAt = today.AddDate(0, 0, -daysAgo).Add(17 * time.Hour)
	w2.TSS = &tss2

	if err := d.InsertWorkout(w1, nil); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertWorkout(w2, nil); err != nil {
		t.Fatal(err)
	}

	// GetFitnessOnDate for that day should reflect the combined TSS.
	fp, err := d.GetFitnessOnDate(today.AddDate(0, 0, -daysAgo))
	if err != nil {
		t.Fatal(err)
	}

	// Also check via chart history.
	history, err := d.GetFitnessHistory(daysAgo)
	if err != nil {
		t.Fatal(err)
	}

	chartPt := history[0] // first point = target date (today - daysAgo)
	if fp.Fitness != chartPt.Fitness {
		t.Errorf("CTL mismatch: OnDate=%.9f, chart=%.9f", fp.Fitness, chartPt.Fitness)
	}
	if fp.Fatigue != chartPt.Fatigue {
		t.Errorf("ATL mismatch: OnDate=%.9f, chart=%.9f", fp.Fatigue, chartPt.Fatigue)
	}
	if fp.Form != chartPt.Form {
		t.Errorf("TSB mismatch: OnDate=%.9f, chart=%.9f", fp.Form, chartPt.Form)
	}

	// Verify TSS was actually summed — CTL should reflect ~140 TSS, not just 60 or 80.
	// After warmup+daysAgo from zero, a single 140 TSS day gives CTL = (1/42)*140 ≈ 3.33
	if fp.Fitness < 2.0 {
		t.Errorf("CTL %.4f too low — TSS may not be summing correctly", fp.Fitness)
	}
}

// ── Integration tokens ────────────────────────────────────────────────────────

func TestIntegrationTokenRoundTrip(t *testing.T) {
	d := newTestDB(t)

	// No token initially
	tok, err := d.GetIntegrationToken("gdrive")
	if err != nil {
		t.Fatal(err)
	}
	if tok != "" {
		t.Error("expected empty token initially")
	}

	// Set token
	if err := d.SetIntegrationToken("gdrive", `{"token":"abc"}`); err != nil {
		t.Fatal(err)
	}

	tok, err = d.GetIntegrationToken("gdrive")
	if err != nil {
		t.Fatal(err)
	}
	if tok != `{"token":"abc"}` {
		t.Errorf("token: got %q want %q", tok, `{"token":"abc"}`)
	}

	// Update token
	if err := d.SetIntegrationToken("gdrive", `{"token":"xyz"}`); err != nil {
		t.Fatal(err)
	}
	tok, _ = d.GetIntegrationToken("gdrive")
	if tok != `{"token":"xyz"}` {
		t.Errorf("updated token: got %q", tok)
	}

	// Delete token
	if err := d.DeleteIntegrationToken("gdrive"); err != nil {
		t.Fatal(err)
	}
	tok, _ = d.GetIntegrationToken("gdrive")
	if tok != "" {
		t.Errorf("expected empty after delete, got %q", tok)
	}
}

func TestIntegrationToken_MultipleIntegrations(t *testing.T) {
	d := newTestDB(t)
	_ = d.SetIntegrationToken("gdrive", "gdrive-token")
	_ = d.SetIntegrationToken("wahoo", "wahoo-token")

	g, _ := d.GetIntegrationToken("gdrive")
	w, _ := d.GetIntegrationToken("wahoo")
	if g != "gdrive-token" {
		t.Errorf("gdrive: got %q", g)
	}
	if w != "wahoo-token" {
		t.Errorf("wahoo: got %q", w)
	}
}

// ── FindDuplicateWorkout ─────────────────────────────────────────────────────

func TestFindDuplicateWorkout_ExactMatch(t *testing.T) {
	d := newTestDB(t)
	w := sampleWorkout("dup_original_1234")
	if err := d.InsertWorkout(w, nil); err != nil {
		t.Fatal(err)
	}

	// Same timestamp, sport, duration → should find duplicate.
	dupID, err := d.FindDuplicateWorkout(w.RecordedAt, w.Sport, w.DurationSecs)
	if err != nil {
		t.Fatal(err)
	}
	if dupID != w.ID {
		t.Errorf("expected duplicate %q, got %q", w.ID, dupID)
	}
}

func TestFindDuplicateWorkout_WithinWindow(t *testing.T) {
	d := newTestDB(t)
	w := sampleWorkout("dup_window_12345")
	if err := d.InsertWorkout(w, nil); err != nil {
		t.Fatal(err)
	}

	// 30 seconds off, 2% duration difference → still a duplicate.
	dupID, err := d.FindDuplicateWorkout(
		w.RecordedAt.Add(30*time.Second), w.Sport, w.DurationSecs+50)
	if err != nil {
		t.Fatal(err)
	}
	if dupID != w.ID {
		t.Errorf("expected duplicate %q, got %q", w.ID, dupID)
	}
}

func TestFindDuplicateWorkout_NoMatch_DifferentSport(t *testing.T) {
	d := newTestDB(t)
	w := sampleWorkout("dup_sport_123456")
	if err := d.InsertWorkout(w, nil); err != nil {
		t.Fatal(err)
	}

	dupID, err := d.FindDuplicateWorkout(w.RecordedAt, "running", w.DurationSecs)
	if err != nil {
		t.Fatal(err)
	}
	if dupID != "" {
		t.Errorf("expected no duplicate for different sport, got %q", dupID)
	}
}

func TestFindDuplicateWorkout_NoMatch_TooFarApart(t *testing.T) {
	d := newTestDB(t)
	w := sampleWorkout("dup_time_12345678")
	if err := d.InsertWorkout(w, nil); err != nil {
		t.Fatal(err)
	}

	// 2 minutes apart → outside the 60s window.
	dupID, err := d.FindDuplicateWorkout(
		w.RecordedAt.Add(2*time.Minute), w.Sport, w.DurationSecs)
	if err != nil {
		t.Fatal(err)
	}
	if dupID != "" {
		t.Errorf("expected no duplicate for distant timestamp, got %q", dupID)
	}
}

func TestFindDuplicateWorkout_NoMatch_Empty(t *testing.T) {
	d := newTestDB(t)

	dupID, err := d.FindDuplicateWorkout(time.Now(), "cycling", 3600)
	if err != nil {
		t.Fatal(err)
	}
	if dupID != "" {
		t.Errorf("expected no duplicate in empty DB, got %q", dupID)
	}
}
