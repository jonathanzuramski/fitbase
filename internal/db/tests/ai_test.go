package db_test

import (
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/models"
)

// The AI tables (ai_settings, ai_insights_cache_v2, workout_decoupling,
// planned_workout_drafts) are created by migration v4 — these round trips are
// what proves the migration actually ran on a fresh database.

func TestAISettingsRoundTrip(t *testing.T) {
	d := newTestDB(t)

	// Nothing saved yet: empty settings, no error.
	s, err := d.GetAISettings()
	if err != nil {
		t.Fatalf("get on empty: %v", err)
	}
	if s.Provider != "" || s.APIKey != "" {
		t.Errorf("expected empty settings, got %+v", s)
	}

	if err := d.SaveAISettings(db.AISettings{Provider: "anthropic", Model: "claude-sonnet-4-6", APIKey: "sk-secret"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := d.GetAISettings()
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Provider != "anthropic" || got.Model != "claude-sonnet-4-6" || got.APIKey != "sk-secret" {
		t.Errorf("round trip mismatch: %+v", got)
	}

	// Upsert overwrites the single row.
	if err := d.SaveAISettings(db.AISettings{Provider: "gemini", Model: "gemini-2.0-flash", APIKey: "g-key"}); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	got, err = d.GetAISettings()
	if err != nil {
		t.Fatalf("get after re-save: %v", err)
	}
	if got.Provider != "gemini" || got.APIKey != "g-key" {
		t.Errorf("upsert mismatch: %+v", got)
	}
}

func TestCachedInsightsRoundTrip(t *testing.T) {
	d := newTestDB(t)

	c, err := d.GetCachedInsights()
	if err != nil {
		t.Fatalf("get on empty: %v", err)
	}
	if c != nil {
		t.Errorf("expected nil cache, got %+v", c)
	}

	if err := d.SaveCachedInsights(db.CachedInsights{Provider: "anthropic", Model: "m", Content: "## Training review\n…"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	c, err = d.GetCachedInsights()
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if c == nil || c.Content != "## Training review\n…" || c.Provider != "anthropic" {
		t.Errorf("round trip mismatch: %+v", c)
	}
	if c.GeneratedAt.IsZero() {
		t.Error("generated_at should be stamped by the server")
	}

	if err := d.ClearCachedInsights(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if c, _ := d.GetCachedInsights(); c != nil {
		t.Errorf("cache should be empty after clear, got %+v", c)
	}
}

func TestWorkoutDecouplingCache(t *testing.T) {
	d := newTestDB(t)
	// FK target for workout_decoupling.
	w := sampleWorkout("decoupling0000001")
	if err := d.InsertWorkout(w, nil); err != nil {
		t.Fatalf("InsertWorkout: %v", err)
	}

	// Nothing cached yet.
	if _, _, found, err := d.GetWorkoutDecoupling(w.ID); err != nil || found {
		t.Fatalf("expected no cache row, found=%v err=%v", found, err)
	}

	// Cache a computed value.
	v := 4.2
	if err := d.SaveWorkoutDecoupling(w.ID, &v); err != nil {
		t.Fatalf("save value: %v", err)
	}
	val, ok, found, err := d.GetWorkoutDecoupling(w.ID)
	if err != nil || !found || !ok || val != 4.2 {
		t.Errorf("value round trip: val=%v ok=%v found=%v err=%v", val, ok, found, err)
	}

	// Cache "not computable" (nil) — must read back as found but not ok.
	if err := d.SaveWorkoutDecoupling(w.ID, nil); err != nil {
		t.Fatalf("save nil: %v", err)
	}
	_, ok, found, err = d.GetWorkoutDecoupling(w.ID)
	if err != nil || !found || ok {
		t.Errorf("nil round trip: ok=%v found=%v err=%v", ok, found, err)
	}
}

func TestCommitPlannedDraftAtomic(t *testing.T) {
	d := newTestDB(t)

	draftID, err := d.SavePlannedDraft(`{"workouts":[]}`)
	if err != nil {
		t.Fatalf("save draft: %v", err)
	}

	date := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	mk := func(id, title string) models.PlannedWorkout {
		return models.PlannedWorkout{ID: id, PlannedDate: date, Title: title, DurationSecs: 3600, Source: "coach"}
	}

	// A duplicate id makes the second insert fail: the whole batch must roll
	// back and the draft must survive for retry — a partial commit here is
	// what used to duplicate workouts when the rider clicked again.
	if _, err := d.CommitPlannedDraft(draftID, []models.PlannedWorkout{mk("dup1", "a"), mk("dup1", "b")}); err == nil {
		t.Fatal("expected commit to fail on duplicate id")
	}
	rows, err := d.ListPlannedWorkoutsBetween(date, date)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("failed commit leaked %d workouts onto the calendar", len(rows))
	}
	if payload, _ := d.GetPlannedDraft(draftID); payload == "" {
		t.Error("draft should survive a failed commit for retry")
	}

	// Retry with a valid batch: workouts land and the draft is consumed.
	saved, err := d.CommitPlannedDraft(draftID, []models.PlannedWorkout{mk("ok1", "a"), mk("ok2", "b")})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if len(saved) != 2 {
		t.Fatalf("saved %d workouts, want 2", len(saved))
	}
	rows, err = d.ListPlannedWorkoutsBetween(date, date)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("calendar has %d workouts, want 2", len(rows))
	}
	if payload, _ := d.GetPlannedDraft(draftID); payload != "" {
		t.Error("draft should be removed after a successful commit")
	}
}
