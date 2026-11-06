package api

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/fitness"
	"github.com/fitbase/fitbase/internal/models"
)

// zoneToolKey is a fixed 32-byte key used only in tests.
var zoneToolKey = []byte("fitbase-test-key-do-not-use-prod")

// zoneDistFixture builds a coach handler over a database holding one ride with
// HR and one recorded without a strap, both inside the window.
func zoneDistFixture(t *testing.T, athlete *models.Athlete) *CoachHandler {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"), zoneToolKey)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.UpdateAthlete(athlete); err != nil {
		t.Fatalf("UpdateAthlete: %v", err)
	}
	if athlete.HRZonesJSON != "" {
		if err := d.SetCustomHRZones(athlete.HRZonesJSON); err != nil {
			t.Fatalf("SetCustomHRZones: %v", err)
		}
	}
	for i, hr := range [][5]int{{60, 600, 300, 0, 0}, {}} {
		w := &models.Workout{
			ID:           "zonetool00000" + string(rune('a'+i)),
			Filename:     "z.fit",
			RecordedAt:   time.Now().Add(-time.Duration(i+1) * 24 * time.Hour),
			Sport:        "cycling",
			DurationSecs: 3600,
		}
		if err := d.InsertWorkout(w, nil); err != nil {
			t.Fatalf("InsertWorkout: %v", err)
		}
		if err := d.InsertZoneTimes(w.ID, [7]int{120, 600, 240, 0, 0, 0, 0}, hr, 60); err != nil {
			t.Fatalf("InsertZoneTimes: %v", err)
		}
	}
	return NewCoachHandler(d)
}

// zoneBuckets pulls the labeled entries out of one array of the tool result.
func zoneBuckets(t *testing.T, result, key string) []zoneEntry {
	t.Helper()
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	var arr struct {
		Unit  string      `json:"unit"`
		Zones []zoneEntry `json:"zones"`
	}
	if err := json.Unmarshal(parsed[key], &arr); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	return arr.Zones
}

// A bare array of five numbers leaves the model to guess that index 0 is Z1 and
// to re-derive the boundaries — which is impossible for a rider on custom
// zones. Every bucket must carry its own label and range.
func TestToolZoneDistribution_LabelsHRZonesWithRanges(t *testing.T) {
	h := zoneDistFixture(t, &models.Athlete{FTPWatts: 230, WeightKG: 75, ThresholdHR: 174})

	out, err := h.toolZoneDistribution([]byte(`{"days":14}`))
	if err != nil {
		t.Fatalf("toolZoneDistribution: %v", err)
	}

	hr := zoneBuckets(t, out, "hr_zones")
	if len(hr) != 5 {
		t.Fatalf("got %d HR buckets, want 5", len(hr))
	}
	for i, want := range []string{"Z1", "Z2", "Z3", "Z4", "Z5"} {
		if hr[i].Label != want {
			t.Errorf("hr[%d].label = %q, want %q", i, hr[i].Label, want)
		}
		if hr[i].Range == "" {
			t.Errorf("hr[%d] (%s) has no bpm range", i, want)
		}
	}
	if hr[2].Range != "145–163 bpm" {
		t.Errorf("Z3 range = %q, want 145–163 bpm", hr[2].Range)
	}
	// 600s in HR Z2 from the one ride that recorded HR.
	if hr[1].Value != 10 {
		t.Errorf("Z2 = %v minutes, want 10", hr[1].Value)
	}
	if power := zoneBuckets(t, out, "power_zones"); len(power) != 7 || power[6].Label != "Z7" {
		t.Errorf("power zones = %+v, want 7 buckets ending at Z7", power)
	}
}

