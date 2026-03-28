package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/api"
	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/models"
	"github.com/fitbase/fitbase/internal/importer"
	"github.com/go-chi/chi/v5"
)

// testKey is a fixed 32-byte key used only in tests.
var testKey = []byte("fitbase-test-key-do-not-use-prod")

func newTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"), testKey)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func newTestHandler(t *testing.T) (*api.Handler, *db.DB) {
	t.Helper()
	d := newTestDB(t)
	imp := importer.NewImporter(d, t.TempDir())
	return api.NewHandler(d, imp), d
}

// withURLParam injects a chi-style URL parameter into the request context.
func withURLParam(r *http.Request, key, val string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, val)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// decodeEnvelope unmarshals the standard {"data":...,"error":...} response envelope.
func decodeEnvelope(t *testing.T, body []byte, dest any) string {
	t.Helper()
	var env struct {
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v (body=%s)", err, body)
	}
	if dest != nil && env.Data != nil {
		if err := json.Unmarshal(env.Data, dest); err != nil {
			t.Fatalf("unmarshal data: %v", err)
		}
	}
	return env.Error
}

func insertSampleWorkout(t *testing.T, d *db.DB, id string) *models.Workout {
	t.Helper()
	avgP := 200.0
	maxP := 350.0
	np := 215.0
	avgHR := 155
	maxHR := 178
	avgCad := 90
	tss := 75.0
	ifac := 0.86
	w := &models.Workout{
		ID:                  id,
		Filename:            id + ".fit",
		RecordedAt:          time.Date(2024, 3, 15, 8, 0, 0, 0, time.UTC),
		Sport:               "cycling",
		DurationSecs:        3600,
		DistanceMeters:      36000,
		ElevationGainMeters: 450,
		AvgPowerWatts:       &avgP,
		MaxPowerWatts:       &maxP,
		NormalizedPower:     &np,
		AvgHeartRate:        &avgHR,
		MaxHeartRate:        &maxHR,
		AvgCadenceRPM:       &avgCad,
		AvgSpeedMPS:         10.0,
		TSS:                 &tss,
		IntensityFactor:     &ifac,
		CreatedAt:           time.Now().UTC(),
	}
	if err := d.InsertWorkout(w, nil); err != nil {
		t.Fatalf("insert workout: %v", err)
	}
	return w
}

// ── GET /api/workouts ─────────────────────────────────────────────────────────

func TestListWorkouts_Empty(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest("GET", "/api/workouts", nil)
	rr := httptest.NewRecorder()
	h.ListWorkouts(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d want %d", rr.Code, http.StatusOK)
	}
	var workouts []models.Workout
	if errMsg := decodeEnvelope(t, rr.Body.Bytes(), &workouts); errMsg != "" {
		t.Errorf("unexpected error: %s", errMsg)
	}
	if len(workouts) != 0 {
		t.Errorf("expected empty list, got %d", len(workouts))
	}
}

func TestListWorkouts_WithData(t *testing.T) {
	h, d := newTestHandler(t)
	insertSampleWorkout(t, d, "workout1aaaaaaaa")
	insertSampleWorkout(t, d, "workout2bbbbbbbb")

	req := httptest.NewRequest("GET", "/api/workouts", nil)
	rr := httptest.NewRecorder()
	h.ListWorkouts(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d", rr.Code)
	}
	var workouts []models.Workout
	decodeEnvelope(t, rr.Body.Bytes(), &workouts)
	if len(workouts) != 2 {
		t.Errorf("expected 2 workouts, got %d", len(workouts))
	}
}

func TestListWorkouts_LimitParam(t *testing.T) {
	h, d := newTestHandler(t)
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("wlimit%010d", i)
		insertSampleWorkout(t, d, id)
	}

	req := httptest.NewRequest("GET", "/api/workouts?limit=2", nil)
	rr := httptest.NewRecorder()
	h.ListWorkouts(rr, req)

	var workouts []models.Workout
	decodeEnvelope(t, rr.Body.Bytes(), &workouts)
	if len(workouts) != 2 {
		t.Errorf("limit=2: got %d workouts", len(workouts))
	}
}

func TestListWorkouts_LimitCappedAt200(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest("GET", "/api/workouts?limit=999", nil)
	rr := httptest.NewRecorder()
	h.ListWorkouts(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d", rr.Code)
	}
}

