package db_test

import (
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/models"
)

// insertEffort stores a ride recorded daysAgo before now with the given best
// 20- and 60-minute power (0 = the ride was too short to have one).
func insertEffort(t *testing.T, d *db.DB, id string, now time.Time, daysAgo, best20, best60 int) {
	t.Helper()
	w := sampleWorkout(id)
	w.RecordedAt = now.AddDate(0, 0, -daysAgo)
	w.TrainingDay = w.RecordedAt.Format("2006-01-02")
	if err := d.InsertWorkout(w, nil); err != nil {
		t.Fatalf("InsertWorkout(%s): %v", id, err)
	}
	curve := map[int]int{300: best20 + 40}
	if best20 > 0 {
		curve[1200] = best20
	}
	if best60 > 0 {
		curve[3600] = best60
	}
	if err := d.InsertPowerCurve(id, curve); err != nil {
		t.Fatalf("InsertPowerCurve(%s): %v", id, err)
	}
}

func TestFTPCheck_UsesBestEffortInWindow(t *testing.T) {
	d := newTestDB(t)
	now := time.Now().UTC()
	if err := d.SetFTP(245); err != nil {
		t.Fatal(err)
	}
	insertEffort(t, d, "old", now, 120, 260, 0) // outside the 90-day window: last season's peak
	insertEffort(t, d, "best", now, 40, 226, 199)
	insertEffort(t, d, "easy", now, 3, 170, 150)

	check, err := d.FTPCheck(now)
	if err != nil {
		t.Fatal(err)
	}
	if check.Status != models.FTPCheckLower || check.EstimateWatts != 215 {
		t.Fatalf("check = %s %d W, want lower 215 W", check.Status, check.EstimateWatts)
	}
	b := check.Basis
	if b == nil || b.WorkoutID != "best" || b.Watts != 226 || b.DurationSecs != 1200 {
		t.Fatalf("basis = %+v, want the 226 W 20-minute effort from ride \"best\"", b)
	}
	if want := now.AddDate(0, 0, -40).Format("2006-01-02"); b.Day != want {
		t.Errorf("basis day = %q, want %q", b.Day, want)
	}
	if check.Dismissed {
		t.Error("a fresh check should not be dismissed")
	}
}

func TestFTPCheck_NoRidesIsUnverified(t *testing.T) {
	d := newTestDB(t)
	check, err := d.FTPCheck(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if check.Status != models.FTPCheckUnverified || check.Basis != nil {
		t.Errorf("check = %+v, want unverified with no basis", check)
	}
}

func TestDismissFTPCheck(t *testing.T) {
	d := newTestDB(t)
	now := time.Now().UTC()
	if err := d.SetFTP(245); err != nil {
		t.Fatal(err)
	}
	insertEffort(t, d, "best", now, 10, 226, 0)
	check, _ := d.FTPCheck(now)
	if err := d.DismissFTPCheck(check, now); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.FTPCheck(now); !got.Dismissed {
		t.Fatal("dismissed check should stay hidden")
	}
	if got, _ := d.FTPCheck(now.Add(31 * 24 * time.Hour)); got.Dismissed {
		t.Error("the dismissal should lapse after 30 days")
	}
	// New evidence the other way is a new question, not the dismissed one.
	insertEffort(t, d, "strong", now, 1, 280, 0)
	if got, _ := d.FTPCheck(now); got.Status != models.FTPCheckRaise || got.Dismissed {
		t.Errorf("after a strong ride: %s dismissed=%v, want raise and shown", got.Status, got.Dismissed)
	}
}

// SetFTP must leave the rest of the profile alone. The ride page's "update
// FTP" button used to PUT a partial athlete, which wiped max HR, resting HR,
// age, location, timezone and units.
func TestSetFTP_KeepsProfile(t *testing.T) {
	d := newTestDB(t)
	a := &models.Athlete{FTPWatts: 245, WeightKG: 80.7, ThresholdHR: 174, MaxHR: 192, RestingHR: 52,
		Age: 41, Location: "Baltimore", Timezone: "America/New_York", Units: "imperial"}
	if err := d.UpdateAthlete(a); err != nil {
		t.Fatal(err)
	}
	if err := d.SetFTP(215); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetAthlete()
	if err != nil {
		t.Fatal(err)
	}
	want := *a
	want.FTPWatts = 215
	if got.FTPWatts != 215 || got.WeightKG != want.WeightKG || got.ThresholdHR != want.ThresholdHR ||
		got.MaxHR != want.MaxHR || got.RestingHR != want.RestingHR || got.Age != want.Age ||
		got.Location != want.Location || got.Timezone != want.Timezone || got.Units != want.Units {
		t.Errorf("profile after SetFTP = %+v, want %+v", got, want)
	}
}

func TestSetFTP_LogsHistoryOnlyOnChange(t *testing.T) {
	d := newTestDB(t)
	count := func() int {
		h, err := d.AllFTPHistory()
		if err != nil {
			t.Fatal(err)
		}
		return len(h)
	}
	// The seeded default has no history; setting it (even unchanged) makes it real.
	a, _ := d.GetAthlete()
	if err := d.SetFTP(a.FTPWatts); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Fatalf("history after confirming the default = %d entries, want 1", n)
	}
	if err := d.SetFTP(a.FTPWatts); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Errorf("history after an unchanged save = %d entries, want 1", n)
	}
	if err := d.SetFTP(215); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 2 {
		t.Errorf("history after a change = %d entries, want 2", n)
	}
}
