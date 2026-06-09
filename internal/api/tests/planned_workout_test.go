package api_test

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/api"
	"github.com/fitbase/fitbase/internal/models"
)

// ─────────────────────────────────────────────────────────────────────────────
// These tests exercise every function in internal/api/planned_workout.go:
//
//   • NewPlannedHandler — via newPlannedHandler below (used by every test)
//   • Create            — POST handler; this is also where toModel runs, so the
//                         Create_* tests double as the coverage for toModel's
//                         validation / defaulting / TSS-estimation branches.
//   • List              — GET handler with the date-range query params.
//   • Delete            — DELETE handler, success + not-found.
//
// toModel is unexported, so it can't be called directly from this external test
// package; instead each of its branches is driven through Create and asserted on
// the persisted result. The comment on each Create_* test names the branch it
// covers so you can trace the data flow request → toModel → model → DB → JSON.
// ─────────────────────────────────────────────────────────────────────────────

func newPlannedHandler(t *testing.T) *api.PlannedHandler {
	t.Helper()
	return api.NewPlannedHandler(newTestDB(t))
}

// today / future return YYYY-MM-DD strings relative to now so the tests aren't
// pinned to a calendar date and stay inside List's default window where needed.
func today() string  { return time.Now().UTC().Format("2006-01-02") }
func future() string { return time.Now().UTC().AddDate(0, 0, 100).Format("2006-01-02") }

// createPlanned posts one body through Create and returns the recorder.
func createPlanned(t *testing.T, h *api.PlannedHandler, body any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.Create(w, postJSON(t, body))
	return w
}

// ── Create / toModel ─────────────────────────────────────────────────────────

// Covers the happy path plus two toModel defaults: empty sport → "cycling" and
// source stamped "manual" by the handler (not the client).
func TestPlannedCreate_MinimalDefaults(t *testing.T) {
	h := newPlannedHandler(t)

	w := createPlanned(t, h, map[string]any{
		"date":          today(),
		"duration_secs": 3600,
	})

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusCreated, w.Body)
	}
	var got models.PlannedWorkout
	if errMsg := decodeEnvelope(t, w.Body.Bytes(), &got); errMsg != "" {
		t.Fatalf("unexpected error in envelope: %s", errMsg)
	}
	if got.ID == "" {
		t.Error("expected DB-assigned ID, got empty")
	}
	if got.Sport != "cycling" {
		t.Errorf("sport = %q, want cycling (default)", got.Sport)
	}
	if got.Source != "manual" {
		t.Errorf("source = %q, want manual", got.Source)
	}
	if got.DurationSecs != 3600 {
		t.Errorf("duration = %d, want 3600", got.DurationSecs)
	}
}

// Covers toModel's whitespace trimming of date/title/description/sport.
func TestPlannedCreate_TrimsFields(t *testing.T) {
	h := newPlannedHandler(t)

	w := createPlanned(t, h, map[string]any{
		"date":          "  " + today() + "  ",
		"sport":         "  running  ",
		"title":         "  Morning Z2  ",
		"description":   "  ride to feel  ",
		"duration_secs": 1800,
	})

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusCreated, w.Body)
	}
	var got models.PlannedWorkout
	decodeEnvelope(t, w.Body.Bytes(), &got)
	if got.Sport != "running" {
		t.Errorf("sport = %q, want trimmed 'running'", got.Sport)
	}
	if got.Title != "Morning Z2" {
		t.Errorf("title = %q, want trimmed 'Morning Z2'", got.Title)
	}
	if got.Description != "ride to feel" {
		t.Errorf("description = %q, want trimmed 'ride to feel'", got.Description)
	}
}

