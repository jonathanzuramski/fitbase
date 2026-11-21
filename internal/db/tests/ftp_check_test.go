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

// A run with a power meter (Stryd, Garmin running power) gets a power curve
// too. Running watts aren't cycling watts: a 300 W run used to read as proof
// a 250 W cycling FTP should go up.
func TestFTPCheck_IgnoresRunningPower(t *testing.T) {
	d := newTestDB(t)
	now := time.Now().UTC()
	if err := d.SetFTP(250); err != nil {
		t.Fatal(err)
	}
	insertEffort(t, d, "ride", now, 10, 255, 0)
	run := sampleWorkout("run")
	run.Sport = "running"
	run.RecordedAt = now.AddDate(0, 0, -5)
	if err := d.InsertWorkout(run, nil); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertPowerCurve("run", map[int]int{1200: 300, 3600: 290}); err != nil {
		t.Fatal(err)
	}

	check, err := d.FTPCheck(now)
	if err != nil {
		t.Fatal(err)
	}
	if check.Status != models.FTPCheckOK || check.EstimateWatts != 242 || check.Basis == nil || check.Basis.WorkoutID != "ride" {
		t.Fatalf("check = %s %d W basis %+v, want ok 242 W from the ride", check.Status, check.EstimateWatts, check.Basis)
	}
	// The coach's power bests come from the same curves.
	for name, get := range map[string]func() (map[int]models.AllTimeBest, error){
		"all time": d.GetAllTimePowerCurve,
		"recent":   func() (map[int]models.AllTimeBest, error) { return d.GetPowerCurveSince(now.AddDate(0, 0, -30)) },
	} {
		curve, err := get()
		if err != nil {
			t.Fatal(err)
		}
		if b := curve[1200]; b.WorkoutID != "ride" || b.Watts != 255 {
			t.Errorf("%s best 20 min = %+v, want 255 W from the ride", name, b)
		}
		if b, ok := curve[3600]; ok {
			t.Errorf("%s best 60 min = %+v, want none (only the run went that long)", name, b)
		}
	}
}