// ── GET /api/workouts/{id} ────────────────────────────────────────────────────

func TestGetWorkout_Found(t *testing.T) {
	h, d := newTestHandler(t)
	w := insertSampleWorkout(t, d, "getworkout000000")

	req := httptest.NewRequest("GET", "/api/workouts/"+w.ID, nil)
	req = withURLParam(req, "id", w.ID)
	rr := httptest.NewRecorder()
	h.GetWorkout(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d", rr.Code)
	}
	var got models.Workout
	decodeEnvelope(t, rr.Body.Bytes(), &got)
	if got.ID != w.ID {
		t.Errorf("ID: got %q want %q", got.ID, w.ID)
	}
}

func TestGetWorkout_NotFound(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest("GET", "/api/workouts/doesnotexist", nil)
	req = withURLParam(req, "id", "doesnotexist")
	rr := httptest.NewRecorder()
	h.GetWorkout(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status: got %d want %d", rr.Code, http.StatusNotFound)
	}
}

// ── GET /api/workouts/{id}/streams ────────────────────────────────────────────

func TestGetStreams_Found(t *testing.T) {
	h, d := newTestHandler(t)
	w := insertSampleWorkout(t, d, "streamsworkout00")

	req := httptest.NewRequest("GET", "/api/workouts/"+w.ID+"/streams", nil)
	req = withURLParam(req, "id", w.ID)
	rr := httptest.NewRecorder()
	h.GetStreams(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d", rr.Code)
	}
	var streams []models.Stream
	decodeEnvelope(t, rr.Body.Bytes(), &streams)
	// No streams inserted, but should return empty slice — not 404
	if streams == nil {
		t.Error("expected empty slice, not nil")
	}
}

func TestGetStreams_NotFound(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest("GET", "/api/workouts/nope/streams", nil)
	req = withURLParam(req, "id", "nope")
	rr := httptest.NewRecorder()
	h.GetStreams(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status: got %d want %d", rr.Code, http.StatusNotFound)
	}
}

// ── GET /api/workouts/{id}/summary ────────────────────────────────────────────

func TestGetWorkoutSummary_Found(t *testing.T) {
	h, d := newTestHandler(t)
	w := insertSampleWorkout(t, d, "summaryworkout00")

	req := httptest.NewRequest("GET", "/api/workouts/"+w.ID+"/summary", nil)
	req = withURLParam(req, "id", w.ID)
	rr := httptest.NewRecorder()
	h.GetWorkoutSummary(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d", rr.Code)
	}
	var summary models.WorkoutSummary
	decodeEnvelope(t, rr.Body.Bytes(), &summary)
	if summary.ID != w.ID {
		t.Errorf("summary ID: got %q want %q", summary.ID, w.ID)
	}
	if summary.DurationMins != 60.0 {
		t.Errorf("DurationMins: got %.1f want 60.0", summary.DurationMins)
	}
	if summary.DistanceKM != 36.0 {
		t.Errorf("DistanceKM: got %.1f want 36.0", summary.DistanceKM)
	}
}

func TestGetWorkoutSummary_NotFound(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest("GET", "/api/workouts/nope/summary", nil)
	req = withURLParam(req, "id", "nope")
	rr := httptest.NewRecorder()
	h.GetWorkoutSummary(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status: got %d want %d", rr.Code, http.StatusNotFound)
	}
}

// ── GET /api/athlete ──────────────────────────────────────────────────────────

func TestGetAthlete(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest("GET", "/api/athlete", nil)
	rr := httptest.NewRecorder()
	h.GetAthlete(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d", rr.Code)
	}
	var athlete models.Athlete
	decodeEnvelope(t, rr.Body.Bytes(), &athlete)
	if athlete.FTPWatts != 250 {
		t.Errorf("FTP: got %d want 250", athlete.FTPWatts)
	}
}

// ── PUT /api/athlete ──────────────────────────────────────────────────────────

