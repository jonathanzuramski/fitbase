package api

import (
	"net/http"
	"strings"
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

// chatRequest is the body of POST /api/coach/chat. The browser owns the
// conversation and posts the full history every turn — the server is stateless.
type chatRequest struct {
	Messages []aicoach.ChatMessage `json:"messages"`
}

const maxChatMessages = 40

// Chat runs a tool-augmented conversation with the coach. The model pulls the
// data it needs via tools (executed against the DB) rather than receiving a
// pre-built blob. Tool rounds run buffered; the final answer is streamed back
// re-chunked over SSE so it still types out live. Nothing is cached — chat is
// ephemeral by design.
//
// Events:
//
//	tool   {"name":"...","status":"running"|"done"|"error"}  // data fetch progress
//	chunk  {"text":"..."}                                     // final answer delta
//	done   {"provider":"...","model":"...","generated_at":""} // finished
//	error  {"error":"..."}                                    // fatal; stream ends
//
// POST /api/coach/chat
func (h *CoachHandler) Chat(w http.ResponseWriter, r *http.Request) {
	settings, err := h.db.GetAISettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if settings.Provider == "" || settings.APIKey == "" {
		writeError(w, http.StatusServiceUnavailable, "AI coach not configured — add provider and API key in settings")
		return
	}
	if !aicoach.SupportsChat(settings.Provider) {
		writeError(w, http.StatusBadRequest,
			"chat requires a provider that supports tool use — switch to Claude in settings (other providers still support generate insights)")
		return
	}

	var req chatRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "messages is empty")
		return
	}
	if len(req.Messages) > maxChatMessages {
		// Keep the most recent turns; old context rarely changes the answer and
		// bounds token cost / latency.
		req.Messages = req.Messages[len(req.Messages)-maxChatMessages:]
		// A trimmed window must still begin on a user turn — providers reject a
		// conversation that opens with an assistant message.
		for len(req.Messages) > 0 && req.Messages[0].Role != aicoach.ChatRoleUser {
			req.Messages = req.Messages[1:]
		}
	}
	for _, m := range req.Messages {
		if m.Role != aicoach.ChatRoleUser && m.Role != aicoach.ChatRoleAssistant {
			writeError(w, http.StatusBadRequest, "message role must be 'user' or 'assistant'")
			return
		}
		if strings.TrimSpace(m.Content) == "" {
			writeError(w, http.StatusBadRequest, "message content is empty")
			return
		}
	}
	if req.Messages[len(req.Messages)-1].Role != aicoach.ChatRoleUser {
		writeError(w, http.StatusBadRequest, "last message must be from the user")
		return
	}

	coach, err := aicoach.New(settings.Provider, settings.Model, settings.APIKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	setupSSE(w)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	_, err = coach.Chat(r.Context(), req.Messages, aicoach.CoachTools(), h.execTool, aicoach.ChatCallbacks{
		OnToolStart: func(name string) {
			writeSSE(w, "tool", map[string]string{"name": name, "status": "running"})
		},
		OnToolEnd: func(name string, ok bool) {
			status := "done"
			if !ok {
				status = "error"
			}
			writeSSE(w, "tool", map[string]string{"name": name, "status": status})
		},
		OnText: func(chunk string) error {
			writeSSE(w, "chunk", map[string]string{"text": chunk})
			return nil
		},
	})
	if err != nil {
		writeSSE(w, "error", map[string]string{"error": err.Error()})
		return
	}

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
		workouts = append(workouts, h.briefFromWorkout(w))
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

	// Same all-time curve + W/kg + %FTP math as GET /api/athlete/power-curve;
	// reuse the core and map it into the compact LLM shape.
	pc, err := powerCurveReport(h.db)
	if err != nil {
		return nil, err
	}
	powerBests := make([]aicoach.PowerBest, 0, len(pc.Entries))
	for _, e := range pc.Entries {
		powerBests = append(powerBests, aicoach.PowerBest{
			Duration: e.DurationLabel,
			Watts:    e.Watts,
			WPerKG:   e.WattsPerKG,
			PctFTP:   e.PctFTP,
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
