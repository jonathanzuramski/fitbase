package api

import (
	"net/http"
	"time"

	"github.com/fitbase/fitbase/internal/aicoach"
	"github.com/fitbase/fitbase/internal/db"
)

// CoachHandler serves AI coaching insight requests.
type CoachHandler struct {
	db *db.DB
}

func NewCoachHandler(database *db.DB) *CoachHandler {
	return &CoachHandler{db: database}
}

// cachedInsightsResponse is the shape returned by GET /api/coach/insights.
// Content is a Markdown blob that the browser renders.
type cachedInsightsResponse struct {
	Content     string `json:"content"`
	Provider    string `json:"provider,omitempty"`
	Model       string `json:"model,omitempty"`
	GeneratedAt string `json:"generated_at,omitempty"`
}

// GetCachedInsights returns the previously generated insights without calling the LLM.
// Returns 204 No Content if nothing has been generated yet.
//
// GET /api/coach/insights
func (h *CoachHandler) GetCachedInsights(w http.ResponseWriter, r *http.Request) {
	cached, err := h.db.GetCachedInsights()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if cached == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, cachedInsightsResponse{
		Content:     cached.Content,
		Provider:    cached.Provider,
		Model:       cached.Model,
		GeneratedAt: cached.GeneratedAt.Format(time.RFC3339),
	})
}

