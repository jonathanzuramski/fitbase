package db_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/models"
	_ "modernc.org/sqlite"
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

// ── FTP history ───────────────────────────────────────────────────────────────

func TestLogFTPChangeAt(t *testing.T) {
	d := newTestDB(t)

	at := time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC)
	if err := d.LogFTPChangeAt(280, at); err != nil {
		t.Fatalf("LogFTPChangeAt: %v", err)
	}

	// A workout recorded after the change should use the new FTP.
	got := d.GetFTPAtDate(at.Add(24 * time.Hour))
	if got != 280 {
		t.Errorf("GetFTPAtDate after change: got %d, want 280", got)
	}

	// A workout recorded before the change should fall back to the athlete default (250).
	got = d.GetFTPAtDate(at.Add(-24 * time.Hour))
	if got != 250 {
		t.Errorf("GetFTPAtDate before change: got %d, want 250 (default)", got)
	}
}

func TestClearFTPHistory(t *testing.T) {
	d := newTestDB(t)

	at := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := d.LogFTPChangeAt(300, at); err != nil {
		t.Fatalf("LogFTPChangeAt: %v", err)
	}

	has, err := d.HasFTPHistory()
	if err != nil || !has {
		t.Fatalf("HasFTPHistory after insert: got (%v,%v), want (true,nil)", has, err)
	}

	if err := d.ClearFTPHistory(); err != nil {
		t.Fatalf("ClearFTPHistory: %v", err)
	}

	has, err = d.HasFTPHistory()
	if err != nil || has {
		t.Errorf("HasFTPHistory after clear: got (%v,%v), want (false,nil)", has, err)
	}
}

func TestClearFTPHistory_Idempotent(t *testing.T) {
	d := newTestDB(t)
	// Clearing an already-empty table must not error.
	if err := d.ClearFTPHistory(); err != nil {
		t.Errorf("ClearFTPHistory on empty table: %v", err)
	}
}

// ── AllWorkoutsForTSSBackfill / UpdateWorkoutLoad ─────────────────────────────

