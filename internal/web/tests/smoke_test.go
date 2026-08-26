package web_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/models"
	"github.com/fitbase/fitbase/internal/web"
)

// TestPagesRender boots the real template handler against a real database and
// requests every page. This is the safety net for the typed view structs:
// html/template fails at execute time when a template references a field the
// view doesn't have — including inside {{if .X}} conditions, which evaluate
// .X even when the branch is skipped — so a 200 here proves every referenced
// field exists. The test workout carries power, HR, and GPS data so the
// data-dependent sections of the workout page render too.
func TestPagesRender(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "smoke.db"), []byte("fitbase-test-key-do-not-use-prod"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	// The dashboard redirects to /welcome until setup completes.
	if err := d.MarkSetupComplete(); err != nil {
		t.Fatalf("mark setup complete: %v", err)
	}

	avgPower, np := 200.0, 220.0
	avgHR := 150
	tss := 80.0
	w := &models.Workout{
		ID:                  "smoke_test_workout",
		Filename:            "smoke.fit",
		RecordedAt:          time.Date(2024, 5, 1, 9, 0, 0, 0, time.UTC),
		Sport:               "cycling",
		DurationSecs:        3600,
		ElapsedSecs:         3700,
		DistanceMeters:      30000,
		ElevationGainMeters: 300,
		AvgPowerWatts:       &avgPower,
		NormalizedPower:     &np,
		AvgHeartRate:        &avgHR,
		AvgSpeedMPS:         8.3,
		TSS:                 &tss,
		CreatedAt:           time.Now().UTC(),
	}
	// A short GPS+power stream so hasGPS and the chart sections render.
	var streams []models.Stream
	for i := 0; i < 10; i++ {
		lat, lng := 39.5+float64(i)*0.001, -76.6+float64(i)*0.001
		pw := 200 + i
		streams = append(streams, models.Stream{
			Timestamp:  w.RecordedAt.Add(time.Duration(i) * time.Second),
			PowerWatts: &pw,
			Lat:        &lat,
			Lng:        &lng,
		})
	}
	if err := d.InsertWorkout(w, streams); err != nil {
		t.Fatalf("insert workout: %v", err)
	}
	if err := d.RefreshAchievements(w.ID); err != nil {
		t.Fatalf("refresh achievements: %v", err)
	}

	h := web.NewTemplateHandler(d, false, web.FS)

	for _, path := range []string{
		"/",
		"/?sort=duration&dir=asc&type=outdoor&page=1",
		"/?goal_sport=running",
		"/workouts/" + w.ID,
		"/settings",
		"/calendar",
		"/calendar?year=2024&month=5",
		"/heatmap",
		"/welcome",
		"/importing",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: got %d, want 200 (body: %.200s)", path, rec.Code, rec.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/workouts/does-not-exist", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /workouts/does-not-exist: got %d, want 404", rec.Code)
	}
}

// TestDashboardSorting proves a sort URL actually reorders the rendered table
// — not just that the page returns 200. Sorting can break silently: the
// handler whitelist-validates ?sort=, so any breakage in the key names,
// sortColumns map, or ORDER BY renders a perfectly healthy page in the
// default order. This covers the request→query half of the chain;
// TestTemplatesNoPaddedURLKeys covers the other half (the templates emitting
// well-formed sort URLs in the first place).
func TestDashboardSorting(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "sort.db"), []byte("fitbase-test-key-do-not-use-prod"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.MarkSetupComplete(); err != nil {
		t.Fatalf("mark setup complete: %v", err)
	}

	// Two workouts whose formatted durations are unambiguous in the HTML.
	short := &models.Workout{
		ID: "short_ride", Filename: "short.fit", Sport: "cycling",
		RecordedAt:   time.Date(2024, 5, 1, 9, 0, 0, 0, time.UTC),
		DurationSecs: 3600, AvgSpeedMPS: 8, DistanceMeters: 28800,
		CreatedAt: time.Now().UTC(),
	}
	long := &models.Workout{
		ID: "long_ride", Filename: "long.fit", Sport: "cycling",
		RecordedAt:   time.Date(2024, 5, 2, 9, 0, 0, 0, time.UTC),
		DurationSecs: 7200, AvgSpeedMPS: 8, DistanceMeters: 57600,
		CreatedAt: time.Now().UTC(),
	}
	for _, w := range []*models.Workout{short, long} {
		if err := d.InsertWorkout(w, nil); err != nil {
			t.Fatalf("insert %s: %v", w.ID, err)
		}
	}

	h := web.NewTemplateHandler(d, false, web.FS)
	rowOrder := func(url string) (shortIdx, longIdx int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, url, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: got %d, want 200", url, rec.Code)
		}
		body := rec.Body.String()
		shortIdx = strings.Index(body, short.ID)
		longIdx = strings.Index(body, long.ID)
		if shortIdx < 0 || longIdx < 0 {
			t.Fatalf("GET %s: workout rows missing from page", url)
		}
		return shortIdx, longIdx
	}

	if s, l := rowOrder("/?sort=duration&dir=asc"); s > l {
		t.Error("sort=duration&dir=asc: short ride should come first")
	}
	if s, l := rowOrder("/?sort=duration&dir=desc"); s < l {
		t.Error("sort=duration&dir=desc: long ride should come first")
	}
	// Default order is newest first — the long (newer) ride leads.
	if s, l := rowOrder("/"); s < l {
		t.Error("default order: newest ride should come first")
	}
}
