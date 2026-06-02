package db_test

import (
	"errors"
	"testing"
	"time"

	"database/sql"

	"github.com/fitbase/fitbase/internal/models"
)

func TestPlannedWorkoutCRUD(t *testing.T) {
	d := newTestDB(t)

	tss := 90.0
	intf := 0.85
	p := models.PlannedWorkout{
		PlannedDate:     time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC),
		Sport:           "cycling",
		Title:           "2x20 sweet spot",
		Description:     "warmup, 2x20min @ 88% FTP w/ 5min easy, cooldown",
		DurationSecs:    90 * 60,
		TSS:             &tss,
		IntensityFactor: &intf,
		Intervals: []models.PlannedInterval{
			{Kind: "warmup", DurationSecs: 15 * 60, TargetPctFTPLow: intPtr(50), TargetPctFTPHigh: intPtr(75)},
			{Repeat: 2, Steps: []models.PlannedInterval{
				{Kind: "sweet_spot", DurationSecs: 20 * 60, TargetPctFTP: intPtr(88)},
				{Kind: "recovery", DurationSecs: 5 * 60},
			}},
			{Kind: "cooldown", DurationSecs: 10 * 60},
		},
		Source: "manual",
	}

	saved, err := d.CreatePlannedWorkout(p)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if saved.ID == "" {
		t.Fatal("server should assign an id")
	}
	if saved.CreatedAt.IsZero() {
		t.Error("server should populate created_at")
	}
	// Round-tripping the PlannedDate is what makes the row visible on the
	// calendar — a silent format mismatch here zeroed the date and hid every
	// planned workout the model proposed.
	if saved.PlannedDate.Year() != 2026 || saved.PlannedDate.Month() != time.May || saved.PlannedDate.Day() != 25 {
		t.Errorf("planned_date round-trip wrong: got %s, want 2026-05-25", saved.PlannedDate.Format(time.RFC3339))
	}
	// Three top-level nodes: warmup ramp, a 2× repeat group, cooldown.
	if len(saved.Intervals) != 3 {
		t.Fatalf("intervals lost in round trip: got %d, want 3", len(saved.Intervals))
	}
	// The warmup ramp's low/high must survive.
	if saved.Intervals[0].TargetPctFTPLow == nil || *saved.Intervals[0].TargetPctFTPLow != 50 ||
		saved.Intervals[0].TargetPctFTPHigh == nil || *saved.Intervals[0].TargetPctFTPHigh != 75 {
		t.Errorf("ramp low/high lost in round trip")
	}
	// The repeat group and its nested target must survive.
	grp := saved.Intervals[1]
	if !grp.IsGroup() || grp.Repeat != 2 || len(grp.Steps) != 2 {
		t.Fatalf("repeat group lost: IsGroup=%v Repeat=%d steps=%d", grp.IsGroup(), grp.Repeat, len(grp.Steps))
	}
	if grp.Steps[0].TargetPctFTP == nil || *grp.Steps[0].TargetPctFTP != 88 {
		t.Errorf("nested interval target_pct_ftp lost")
	}

	// List a window that includes the date.
	rows, err := d.ListPlannedWorkoutsBetween(
		time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 30, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != saved.ID {
		t.Errorf("list returned %d rows, want 1 with id %s", len(rows), saved.ID)
	}

	// Range that excludes the date returns nothing.
	rows, err = d.ListPlannedWorkoutsBetween(
		time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("list out-of-range: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows out of range, got %d", len(rows))
	}

	// Delete by id.
	if err := d.DeletePlannedWorkout(saved.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, _ := d.GetPlannedWorkout(saved.ID)
	if got != nil {
		t.Error("planned workout should be gone after delete")
	}

	// Deleting again should return ErrNoRows so handlers can return 404.
	if err := d.DeletePlannedWorkout(saved.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("re-delete err = %v, want sql.ErrNoRows", err)
	}
}

func TestPlannedWorkoutDrafts(t *testing.T) {
	d := newTestDB(t)

	payload := `{"workouts":[{"date":"2026-05-25","duration_secs":3600}]}`
	id, err := d.SavePlannedDraft(payload)
	if err != nil {
		t.Fatalf("save draft: %v", err)
	}
	if id == "" {
		t.Fatal("draft id should not be empty")
	}

	got, err := d.GetPlannedDraft(id)
	if err != nil {
		t.Fatalf("get draft: %v", err)
	}
	if got != payload {
		t.Errorf("draft payload round-trip mismatch:\n got: %s\nwant: %s", got, payload)
	}

	// Missing id returns empty string, no error — handlers map this to 404.
	missing, err := d.GetPlannedDraft("does-not-exist")
	if err != nil {
		t.Errorf("missing draft should not error: %v", err)
	}
	if missing != "" {
		t.Errorf("missing draft should return empty string, got %q", missing)
	}

	if err := d.DeletePlannedDraft(id); err != nil {
		t.Fatalf("delete draft: %v", err)
	}
	after, _ := d.GetPlannedDraft(id)
	if after != "" {
		t.Error("draft should be gone after delete")
	}
}

func intPtr(v int) *int { return &v }
