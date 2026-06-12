package api

import (
	"database/sql"
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