func TestUpdateAthlete_Valid(t *testing.T) {
	h, d := newTestHandler(t)
	body := `{"ftp_watts":300,"weight_kg":72.5,"threshold_hr_bpm":170,"max_hr_bpm":190}`

	req := httptest.NewRequest("PUT", "/api/athlete", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.UpdateAthlete(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d (body=%s)", rr.Code, rr.Body.String())
	}
	a, _ := d.GetAthlete()
	if a.FTPWatts != 300 {
		t.Errorf("FTP not updated: got %d", a.FTPWatts)
	}
	if a.WeightKG != 72.5 {
		t.Errorf("Weight not updated: got %.1f", a.WeightKG)
	}
	if a.ThresholdHR != 170 {
		t.Errorf("ThresholdHR not updated: got %d", a.ThresholdHR)
	}
	if a.MaxHR != 190 {
		t.Errorf("MaxHR not updated: got %d", a.MaxHR)
	}
}

func TestUpdateAthlete_InvalidJSON(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest("PUT", "/api/athlete", strings.NewReader("not json"))
	rr := httptest.NewRecorder()
	h.UpdateAthlete(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestUpdateAthlete_ZeroFTP(t *testing.T) {
	h, _ := newTestHandler(t)
	body := `{"ftp_watts":0,"weight_kg":70}`
	req := httptest.NewRequest("PUT", "/api/athlete", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.UpdateAthlete(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestUpdateAthlete_ZeroWeight(t *testing.T) {
	h, _ := newTestHandler(t)
	body := `{"ftp_watts":250,"weight_kg":0}`
	req := httptest.NewRequest("PUT", "/api/athlete", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.UpdateAthlete(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d want %d", rr.Code, http.StatusBadRequest)
	}
}

// ── GET /api/fitness ──────────────────────────────────────────────────────────

func TestGetFitness_DefaultDays(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest("GET", "/api/fitness", nil)
	rr := httptest.NewRecorder()
	h.GetFitness(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d", rr.Code)
	}
	var points []models.FitnessPoint
	decodeEnvelope(t, rr.Body.Bytes(), &points)
	if len(points) != 90 {
		t.Errorf("expected 90 points (default), got %d", len(points))
	}
}

func TestGetFitness_DaysParam(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest("GET", "/api/fitness?days=30", nil)
	rr := httptest.NewRecorder()
	h.GetFitness(rr, req)

	var points []models.FitnessPoint
	decodeEnvelope(t, rr.Body.Bytes(), &points)
	if len(points) != 30 {
		t.Errorf("days=30: got %d points", len(points))
	}
}

func TestGetFitness_DaysCappedAt365(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest("GET", "/api/fitness?days=9999", nil)
	rr := httptest.NewRecorder()
	h.GetFitness(rr, req)

	var points []models.FitnessPoint
	decodeEnvelope(t, rr.Body.Bytes(), &points)
	if len(points) > 365 {
		t.Errorf("expected max 365 points, got %d", len(points))
	}
}

// ── POST /api/upload ──────────────────────────────────────────────────────────

func TestUpload_WrongExtension(t *testing.T) {
	h, _ := newTestHandler(t)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "photo.jpg")
	_, _ = fw.Write([]byte("not a fit file"))
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/api/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	h.Upload(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestUpload_InvalidFITContent(t *testing.T) {
	h, _ := newTestHandler(t)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "ride.fit")
	_, _ = fw.Write([]byte("not a fit file"))
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/api/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	h.Upload(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("status: got %d want %d", rr.Code, http.StatusUnprocessableEntity)
	}
}

func TestUpload_MissingFileField(t *testing.T) {
	h, _ := newTestHandler(t)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/api/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	h.Upload(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d want %d", rr.Code, http.StatusBadRequest)
	}
}

// ── Response format ───────────────────────────────────────────────────────────

func TestResponseContentType(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest("GET", "/api/workouts", nil)
	rr := httptest.NewRecorder()
	h.ListWorkouts(rr, req)

	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type: got %q want application/json", ct)
	}
}

func TestErrorResponseHasErrorField(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest("GET", "/api/workouts/nope", nil)
	req = withURLParam(req, "id", "nope")
	rr := httptest.NewRecorder()
	h.GetWorkout(rr, req)

	var env struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	if env.Error == "" {
		t.Error("expected non-empty error field in 404 response")
	}
}

func TestSuccessResponseHasDataField(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest("GET", "/api/workouts", nil)
	rr := httptest.NewRecorder()
	h.ListWorkouts(rr, req)

	var env struct {
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	if env.Data == nil {
		t.Error("expected data field in success response")
	}
	if env.Error != "" {
		t.Errorf("unexpected error field: %q", env.Error)
	}
}
