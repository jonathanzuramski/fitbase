package db_test

import (
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/models"
)

// achWorkout builds a minimal workout for achievement tests. Distance and
// elevation are held constant by default so route/power assertions aren't
// muddied by stray longest-ride trophies (equal values are never "worse").
func achWorkout(id string, day, durationSecs int, avgPower float64) *models.Workout {
	w := &models.Workout{
		ID:             id,
		Filename:       id + ".fit",
		RecordedAt:     time.Date(2024, 3, day, 8, 0, 0, 0, time.UTC),
		Sport:          "cycling",
		DurationSecs:   durationSecs,
		DistanceMeters: 30000,
		AvgSpeedMPS:    10,
		IsIndoor:       true, // skip GPS/geo resolution in InsertWorkout
		CreatedAt:      time.Now().UTC(),
	}
	if avgPower > 0 {
		w.AvgPowerWatts = &avgPower
	}
	return w
}

// insertOnRoute inserts a workout, assigns it to routeID, and computes its
// trophies — the same sequence the importer runs.
func insertOnRoute(t *testing.T, d *db.DB, w *models.Workout, routeID string) {
	t.Helper()
	if err := d.InsertWorkout(w, nil); err != nil {
		t.Fatalf("InsertWorkout(%s): %v", w.ID, err)
	}
	if routeID != "" {
		if err := d.SetWorkoutRouteID(w.ID, routeID); err != nil {
			t.Fatalf("SetWorkoutRouteID(%s): %v", w.ID, err)
		}
	}
	if err := d.RefreshAchievements(w.ID); err != nil {
		t.Fatalf("RefreshAchievements(%s): %v", w.ID, err)
	}
}

// ranks returns the workout's achievements as kind → rank.
func ranks(t *testing.T, d *db.DB, workoutID string) map[string]int {
	t.Helper()
	achs, err := d.GetWorkoutAchievements(workoutID)
	if err != nil {
		t.Fatalf("GetWorkoutAchievements(%s): %v", workoutID, err)
	}
	m := make(map[string]int, len(achs))
	for _, a := range achs {
		m[a.Kind] = a.Rank
	}
	return m
}

func newRouteDB(t *testing.T) *db.DB {
	t.Helper()
	d := newTestDB(t)
	if err := d.InsertRoute("route1", "cells", 10); err != nil {
		t.Fatalf("InsertRoute: %v", err)
	}
	return d
}

func TestFirstEffortSetsBaselineNoTrophy(t *testing.T) {
	d := newRouteDB(t)
	insertOnRoute(t, d, achWorkout("w1", 1, 3600, 200), "route1")

	if got := ranks(t, d, "w1"); len(got) != 0 {
		t.Errorf("first effort should earn nothing, got %v", got)
	}
}

func TestRouteAndPowerPRs(t *testing.T) {
	d := newRouteDB(t)
	insertOnRoute(t, d, achWorkout("w1", 1, 3600, 200), "route1")
	// Faster and stronger than w1 → course record + best power.
	insertOnRoute(t, d, achWorkout("w2", 2, 3400, 220), "route1")
	// Between the two on both axes → rank 2 on both.
	insertOnRoute(t, d, achWorkout("w3", 3, 3500, 210), "route1")
	// Slower and weaker than everything → nothing.
	insertOnRoute(t, d, achWorkout("w4", 4, 3700, 190), "route1")

	if got := ranks(t, d, "w2"); got[db.AchievementRouteTime] != 1 || got[db.AchievementRoutePower] != 1 {
		t.Errorf("w2: want route_time=1 route_power=1, got %v", got)
	}
	if got := ranks(t, d, "w3"); got[db.AchievementRouteTime] != 2 || got[db.AchievementRoutePower] != 2 {
		t.Errorf("w3: want route_time=2 route_power=2, got %v", got)
	}
	if got := ranks(t, d, "w4"); len(got) != 0 {
		t.Errorf("w4 beat nothing, want no trophies, got %v", got)
	}
}

func TestRouteTrophiesFrozenAtImportTime(t *testing.T) {
	d := newRouteDB(t)
	insertOnRoute(t, d, achWorkout("w1", 1, 3600, 0), "route1")
	insertOnRoute(t, d, achWorkout("w2", 2, 3400, 0), "route1") // course record
	insertOnRoute(t, d, achWorkout("w3", 3, 3300, 0), "route1") // new course record

	// w2's trophy was earned against what existed on day 2; w3 beating it
	// later must not demote it.
	if got := ranks(t, d, "w2"); got[db.AchievementRouteTime] != 1 {
		t.Errorf("w2 should keep its day-2 course record, got %v", got)
	}
	if got := ranks(t, d, "w3"); got[db.AchievementRouteTime] != 1 {
		t.Errorf("w3: want route_time=1, got %v", got)
	}
}

