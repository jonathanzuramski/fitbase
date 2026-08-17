package db_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/models"
	"github.com/fitbase/fitbase/internal/timeutil"
)

// Boulder, CO — America/Denver: MST (-7h) in winter, MDT (-6h) in summer.
const (
	boulderLat = 40.0150
	boulderLng = -105.2705
)

func ptr[T any](v T) *T { return &v }

// An outdoor ride with GPS but no device-declared offset resolves its offset
// from the start coordinate's timezone — at the ride's date, not today's DST.
func TestInsertWorkout_OffsetFromGPS(t *testing.T) {
	d := newTestDB(t)

	w := sampleWorkout("gps0000000000001")
	// 04:30 UTC on Jan 15 is 21:30 MST on Jan 14 — the ride belongs to the 14th.
	w.RecordedAt = time.Date(2026, 1, 15, 4, 30, 0, 0, time.UTC)
	w.StartLat, w.StartLng = ptr(boulderLat), ptr(boulderLng)

	if err := d.InsertWorkout(w, nil); err != nil {
		t.Fatalf("InsertWorkout: %v", err)
	}

	got, err := d.GetWorkout(w.ID)
	if err != nil || got == nil {
		t.Fatalf("GetWorkout: %v, %v", got, err)
	}
	if got.UTCOffsetSecs == nil || *got.UTCOffsetSecs != -7*3600 {
		t.Errorf("UTCOffsetSecs = %v, want -25200 (MST)", got.UTCOffsetSecs)
	}
	if got.TrainingDay != "2026-01-14" {
		t.Errorf("TrainingDay = %q, want 2026-01-14", got.TrainingDay)
	}
	if got.County != "Boulder County" || got.State != "CO" {
		t.Errorf("place = %q, %q; want Boulder County, CO", got.County, got.State)
	}
	// RecordedAt comes back re-homed: same instant, ride-local wall clock.
	if !got.RecordedAt.Equal(w.RecordedAt) {
		t.Errorf("RecordedAt instant changed: %v != %v", got.RecordedAt, w.RecordedAt)
	}
	if hm := got.RecordedAt.Format("15:04"); hm != "21:30" {
		t.Errorf("ride-local wall clock = %s, want 21:30", hm)
	}
}

// A device-declared offset (FIT local_timestamp) wins over the GPS timezone.
func TestInsertWorkout_DeviceOffsetWins(t *testing.T) {
	d := newTestDB(t)

	w := sampleWorkout("dev0000000000001")
	w.RecordedAt = time.Date(2026, 6, 1, 22, 0, 0, 0, time.UTC)
	w.UTCOffsetSecs = ptr(2 * 3600) // device says UTC+2
	w.StartLat, w.StartLng = ptr(boulderLat), ptr(boulderLng)

	if err := d.InsertWorkout(w, nil); err != nil {
		t.Fatalf("InsertWorkout: %v", err)
	}
	got, err := d.GetWorkout(w.ID)
	if err != nil || got == nil {
		t.Fatalf("GetWorkout: %v, %v", got, err)
	}
	if got.UTCOffsetSecs == nil || *got.UTCOffsetSecs != 2*3600 {
		t.Errorf("UTCOffsetSecs = %v, want 7200 (device-declared)", got.UTCOffsetSecs)
	}
	// 22:00 UTC + 2h = 00:00 next day.
	if got.TrainingDay != "2026-06-02" {
		t.Errorf("TrainingDay = %q, want 2026-06-02", got.TrainingDay)
	}
	// Place still resolves from GPS regardless of where the offset came from.
	if got.County != "Boulder County" || got.State != "CO" {
		t.Errorf("place = %q, %q; want Boulder County, CO", got.County, got.State)
	}
}

// Indoor rides have no usable GPS: the offset falls back to the athlete
// profile timezone, evaluated at the ride's date.
func TestInsertWorkout_IndoorProfileFallback(t *testing.T) {
	d := newTestDB(t)
	if _, err := d.Exec(`UPDATE athlete SET timezone = 'America/Chicago' WHERE id = 1`); err != nil {
		t.Fatalf("set athlete tz: %v", err)
	}

	w := sampleWorkout("ind0000000000001")
	w.IsIndoor = true
	// 03:00 UTC on Jul 15 is 22:00 CDT (-5h) on Jul 14.
	w.RecordedAt = time.Date(2026, 7, 15, 3, 0, 0, 0, time.UTC)

	if err := d.InsertWorkout(w, nil); err != nil {
		t.Fatalf("InsertWorkout: %v", err)
	}
	got, err := d.GetWorkout(w.ID)
	if err != nil || got == nil {
		t.Fatalf("GetWorkout: %v, %v", got, err)
	}
	if got.UTCOffsetSecs == nil || *got.UTCOffsetSecs != -5*3600 {
		t.Errorf("UTCOffsetSecs = %v, want -18000 (CDT)", got.UTCOffsetSecs)
	}
	if got.TrainingDay != "2026-07-14" {
		t.Errorf("TrainingDay = %q, want 2026-07-14", got.TrainingDay)
	}
	if got.County != "" || got.State != "" {
		t.Errorf("indoor ride resolved a place: %q, %q", got.County, got.State)
	}
}

