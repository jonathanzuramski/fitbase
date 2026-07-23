package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/fitness"
	"github.com/fitbase/fitbase/internal/importer"
	"github.com/fitbase/fitbase/internal/models"
	"github.com/go-chi/chi/v5"
)

type Handler struct {
	db       *db.DB
	importer *importer.Importer
}

func NewHandler(database *db.DB, importer *importer.Importer) *Handler {
	return &Handler{db: database, importer: importer}
}

// GET /api/workouts
func (h *Handler) ListWorkouts(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50)
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}
	offset := queryInt(r, "offset", 0)
	if offset < 0 {
		offset = 0
	}

	workouts, err := h.db.ListWorkouts(limit, offset, "", "", "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if workouts == nil {
		workouts = []models.Workout{}
	}
	writeJSON(w, http.StatusOK, workouts)
}

// GET /api/workouts/{id}
func (h *Handler) GetWorkout(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	workout, err := h.db.GetWorkout(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if workout == nil {
		writeError(w, http.StatusNotFound, "workout not found")
		return
	}
	writeJSON(w, http.StatusOK, workout)
}

// GET /api/workouts/{id}/streams
func (h *Handler) GetStreams(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	workout, err := h.db.GetWorkout(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if workout == nil {
		writeError(w, http.StatusNotFound, "workout not found")
		return
	}
	streams, err := h.db.GetStreams(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if streams == nil {
		streams = []models.Stream{}
	}
	writeJSON(w, http.StatusOK, streams)
}

// GET /api/workouts/{id}/summary — compact, prose-friendly for LLM consumption
func (h *Handler) GetWorkoutSummary(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	workout, err := h.db.GetWorkout(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if workout == nil {
		writeError(w, http.StatusNotFound, "workout not found")
		return
	}
	writeJSON(w, http.StatusOK, workout.ToSummary())
}

// DELETE /api/workouts/{id} — removes the workout, its import-ledger entries,
// and its archived FIT file, so the same file can be re-imported.
func (h *Handler) DeleteWorkout(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.importer.DeleteWorkout(id); errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "workout not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/workouts — wipe all workouts and reset imported_files
func (h *Handler) DeleteAllWorkouts(w http.ResponseWriter, r *http.Request) {
	if err := h.db.DeleteAllWorkouts(); err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/athlete
func (h *Handler) GetAthlete(w http.ResponseWriter, r *http.Request) {
	athlete, err := h.db.GetAthlete()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	writeJSON(w, http.StatusOK, athlete)
}

// GET /api/workouts/{id}/download — serves the original archived FIT file
func (h *Handler) DownloadFIT(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	workout, err := h.db.GetWorkout(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if workout == nil {
		writeError(w, http.StatusNotFound, "workout not found")
		return
	}

	archivePath := h.importer.ArchivePath(workout)
	f, err := os.Open(archivePath)
	if os.IsNotExist(err) {
		writeError(w, http.StatusNotFound, "original FIT file not available (imported before archiving was enabled)")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read archive")
		return
	}
	defer f.Close() //nolint:errcheck

	w.Header().Set("Content-Type", "application/octet-stream")
	safeName := sanitizeFilename(workout.Filename)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, safeName))
	if _, err := io.Copy(w, f); err != nil {
		slog.Error("download: copy failed", "id", id, "err", err)
	}
}

// GET /api/ftp-history/recompute streams SSE progress while recomputing
// TSS and intensity factor for every power-based workout from ftp_history.
// Used by the manual FTP-history editor in settings.
func (h *Handler) RecomputePowerLoad(w http.ResponseWriter, r *http.Request) {
	setupSSE(w)

	// Throttle "file" events: for thousands of workouts, every-row updates
	// flood the stream and provide no extra signal.
	var lastSent int
	updated, err := h.db.RecomputePowerLoad(func(done, total int) {
		if total == 0 {
			writeSSE(w, "start", map[string]any{"total": 0, "pending": 0})
			return
		}
		if done == 0 {
			writeSSE(w, "start", map[string]any{"total": total, "pending": total})
			lastSent = 0
			return
		}
		// Send at most ~50 progress events plus the final one.
		step := total / 50
		if step < 1 {
			step = 1
		}
		if done == total || done-lastSent >= step {
			writeSSE(w, "file", map[string]any{"name": "", "index": done, "total": total})
			lastSent = done
		}
	})
	if err != nil {
		writeSSE(w, "error", map[string]string{"error": err.Error()})
		return
	}
	writeSSE(w, "done", map[string]any{"updated": updated})
}

// PUT /api/athlete
func (h *Handler) UpdateAthlete(w http.ResponseWriter, r *http.Request) {
	var body models.Athlete
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.FTPWatts <= 0 || body.WeightKG <= 0 {
		writeError(w, http.StatusBadRequest, "ftp_watts and weight_kg must be positive")
		return
	}
	if err := h.db.UpdateAthlete(&body); err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// GET /api/fitness
func (h *Handler) GetFitness(w http.ResponseWriter, r *http.Request) {
	days := queryInt(r, "days", 90)
	if days < 1 {
		days = 1
	}
	if days > 365 {
		days = 365
	}
	points, err := h.db.GetFitnessHistory(days)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if points == nil {
		points = []models.FitnessPoint{}
	}
	writeJSON(w, http.StatusOK, points)
}

// POST /api/upload — multipart FIT file upload.
// Writes to a temp file and calls Import so archiving, Drive backup, and power
// curves all go through the same path as the file watcher.
func (h *Handler) Upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20) // 32MB limit
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "failed to parse form")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing 'file' field")
		return
	}
	defer file.Close() //nolint:errcheck

	name := header.Filename
	lower := strings.ToLower(name)
	if !strings.HasSuffix(lower, ".fit") && !strings.HasSuffix(lower, ".fit.gz") {
		writeError(w, http.StatusBadRequest, "file must be a .fit or .fit.gz file")
		return
	}

	tmp, err := os.CreateTemp("", "fitbase-upload-*-"+filepath.Base(name))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create temp file")
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) //nolint:errcheck

	if _, err := io.Copy(tmp, file); err != nil {
		_ = tmp.Close()
		writeError(w, http.StatusInternalServerError, "failed to write temp file")
		return
	}
	_ = tmp.Close()

	id, err := h.importer.Import(tmpPath)
	if err != nil {
		slog.Error("upload: import failed", "file", name, "err", err)
		writeError(w, http.StatusUnprocessableEntity, "invalid FIT file: "+err.Error())
		return
	}
	if id == "" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "already_exists"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "status": "imported"})
}