// GenerateInsights streams markdown chunks from the LLM over SSE as they arrive,
// and caches the full response on completion. POST because it mutates the cache
// and is rate-limit-worthy.
//
// Events:
//
//	chunk  {"text":"..."}                               // text delta
//	done   {"provider":"...","model":"...","at":"..."}  // stream finished, cache saved
//	error  {"error":"..."}                              // fatal error; stream ends
//
// POST /api/coach/insights
func (h *CoachHandler) GenerateInsights(w http.ResponseWriter, r *http.Request) {
	settings, err := h.db.GetAISettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if settings.Provider == "" || settings.APIKey == "" {
		writeError(w, http.StatusServiceUnavailable, "AI coach not configured — add provider and API key in settings")
		return
	}

	coach, err := aicoach.New(settings.Provider, settings.Model, settings.APIKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	data, err := h.collectCoachingData()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	setupSSE(w)
	// Some proxies hold the response until Content-Length is known; flush so the
	// browser sees headers immediately and begins streaming.
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	full, err := coach.StreamInsights(r.Context(), data, func(chunk string) error {
		writeSSE(w, "chunk", map[string]string{"text": chunk})
		return nil
	})
	if err != nil {
		writeSSE(w, "error", map[string]string{"error": err.Error()})
		return
	}

	// Non-fatal if the cache write fails — the user still got their insights.
	_ = h.db.SaveCachedInsights(db.CachedInsights{
		Provider: settings.Provider,
		Model:    settings.Model,
		Content:  full,
	})

	writeSSE(w, "done", map[string]string{
		"provider":     settings.Provider,
		"model":        settings.Model,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
	})
}

// collectCoachingData gathers all the raw training data the LLM needs: athlete
// profile, 90-day fitness history, 56-day workout briefs, 8-week breakdown,
// key-duration power bests, and 56-day zone totals.
func (h *CoachHandler) collectCoachingData() (*aicoach.CoachingData, error) {
	athlete, err := h.db.GetAthlete()
	if err != nil {
		return nil, err
	}
	profile := aicoach.AthleteProfile{
		FTPWatts:    athlete.FTPWatts,
		WeightKG:    athlete.WeightKG,
		ThresholdHR: athlete.ThresholdHR,
		MaxHR:       athlete.MaxHR,
	}

	fitnessRaw, err := h.db.GetFitnessHistory(90)
	if err != nil {
		return nil, err
	}
	fitnessPts := make([]aicoach.FitnessPoint, len(fitnessRaw))
	for i, fp := range fitnessRaw {
		fitnessPts[i] = aicoach.FitnessPoint{
			Date:    fp.Date.Format("2006-01-02"),
			Fitness: fp.Fitness,
			Fatigue: fp.Fatigue,
			Form:    fp.Form,
		}
	}

	cutoff56 := time.Now().UTC().AddDate(0, 0, -56)
	workoutRows, err := h.db.ListWorkoutsSince(cutoff56)
	if err != nil {
		return nil, err
	}
	workouts := make([]aicoach.WorkoutBrief, 0, len(workoutRows))
	for _, w := range workoutRows {
		brief := aicoach.WorkoutBrief{
			Date:            w.RecordedAt.Format("2006-01-02"),
			Sport:           w.Sport,
			DurationMins:    float64(w.DurationSecs) / 60,
			DistanceKM:      w.DistanceMeters / 1000,
			ElevationM:      w.ElevationGainMeters,
			AvgPowerWatts:   w.AvgPowerWatts,
			NormalizedPower: w.NormalizedPower,
			AvgHR:           w.AvgHeartRate,
			TSS:             w.TSS,
			IntensityFactor: w.IntensityFactor,
			Indoor:          w.IsIndoor,
		}
		if w.NormalizedPower != nil && w.AvgHeartRate != nil && *w.AvgHeartRate > 0 {
			ef := round2(*w.NormalizedPower / float64(*w.AvgHeartRate))
			brief.EF = &ef
		}
		if w.NormalizedPower != nil && w.AvgPowerWatts != nil && *w.AvgPowerWatts > 0 {
			vi := round2(*w.NormalizedPower / *w.AvgPowerWatts)
			brief.VI = &vi
		}
		// Decoupling needs per-sample data — only pay the stream fetch for rides
		// long enough for drift to be meaningful (≥60 min, power + HR present).
		if w.DurationSecs >= 3600 && w.AvgPowerWatts != nil && w.AvgHeartRate != nil {
			if dec, ok := h.computeDecoupling(w.ID); ok {
				r := round2(dec)
				brief.DecouplingPct = &r
			}
		}
		workouts = append(workouts, brief)
	}

	weeklyRaw, err := h.db.GetWeeklyBreakdown(8)
	if err != nil {
		return nil, err
	}
	weekly := make([]aicoach.WeeklyLoad, len(weeklyRaw))
	for i, wl := range weeklyRaw {
		weekly[i] = aicoach.WeeklyLoad{
			Week:          wl.Week,
			TSS:           wl.TSS,
			HoursTraining: float64(wl.DurationSecs) / 3600,
			WorkoutCount:  wl.WorkoutCount,
			LoadType:      wl.LoadType,
		}
	}

	allTimeCurve, err := h.db.GetAllTimePowerCurve()
	if err != nil {
		return nil, err
	}
	var powerBests []aicoach.PowerBest
	for _, kd := range aicoach.KeyDurations {
		best, ok := allTimeCurve[kd.Secs]
		if !ok {
			continue
		}
		var wPerKG, pctFTP float64
		if athlete.WeightKG > 0 {
			wPerKG = float64(best.Watts) / athlete.WeightKG
		}
		if athlete.FTPWatts > 0 {
			pctFTP = float64(best.Watts) / float64(athlete.FTPWatts) * 100
		}
		powerBests = append(powerBests, aicoach.PowerBest{
			Duration: kd.Label,
			Watts:    best.Watts,
			WPerKG:   round2(wPerKG),
			PctFTP:   round2(pctFTP),
		})
	}

	powerZones, hrZones, err := h.db.GetRecentZoneTotals(56)
	if err != nil {
		return nil, err
	}
	zones := aicoach.ZoneDist{
		PowerZones: formatZoneValues(powerZones[:]),
		HRZones:    formatZoneValues(hrZones[:]),
	}

	return aicoach.BuildData(profile, fitnessPts, workouts, weekly, powerBests, zones), nil
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}

func round1(f float64) float64 {
	return float64(int(f*10+0.5)) / 10
}

// formatZoneValues picks a readable unit for a zone-time array: hours (1 decimal)
// once any single zone exceeds 5h, otherwise whole minutes. Keeps small values
// precise (90 min reads better than 1.5h) without ballooning big ones.
func formatZoneValues(secs []int) aicoach.ZoneValues {
	var maxSecs int
	for _, s := range secs {
		if s > maxSecs {
			maxSecs = s
		}
	}
	values := make([]float64, len(secs))
	if maxSecs >= 5*3600 {
		for i, s := range secs {
			values[i] = round1(float64(s) / 3600)
		}
		return aicoach.ZoneValues{Unit: "hours", Values: values}
	}
	for i, s := range secs {
		values[i] = float64((s + 30) / 60) // round to nearest minute
	}
	return aicoach.ZoneValues{Unit: "minutes", Values: values}
}

// computeDecoupling returns aerobic decoupling % for a ride: the drop in
// Pw:HR ratio from the first half of the ride to the second. Positive =
// heart rate drifted up relative to power, which flags aerobic limitation.
// Under 5% on 2h+ rides is the standard "aerobically durable" threshold.
// Returns (0, false) if the stream lacks enough paired power/HR samples.
func (h *CoachHandler) computeDecoupling(workoutID string) (float64, bool) {
	streams, err := h.db.GetStreams(workoutID)
	if err != nil || len(streams) < 2 {
		return 0, false
	}
	start := streams[0].Timestamp
	end := streams[len(streams)-1].Timestamp
	mid := start.Add(end.Sub(start) / 2)

	var p1Sum, hr1Sum, p2Sum, hr2Sum float64
	var p1N, hr1N, p2N, hr2N int
	for _, s := range streams {
		inFirst := s.Timestamp.Before(mid)
		if s.PowerWatts != nil {
			if inFirst {
				p1Sum += float64(*s.PowerWatts)
				p1N++
			} else {
				p2Sum += float64(*s.PowerWatts)
				p2N++
			}
		}
		if s.HeartRateBPM != nil {
			if inFirst {
				hr1Sum += float64(*s.HeartRateBPM)
				hr1N++
			} else {
				hr2Sum += float64(*s.HeartRateBPM)
				hr2N++
			}
		}
	}
	if p1N == 0 || hr1N == 0 || p2N == 0 || hr2N == 0 {
		return 0, false
	}
	r1 := (p1Sum / float64(p1N)) / (hr1Sum / float64(hr1N))
	r2 := (p2Sum / float64(p2N)) / (hr2Sum / float64(hr2N))
	if r1 == 0 {
		return 0, false
	}
	return (r1 - r2) / r1 * 100, true
}
