package db_test

import (
	"testing"

	"github.com/fitbase/fitbase/internal/fitness"
)

func cachedShape() fitness.RideShape {
	return fitness.RideShape{
		ElapsedSecs: 3600,
		HasPower:    true,
		Efforts: []fitness.Effort{
			{StartSecs: 600, DurationSecs: 240, AvgWatts: 300, MaxWatts: 320, RecoverySecs: 240, RecoveryAvgWatts: 120},
			{StartSecs: 1080, DurationSecs: 240, AvgWatts: 290, MaxWatts: 310},
		},
		Sets:       []fitness.EffortSet{{First: 0, Count: 2}},
		BucketSecs: 120,
		Buckets:    []fitness.ShapeBucket{{StartSecs: 0, ValidSecs: 120, AvgWatts: 150}},
	}
}

func TestWorkoutShapeCache_RoundTrip(t *testing.T) {
	d := newTestDB(t)
	w := sampleWorkout("shapecache00001")
	if err := d.InsertWorkout(w, nil); err != nil {
		t.Fatalf("InsertWorkout: %v", err)
	}

	if _, found, err := d.GetWorkoutShape(w.ID, 250); err != nil || found {
		t.Fatalf("before save: found=%v err=%v, want a clean miss", found, err)
	}
	if err := d.SaveWorkoutShape(w.ID, 250, cachedShape()); err != nil {
		t.Fatalf("SaveWorkoutShape: %v", err)
	}
	got, found, err := d.GetWorkoutShape(w.ID, 250)
	if err != nil || !found {
		t.Fatalf("after save: found=%v err=%v", found, err)
	}
	if len(got.Efforts) != 2 || got.Efforts[1].AvgWatts != 290 || len(got.Sets) != 1 || got.BucketSecs != 120 {
		t.Errorf("round-tripped shape = %+v", got)
	}
}

// A shape's thresholds and %FTP are relative to the FTP it was computed
// against; after an FTP-history edit the cached row must read as a miss.
func TestWorkoutShapeCache_StaleOnFTPOrVersionChange(t *testing.T) {
	d := newTestDB(t)
	w := sampleWorkout("shapecache00002")
	if err := d.InsertWorkout(w, nil); err != nil {
		t.Fatalf("InsertWorkout: %v", err)
	}
	if err := d.SaveWorkoutShape(w.ID, 250, cachedShape()); err != nil {
		t.Fatalf("SaveWorkoutShape: %v", err)
	}
	if _, found, _ := d.GetWorkoutShape(w.ID, 260); found {
		t.Error("shape computed at 250W served for FTP 260")
	}

	// A row from an older detector is stale too.
	if _, err := d.Exec(`UPDATE workout_shape SET version = ? WHERE workout_id = ?`, fitness.ShapeVersion-1, w.ID); err != nil {
		t.Fatalf("age row: %v", err)
	}
	if _, found, _ := d.GetWorkoutShape(w.ID, 250); found {
		t.Error("shape from an older detector version served as current")
	}

	// Saving again replaces the stale row rather than failing on the key.
	if err := d.SaveWorkoutShape(w.ID, 260, cachedShape()); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	if _, found, _ := d.GetWorkoutShape(w.ID, 260); !found {
		t.Error("re-saved shape not found")
	}
}

func TestWorkoutShapeCache_DeletedWithWorkout(t *testing.T) {
	d := newTestDB(t)
	w := sampleWorkout("shapecache00003")
	if err := d.InsertWorkout(w, nil); err != nil {
		t.Fatalf("InsertWorkout: %v", err)
	}
	if err := d.SaveWorkoutShape(w.ID, 250, cachedShape()); err != nil {
		t.Fatalf("SaveWorkoutShape: %v", err)
	}
	if err := d.DeleteWorkout(w.ID); err != nil {
		t.Fatalf("DeleteWorkout: %v", err)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM workout_shape`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("%d workout_shape rows survived deleting their workout", n)
	}
}