func TestAllWorkoutsForTSSBackfill_OnlyReturnsWithNP(t *testing.T) {
	d := newTestDB(t)

	// Insert one workout that has NP (sampleWorkout sets NP=215).
	withNP := sampleWorkout("with-np-0000001")
	if err := d.InsertWorkout(withNP, nil); err != nil {
		t.Fatalf("InsertWorkout (with NP): %v", err)
	}

	// Insert one without NP.
	noNP := sampleWorkout("no-np-00000002")
	noNP.NormalizedPower = nil
	noNP.TSS = nil
	noNP.IntensityFactor = nil
	if err := d.InsertWorkout(noNP, nil); err != nil {
		t.Fatalf("InsertWorkout (no NP): %v", err)
	}

	rows, err := d.AllWorkoutsForTSSBackfill()
	if err != nil {
		t.Fatalf("AllWorkoutsForTSSBackfill: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].ID != withNP.ID {
		t.Errorf("row ID = %q, want %q", rows[0].ID, withNP.ID)
	}
	if rows[0].NormalizedPower != 215.0 {
		t.Errorf("NormalizedPower = %.1f, want 215.0", rows[0].NormalizedPower)
	}
	if rows[0].DurationSecs != withNP.DurationSecs {
		t.Errorf("DurationSecs = %d, want %d", rows[0].DurationSecs, withNP.DurationSecs)
	}
}

func TestUpdateWorkoutLoad(t *testing.T) {
	d := newTestDB(t)

	w := sampleWorkout("load-test-000001")
	if err := d.InsertWorkout(w, nil); err != nil {
		t.Fatalf("InsertWorkout: %v", err)
	}

	if err := d.UpdateWorkoutLoad(w.ID, 99.9, 0.777); err != nil {
		t.Fatalf("UpdateWorkoutLoad: %v", err)
	}

	got, err := d.GetWorkout(w.ID)
	if err != nil {
		t.Fatalf("GetWorkout: %v", err)
	}
	if got.TSS == nil || *got.TSS != 99.9 {
		t.Errorf("TSS = %v, want 99.9", got.TSS)
	}
	if got.IntensityFactor == nil || *got.IntensityFactor != 0.777 {
		t.Errorf("IntensityFactor = %v, want 0.777", got.IntensityFactor)
	}
}

// ── AllFTPHistory / DeleteFTPHistoryEntry ────────────────────────────────────

func TestAllFTPHistory_Empty(t *testing.T) {
	d := newTestDB(t)
	entries, err := d.AllFTPHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestAllFTPHistory_OrdersNewestFirst(t *testing.T) {
	d := newTestDB(t)

	older := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	if err := d.LogFTPChangeAt(240, older); err != nil {
		t.Fatal(err)
	}
	if err := d.LogFTPChangeAt(260, newer); err != nil {
		t.Fatal(err)
	}

	entries, err := d.AllFTPHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].FTPWatts != 260 {
		t.Errorf("first entry FTP = %d, want 260 (newer should sort first)", entries[0].FTPWatts)
	}
	if entries[1].FTPWatts != 240 {
		t.Errorf("second entry FTP = %d, want 240", entries[1].FTPWatts)
	}
	if !entries[0].EffectiveFrom.Equal(newer) {
		t.Errorf("EffectiveFrom = %v, want %v", entries[0].EffectiveFrom, newer)
	}
	if entries[0].ID == 0 {
		t.Error("expected non-zero auto-increment ID")
	}
}

func TestDeleteFTPHistoryEntry(t *testing.T) {
	d := newTestDB(t)

	if err := d.LogFTPChangeAt(240, time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if err := d.LogFTPChangeAt(260, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	entries, _ := d.AllFTPHistory()
	if len(entries) != 2 {
		t.Fatalf("setup: expected 2 entries, got %d", len(entries))
	}
	targetID := entries[0].ID

	if err := d.DeleteFTPHistoryEntry(targetID); err != nil {
		t.Fatalf("DeleteFTPHistoryEntry: %v", err)
	}

	after, _ := d.AllFTPHistory()
	if len(after) != 1 {
		t.Fatalf("expected 1 entry after delete, got %d", len(after))
	}
	if after[0].ID == targetID {
		t.Error("deleted entry still present")
	}
}

func TestDeleteFTPHistoryEntry_MissingIDIsNoop(t *testing.T) {
	d := newTestDB(t)
	// Deleting a non-existent row must not error — UI form might double-submit.
	if err := d.DeleteFTPHistoryEntry(99999); err != nil {
		t.Errorf("delete missing id should not error, got: %v", err)
	}
}

// ── RecomputePowerLoad ────────────────────────────────────────────────────────

func TestRecomputePowerLoad_UsesFTPAtDate(t *testing.T) {
	d := newTestDB(t)

	// Two FTP eras: 200W from 2023-01-01, 250W from 2024-01-01.
	if err := d.LogFTPChangeAt(200, time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if err := d.LogFTPChangeAt(250, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	// Workout in the 200W era.
	wOld := sampleWorkout("recomp-old-000001")
	wOld.RecordedAt = time.Date(2023, 6, 1, 12, 0, 0, 0, time.UTC)
	if err := d.InsertWorkout(wOld, nil); err != nil {
		t.Fatal(err)
	}

	// Workout in the 250W era.
	wNew := sampleWorkout("recomp-new-000001")
	wNew.RecordedAt = time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	if err := d.InsertWorkout(wNew, nil); err != nil {
		t.Fatal(err)
	}

	updated, err := d.RecomputePowerLoad(nil)
	if err != nil {
		t.Fatalf("RecomputePowerLoad: %v", err)
	}
	if updated != 2 {
		t.Errorf("updated = %d, want 2", updated)
	}

	got, _ := d.GetWorkout(wOld.ID)
	if got.IntensityFactor == nil {
		t.Fatal("old workout IF nil")
	}
	// IF for older workout uses NP=215, FTP=200 → 1.075
	if *got.IntensityFactor < 1.07 || *got.IntensityFactor > 1.08 {
		t.Errorf("old workout IF = %.4f, want ~1.075 (NP/200)", *got.IntensityFactor)
	}

	got, _ = d.GetWorkout(wNew.ID)
	if got.IntensityFactor == nil {
		t.Fatal("new workout IF nil")
	}
	// IF for newer workout uses FTP=250 → 0.86
	if *got.IntensityFactor < 0.85 || *got.IntensityFactor > 0.87 {
		t.Errorf("new workout IF = %.4f, want ~0.86 (NP/250)", *got.IntensityFactor)
	}
}

func TestRecomputePowerLoad_ReportsProgress(t *testing.T) {
	d := newTestDB(t)

	if err := d.LogFTPChangeAt(250, time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	for i := range 3 {
		w := sampleWorkout(fmt.Sprintf("progress-%010d", i))
		w.RecordedAt = time.Date(2024, 1, i+1, 0, 0, 0, 0, time.UTC)
		if err := d.InsertWorkout(w, nil); err != nil {
			t.Fatal(err)
		}
	}

	type call struct{ done, total int }
	var calls []call
	if _, err := d.RecomputePowerLoad(func(done, total int) {
		calls = append(calls, call{done, total})
	}); err != nil {
		t.Fatal(err)
	}

	if len(calls) < 4 {
		t.Fatalf("expected at least 4 progress calls (0 + 3 workouts), got %d", len(calls))
	}
	if calls[0] != (call{0, 3}) {
		t.Errorf("first progress call = %+v, want {0, 3}", calls[0])
	}
	last := calls[len(calls)-1]
	if last.done != 3 || last.total != 3 {
		t.Errorf("last progress call = %+v, want {3, 3}", last)
	}
}

func TestRecomputePowerLoad_Empty(t *testing.T) {
	d := newTestDB(t)
	updated, err := d.RecomputePowerLoad(nil)
	if err != nil {
		t.Fatal(err)
	}
	if updated != 0 {
		t.Errorf("updated = %d, want 0", updated)
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

// ── GetWorkoutRouteTracks ─────────────────────────────────────────────────────

func gpsStream(ts time.Time, lat, lng float64) models.Stream {
	return models.Stream{Timestamp: ts, Lat: &lat, Lng: &lng}
}

func TestGetWorkoutRouteTracks_Empty(t *testing.T) {
	d := newTestDB(t)
	tracks, err := d.GetWorkoutRouteTracks(nil)
	if err != nil {
		t.Fatalf("GetWorkoutRouteTracks: %v", err)
	}
	if len(tracks) != 0 {
		t.Errorf("expected 0 tracks, got %d", len(tracks))
	}
}

func TestGetWorkoutRouteTracks_OutdoorWithGPS(t *testing.T) {
	d := newTestDB(t)
	w := sampleWorkout("outdoor1aaaaaaaa")
	w.IsIndoor = false
	// Points are intentionally non-collinear: route_coords is RDP-simplified,
	// which (correctly) drops a midpoint lying on the line between its
	// neighbours. A bent path keeps all three so this test exercises the
	// round-trip and [lng, lat] ordering, not the simplifier's thresholds.
	streams := []models.Stream{
		gpsStream(w.RecordedAt.Add(1*time.Second), 47.1, -122.1),
		gpsStream(w.RecordedAt.Add(2*time.Second), 47.2, -122.05),
		gpsStream(w.RecordedAt.Add(3*time.Second), 47.3, -122.3),
	}
	if err := d.InsertWorkout(w, streams); err != nil {
		t.Fatalf("InsertWorkout: %v", err)
	}

	tracks, err := d.GetWorkoutRouteTracks(nil)
	if err != nil {
		t.Fatalf("GetWorkoutRouteTracks: %v", err)
	}
	if len(tracks) != 1 {
		t.Fatalf("expected 1 track, got %d", len(tracks))
	}
	tr := tracks[0]
	if tr.WorkoutID != w.ID {
		t.Errorf("WorkoutID: got %q want %q", tr.WorkoutID, w.ID)
	}
	if tr.Sport != "cycling" {
		t.Errorf("Sport: got %q want %q", tr.Sport, "cycling")
	}
	if len(tr.Coords) != 3 {
		t.Fatalf("expected 3 coords, got %d", len(tr.Coords))
	}
	// Coords must be in GeoJSON [lng, lat] order.
	if tr.Coords[0][0] != -122.1 || tr.Coords[0][1] != 47.1 {
		t.Errorf("coord[0]: got [%v, %v] want [-122.1, 47.1]", tr.Coords[0][0], tr.Coords[0][1])
	}
}

func TestGetWorkoutRouteTracks_IndoorExcluded(t *testing.T) {
	d := newTestDB(t)
	w := sampleWorkout("indoor1aaaaaaaaa")
	w.IsIndoor = true
	if err := d.InsertWorkout(w, []models.Stream{
		gpsStream(w.RecordedAt.Add(1*time.Second), 47.1, -122.1),
		gpsStream(w.RecordedAt.Add(2*time.Second), 47.2, -122.2),
	}); err != nil {
		t.Fatalf("InsertWorkout: %v", err)
	}

	tracks, err := d.GetWorkoutRouteTracks(nil)
	if err != nil {
		t.Fatalf("GetWorkoutRouteTracks: %v", err)
	}
	if len(tracks) != 0 {
		t.Errorf("indoor workout should be excluded from nil-ids query, got %d tracks", len(tracks))
	}
}

func TestGetWorkoutRouteTracks_SpecificIDs(t *testing.T) {
	d := newTestDB(t)
	w1 := sampleWorkout("outdoor1aaaaaaaa")
	w1.IsIndoor = false
	w2 := sampleWorkout("outdoor2bbbbbbbb")
	w2.IsIndoor = false
	for _, w := range []*models.Workout{w1, w2} {
		if err := d.InsertWorkout(w, []models.Stream{
			gpsStream(w.RecordedAt.Add(1*time.Second), 47.1, -122.1),
			gpsStream(w.RecordedAt.Add(2*time.Second), 47.2, -122.2),
		}); err != nil {
			t.Fatalf("InsertWorkout %s: %v", w.ID, err)
		}
	}

	tracks, err := d.GetWorkoutRouteTracks([]string{w1.ID})
	if err != nil {
		t.Fatalf("GetWorkoutRouteTracks: %v", err)
	}
	if len(tracks) != 1 {
		t.Fatalf("expected 1 track, got %d", len(tracks))
	}
	if tracks[0].WorkoutID != w1.ID {
		t.Errorf("WorkoutID: got %q want %q", tracks[0].WorkoutID, w1.ID)
	}
}

func TestGetWorkoutRouteTracks_Downsampling(t *testing.T) {
	d := newTestDB(t)
	w := sampleWorkout("downsamp1aaaaaaa")
	w.IsIndoor = false

	const nPts = 400
	streams := make([]models.Stream, nPts)
	for i := range streams {
		lat := 47.0 + float64(i)*0.0001
		lng := -122.0 - float64(i)*0.0001
		streams[i] = gpsStream(w.RecordedAt.Add(time.Duration(i+1)*time.Second), lat, lng)
	}
	if err := d.InsertWorkout(w, streams); err != nil {
		t.Fatalf("InsertWorkout: %v", err)
	}

	tracks, err := d.GetWorkoutRouteTracks(nil)
	if err != nil {
		t.Fatalf("GetWorkoutRouteTracks: %v", err)
	}
	if len(tracks) != 1 {
		t.Fatalf("expected 1 track, got %d", len(tracks))
	}
	n := len(tracks[0].Coords)
	if n > 201 {
		t.Errorf("expected ≤201 coords after downsampling 400 points, got %d", n)
	}
	if n < 2 {
		t.Errorf("expected at least 2 coords, got %d", n)
	}
	// Last coord must always be the final GPS point.
	last := tracks[0].Coords[len(tracks[0].Coords)-1]
	wantLng := -122.0 - float64(nPts-1)*0.0001
	wantLat := 47.0 + float64(nPts-1)*0.0001
	if last[0] != wantLng || last[1] != wantLat {
		t.Errorf("last coord: got [%v, %v] want [%v, %v]", last[0], last[1], wantLng, wantLat)
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

// ── imported_files composite PK ───────────────────────────────────────────────

// Same content imported under two different filenames must record both.
// Regression test for the infinite re-download loop where INSERT OR IGNORE
// silently dropped the second filename when the PK was hash-only.
func TestMarkImported_TwoFilenamesPerHash(t *testing.T) {
	d := newTestDB(t)

	if err := d.MarkImported("hashA", "archive-2024-03-15.fit"); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkImported("hashA", "intervals-i12345.fit"); err != nil {
		t.Fatal(err)
	}

	names, err := d.AllImportedFilenames()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := names["archive-2024-03-15.fit"]; !ok {
		t.Errorf("archive filename missing from AllImportedFilenames: %v", names)
	}
	if _, ok := names["intervals-i12345.fit"]; !ok {
		t.Errorf("intervals filename missing from AllImportedFilenames: %v", names)
	}

	// Hash-based dedup still works.
	imported, err := d.IsImported("hashA")
	if err != nil {
		t.Fatal(err)
	}
	if !imported {
		t.Error("IsImported(hashA) = false, want true")
	}
}

// MarkImported with an exact (hash, filename) pair already present is a no-op.
func TestMarkImported_SameHashSameFilenameIdempotent(t *testing.T) {
	d := newTestDB(t)

	for range 3 {
		if err := d.MarkImported("hashB", "same.fit"); err != nil {
			t.Fatal(err)
		}
	}

	names, err := d.AllImportedFilenames()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 {
		t.Errorf("got %d filenames, want 1: %v", len(names), names)
	}
}

// Open() must convert a pre-existing hash-only PK table into the composite-PK
// shape, preserving rows.
func TestOpen_MigratesImportedFilesPK(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "old.db")

	// Build an old-schema DB by hand.
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
		CREATE TABLE imported_files (
			hash       TEXT PRIMARY KEY,
			filename   TEXT NOT NULL,
			imported_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
		);
		INSERT INTO imported_files (hash, filename) VALUES ('h1', 'archive.fit');
	`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	// Open via the production path — migration should run.
	d, err := db.Open(dbPath, testKey)
	if err != nil {
		t.Fatalf("open after migration: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	// Pre-existing row preserved.
	imported, err := d.IsImported("h1")
	if err != nil {
		t.Fatal(err)
	}
	if !imported {
		t.Error("pre-migration row 'h1' missing after migration")
	}

	// Composite PK now active: a second filename for the same hash sticks.
	if err := d.MarkImported("h1", "intervals.fit"); err != nil {
		t.Fatal(err)
	}
	names, err := d.AllImportedFilenames()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := names["archive.fit"]; !ok {
		t.Errorf("archive.fit missing post-migration: %v", names)
	}
	if _, ok := names["intervals.fit"]; !ok {
		t.Errorf("intervals.fit missing post-migration: %v", names)
	}
}

// ── Zone times (Sweet Spot) ───────────────────────────────────────────────────

func TestZoneTimes_RoundTripWithSS(t *testing.T) {
	d := newTestDB(t)
	w := sampleWorkout("zonetimes000001a")
	if err := d.InsertWorkout(w, nil); err != nil {
		t.Fatalf("InsertWorkout: %v", err)
	}
	power := [7]int{60, 120, 300, 200, 30, 0, 0}
	hr := [5]int{100, 200, 150, 50, 0}
	if err := d.InsertZoneTimes(w.ID, power, hr, 420); err != nil {
		t.Fatalf("InsertZoneTimes: %v", err)
	}

	gotPower, gotHR, gotSS, err := d.GetZoneTimes(w.ID)
	if err != nil {
		t.Fatalf("GetZoneTimes: %v", err)
	}
	if gotPower == nil || *gotPower != power {
		t.Errorf("power = %v, want %v", gotPower, power)
	}
	if gotHR == nil || *gotHR != hr {
		t.Errorf("hr = %v, want %v", gotHR, hr)
	}
	if gotSS == nil || *gotSS != 420 {
		t.Errorf("ss = %v, want 420", gotSS)
	}
}

func TestZoneTimes_NullSSSurfacesInBackfillQueue(t *testing.T) {
	d := newTestDB(t)
	w := sampleWorkout("zonetimes000002b")
	if err := d.InsertWorkout(w, nil); err != nil {
		t.Fatalf("InsertWorkout: %v", err)
	}
	// ss = -1 → store SQL NULL (FTP unknown at import time).
	if err := d.InsertZoneTimes(w.ID, [7]int{}, [5]int{}, -1); err != nil {
		t.Fatalf("InsertZoneTimes: %v", err)
	}

	_, _, gotSS, err := d.GetZoneTimes(w.ID)
	if err != nil {
		t.Fatalf("GetZoneTimes: %v", err)
	}
	if gotSS != nil {
		t.Errorf("ss = %v, want nil (NULL round-trips as nil pointer)", *gotSS)
	}

	ids, err := d.WorkoutIDsWithoutSSZone()
	if err != nil {
		t.Fatalf("WorkoutIDsWithoutSSZone: %v", err)
	}
	if !slices.Contains(ids, w.ID) {
		t.Errorf("WorkoutIDsWithoutSSZone = %v, want to contain %s", ids, w.ID)
	}
}

func TestZoneTimes_NoRowReturnsNil(t *testing.T) {
	d := newTestDB(t)
	p, h, ss, err := d.GetZoneTimes("doesnotexist0001")
	if err != nil {
		t.Fatalf("GetZoneTimes: %v", err)
	}
	if p != nil || h != nil || ss != nil {
		t.Errorf("missing row should give all-nil, got %v %v %v", p, h, ss)
	}
}

func TestSetSSZoneSecs_FillsNullAndClearsQueue(t *testing.T) {
	d := newTestDB(t)
	w := sampleWorkout("zonetimes000003c")
	if err := d.InsertWorkout(w, nil); err != nil {
		t.Fatalf("InsertWorkout: %v", err)
	}
	if err := d.InsertZoneTimes(w.ID, [7]int{}, [5]int{}, -1); err != nil {
		t.Fatalf("InsertZoneTimes: %v", err)
	}

	if err := d.SetSSZoneSecs(w.ID, 333); err != nil {
		t.Fatalf("SetSSZoneSecs: %v", err)
	}

	_, _, ss, err := d.GetZoneTimes(w.ID)
	if err != nil {
		t.Fatalf("GetZoneTimes: %v", err)
	}
	if ss == nil || *ss != 333 {
		t.Errorf("ss = %v, want 333", ss)
	}
	ids, err := d.WorkoutIDsWithoutSSZone()
	if err != nil {
		t.Fatalf("WorkoutIDsWithoutSSZone: %v", err)
	}
	if slices.Contains(ids, w.ID) {
		t.Errorf("workout %s should be gone from the backfill queue after SetSSZoneSecs", w.ID)
	}
}

// ── Schema migrations ─────────────────────────────────────────────────────────

func TestMigration_SportIndexRebuiltDESC(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mig.db")
	d, err := db.Open(path, testKey)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Simulate a legacy DB by forcing the index back to its old ASC shape.
	if _, err := d.Exec(`DROP INDEX idx_workouts_sport_recorded_at;
		CREATE INDEX idx_workouts_sport_recorded_at ON workouts(sport, recorded_at)`); err != nil {
		t.Fatalf("force ASC index: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen — the guarded migration should rebuild the index DESC.
	d2, err := db.Open(path, testKey)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = d2.Close() })

	var def string
	if err := d2.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_workouts_sport_recorded_at'`).Scan(&def); err != nil {
		t.Fatalf("read index def: %v", err)
	}
	if !strings.Contains(def, "recorded_at DESC") {
		t.Errorf("index not rebuilt DESC after reopen: %q", def)
	}
}

func TestMigration_Idempotent(t *testing.T) {
	// Opening an already-migrated DB must not error: the ss_secs ALTER and the
	// DESC-index rebuild are both guarded against re-running.
	path := filepath.Join(t.TempDir(), "idem.db")
	d, err := db.Open(path, testKey)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	d2, err := db.Open(path, testKey)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	t.Cleanup(func() { _ = d2.Close() })
}
