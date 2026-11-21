package db_test

import (
	"testing"
	"time"
)

// "Days since the last ride" counts calendar days: a ride late yesterday is a
// day ago, however few hours have passed (24-hour periods called it "today").
func TestReadiness_DaysSinceCountsCalendarDays(t *testing.T) {
	d := newTestDB(t)
	now := time.Now().UTC()
	y := now.AddDate(0, 0, -1)
	w := sampleWorkout("late")
	w.RecordedAt = time.Date(y.Year(), y.Month(), y.Day(), 23, 59, 0, 0, time.UTC)
	w.TrainingDay = w.RecordedAt.Format("2006-01-02")
	if err := d.InsertWorkout(w, nil); err != nil {
		t.Fatal(err)
	}
	r, err := d.Readiness(time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if r.DaysSinceLastWorkout != 1 {
		t.Errorf("a ride at 23:59 yesterday: %d days since, want 1", r.DaysSinceLastWorkout)
	}
}

// With no rides there is nothing to be ready from: no settled day.
func TestReadiness_NoRides(t *testing.T) {
	d := newTestDB(t)
	r, err := d.Readiness(time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if r.AsOf != "" {
		t.Errorf("AsOf = %q with no rides, want empty", r.AsOf)
	}
}
