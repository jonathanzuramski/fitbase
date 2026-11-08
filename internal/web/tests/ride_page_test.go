package web_test

import (
	"strings"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/models"
)

// segment is a stretch of steady power (and the HR that goes with it).
type segment struct {
	secs, watts, hr int
}

// rideFrom stores a 1 Hz ride built from segments, with its NP-based IF
// against ftp set the way the importer would.
func rideFrom(t *testing.T, d *db.DB, id string, ftp int, indoor bool, segs ...segment) *models.Workout {
	t.Helper()
	start := time.Date(2026, 10, 7, 17, 0, 0, 0, time.UTC)
	var streams []models.Stream
	var total, sum float64
	for _, s := range segs {
		for i := 0; i < s.secs; i++ {
			p, hr := s.watts, s.hr
			streams = append(streams, models.Stream{
				Timestamp:  start.Add(time.Duration(len(streams)) * time.Second),
				PowerWatts: &p, HeartRateBPM: &hr,
			})
			total++
			sum += float64(s.watts)
		}
	}
	avg := sum / total
	ifac := avg / float64(ftp)
	w := &models.Workout{
		ID: id, Filename: id + ".fit", Sport: "cycling", RecordedAt: start, TrainingDay: "2026-10-07",
		DurationSecs: int(total), ElapsedSecs: int(total), AvgPowerWatts: &avg, NormalizedPower: &avg,
		IntensityFactor: &ifac, IsIndoor: indoor, CreatedAt: time.Now().UTC(),
	}
	if err := d.InsertWorkout(w, streams); err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
	return w
}

func TestRidePage_IntervalSession(t *testing.T) {
	d, h := ftpFixture(t)
	if err := d.SetFTP(250); err != nil {
		t.Fatal(err)
	}
	segs := []segment{{600, 150, 120}}
	for i := 0; i < 5; i++ {
		segs = append(segs, segment{180, 275, 165}, segment{180, 120, 125})
	}
	segs = append(segs, segment{600, 150, 120})
	rideFrom(t, d, "vo2", 250, false, segs...)
	ftp := 230.0
	if _, err := d.CreatePlannedWorkout(models.PlannedWorkout{
		PlannedDate: time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC), Sport: "cycling",
		Title: "5x3 VO2", DurationSecs: 3600, TSS: &ftp, Source: "coach",
	}); err != nil {
		t.Fatal(err)
	}

	body := get(t, h, "/workouts/vo2")
	for _, want := range []string{
		`<span class="ride-kind ride-kind-intervals">Intervals</span>`,
		`5×3:00 @ 110% FTP, held power across reps`,
		`<tr class="intervals-group"><td colspan="11">Set 1 · 5×3:00 @ 110% FTP`,
		`data-start="600" data-end="780"`, // rep 1 starts after the 10-minute warmup
		`<span class="ride-planned-label">planned</span>`, `<strong>5x3 VO2</strong>`,
		`id="ride-charts"`, `/static/js/ride.js`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("ride page lacks %q", want)
		}
	}
	if n := strings.Count(body, `class="interval-row"`); n != 5 {
		t.Errorf("interval rows = %d, want 5", n)
	}
}

// A couple of short surges don't make an endurance ride "mixed"; the headline
// still names them.
func TestRidePage_EnduranceWithSurges(t *testing.T) {
	d, h := ftpFixture(t)
	if err := d.SetFTP(250); err != nil {
		t.Fatal(err)
	}
	rideFrom(t, d, "z2", 250, false,
		segment{1800, 160, 130}, segment{30, 380, 150}, segment{1800, 160, 132}, segment{30, 380, 152}, segment{600, 150, 128})
	body := get(t, h, "/workouts/z2")
	if !strings.Contains(body, `ride-kind-endurance">Endurance</span>`) || !strings.Contains(body, "Mostly steady at 65% of FTP; 2 efforts:") {
		t.Errorf("surges on an endurance ride should read as endurance:\n%s", between(body, `class="ride-what"`, `</div>`))
	}

	rideFrom(t, d, "steady", 250, false, segment{3600, 165, 130})
	if body := get(t, h, "/workouts/steady"); !strings.Contains(body, "Steady riding at 66% of FTP, no efforts at or above 85%.") ||
		strings.Contains(body, "intervals-table") {
		t.Error("a steady ride should say so and show no intervals table")
	}
}

// "FTP (indoor)" only when an indoor FTP entry actually applied.
func TestRidePage_IndoorFTPLabel(t *testing.T) {
	d, h := ftpFixture(t)
	if err := d.SetFTP(250); err != nil {
		t.Fatal(err)
	}
	rideFrom(t, d, "trainer", 250, true, segment{3600, 165, 130})
	if body := get(t, h, "/workouts/trainer"); strings.Contains(body, "FTP (indoor)") {
		t.Error("no indoor FTP entry: the label should be plain FTP")
	}
	if err := d.LogFTPChangeAtFor(240, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), true); err != nil {
		t.Fatal(err)
	}
	if body := get(t, h, "/workouts/trainer"); !strings.Contains(body, "<dt>FTP (indoor)</dt><dd>240 W</dd>") {
		t.Error("an indoor ride under an indoor FTP entry should say so")
	}
}

func between(s, from, to string) string {
	i := strings.Index(s, from)
	if i < 0 {
		return ""
	}
	j := strings.Index(s[i:], to)
	if j < 0 {
		return s[i:]
	}
	return s[i : i+j]
}

// "How did it feel?": the ride page shows the saved rating and note; Today
// shows the rating, or asks for one.
func TestRideFeedback_Pages(t *testing.T) {
	d, h := ftpFixture(t)
	if err := d.SetFTP(250); err != nil {
		t.Fatal(err)
	}
	w := steadyRide(t, d, "today", 0, 200)
	body := get(t, h, "/workouts/"+w.ID)
	if !strings.Contains(body, `id="feel" data-workout="today"`) || strings.Contains(body, "rpe-btn rpe-hard selected") {
		t.Error("an unrated ride should show the scale with nothing picked")
	}
	if !strings.Contains(get(t, h, "/"), `href="/workouts/today#feel">How did it feel? Rate it →</a>`) {
		t.Error("Today should ask how an unrated ride felt")
	}

	rpe := 7
	if err := d.SaveWorkoutFeedback(w.ID, &rpe, "windy"); err != nil {
		t.Fatal(err)
	}
	body = get(t, h, "/workouts/"+w.ID)
	if !strings.Contains(body, `class="rpe-btn rpe-hard selected" data-rpe="7" role="radio" aria-checked="true"`) ||
		!strings.Contains(body, ">windy</textarea>") {
		t.Error("the ride page should show the saved rating and note")
	}
	if !strings.Contains(get(t, h, "/"), "felt <strong>7/10</strong> · hard · “windy”") {
		t.Error("Today should show how the ride felt")
	}
}
