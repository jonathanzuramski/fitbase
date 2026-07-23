package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/fitness"
	"github.com/fitbase/fitbase/internal/models"
)

// PlannedHandler serves CRUD endpoints for planned (future) workouts.
type PlannedHandler struct {
	db *db.DB
}

func NewPlannedHandler(database *db.DB) *PlannedHandler {
	return &PlannedHandler{db: database}
}

// plannedRequest is the body shape for POST /api/planned-workouts and each
// element of a coach-proposed draft. Date is a plain "YYYY-MM-DD" string so
// clients don't have to worry about timezone offsets sneaking into a date.
type plannedRequest struct {
	Date            string                   `json:"date"`
	Sport           string                   `json:"sport"`
	Title           string                   `json:"title"`
	Description     string                   `json:"description"`
	DurationSecs    int                      `json:"duration_secs"`
	TSS             *float64                 `json:"tss,omitempty"`
	IntensityFactor *float64                 `json:"intensity_factor,omitempty"`
	Intervals       []models.PlannedInterval `json:"intervals,omitempty"`
}

// toModel validates and converts the request to a model, filling in defaults
// and estimating TSS when an intensity factor was given without an explicit TSS.
// source is "manual" or "coach"; persisted so the UI can show provenance.
func (r plannedRequest) toModel(source string) (models.PlannedWorkout, error) {
	date, err := time.Parse("2006-01-02", strings.TrimSpace(r.Date))
	if err != nil {
		return models.PlannedWorkout{}, errors.New("date must be YYYY-MM-DD")
	}
	if r.IntensityFactor != nil && (*r.IntensityFactor <= 0 || *r.IntensityFactor > 1.5) {
		return models.PlannedWorkout{}, errors.New("intensity_factor must be between 0 and 1.5")
	}

	// duration should be built from the intervals, if they are
	// present they are the source of truth for our planned workout.
	durationSecs := r.DurationSecs
	if len(r.Intervals) > 0 {
		total := 0
		for i, iv := range r.Intervals {
			if err := iv.Validate(); err != nil {
				return models.PlannedWorkout{}, fmt.Errorf("intervals[%d]: %w", i, err)
			}
			total += iv.TotalSecs()
		}
		if total > 0 {
			durationSecs = total
		}
	}
	if durationSecs <= 0 {
		return models.PlannedWorkout{}, errors.New("duration_secs must be positive")
	}

	sport := strings.TrimSpace(r.Sport)
	if sport == "" {
		sport = "cycling"
	}

	tss := r.TSS
	if tss == nil && r.IntensityFactor != nil {
		est := fitness.EstimateTSS(durationSecs, *r.IntensityFactor)
		if est > 0 {
			tss = &est
		}
	}

	return models.PlannedWorkout{
		PlannedDate:     date,
		Sport:           sport,
		Title:           strings.TrimSpace(r.Title),
		Description:     strings.TrimSpace(r.Description),
		DurationSecs:    durationSecs,
		TSS:             tss,
		IntensityFactor: r.IntensityFactor,
		Intervals:       r.Intervals,
		Source:          source,
	}, nil
}

// List returns planned workouts in [start, end]. Both query params are
// "YYYY-MM-DD"; if omitted the default is today through +30 days.
//
// GET /api/planned-workouts?start=…&end=…
func (h *PlannedHandler) List(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	start := now.AddDate(0, 0, -7)
	end := now.AddDate(0, 0, 30)
	if s := r.URL.Query().Get("start"); s != "" {
		if t, err := time.Parse("2006-01-02", s); err == nil {
			start = t
		}
	}
	if s := r.URL.Query().Get("end"); s != "" {
		if t, err := time.Parse("2006-01-02", s); err == nil {
			end = t
		}
	}
	rows, err := h.db.ListPlannedWorkoutsBetween(start, end)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if rows == nil {
		rows = []models.PlannedWorkout{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// Create persists one user-authored planned workout (the calendar quick-add).
//
// POST /api/planned-workouts
func (h *PlannedHandler) Create(w http.ResponseWriter, r *http.Request) {
	var pRequest plannedRequest
	// decodes JSON directly into the planntedRequest struct
	if err := decodeJSON(r, &pRequest); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	p, err := pRequest.toModel("manual")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	saved, err := h.db.CreatePlannedWorkout(p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	writeJSON(w, http.StatusCreated, saved)
}

// Delete removes a planned workout by id.
//
// DELETE /api/planned-workouts/{id}
func (h *PlannedHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	switch err := h.db.DeletePlannedWorkout(id); {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "planned workout not found")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "database error")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// draftPayload is the on-disk shape of a coach-proposed schedule. Stored as
// JSON in planned_workout_drafts.payload_json.
type draftPayload struct {
	Workouts []plannedRequest `json:"workouts"`
}

// GetDraft returns the proposed schedule for a preview id, normalized into
// PlannedWorkout shape so the chat UI can render it directly. The draft is
// NOT consumed by this call — only Commit removes it.
//
// GET /api/planned-workouts/drafts/{id}
func (h *PlannedHandler) GetDraft(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	payload, err := h.db.GetPlannedDraft(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if payload == "" {
		writeError(w, http.StatusNotFound, "draft not found")
		return
	}
	var d draftPayload
	if err := json.Unmarshal([]byte(payload), &d); err != nil {
		writeError(w, http.StatusInternalServerError, "draft is corrupt")
		return
	}
	out := make([]models.PlannedWorkout, 0, len(d.Workouts))
	for _, req := range d.Workouts {
		p, err := req.toModel("coach")
		if err != nil {
			writeError(w, http.StatusBadRequest, "draft contains invalid item: "+err.Error())
			return
		}
		out = append(out, p)
	}
	writeJSON(w, http.StatusOK, out)
}

// CommitDraft persists every workout in a draft (as source=coach) and removes
// the draft. Returns the created list. If any item fails validation nothing
// is persisted and the draft is left intact for retry/correction.
//
// POST /api/planned-workouts/drafts/{id}/commit
func (h *PlannedHandler) CommitDraft(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	payload, err := h.db.GetPlannedDraft(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if payload == "" {
		writeError(w, http.StatusNotFound, "draft not found")
		return
	}
	var d draftPayload
	if err := json.Unmarshal([]byte(payload), &d); err != nil {
		writeError(w, http.StatusInternalServerError, "draft is corrupt")
		return
	}

	// Validate everything first so a bad item doesn't leave a half-committed
	// schedule on the calendar.
	pending := make([]models.PlannedWorkout, 0, len(d.Workouts))
	for _, req := range d.Workouts {
		p, err := req.toModel("coach")
		if err != nil {
			writeError(w, http.StatusBadRequest, "draft contains invalid item: "+err.Error())
			return
		}
		pending = append(pending, p)
	}

	saved := make([]models.PlannedWorkout, 0, len(pending))
	for _, p := range pending {
		s, err := h.db.CreatePlannedWorkout(p)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "database error")
			return
		}
		saved = append(saved, s)
	}
	_ = h.db.DeletePlannedDraft(id) // non-fatal if delete fails; the draft is now harmless

	writeJSON(w, http.StatusCreated, saved)
}

// DiscardDraft removes a draft without committing it.
//
// DELETE /api/planned-workouts/drafts/{id}
func (h *PlannedHandler) DiscardDraft(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.db.DeletePlannedDraft(id); err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
