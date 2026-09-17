package api

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/db"
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
