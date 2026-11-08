package api

import (
	"encoding/json"
	"net/http"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/fitbase/fitbase/internal/models"
)

// GET /api/workouts/{id}/feedback — the rider's RPE and note on a ride
// (an empty object when there is none).
func (h *Handler) GetWorkoutFeedback(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.workoutExists(w, id) {
		return
	}
	f, _, err := h.db.GetWorkoutFeedback(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// PUT /api/workouts/{id}/feedback — set the rider's RPE (1–10, or null to
// clear it) and note. Both null/empty removes the feedback.
func (h *Handler) SetWorkoutFeedback(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		RPE  *int   `json:"rpe"`
		Note string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.RPE != nil && (*body.RPE < models.MinRPE || *body.RPE > models.MaxRPE) {
		writeError(w, http.StatusBadRequest, "rpe must be between 1 and 10, or null")
		return
	}
	if utf8.RuneCountInString(body.Note) > models.MaxFeedbackNote {
		writeError(w, http.StatusBadRequest, "note must be at most 2000 characters")
		return
	}
	if !h.workoutExists(w, id) {
		return
	}
	if err := h.db.SaveWorkoutFeedback(id, body.RPE, body.Note); err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	h.GetWorkoutFeedback(w, r)
}

// workoutExists writes a 404 (or 500) and returns false when there is no
// workout with this id.
func (h *Handler) workoutExists(w http.ResponseWriter, id string) bool {
	wk, err := h.db.GetWorkout(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return false
	}
	if wk == nil {
		writeError(w, http.StatusNotFound, "workout not found")
		return false
	}
	return true
}
