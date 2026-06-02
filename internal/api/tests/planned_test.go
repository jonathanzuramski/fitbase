package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/fitbase/fitbase/internal/api"
	"github.com/fitbase/fitbase/internal/models"
)

// routerForPlanned mounts only the planned-workouts routes the same way
// NewRouter does. Avoids constructing every other handler.
func routerForPlanned(h *api.PlannedHandler) http.Handler {
	r := chi.NewRouter()
	r.Route("/api/planned-workouts", func(r chi.Router) {
		r.Get("/", h.List)
		r.Post("/", h.Create)
		r.Delete("/{id}", h.Delete)
		r.Get("/drafts/{id}", h.GetDraft)
		r.Post("/drafts/{id}/commit", h.CommitDraft)
		r.Delete("/drafts/{id}", h.DiscardDraft)
	})
	return r
}

func TestPlannedDraftCommitRoundTrip(t *testing.T) {
	d := newTestDB(t)
	h := api.NewPlannedHandler(d)
	rt := routerForPlanned(h)

	// Simulate what the coach's propose_schedule executor writes: a JSON
	// payload of {"workouts":[…]} keyed by a fresh draft id.
	payload := `{"workouts":[
		{"date":"2026-05-26","sport":"cycling","title":"Endurance","duration_secs":3600,"intensity_factor":0.65},
		{"date":"2026-05-27","sport":"cycling","title":"2x20 SS","duration_secs":5400,"intensity_factor":0.88,
		 "intervals":[
			{"kind":"warmup","duration_secs":900,"target_pct_ftp_low":50,"target_pct_ftp_high":75},
			{"repeat":2,"steps":[
				{"kind":"sweet_spot","duration_secs":1200,"target_pct_ftp":88},
				{"kind":"recovery","duration_secs":300}
			]},
			{"kind":"cooldown","duration_secs":600}
		 ]}
	]}`
	id, err := d.SavePlannedDraft(payload)
	if err != nil {
		t.Fatalf("save draft: %v", err)
	}

	// GET the draft — what the chat UI does on receiving a preview SSE event.
	req := httptest.NewRequest(http.MethodGet, "/api/planned-workouts/drafts/"+id, nil)
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET draft: status %d body=%s", rr.Code, rr.Body.String())
	}

	// POST the commit — what "add to calendar" does.
	req = httptest.NewRequest(http.MethodPost, "/api/planned-workouts/drafts/"+id+"/commit", nil)
	rr = httptest.NewRecorder()
	rt.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST commit: status %d body=%s", rr.Code, rr.Body.String())
	}

	var env struct {
		Data []models.PlannedWorkout `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode commit response: %v body=%s", err, rr.Body.String())
	}
	if len(env.Data) != 2 {
		t.Fatalf("expected 2 created planned workouts, got %d", len(env.Data))
	}
	// IF→TSS estimation must have filled in TSS for both items.
	for i, p := range env.Data {
		if p.TSS == nil || *p.TSS <= 0 {
			t.Errorf("item %d: TSS not estimated (IF was provided)", i)
		}
		if p.Source != "coach" {
			t.Errorf("item %d: source = %q, want 'coach'", i, p.Source)
		}
	}
	// The second item had structured intervals (warmup, 2× group, cooldown) —
	// they should survive commit, including the nested repeat group.
	ivs := env.Data[1].Intervals
	if len(ivs) != 3 {
		t.Fatalf("intervals lost in commit: got %d, want 3", len(ivs))
	}
	if !ivs[1].IsGroup() || ivs[1].Repeat != 2 || len(ivs[1].Steps) != 2 {
		t.Errorf("repeat group lost in commit: %+v", ivs[1])
	}
	// duration_secs must be derived from the steps: 900 + 2×(1200+300) + 600 = 4500.
	if env.Data[1].DurationSecs != 4500 {
		t.Errorf("duration not derived from intervals: got %d, want 4500", env.Data[1].DurationSecs)
	}

	// Draft should be gone after commit.
	if got, _ := d.GetPlannedDraft(id); got != "" {
		t.Error("draft should be deleted after commit")
	}

	// Second commit must 404 (draft consumed).
	req = httptest.NewRequest(http.MethodPost, "/api/planned-workouts/drafts/"+id+"/commit", nil)
	rr = httptest.NewRecorder()
	rt.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("second commit: status %d, want 404", rr.Code)
	}
}

func TestPlannedManualCreate(t *testing.T) {
	d := newTestDB(t)
	h := api.NewPlannedHandler(d)
	rt := routerForPlanned(h)

	body := strings.NewReader(`{"date":"2026-05-28","sport":"cycling","title":"Quick add","duration_secs":2700}`)
	req := httptest.NewRequest(http.MethodPost, "/api/planned-workouts/", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: status %d body=%s", rr.Code, rr.Body.String())
	}
}

// Helper for diagnosing a failing request body — keeps the error message
// readable when assertions trip.
func dumpBody(b *bytes.Buffer) string {
	out, _ := io.ReadAll(b)
	return string(out)
}
