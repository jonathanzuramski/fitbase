package db_test

import (
	"testing"
)

func TestWorkoutFeedback_RoundTrip(t *testing.T) {
	d := newTestDB(t)
	w := sampleWorkout("w1")
	if err := d.InsertWorkout(w, nil); err != nil {
		t.Fatal(err)
	}
	if _, found, err := d.GetWorkoutFeedback("w1"); err != nil || found {
		t.Fatalf("fresh ride: found=%v err=%v", found, err)
	}
	rpe := 8
	if err := d.SaveWorkoutFeedback("w1", &rpe, "  legs heavy, cut the last rep  "); err != nil {
		t.Fatal(err)
	}
	f, found, err := d.GetWorkoutFeedback("w1")
	if err != nil || !found || f.RPE == nil || *f.RPE != 8 || f.Note != "legs heavy, cut the last rep" || f.UpdatedAt.IsZero() {
		t.Fatalf("feedback = %+v found=%v err=%v", f, found, err)
	}

	// A note alone is kept; clearing both removes the row.
	if err := d.SaveWorkoutFeedback("w1", nil, "just a note"); err != nil {
		t.Fatal(err)
	}
	if f, _, _ := d.GetWorkoutFeedback("w1"); f.RPE != nil || f.Note != "just a note" {
		t.Errorf("note-only feedback = %+v", f)
	}
	if err := d.SaveWorkoutFeedback("w1", nil, "   "); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := d.GetWorkoutFeedback("w1"); found {
		t.Error("clearing the rating and note should remove the feedback")
	}
}

// Feedback is the one thing about a ride the rider typed in: an archive
// rebuild (every workout deleted and re-imported under the same ID) keeps
// it, while deleting the ride on purpose removes it.
func TestWorkoutFeedback_SurvivesRebuildNotDelete(t *testing.T) {
	d := newTestDB(t)
	for _, id := range []string{"keep", "gone"} {
		if err := d.InsertWorkout(sampleWorkout(id), nil); err != nil {
			t.Fatal(err)
		}
		rpe := 6
		if err := d.SaveWorkoutFeedback(id, &rpe, "note"); err != nil {
			t.Fatal(err)
		}
	}

	if err := d.ResetDerivedData(); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertWorkout(sampleWorkout("keep"), nil); err != nil { // the re-import
		t.Fatal(err)
	}
	if err := d.InsertWorkout(sampleWorkout("gone"), nil); err != nil {
		t.Fatal(err)
	}
	if f, found, _ := d.GetWorkoutFeedback("keep"); !found || f.RPE == nil || *f.RPE != 6 {
		t.Errorf("feedback after a rebuild = %+v found=%v, want it kept", f, found)
	}

	if err := d.DeleteWorkout("gone"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := d.GetWorkoutFeedback("gone"); found {
		t.Error("deleting a ride should delete its feedback")
	}

	got, err := d.FeedbackForWorkouts([]string{"keep", "gone", "never"})
	if err != nil || len(got) != 1 || got["keep"].Note != "note" {
		t.Errorf("FeedbackForWorkouts = %+v (%v), want only \"keep\"", got, err)
	}
}