// Indoor rides are scored against the indoor FTP line. An indoor effort is
// compared with the general FTP in general-line terms: 238 W for 20 minutes on
// a 230 W indoor line is what a 250 W rider holds indoors, not a sign the FTP
// is high. Each line keeps its own best, since the indoor effort can win only
// after scaling.
func TestFTPCheck_IndoorEffortOnIndoorLine(t *testing.T) {
	d := newTestDB(t)
	now := time.Now().UTC()
	if err := d.SetFTP(250); err != nil {
		t.Fatal(err)
	}
	if err := d.LogFTPChangeAt(250, now.AddDate(0, 0, -300)); err != nil {
		t.Fatal(err)
	}
	if err := d.LogFTPChangeAtFor(230, now.AddDate(0, 0, -200), true); err != nil {
		t.Fatal(err)
	}
	insertEffort(t, d, "road", now, 10, 240, 0) // 228 W estimate: 91%, "lower" on its own
	insertEffort(t, d, "trainer", now, 5, 238, 0)
	if _, err := d.Exec("UPDATE workouts SET is_indoor = 1 WHERE id = 'trainer'"); err != nil {
		t.Fatal(err)
	}

	check, err := d.FTPCheck(now)
	if err != nil {
		t.Fatal(err)
	}
	if check.Status != models.FTPCheckOK || check.EstimateWatts != 246 {
		t.Fatalf("check = %s %d W, want ok 246 W (238 W × 250/230 × 0.95)", check.Status, check.EstimateWatts)
	}
	b := check.Basis
	if b == nil || b.WorkoutID != "trainer" || b.Watts != 238 || !b.Indoor || b.IndoorFTPWatts != 230 || b.EquivalentWatts != 259 {
		t.Errorf("basis = %+v, want the trainer ride's raw 238 W, indoor line 230 W, 259 W equivalent", b)
	}

	// Without a separate indoor line an indoor effort counts as it is.
	if err := d.ClearFTPHistory(); err != nil {
		t.Fatal(err)
	}
	check, _ = d.FTPCheck(now)
	if check.Status != models.FTPCheckLower || check.EstimateWatts != 228 || check.Basis == nil || check.Basis.EquivalentWatts != 0 {
		t.Errorf("no indoor line: check = %s %d W basis %+v, want lower 228 W from the road ride", check.Status, check.EstimateWatts, check.Basis)
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

func TestGetFTPForRide_IndoorLine(t *testing.T) {
	d := newTestDB(t)
	day := func(m time.Month, dd int) time.Time { return time.Date(2025, m, dd, 12, 0, 0, 0, time.UTC) }
	for _, e := range []struct {
		ftp    int
		at     time.Time
		indoor bool
	}{
		{242, day(9, 1), false},
		{235, day(11, 1), true},
		{250, day(12, 1), false}, // a newer general change moves indoor rides too
		{245, day(12, 10), true}, // until the indoor line is set again
	} {
		if err := d.LogFTPChangeAtFor(e.ftp, e.at, e.indoor); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		at     time.Time
		indoor bool
		want   int
	}{
		{day(10, 1), true, 242},  // indoor ride before any indoor entry: general line
		{day(11, 15), true, 235}, // indoor line
		{day(11, 15), false, 242},
		{day(12, 5), true, 250},
		{day(12, 15), true, 245},
		{day(12, 15), false, 250},
	}
	for _, c := range cases {
		if got := d.GetFTPForRide(c.at, c.indoor); got != c.want {
			t.Errorf("GetFTPForRide(%s, indoor=%v) = %d, want %d", c.at.Format("Jan 2"), c.indoor, got, c.want)
		}
	}
	if got := d.GetFTPAtDate(day(11, 15)); got != 242 {
		t.Errorf("GetFTPAtDate ignores the indoor line: got %d, want 242", got)
	}
}

// SetFTP, the settings form and the FTP check's apply write the general line
// only. An older indoor entry used to outrank every later general change, so
// indoor rides stayed on a stale FTP indefinitely.
func TestGetFTPForRide_SetFTPReachesIndoorRides(t *testing.T) {
	d := newTestDB(t)
	now := time.Now().UTC()
	if err := d.LogFTPChangeAtFor(230, now.AddDate(0, 0, -200), true); err != nil {
		t.Fatal(err)
	}
	if err := d.LogFTPChangeAtFor(250, now.AddDate(0, 0, -100), true); err != nil {
		t.Fatal(err)
	}
	if err := d.SetFTP(270); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Hour)
	if got := d.GetFTPForRide(later, true); got != 270 {
		t.Errorf("indoor ride after SetFTP(270) = %d, want 270", got)
	}
	if got := d.GetFTPForRide(now.AddDate(0, 0, -50), true); got != 250 {
		t.Errorf("indoor ride before the change = %d, want 250", got)
	}
}

// Saving the profile with the FTP it shows makes that FTP the effective one,
// even when it equals the stored profile value: a history synced from
// intervals.icu (or the newest entry deleted) can say otherwise.
func TestUpdateAthlete_SaveReconcilesWithHistory(t *testing.T) {
	d := newTestDB(t)
	a := &models.Athlete{FTPWatts: 240, WeightKG: 80}
	if err := d.UpdateAthlete(a); err != nil {
		t.Fatal(err)
	}
	if err := d.ClearFTPHistory(); err != nil {
		t.Fatal(err)
	}
	if err := d.LogFTPChangeAt(250, time.Now().AddDate(0, -1, 0)); err != nil {
		t.Fatal(err)
	}
	soon := time.Now().Add(2 * time.Second)
	if got := d.GetFTPAtDate(soon); got != 250 {
		t.Fatalf("setup: effective FTP = %d, want the history's 250", got)
	}
	if err := d.UpdateAthlete(a); err != nil {
		t.Fatal(err)
	}
	if got := d.GetFTPAtDate(soon); got != 240 {
		t.Errorf("effective FTP after saving the profile's 240 = %d, want 240", got)
	}
	if h, _ := d.AllFTPHistory(); len(h) != 2 {
		t.Errorf("history = %d entries, want 2", len(h))
	}
	// A second identical save is not a change.
	if err := d.SetFTP(240); err != nil {
		t.Fatal(err)
	}
	if h, _ := d.AllFTPHistory(); len(h) != 2 {
		t.Errorf("history after an unchanged save = %d entries, want 2", len(h))
	}
}

// Saving the profile used to log an FTP history entry on every save.
func TestUpdateAthlete_LogsFTPOnlyOnChange(t *testing.T) {
	d := newTestDB(t)
	a := &models.Athlete{FTPWatts: 245, WeightKG: 80}
	for i := 0; i < 3; i++ {
		if err := d.UpdateAthlete(a); err != nil {
			t.Fatal(err)
		}
	}
	a.FTPWatts = 250
	if err := d.UpdateAthlete(a); err != nil {
		t.Fatal(err)
	}
	if h, _ := d.AllFTPHistory(); len(h) != 2 {
		t.Errorf("history after three identical saves and one change = %d entries, want 2", len(h))
	}
}

// GetPowerCurveBetween takes the best efforts in [from, to) only: the
// Progress page walks it back through time for the FTP estimate line.
func TestGetPowerCurveBetween(t *testing.T) {
	d := newTestDB(t)
	now := time.Now().UTC()
	insertEffort(t, d, "old", now, 200, 260, 0)
	insertEffort(t, d, "mid", now, 100, 240, 0)
	insertEffort(t, d, "new", now, 5, 230, 0)

	curve, err := d.GetPowerCurveBetween(now.AddDate(0, 0, -150), now.AddDate(0, 0, -50))
	if err != nil {
		t.Fatal(err)
	}
	if b := curve[1200]; b.Watts != 240 || b.WorkoutID != "mid" {
		t.Errorf("best 20 min in the window = %+v, want 240 W from \"mid\"", b)
	}
	since, _ := d.GetPowerCurveSince(now.AddDate(0, 0, -10))
	if b := since[1200]; b.Watts != 230 {
		t.Errorf("GetPowerCurveSince still bounds only below: got %+v", b)
	}
}
