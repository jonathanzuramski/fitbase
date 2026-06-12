package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/fitbase/fitbase/internal/api"
)

// routerForPlanned mounts only the planned-workouts routes the same way
// NewRouter does. Avoids constructing every other handler.
func routerForPlanned(h *api.PlannedHandler) http.Handler {
	r := chi.NewRouter()
	r.Route("/api/planned-workouts", func(r chi.Router) {
		r.Get("/", h.List)
		r.Post("/", h.Create)
		r.Delete("/{id}", h.Delete)
	})
	return r
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