// Rides without a strap contribute power time and zero HR time, so the two
// arrays have different denominators. Saying so is the difference between the
// model reading "12h easy riding" and "no HR recorded".
func TestToolZoneDistribution_WarnsOnPartialHRCoverage(t *testing.T) {
	h := zoneDistFixture(t, &models.Athlete{FTPWatts: 230, WeightKG: 75, ThresholdHR: 174})

	out, err := h.toolZoneDistribution([]byte(`{"days":14}`))
	if err != nil {
		t.Fatalf("toolZoneDistribution: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	caveat, _ := parsed["hr_coverage_caveat"].(string)
	if !strings.Contains(caveat, "1 of 2 rides") {
		t.Errorf("hr_coverage_caveat = %q, want it to report 1 of 2 rides", caveat)
	}
	if parsed["workout_count"] != float64(2) {
		t.Errorf("workout_count = %v, want 2", parsed["workout_count"])
	}
}

// With HR on every ride there is nothing to caveat, and an unconditional
// warning would train the model to discount good data.
func TestToolZoneDistribution_NoCaveatWhenHRIsComplete(t *testing.T) {
	h := zoneDistFixture(t, &models.Athlete{FTPWatts: 230, WeightKG: 75, ThresholdHR: 174})
	// Give the strapless ride HR too.
	if err := h.db.InsertZoneTimes("zonetool00000b", [7]int{120, 600, 240, 0, 0, 0, 0}, [5]int{0, 500, 100, 0, 0}, 60); err != nil {
		t.Fatalf("InsertZoneTimes: %v", err)
	}

	out, err := h.toolZoneDistribution([]byte(`{"days":14}`))
	if err != nil {
		t.Fatalf("toolZoneDistribution: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := parsed["hr_coverage_caveat"]; ok {
		t.Errorf("hr_coverage_caveat present with full HR coverage: %v", parsed["hr_coverage_caveat"])
	}
}

// A rider on custom zones has boundaries that cannot be derived from LTHR — if
// the profile doesn't carry them, nothing else will.
func TestToolAthleteProfile_CarriesCustomHRZoneBounds(t *testing.T) {
	h := zoneDistFixture(t, &models.Athlete{
		FTPWatts: 230, WeightKG: 75, HRZonesJSON: "[120,145,160,175,0,0]",
	})

	out, err := h.toolAthleteProfile()
	if err != nil {
		t.Fatalf("toolAthleteProfile: %v", err)
	}
	var profile struct {
		HRZones []struct {
			Label string `json:"label"`
			Range string `json:"range"`
		} `json:"hr_zones"`
	}
	if err := json.Unmarshal([]byte(out), &profile); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(profile.HRZones) != 5 {
		t.Fatalf("got %d zones, want 5: %s", len(profile.HRZones), out)
	}
	if profile.HRZones[1].Range != "121–145 bpm" {
		t.Errorf("Z2 range = %q, want 121–145 bpm", profile.HRZones[1].Range)
	}
}

// intervalRide builds a 1Hz stream: 10min warmup, five 4min reps at the given
// watts with rising HR, 4min recoveries, 10min cooldown.
func intervalRide(start time.Time, repWatts []int) []models.Stream {
	var out []models.Stream
	at := start
	add := func(secs, watts, hr int) {
		for i := 0; i < secs; i++ {
			w, h := watts, hr
			out = append(out, models.Stream{Timestamp: at, PowerWatts: &w, HeartRateBPM: &h})
			at = at.Add(time.Second)
		}
	}
	add(600, 120, 115)
	for i, w := range repWatts {
		add(240, w, 160+2*i)
		if i < len(repWatts)-1 {
			add(240, 100, 130)
		}
	}
	add(600, 100, 120)
	return out
}

// The coach can only grade an interval session if it sees the reps, how they
// faded, and what was prescribed. %FTP must use the FTP in effect on the
// ride's date — the rider has since raised it, and grading old reps against
// today's FTP would call a well-executed session a failure.
func TestToolWorkoutIntervals_ReportsSetFadeAndPlan(t *testing.T) {
	h := zoneDistFixture(t, &models.Athlete{FTPWatts: 250, WeightKG: 75, ThresholdHR: 174})
	rideAt := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	if err := h.db.LogFTPChangeAt(200, rideAt.Add(-10*24*time.Hour)); err != nil {
		t.Fatalf("LogFTPChangeAt: %v", err)
	}

	w := &models.Workout{
		ID: "shapetool000001", Filename: "vo2.fit", RecordedAt: rideAt,
		Sport: "cycling", DurationSecs: 4320, IsIndoor: true,
	}
	if err := h.db.InsertWorkout(w, intervalRide(rideAt, []int{240, 238, 236, 232, 226})); err != nil {
		t.Fatalf("InsertWorkout: %v", err)
	}
	stored, err := h.db.GetWorkout(w.ID)
	if err != nil || stored == nil {
		t.Fatalf("GetWorkout: %v", err)
	}
	day, err := time.Parse("2006-01-02", stored.TrainingDay)
	if err != nil {
		t.Fatalf("training day %q: %v", stored.TrainingDay, err)
	}
	work, rec := 120, 50
	if _, err := h.db.CreatePlannedWorkout(models.PlannedWorkout{
		PlannedDate: day, Sport: "cycling", Title: "5x4 VO2", DurationSecs: 4320,
		Intervals: []models.PlannedInterval{{Repeat: 5, Steps: []models.PlannedInterval{
			{Kind: "vo2max", DurationSecs: 240, TargetPctFTP: &work},
			{Kind: "recovery", DurationSecs: 240, TargetPctFTP: &rec},
		}}},
	}); err != nil {
		t.Fatalf("CreatePlannedWorkout: %v", err)
	}

	out, err := h.toolWorkoutIntervals([]byte(`{"id":"shapetool000001"}`))
	if err != nil {
		t.Fatalf("toolWorkoutIntervals: %v", err)
	}
	var got models.WorkoutShape
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}

	if got.FTPWatts != 200 {
		t.Errorf("ftp_watts = %d, want 200 (FTP on the ride's date, not today's 250)", got.FTPWatts)
	}
	if len(got.Efforts) != 5 {
		t.Fatalf("got %d efforts, want 5:\n%s", len(got.Efforts), out)
	}
	if e := got.Efforts[0]; e.PctFTP != 120 || e.AvgWatts != 240 || e.Start != "0:10:00" {
		t.Errorf("effort 1 = %+v, want 240W / 120%% FTP starting 0:10:00", e)
	}
	if len(got.Sets) != 1 {
		t.Fatalf("got %d sets, want 1:\n%s", len(got.Sets), out)
	}
	set := got.Sets[0]
	if set.Reps != 5 || set.FirstEffort != 1 {
		t.Errorf("set = %d reps from effort %d, want 5 from 1", set.Reps, set.FirstEffort)
	}
	if want := []int{240, 238, 236, 232, 226}; fmt.Sprint(set.RepAvgWatts) != fmt.Sprint(want) {
		t.Errorf("rep_avg_watts = %v, want %v", set.RepAvgWatts, want)
	}
	if set.FadePct != -5.8 {
		t.Errorf("fade_pct = %v, want -5.8 (226 vs 240)", set.FadePct)
	}
	if set.HRCreepBPM == nil || *set.HRCreepBPM != 8 {
		t.Errorf("hr_creep_bpm = %v, want 8", set.HRCreepBPM)
	}
	if set.AvgRecoverySecs == nil || *set.AvgRecoverySecs < 238 || *set.AvgRecoverySecs > 242 {
		t.Errorf("avg_recovery_secs = %v, want ~240", set.AvgRecoverySecs)
	}
	if len(got.Planned) != 1 || got.Planned[0].Title != "5x4 VO2" {
		t.Errorf("planned = %+v, want the 5x4 VO2 plan for that day", got.Planned)
	}
	if !strings.Contains(strings.Join(got.Notes, " "), "planned for this day") {
		t.Errorf("notes = %v, want a note pointing at the plan", got.Notes)
	}
	if len(got.Profile.Buckets) != 28 || got.Profile.BucketSecs != 120 {
		t.Errorf("profile = %d buckets of %ds, want 28 × 2-min buckets for a 56-min ride", len(got.Profile.Buckets), got.Profile.BucketSecs)
	}
}

// A steady ride has no efforts; the tool must say so explicitly so the model
// reads "no intervals" rather than "missing data".
func TestToolWorkoutIntervals_SteadyRideSaysSo(t *testing.T) {
	h := zoneDistFixture(t, &models.Athlete{FTPWatts: 250, WeightKG: 75})
	rideAt := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	var streams []models.Stream
	for i := 0; i < 3600; i++ {
		p := 160
		streams = append(streams, models.Stream{Timestamp: rideAt.Add(time.Duration(i) * time.Second), PowerWatts: &p})
	}
	w := &models.Workout{ID: "shapetool000002", Filename: "z2.fit", RecordedAt: rideAt, Sport: "cycling", DurationSecs: 3600}
	if err := h.db.InsertWorkout(w, streams); err != nil {
		t.Fatalf("InsertWorkout: %v", err)
	}

	out, err := h.toolWorkoutIntervals([]byte(`{"id":"shapetool000002"}`))
	if err != nil {
		t.Fatalf("toolWorkoutIntervals: %v", err)
	}
	var got models.WorkoutShape
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Efforts) != 0 || len(got.Sets) != 0 {
		t.Errorf("steady ride: %d efforts / %d sets, want none", len(got.Efforts), len(got.Sets))
	}
	if !strings.Contains(strings.Join(got.Notes, " "), "No sustained efforts") {
		t.Errorf("notes = %v, want the steady-ride note", got.Notes)
	}
	// Arrays must be [] not null, so the model never sees a missing field.
	if !strings.Contains(out, `"efforts":[]`) || !strings.Contains(out, `"sets":[]`) {
		t.Errorf("efforts/sets not serialized as empty arrays: %s", out)
	}
}

// A ride's shape is cached after the first call: the second call must not
// need the streams at all. Changing the FTP in effect on the ride's date
// makes the cached row stale, so the shape is recomputed — here from streams
// that are gone, which proves the stale row was not served.
func TestToolWorkoutIntervals_CachesAndRecomputesOnFTPChange(t *testing.T) {
	h := zoneDistFixture(t, &models.Athlete{FTPWatts: 250, WeightKG: 75})
	rideAt := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	if err := h.db.LogFTPChangeAt(200, rideAt.Add(-10*24*time.Hour)); err != nil {
		t.Fatalf("LogFTPChangeAt: %v", err)
	}
	w := &models.Workout{ID: "shapetool000003", Filename: "c.fit", RecordedAt: rideAt, Sport: "cycling", DurationSecs: 3360}
	if err := h.db.InsertWorkout(w, intervalRide(rideAt, []int{240, 240, 240})); err != nil {
		t.Fatalf("InsertWorkout: %v", err)
	}
	efforts := func() (int, []string) {
		t.Helper()
		out, err := h.toolWorkoutIntervals([]byte(`{"id":"shapetool000003"}`))
		if err != nil {
			t.Fatalf("toolWorkoutIntervals: %v", err)
		}
		var got models.WorkoutShape
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return len(got.Efforts), got.Notes
	}

	if n, _ := efforts(); n != 3 {
		t.Fatalf("first call: %d efforts, want 3", n)
	}
	if _, err := h.db.Exec(`DELETE FROM workout_streams WHERE workout_id = ?`, w.ID); err != nil {
		t.Fatalf("drop streams: %v", err)
	}
	if n, _ := efforts(); n != 3 {
		t.Errorf("second call: %d efforts, want 3 from the cache without streams", n)
	}

	// Backdate a new FTP so it covers the ride: the cached 200W shape is stale.
	if err := h.db.LogFTPChangeAt(230, rideAt.Add(-24*time.Hour)); err != nil {
		t.Fatalf("LogFTPChangeAt: %v", err)
	}
	n, notes := efforts()
	if n != 0 || !strings.Contains(strings.Join(notes, " "), "no recorded stream data") {
		t.Errorf("after FTP change: %d efforts, notes %v — want a recompute (no streams left)", n, notes)
	}
}

func TestShapeHeadline(t *testing.T) {
	set := func(watts []float64, hr []int, dur int) fitness.RideShape {
		s := fitness.RideShape{HasPower: true}
		for i, w := range watts {
			e := fitness.Effort{StartSecs: 600 + i*2*dur, DurationSecs: dur, AvgWatts: w}
			if hr != nil {
				e.MaxHR = hr[i]
			}
			s.Efforts = append(s.Efforts, e)
		}
		s.Sets = []fitness.EffortSet{{First: 0, Count: len(watts)}}
		return s
	}
	cases := []struct {
		name  string
		shape fitness.RideShape
		ftp   int
		want  string
	}{
		{"no power", fitness.RideShape{}, 250, ""},
		{"no ftp", fitness.RideShape{HasPower: true}, 0, ""},
		{"steady", fitness.RideShape{HasPower: true}, 250, "steady — no efforts at or above 85% FTP"},
		{
			"set with fade and HR creep",
			set([]float64{300, 298, 296, 290, 281}, []int{160, 162, 164, 166, 168}, 240), 250,
			"5×4:00 @ 117% FTP, last rep -6.3% vs first, HR +8",
		},
		{
			"set without HR",
			set([]float64{230, 232}, nil, 1200), 250,
			"2×20:00 @ 92% FTP, last rep +0.9% vs first",
		},
		{
			"set plus a loose effort",
			func() fitness.RideShape {
				s := set([]float64{300, 300, 300}, nil, 240)
				s.Efforts = append(s.Efforts, fitness.Effort{StartSecs: 5000, DurationSecs: 600, AvgWatts: 230})
				return s
			}(), 250,
			"3×4:00 @ 120% FTP, last rep +0.0% vs first; 1 other effort: 10:00 @ 92%",
		},
		{
			"loose efforts only, capped",
			fitness.RideShape{HasPower: true, Efforts: []fitness.Effort{
				{DurationSecs: 300, AvgWatts: 240}, {DurationSecs: 30, AvgWatts: 400},
				{DurationSecs: 720, AvgWatts: 235}, {DurationSecs: 25, AvgWatts: 380},
				{DurationSecs: 480, AvgWatts: 250}, {DurationSecs: 3900, AvgWatts: 215},
			}}, 250,
			"6 efforts: 5:00 @ 96%, 12:00 @ 94%, 8:00 @ 100%, 1:05:00 @ 86% +2 more",
		},
	}
	for _, c := range cases {
		if got := shapeHeadline(c.shape, c.ftp); got != c.want {
			t.Errorf("%s:\n got  %q\n want %q", c.name, got, c.want)
		}
	}
}

// The insights payload carries every recent ride; its structure line is what
// lets the report talk about how sessions were executed.
func TestBriefFromWorkout_CarriesStructure(t *testing.T) {
	h := zoneDistFixture(t, &models.Athlete{FTPWatts: 250, WeightKG: 75})
	rideAt := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	avg := 180.0
	w := &models.Workout{ID: "shapebrief00001", Filename: "b.fit", RecordedAt: rideAt, Sport: "cycling", DurationSecs: 3360, AvgPowerWatts: &avg}
	if err := h.db.InsertWorkout(w, intervalRide(rideAt, []int{300, 300, 300, 300, 300})); err != nil {
		t.Fatalf("InsertWorkout: %v", err)
	}
	stored, err := h.db.GetWorkout(w.ID)
	if err != nil || stored == nil {
		t.Fatalf("GetWorkout: %v", err)
	}
	brief := h.briefFromWorkout(*stored)
	if want := "5×4:00 @ 120% FTP, last rep +0.0% vs first, HR +8"; brief.Structure != want {
		t.Errorf("structure = %q, want %q", brief.Structure, want)
	}
}

func TestToolWorkoutIntervals_UnknownID(t *testing.T) {
	h := zoneDistFixture(t, &models.Athlete{FTPWatts: 250, WeightKG: 75})
	out, err := h.toolWorkoutIntervals([]byte(`{"id":"nope"}`))
	if err != nil {
		t.Fatalf("toolWorkoutIntervals: %v", err)
	}
	if !strings.Contains(out, "no workout with that id") {
		t.Errorf("result = %s, want a not-found error the model can read", out)
	}
	if _, err := h.toolWorkoutIntervals([]byte(`{}`)); err == nil {
		t.Error("missing id: want an error")
	}
}
