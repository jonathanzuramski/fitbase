package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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

// DELETE /api/workouts/{id}
func (h *Handler) DeleteWorkout(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.db.DeleteWorkout(id); errors.Is(err, sql.ErrNoRows) {
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
	curve, err := h.db.GetAllTimePowerCurve()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	athlete, err := h.db.GetAthlete()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	displayDurations := []struct {
		secs  int
		label string
	}{
		{5, "5s"}, {30, "30s"}, {60, "1min"}, {300, "5min"}, {1200, "20min"}, {3600, "60min"},
	}

	entries := make([]models.PowerCurveEntry, 0, len(displayDurations))
	for _, d := range displayDurations {
		best, ok := curve[d.secs]
		if !ok {
			continue
		}
		entry := models.PowerCurveEntry{
			DurationSecs:  d.secs,
			DurationLabel: d.label,
			Watts:         best.Watts,
			WorkoutID:     best.WorkoutID,
		}
		if athlete.WeightKG > 0 {
			entry.WattsPerKG = math.Round(float64(best.Watts)/athlete.WeightKG*100) / 100
		}
		if athlete.FTPWatts > 0 {
			entry.PctFTP = math.Round(float64(best.Watts)/float64(athlete.FTPWatts)*1000) / 10
		}
		entries = append(entries, entry)
	}
	writeJSON(w, http.StatusOK, models.PowerCurveReport{
		Entries:  entries,
		FTPWatts: athlete.FTPWatts,
		WeightKG: athlete.WeightKG,
	})
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
	today := time.Now().UTC()

	fp, err := h.db.GetFitnessOnDate(today)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	lastDate, err := h.db.GetLastWorkoutDate()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	daysSince := 0
	if lastDate != nil {
		daysSince = int(today.Sub(*lastDate).Hours() / 24)
	}

	// Ramp rate: change in fitness (CTL) over the last 28 days.
	var rampRate float64
	if history, histErr := h.db.GetFitnessHistory(42); histErr == nil && len(history) >= 29 {
		rampRate = math.Round((history[len(history)-1].Fitness-history[len(history)-29].Fitness)*10) / 10
	}

	rec, detail := readinessRecommendation(fp.Form, rampRate, daysSince)
	writeJSON(w, http.StatusOK, models.ReadinessReport{
		Date:                 today.Format("2006-01-02"),
		Fitness:              math.Round(fp.Fitness*10) / 10,
		Fatigue:              math.Round(fp.Fatigue*10) / 10,
		Form:                 math.Round(fp.Form*10) / 10,
		DaysSinceLastWorkout: daysSince,
		RampRate:             rampRate,
		Recommendation:       rec,
		RecommendationDetail: detail,
	})
}

func readinessRecommendation(form, rampRate float64, daysSince int) (string, string) {
	switch {
	case daysSince > 7:
		return "Resume Training", "More than a week without training — fitness is declining. Ease back in with a light ride."
	case rampRate > 10:
		return "Ease Up", "Training load has increased sharply over the last 4 weeks. Prioritise sleep and monitor fatigue."
	case form > 15:
		return "Race Ready", "You're very fresh. Consider a peak effort or race — extended rest risks fitness loss."
	case form > 5:
		return "Go Ride", "Good form. Ready for a quality session or race effort."
	case form >= -10:
		return "Maintain", "Normal training load. Keep building consistency."
	case form >= -25:
		return "Training Block", "Productive fatigue — you're adapting. A recovery day will pay off soon."
	case form >= -40:
		return "Ease Up", "Heavy fatigue. Schedule a recovery ride or rest day before your next hard effort."
	default:
		return "Rest", "Significant overreach — rest now to avoid illness or injury."
	}
}

// GET /api/workouts/{id}/analysis
func (h *Handler) GetWorkoutAnalysis(w http.ResponseWriter, r *http.Request) {
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

	athlete, err := h.db.GetAthlete()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	powerZoneDefs := fitness.PowerZones(athlete.FTPWatts)
	hrZoneDefs := fitness.ResolveHRZones(athlete)

	powerSecs, hrSecs, err := h.db.GetZoneTimes(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	analysis := models.WorkoutAnalysis{
		WorkoutID:  id,
		PowerZones: []models.ZoneBreakdown{},
		HRZones:    []models.ZoneBreakdown{},
	}

	if powerSecs != nil {
		total := 0
		for _, s := range powerSecs {
			total += s
		}
		for i, z := range powerZoneDefs[:7] {
			pct := 0.0
			if total > 0 {
				pct = math.Round(float64(powerSecs[i])/float64(total)*1000) / 10
			}
			analysis.PowerZones = append(analysis.PowerZones, models.ZoneBreakdown{
				Label:     z.Label,
				Name:      z.Name,
				Seconds:   powerSecs[i],
				PctTime:   pct,
				WattsLow:  z.WattsLow,
				WattsHigh: z.WattsHigh,
			})
		}
	}

	if hrSecs != nil {
		total := 0
		for _, s := range hrSecs {
			total += s
		}
		for i, z := range hrZoneDefs {
			if i >= len(hrSecs) {
				break
			}
			pct := 0.0
			if total > 0 {
				pct = math.Round(float64(hrSecs[i])/float64(total)*1000) / 10
			}
			analysis.HRZones = append(analysis.HRZones, models.ZoneBreakdown{
				Label:   z.Label,
				Name:    z.Name,
				Seconds: hrSecs[i],
				PctTime: pct,
				BPMLow:  z.BPMLow,
				BPMHigh: z.BPMHigh,
			})
		}
	}

	if workout.NormalizedPower != nil && workout.AvgPowerWatts != nil && *workout.AvgPowerWatts > 0 {
		vi := math.Round(*workout.NormalizedPower / *workout.AvgPowerWatts * 1000) / 1000
		analysis.VariabilityIndex = &vi
	}
	if workout.NormalizedPower != nil && workout.AvgHeartRate != nil && *workout.AvgHeartRate > 0 {
		ef := math.Round(*workout.NormalizedPower/float64(*workout.AvgHeartRate)*1000) / 1000
		analysis.EfficiencyFactor = &ef
	}

	if avgs, err := h.db.Get90DayAverages(workout.Sport); err == nil && avgs != nil {
		analysis.AvgNP90Day = avgs.AvgNP
		analysis.AvgHR90Day = avgs.AvgHR
		analysis.AvgTSS90Day = avgs.AvgTSS
		analysis.AvgIF90Day = avgs.AvgIF
		analysis.AvgDuration90Day = avgs.AvgDurationSecs
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