// GetWeeklyBreakdown buckets by training_day into ISO weeks and carries
// per-workout refs.
func TestGetWeeklyBreakdown_TrainingDayWeeks(t *testing.T) {
	d := newTestDB(t)

	thisMonday := timeutil.MondayOf(time.Now().UTC())
	lastSunday := thisMonday.AddDate(0, 0, -1)

	mk := func(id string, day time.Time) *models.Workout {
		w := sampleWorkout(id)
		w.IsIndoor = true // profile tz = UTC → training_day equals the UTC date
		w.RecordedAt = time.Date(day.Year(), day.Month(), day.Day(), 10, 0, 0, 0, time.UTC)
		return w
	}
	for i, w := range []*models.Workout{
		mk("wk00000000000001", lastSunday),
		mk("wk00000000000002", thisMonday),
		mk("wk00000000000003", thisMonday.AddDate(0, 0, 1)),
	} {
		if err := d.InsertWorkout(w, nil); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	weeks, err := d.GetWeeklyBreakdown(2)
	if err != nil {
		t.Fatalf("GetWeeklyBreakdown: %v", err)
	}
	if len(weeks) != 2 {
		t.Fatalf("got %d weeks, want 2: %+v", len(weeks), weeks)
	}
	if weeks[0].Week != timeutil.ISOWeekLabel(lastSunday) || weeks[0].WorkoutCount != 1 {
		t.Errorf("week[0] = %q count %d, want %q count 1",
			weeks[0].Week, weeks[0].WorkoutCount, timeutil.ISOWeekLabel(lastSunday))
	}
	if weeks[1].Week != timeutil.ISOWeekLabel(thisMonday) || weeks[1].WorkoutCount != 2 {
		t.Errorf("week[1] = %q count %d, want %q count 2",
			weeks[1].Week, weeks[1].WorkoutCount, timeutil.ISOWeekLabel(thisMonday))
	}
	if len(weeks[1].WorkoutRefs) != 2 || weeks[1].WorkoutRefs[0].ID != "wk00000000000002" {
		t.Errorf("week[1] refs = %+v, want 2 refs starting with wk…02", weeks[1].WorkoutRefs)
	}
	if weeks[1].TSS != 150 { // two sampleWorkouts at 75 TSS each
		t.Errorf("week[1] TSS = %v, want 150", weeks[1].TSS)
	}
}

// Migration v5's backfill: legacy rows (no offset/place/geo_v) are converged
// on reopen — outdoor rides from their GPS track, indoor rides from the
// profile timezone, never from virtual-world coordinates.
func TestMigrationV5_Backfill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	d, err := db.Open(path, testKey)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// Outdoor ride in Boulder, summer → MDT (-6h).
	outdoor := sampleWorkout("mig0000000000001")
	outdoor.RecordedAt = time.Date(2025, 7, 10, 1, 0, 0, 0, time.UTC) // 19:00 MDT Jul 9
	outdoorStreams := []models.Stream{{
		Timestamp: outdoor.RecordedAt,
		Lat:       ptr(boulderLat),
		Lng:       ptr(boulderLng),
	}}
	// Indoor Zwift ride with virtual-world GPS (Watopia — Solomon Islands,
	// UTC+11). The backfill must ignore these coordinates.
	indoor := sampleWorkout("mig0000000000002")
	indoor.IsIndoor = true
	indoor.RecordedAt = time.Date(2025, 7, 10, 1, 0, 0, 0, time.UTC)
	indoorStreams := []models.Stream{{
		Timestamp: indoor.RecordedAt,
		Lat:       ptr(-11.64),
		Lng:       ptr(166.97),
	}}

	for _, ins := range []struct {
		w *models.Workout
		s []models.Stream
	}{{outdoor, outdoorStreams}, {indoor, indoorStreams}} {
		if err := d.InsertWorkout(ins.w, ins.s); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	// Regress the rows to their pre-v5 shape and rewind the ladder.
	if _, err := d.Exec(`
		UPDATE workouts SET utc_offset_secs = NULL, start_lat = NULL, start_lng = NULL,
		                    county = NULL, state = NULL, geo_v = NULL, training_day = NULL`); err != nil {
		t.Fatalf("regress rows: %v", err)
	}
	if _, err := d.Exec(`PRAGMA user_version = 4`); err != nil {
		t.Fatalf("rewind version: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen: v5 replays and backfills.
	d, err = db.Open(path, testKey)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	got, err := d.GetWorkout(outdoor.ID)
	if err != nil || got == nil {
		t.Fatalf("get outdoor: %v, %v", got, err)
	}
	if got.UTCOffsetSecs == nil || *got.UTCOffsetSecs != -6*3600 {
		t.Errorf("outdoor offset = %v, want -21600 (MDT)", got.UTCOffsetSecs)
	}
	if got.TrainingDay != "2025-07-09" {
		t.Errorf("outdoor TrainingDay = %q, want 2025-07-09", got.TrainingDay)
	}
	if got.County != "Boulder County" || got.State != "CO" {
		t.Errorf("outdoor place = %q, %q; want Boulder County, CO", got.County, got.State)
	}
	if got.StartLat == nil || *got.StartLat != boulderLat {
		t.Errorf("outdoor StartLat = %v, want %v", got.StartLat, boulderLat)
	}

	got, err = d.GetWorkout(indoor.ID)
	if err != nil || got == nil {
		t.Fatalf("get indoor: %v, %v", got, err)
	}
	// Profile tz is UTC in a fresh test DB: offset 0, training_day = UTC date.
	if got.UTCOffsetSecs == nil || *got.UTCOffsetSecs != 0 {
		t.Errorf("indoor offset = %v, want 0 (profile UTC, not Watopia's +11)", got.UTCOffsetSecs)
	}
	if got.TrainingDay != "2025-07-10" {
		t.Errorf("indoor TrainingDay = %q, want 2025-07-10", got.TrainingDay)
	}
	if got.County != "" {
		t.Errorf("indoor ride resolved a county from virtual coords: %q", got.County)
	}
}