// Covers the branch where structured intervals are the source of truth for
// duration: each node validates, TotalSecs sums (including a repeat group), and
// the summed total overrides the client-supplied duration_secs.
func TestPlannedCreate_DurationFromIntervals(t *testing.T) {
	h := newPlannedHandler(t)

	w := createPlanned(t, h, map[string]any{
		"date":          today(),
		"duration_secs": 1, // deliberately wrong; intervals must win
		"intervals": []map[string]any{
			{"kind": "warmup", "duration_secs": 600},
			{ // repeat group: 3 × (60 + 60) = 360
				"repeat": 3,
				"steps": []map[string]any{
					{"kind": "work", "duration_secs": 60, "target_pct_ftp": 110},
					{"kind": "recovery", "duration_secs": 60},
				},
			},
		},
	})

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusCreated, w.Body)
	}
	var got models.PlannedWorkout
	decodeEnvelope(t, w.Body.Bytes(), &got)
	if got.DurationSecs != 960 { // 600 + 360
		t.Errorf("duration = %d, want 960 (summed from intervals)", got.DurationSecs)
	}
	if len(got.Intervals) != 2 {
		t.Fatalf("intervals len = %d, want 2", len(got.Intervals))
	}
}

// Covers toModel's TSS estimation: IF given, TSS omitted → est = IF²·hours·100.
func TestPlannedCreate_EstimatesTSS(t *testing.T) {
	h := newPlannedHandler(t)

	w := createPlanned(t, h, map[string]any{
		"date":             today(),
		"duration_secs":    3600, // 1 hour
		"intensity_factor": 0.85,
	})

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusCreated, w.Body)
	}
	var got models.PlannedWorkout
	decodeEnvelope(t, w.Body.Bytes(), &got)
	if got.TSS == nil {
		t.Fatal("TSS = nil, want estimated value")
	}
	want := 0.85 * 0.85 * 1.0 * 100 // 72.25
	if math.Abs(*got.TSS-want) > 0.001 {
		t.Errorf("TSS = %.3f, want %.3f", *got.TSS, want)
	}
}

// Covers: an explicit TSS is preserved and NOT overwritten by the estimate.
func TestPlannedCreate_ExplicitTSSPreserved(t *testing.T) {
	h := newPlannedHandler(t)

	w := createPlanned(t, h, map[string]any{
		"date":             today(),
		"duration_secs":    3600,
		"intensity_factor": 0.85,
		"tss":              50.0,
	})

	var got models.PlannedWorkout
	decodeEnvelope(t, w.Body.Bytes(), &got)
	if got.TSS == nil || *got.TSS != 50.0 {
		t.Errorf("TSS = %v, want explicit 50.0", got.TSS)
	}
}

// Covers the malformed-JSON guard in Create (decodeJSON fails before toModel).
func TestPlannedCreate_BadJSON(t *testing.T) {
	h := newPlannedHandler(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// Covers every toModel validation error → 400. Table-driven so each branch is
// visible: bad date, IF out of range, no positive duration, and a bad interval.
func TestPlannedCreate_ValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
	}{
		{
			name: "bad date format",
			body: map[string]any{"date": "07-06-2026", "duration_secs": 3600},
		},
		{
			name: "intensity factor too high",
			body: map[string]any{"date": today(), "duration_secs": 3600, "intensity_factor": 2.0},
		},
		{
			name: "intensity factor zero",
			body: map[string]any{"date": today(), "duration_secs": 3600, "intensity_factor": 0.0},
		},
		{
			name: "no positive duration and no intervals",
			body: map[string]any{"date": today(), "duration_secs": 0},
		},
		{
			name: "invalid interval (zero duration leaf)",
			body: map[string]any{
				"date": today(),
				"intervals": []map[string]any{
					{"kind": "work", "duration_secs": 0},
				},
			},
		},
		{
			name: "invalid interval (ramp missing end)",
			body: map[string]any{
				"date": today(),
				"intervals": []map[string]any{
					{"kind": "work", "duration_secs": 300, "target_pct_ftp_start": 80},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newPlannedHandler(t)
			w := createPlanned(t, h, tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusBadRequest, w.Body)
			}
			if msg := decodeEnvelope(t, w.Body.Bytes(), nil); msg == "" {
				t.Error("expected a non-empty error message in envelope")
			}
		})
	}
}

// ── List ─────────────────────────────────────────────────────────────────────

