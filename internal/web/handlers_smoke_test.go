package web

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/models"
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

	h := NewTemplateHandler(d, false, FS)

	for _, path := range []string{
		"/",
		"/workouts/" + w.ID,
		"/settings",
		"/calendar",
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
}