// GET /api/import/status — progress of a background startup archive reimport.
// Cheap, in-memory only; the UI polls this so a fresh install shows an import
// modal instead of an empty dashboard. Reports Active=false when idle.
func (h *Handler) ImportStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.importer.ReimportStatus())
}

func queryInt(r *http.Request, key string, def int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// GET /api/workouts/{id}/route — route info + all workouts on this route
func (h *Handler) GetWorkoutRoute(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	workout, err := h.db.GetWorkout(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if workout == nil {
		writeError(w, http.StatusNotFound, "workout not found")
		return
	}
	if workout.RouteID == nil {
		writeJSON(w, http.StatusOK, map[string]any{"route_id": nil, "workouts": []models.Workout{}})
		return
	}
	routeName, workouts, err := h.db.GetRouteHistory(*workout.RouteID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"route_id":   *workout.RouteID,
		"route_name": routeName,
		"workouts":   workouts,
	})
}

// GET /api/workouts/routes?ids=id1,id2,...
// Omit ids to return tracks for the 500 most recent outdoor workouts (heatmap).
func (h *Handler) GetWorkoutRouteTracks(w http.ResponseWriter, r *http.Request) {
	idsParam := strings.TrimSpace(r.URL.Query().Get("ids"))
	var ids []string
	if idsParam != "" {
		raw := strings.Split(idsParam, ",")
		ids = make([]string, 0, len(raw))
		for _, id := range raw {
			if id = strings.TrimSpace(id); id != "" {
				ids = append(ids, id)
			}
		}
	}
	tracks, err := h.db.GetWorkoutRouteTracks(ids)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	type trackOut struct {
		WorkoutID           string       `json:"workout_id"`
		Sport               string       `json:"sport"`
		Date                string       `json:"date"`
		DistanceMeters      float64      `json:"distance_meters"`
		DurationSecs        int          `json:"duration_secs"`
		ElevationGainMeters float64      `json:"elevation_gain_meters"`
		Coords              [][2]float64 `json:"coords"`
	}
	out := make([]trackOut, len(tracks))
	for i, t := range tracks {
		out[i] = trackOut{
			WorkoutID:           t.WorkoutID,
			Sport:               t.Sport,
			Date:                t.Date.Format("Jan 02, 2006"),
			DistanceMeters:      t.DistanceMeters,
			DurationSecs:        t.DurationSecs,
			ElevationGainMeters: t.ElevationGainMeters,
			Coords:              t.Coords,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/athlete/zones
func (h *Handler) GetAthleteZones(w http.ResponseWriter, r *http.Request) {
	athlete, err := h.db.GetAthlete()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	writeJSON(w, http.StatusOK, models.ZonesReport{
		FTPWatts:    athlete.FTPWatts,
		ThresholdHR: athlete.ThresholdHR,
		PowerZones:  fitness.PowerZones(athlete.FTPWatts),
		HRZones:     fitness.ResolveHRZones(athlete),
	})
}

// GET /api/athlete/power-curve
func (h *Handler) GetPowerCurve(w http.ResponseWriter, r *http.Request) {
	report, err := powerCurveReport(h.db)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// GET /api/training/weekly
func (h *Handler) GetWeeklyTraining(w http.ResponseWriter, r *http.Request) {
	weeks := queryInt(r, "weeks", 12)
	if weeks < 1 {
		weeks = 1
	}
	if weeks > 52 {
		weeks = 52
	}
	rows, err := h.db.GetWeeklyBreakdown(weeks)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if rows == nil {
		rows = []models.WeeklyLoad{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// GET /api/athlete/readiness
func (h *Handler) GetReadiness(w http.ResponseWriter, r *http.Request) {
	report, err := readinessReport(h.db)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// GET /api/workouts/{id}/analysis
func (h *Handler) GetWorkoutAnalysis(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	analysis, err := workoutAnalysis(h.db, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if analysis == nil {
		writeError(w, http.StatusNotFound, "workout not found")
		return
	}

	writeJSON(w, http.StatusOK, analysis)
}

// sanitizeFilename strips characters that could break or inject into HTTP headers.
func sanitizeFilename(name string) string {
	safe := strings.Map(func(r rune) rune {
		if r == '"' || r == '\\' || r == '\n' || r == '\r' || r < 0x20 {
			return '_'
		}
		return r
	}, name)
	if safe == "" {
		return "download.fit"
	}
	return safe
}