// Covers the default window (today through +30d) and that the created workout
// round-trips through the DB and back out of List.
func TestPlannedList_DefaultWindow(t *testing.T) {
	h := newPlannedHandler(t)
	createPlanned(t, h, map[string]any{"date": today(), "duration_secs": 3600, "title": "in range"})

	w := httptest.NewRecorder()
	h.List(w, httptest.NewRequest(http.MethodGet, "/api/planned-workouts", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var got []models.PlannedWorkout
	decodeEnvelope(t, w.Body.Bytes(), &got)
	if len(got) != 1 || got[0].Title != "in range" {
		t.Errorf("got %d workouts %+v, want 1 titled 'in range'", len(got), got)
	}
}

// Covers explicit start/end query params: a +100d workout is outside the default
// window but appears once end is widened to include it.
func TestPlannedList_DateRangeFilter(t *testing.T) {
	h := newPlannedHandler(t)
	createPlanned(t, h, map[string]any{"date": future(), "duration_secs": 3600, "title": "far future"})

	// Default window excludes it.
	wDefault := httptest.NewRecorder()
	h.List(wDefault, httptest.NewRequest(http.MethodGet, "/api/planned-workouts", nil))
	var def []models.PlannedWorkout
	decodeEnvelope(t, wDefault.Body.Bytes(), &def)
	if len(def) != 0 {
		t.Errorf("default window returned %d, want 0 (far-future excluded)", len(def))
	}

	// Widened end includes it.
	end := time.Now().UTC().AddDate(0, 0, 200).Format("2006-01-02")
	wWide := httptest.NewRecorder()
	h.List(wWide, httptest.NewRequest(http.MethodGet, "/api/planned-workouts?start="+today()+"&end="+end, nil))
	var wide []models.PlannedWorkout
	decodeEnvelope(t, wWide.Body.Bytes(), &wide)
	if len(wide) != 1 {
		t.Errorf("widened window returned %d, want 1", len(wide))
	}
}

// Covers the nil-rows guard: an empty result serializes as [] (a JSON array),
// never null, so the calendar front end can iterate it unconditionally.
func TestPlannedList_EmptyIsArrayNotNull(t *testing.T) {
	h := newPlannedHandler(t)

	w := httptest.NewRecorder()
	h.List(w, httptest.NewRequest(http.MethodGet, "/api/planned-workouts", nil))

	if !strings.Contains(w.Body.String(), `"data":[]`) {
		t.Errorf("empty list body = %s, want data:[]", w.Body.String())
	}
}

// Covers the branch where malformed start/end params are ignored (parse fails →
// defaults kept), so a bad query string still returns the default window.
func TestPlannedList_BadDateParamsIgnored(t *testing.T) {
	h := newPlannedHandler(t)
	createPlanned(t, h, map[string]any{"date": today(), "duration_secs": 3600})

	w := httptest.NewRecorder()
	h.List(w, httptest.NewRequest(http.MethodGet, "/api/planned-workouts?start=garbage&end=nope", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var got []models.PlannedWorkout
	decodeEnvelope(t, w.Body.Bytes(), &got)
	if len(got) != 1 {
		t.Errorf("got %d, want 1 (bad params should fall back to defaults)", len(got))
	}
}

// ── Delete ───────────────────────────────────────────────────────────────────

// Covers a successful delete: 204 No Content, and the row is actually gone.
func TestPlannedDelete_Success(t *testing.T) {
	h := newPlannedHandler(t)

	cw := createPlanned(t, h, map[string]any{"date": today(), "duration_secs": 3600})
	var created models.PlannedWorkout
	decodeEnvelope(t, cw.Body.Bytes(), &created)

	dw := httptest.NewRecorder()
	h.Delete(dw, withURLParam(httptest.NewRequest(http.MethodDelete, "/", nil), "id", created.ID))
	if dw.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want %d (body: %s)", dw.Code, http.StatusNoContent, dw.Body)
	}

	// Confirm it's gone from List.
	lw := httptest.NewRecorder()
	h.List(lw, httptest.NewRequest(http.MethodGet, "/api/planned-workouts", nil))
	var got []models.PlannedWorkout
	decodeEnvelope(t, lw.Body.Bytes(), &got)
	if len(got) != 0 {
		t.Errorf("after delete List returned %d, want 0", len(got))
	}
}

// Covers the sql.ErrNoRows branch → 404 for an unknown id.
func TestPlannedDelete_NotFound(t *testing.T) {
	h := newPlannedHandler(t)

	w := httptest.NewRecorder()
	h.Delete(w, withURLParam(httptest.NewRequest(http.MethodDelete, "/", nil), "id", "does-not-exist"))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}
