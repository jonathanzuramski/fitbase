package web_test

import (
	"strings"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/models"
)

// Today leads with the day's plan (its interval profile included) and what has
// been ridden, and the week strip sets planned load against ridden load.
func TestToday_PlanRideAndWeek(t *testing.T) {
	d, h := ftpFixture(t)
	if err := d.SetFTP(250); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	tss := 72.0
	if _, err := d.CreatePlannedWorkout(models.PlannedWorkout{
		PlannedDate: today, Sport: "cycling", Title: "3x12 sweet spot", DurationSecs: 3900, TSS: &tss, Source: "coach",
		Intervals: []models.PlannedInterval{{Kind: "work", DurationSecs: 720, TargetPctFTP: ptr(90)}},
	}); err != nil {
		t.Fatal(err)
	}

	body := get(t, h, "/")
	for _, want := range []string{
		"<h1>today</h1>",
		`<div class="today-planned-title">3x12 sweet spot</div>`,
		"from your coach",
		`<canvas id="plan-profile"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Today lacks %q", want)
		}
	}
	if strings.Contains(body, "Nothing planned today.") {
		t.Error("a planned day should not say nothing is planned")
	}
	if !strings.Contains(body, `<span class="muted"> / 72</span> TSS`) {
		t.Error("week totals should show the planned TSS")
	}

	// Ride it: the day shows the ride, and the strip counts it.
	w := steadyRide(t, d, "done", 0, 200)
	ride := 60.0
	if err := d.UpdateWorkoutLoad(w.ID, ride, 0.8); err != nil {
		t.Fatal(err)
	}
	body = get(t, h, "/")
	if !strings.Contains(body, `<a class="today-ride" href="/workouts/done">`) || !strings.Contains(body, "<strong>60</strong>") {
		t.Error("today's ride should show on the Today card and count in the week")
	}
}

func TestToday_EmptyDay(t *testing.T) {
	_, h := ftpFixture(t)
	if body := get(t, h, "/"); !strings.Contains(body, "Nothing planned today.") {
		t.Error("an empty day should point to the calendar")
	}
}

func ptr[T any](v T) *T { return &v }