func TestOutOfOrderImportRewritesLaterTrophies(t *testing.T) {
	d := newRouteDB(t)
	insertOnRoute(t, d, achWorkout("w1", 1, 3600, 0), "route1")
	insertOnRoute(t, d, achWorkout("w3", 10, 3400, 0), "route1") // course record on import

	if got := ranks(t, d, "w3"); got[db.AchievementRouteTime] != 1 {
		t.Fatalf("w3: want route_time=1 before backfill, got %v", got)
	}

	// A day-5 ride faster than w3 arrives late (e.g. from an old device dump):
	// history now says w3 was never the record.
	insertOnRoute(t, d, achWorkout("w2", 5, 3300, 0), "route1")

	if got := ranks(t, d, "w2"); got[db.AchievementRouteTime] != 1 {
		t.Errorf("w2: want route_time=1, got %v", got)
	}
	if got := ranks(t, d, "w3"); got[db.AchievementRouteTime] != 2 {
		t.Errorf("w3: want demotion to route_time=2, got %v", got)
	}
}

func TestDeletePromotesLaterEfforts(t *testing.T) {
	d := newRouteDB(t)
	insertOnRoute(t, d, achWorkout("w1", 1, 3600, 0), "route1")
	w2 := achWorkout("w2", 2, 3300, 0)
	insertOnRoute(t, d, w2, "route1") // course record
	insertOnRoute(t, d, achWorkout("w3", 3, 3400, 0), "route1")

	if got := ranks(t, d, "w3"); got[db.AchievementRouteTime] != 2 {
		t.Fatalf("w3: want route_time=2 while w2 exists, got %v", got)
	}

	if err := d.DeleteWorkout("w2"); err != nil {
		t.Fatalf("DeleteWorkout: %v", err)
	}
	if err := d.RecomputeAchievementsAfter(w2.RecordedAt); err != nil {
		t.Fatalf("RecomputeAchievementsAfter: %v", err)
	}

	if got := ranks(t, d, "w3"); got[db.AchievementRouteTime] != 1 {
		t.Errorf("w3: want promotion to course record after delete, got %v", got)
	}
}

func TestPowerDurationBests(t *testing.T) {
	d := newTestDB(t)

	w1 := achWorkout("w1", 1, 3600, 0)
	insertOnRoute(t, d, w1, "")
	if err := d.InsertPowerCurve("w1", map[int]int{5: 600, 300: 280, 1200: 240}); err != nil {
		t.Fatalf("InsertPowerCurve: %v", err)
	}
	if err := d.RefreshAchievements("w1"); err != nil {
		t.Fatalf("RefreshAchievements: %v", err)
	}

	w2 := achWorkout("w2", 2, 3600, 0)
	insertOnRoute(t, d, w2, "")
	// Beats w1 at 5s and 20min, loses at 5min, no 1hr entry anywhere.
	if err := d.InsertPowerCurve("w2", map[int]int{5: 650, 300: 270, 1200: 250}); err != nil {
		t.Fatalf("InsertPowerCurve: %v", err)
	}
	if err := d.RefreshAchievements("w2"); err != nil {
		t.Fatalf("RefreshAchievements: %v", err)
	}

	got := ranks(t, d, "w2")
	if got[db.PowerAchievementKind(5)] != 1 {
		t.Errorf("power_5: want 1, got %v", got)
	}
	if got[db.PowerAchievementKind(1200)] != 1 {
		t.Errorf("power_1200: want 1, got %v", got)
	}
	if _, ok := got[db.PowerAchievementKind(300)]; ok {
		t.Errorf("power_300: w2 lost that duration, got %v", got)
	}
	if got := ranks(t, d, "w1"); len(got) != 0 {
		t.Errorf("w1 was first, want no trophies, got %v", got)
	}
}

func TestVolumeRecordsRankOneOnly(t *testing.T) {
	d := newTestDB(t)

	w1 := achWorkout("w1", 1, 3600, 0)
	w1.ElevationGainMeters = 100
	insertOnRoute(t, d, w1, "")

	w2 := achWorkout("w2", 2, 3600, 0)
	w2.DistanceMeters = 50000 // longest
	w2.ElevationGainMeters = 800
	insertOnRoute(t, d, w2, "")

	w3 := achWorkout("w3", 3, 3600, 0)
	w3.DistanceMeters = 40000 // 2nd longest — no trophy for volume kinds
	insertOnRoute(t, d, w3, "")

	got := ranks(t, d, "w2")
	if got[db.AchievementLongestRide] != 1 {
		t.Errorf("w2: want longest_distance=1, got %v", got)
	}
	if got[db.AchievementMostClimbing] != 1 {
		t.Errorf("w2: want most_climbing=1, got %v", got)
	}
	if got := ranks(t, d, "w3"); len(got) != 0 {
		t.Errorf("w3: volume records are rank-1 only, got %v", got)
	}
}

func TestGetAchievementCounts(t *testing.T) {
	d := newRouteDB(t)
	insertOnRoute(t, d, achWorkout("w1", 1, 3600, 200), "route1")
	insertOnRoute(t, d, achWorkout("w2", 2, 3400, 220), "route1")

	counts, err := d.GetAchievementCounts([]string{"w1", "w2", "missing"})
	if err != nil {
		t.Fatalf("GetAchievementCounts: %v", err)
	}
	if _, ok := counts["w1"]; ok {
		t.Errorf("w1 has no trophies, want it omitted, got %v", counts)
	}
	if counts["w2"] != 2 {
		t.Errorf("w2: want 2 trophies (route time + power), got %v", counts)
	}
}
