package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/models"
)

// ── GET /api/workouts/{id}/shape ──────────────────────────────────────────────

func TestGetWorkoutShape_DetectsEfforts(t *testing.T) {
	h, d := newTestHandler(t)
	// Default athlete FTP is 250: 3×5min at 300W (120%) with 5min easy between.
	start := time.Date(2024, 3, 15, 8, 0, 0, 0, time.UTC)
	var streams []models.Stream
	add := func(secs, watts int) {
		for i := 0; i < secs; i++ {
			w := watts
			streams = append(streams, models.Stream{Timestamp: start.Add(time.Duration(len(streams)) * time.Second), PowerWatts: &w})
		}
	}
	add(600, 140)
	for i := 0; i < 3; i++ {
		add(300, 300)
		add(300, 120)
	}
	w := &models.Workout{ID: "shaperest000001", Filename: "s.fit", RecordedAt: start, Sport: "cycling", DurationSecs: len(streams)}
	if err := d.InsertWorkout(w, streams); err != nil {
		t.Fatalf("InsertWorkout: %v", err)
	}

	req := withURLParam(httptest.NewRequest("GET", "/api/workouts/"+w.ID+"/shape", nil), "id", w.ID)
	rr := httptest.NewRecorder()
	h.GetWorkoutShape(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	var got models.WorkoutShape
	if errStr := decodeEnvelope(t, rr.Body.Bytes(), &got); errStr != "" {
		t.Fatalf("response error: %s", errStr)
	}
	if len(got.Efforts) != 3 || len(got.Sets) != 1 || got.Sets[0].Reps != 3 {
		t.Fatalf("efforts=%d sets=%+v, want 3 efforts in one set", len(got.Efforts), got.Sets)
	}
	if got.Efforts[1].PctFTP != 120 || got.Efforts[1].DurationSecs != 300 {
		t.Errorf("effort 2 = %+v, want 300s at 120%% FTP", got.Efforts[1])
	}
	if got.Detection == nil || got.Detection.StartWatts != 213 {
		t.Errorf("detection = %+v, want start_watts 213 (85%% of 250)", got.Detection)
	}
}

func TestGetWorkoutShape_NotFound(t *testing.T) {
	h, _ := newTestHandler(t)
	req := withURLParam(httptest.NewRequest("GET", "/api/workouts/missing/shape", nil), "id", "missing")
	rr := httptest.NewRecorder()
	h.GetWorkoutShape(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}
