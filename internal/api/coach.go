package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fitbase/fitbase/internal/aicoach"
	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/fitness"
	"github.com/fitbase/fitbase/internal/models"
)

// CoachHandler serves AI coaching insight requests.
type CoachHandler struct {
	db *db.DB

	// jobs holds the current detached LLM run per kind ("chat", "insights") so
	// a browser that navigates away can re-attach instead of the run dying with
	// the connection. See coach_job.go.
	jobsMu sync.Mutex
	jobs   map[string]*sseJob
}

func NewCoachHandler(database *db.DB) *CoachHandler {
	return &CoachHandler{db: database, jobs: map[string]*sseJob{}}
}

// llmRunTimeout bounds a detached chat/insights run — without a client
// connection to cancel it, this is what stops a hung provider call.
const llmRunTimeout = 10 * time.Minute

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

// ListModels returns the available models for a provider so the settings page
// can refresh its dropdown from the provider's live catalog instead of a
// hardcoded list. The API key is taken from the request body (the value the
// user just typed) and falls back to the saved key for that provider, so a
// refresh works both before and after saving. Providers that can't list live
// return their curated fallback.
//
// POST /api/coach/models   body: {"provider":"anthropic","api_key":"..."}
func (h *CoachHandler) ListModels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider"`
		APIKey   string `json:"api_key"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Provider == "" {
		writeError(w, http.StatusBadRequest, "provider is required")
		return
	}

	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" {
		// Fall back to the saved key, but only if it belongs to the same
		// provider — a key for provider A won't authenticate against provider B.
		if s, err := h.db.GetAISettings(); err == nil && s.Provider == req.Provider {
			apiKey = s.APIKey
		}
	}

	models, err := aicoach.ListModels(r.Context(), req.Provider, apiKey)
	if err != nil {
		writeError(w, http.StatusBadGateway, "could not fetch models: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
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

	job, ok := h.startJob("insights")
	if !ok {
		writeError(w, http.StatusConflict, "insights generation is already in progress")
		return
	}
	job.emit("job", map[string]string{"id": job.ID})

	// Detached from the request context: navigating away doesn't cancel the
	// generation — it finishes, lands in the cache, and the browser re-attaches
	// (or just loads the cache) when it comes back.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), llmRunTimeout)
		defer cancel()

		full, genErr := coach.StreamInsights(ctx, data, func(chunk string) error {
			job.emit("chunk", map[string]string{"text": chunk})
			return nil
		})
		if genErr != nil {
			job.emit("error", map[string]string{"error": genErr.Error()})
			job.finish()
			return
		}

		// Non-fatal if the cache write fails — the user still got their insights.
		_ = h.db.SaveCachedInsights(db.CachedInsights{
			Provider: settings.Provider,
			Model:    settings.Model,
			Content:  full,
		})

		job.emit("done", map[string]string{
			"provider":     settings.Provider,
			"model":        settings.Model,
			"generated_at": time.Now().UTC().Format(time.RFC3339),
		})
		job.finish()
	}()

	setupSSE(w)
	// Some proxies hold the response until Content-Length is known; flush so the
	// browser sees headers immediately and begins streaming.
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	job.streamTo(w, r.Context())
}

// InsightsActive reports the current insights run's {id, done}, or 204.
//
// GET /api/coach/insights/active
func (h *CoachHandler) InsightsActive(w http.ResponseWriter, r *http.Request) {
	h.jobStatus(w, "insights")
}

// InsightsEvents re-attaches to an insights run's SSE stream.
//
// GET /api/coach/insights/{id}/events
func (h *CoachHandler) InsightsEvents(w http.ResponseWriter, r *http.Request) {
	h.attachJob(w, r, "insights", chi.URLParam(r, "id"))
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
//	job    {"id":"..."}                                       // detached-turn id, always first
//	tool   {"name":"...","status":"running"|"done"|"error"}  // data fetch progress
//	chunk  {"text":"..."}                                     // final answer delta
//	done   {"provider":"...","model":"...","generated_at":""} // finished
//	error  {"error":"..."}                                    // fatal; stream ends
//
// The turn itself runs detached (see coach_job.go): closing this response —
// navigating to another page — does not cancel it. The browser stores the job
// id and re-attaches via GET /api/coach/chat/{id}/events for a full replay.
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
		// Name the capable providers from the registry rather than hardcoding
		// one — the message stays true as providers gain chat support.
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("chat requires a provider that supports tool use — switch to %s in settings (other providers still support generate insights)",
				strings.Join(aicoach.ChatCapableLabels(), " or ")))
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
		// If the window was entirely assistant turns, stripping emptied it. Guard
		// before the len-1 index below so a crafted history can't panic the
		// handler (the earlier len==0 check ran before this trim).
		if len(req.Messages) == 0 {
			writeError(w, http.StatusBadRequest, "no user message in recent history")
			return
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

	// Build the dynamic system suffix: today's date (in the rider's timezone,
	// so propose_schedule dates and "this week" reasoning line up with their
	// calendar) plus the rider profile so the model has FTP/HR in context from
	// turn 1. Failing to load the athlete is non-fatal — the chat still works,
	// the model just has to fall back to calling get_athlete_profile.
	loc := h.db.AthleteLocation()
	var profileBlock string
	if athlete, err := h.db.GetAthlete(); err == nil && athlete != nil {
		profileBlock = buildRiderProfileBlock(athlete)
	}
	systemContext := "Today is " + time.Now().In(loc).Format("Monday, 2006-01-02") + "."
	if profileBlock != "" {
		systemContext += "\n\n" + profileBlock
	}

	// One chat turn at a time: the browser re-attaches to the running turn
	// rather than racing a second one against it.
	job, ok := h.startJob("chat")
	if !ok {
		writeError(w, http.StatusConflict, "a coach reply is already in progress")
		return
	}
	// First event carries the job id so the browser can persist it and
	// re-attach via GET /api/coach/chat/{id}/events after navigating away.
	job.emit("job", map[string]string{"id": job.ID})

	// Tool side effects reach the browser through a typed callback instead of
	// the HTTP layer re-parsing model-facing tool JSON: when propose_schedule
	// creates a draft, its executor reports the preview id here and it is
	// forwarded as a dedicated SSE event. The chat UI uses it to fetch the
	// draft and render the "review and add to calendar" card.
	ev := toolEvents{
		OnPreview: func(id string, count int) {
			job.emit("preview", map[string]any{"id": id, "count": count})
		},
	}
	exec := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		return h.execTool(ctx, name, input, ev)
	}

	// The catalog owns each tool's user-facing label; sending it with the tool
	// events keeps the frontend from re-declaring backend knowledge.
	tools := aicoach.CoachTools()
	toolLabels := make(map[string]string, len(tools))
	for _, t := range tools {
		toolLabels[t.Name] = t.Label
	}

	// The turn runs detached from the request context: navigating away closes
	// the SSE stream but the model keeps working, buffering into the job for
	// the browser to replay when it comes back.
	messages := req.Messages
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), llmRunTimeout)
		defer cancel()

		_, chatErr := coach.Chat(ctx, messages, systemContext, tools, exec, aicoach.ChatCallbacks{
			OnToolStart: func(name string) {
				job.emit("tool", map[string]string{"name": name, "label": toolLabels[name], "status": "running"})
			},
			OnToolEnd: func(name string, ok bool) {
				status := "done"
				if !ok {
					status = "error"
				}
				job.emit("tool", map[string]string{"name": name, "label": toolLabels[name], "status": status})
			},
			OnText: func(chunk string) error {
				job.emit("chunk", map[string]string{"text": chunk})
				return nil
			},
		})
		if chatErr != nil {
			job.emit("error", map[string]string{"error": chatErr.Error()})
		} else {
			job.emit("done", map[string]string{
				"provider":     settings.Provider,
				"model":        settings.Model,
				"generated_at": time.Now().UTC().Format(time.RFC3339),
			})
		}
		job.finish()
	}()

	setupSSE(w)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	job.streamTo(w, r.Context())
}

// ChatActive reports the current chat turn's {id, done}, or 204 when none has
// run since startup. The dashboard calls this on load to decide whether a
// stored turn id can be re-attached.
//
// GET /api/coach/chat/active
func (h *CoachHandler) ChatActive(w http.ResponseWriter, r *http.Request) {
	h.jobStatus(w, "chat")
}

// ChatEvents re-attaches to a chat turn's SSE stream, replaying everything
// buffered so far and tailing until the turn completes.
//
// GET /api/coach/chat/{id}/events
func (h *CoachHandler) ChatEvents(w http.ResponseWriter, r *http.Request) {
	h.attachJob(w, r, "chat", chi.URLParam(r, "id"))
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

	fitnessRaw, err := h.db.GetFitnessHistory(90, h.db.AthleteLocation())
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

	cutoff56 := time.Now().In(h.db.AthleteLocation()).AddDate(0, 0, -56)
	workoutRows, err := h.db.ListWorkoutsSince(cutoff56, "", 0)
	if err != nil {
		return nil, err
	}
	workouts := make([]aicoach.WorkoutBrief, 0, len(workoutRows))
	for _, w := range workoutRows {
		workouts = append(workouts, h.briefFromWorkout(w))
	}

	weeklyRaw, err := h.db.GetWeeklyBreakdown(8, h.db.AthleteLocation())
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

	powerZones, hrZones, ssSecs, err := h.db.GetRecentZoneTotals(56)
	if err != nil {
		return nil, err
	}
	zones := aicoach.ZoneDist{
		PowerZones:    formatZoneValues(powerZones[:]),
		HRZones:       formatZoneValues(hrZones[:]),
		SweetSpotSecs: ssSecs,
	}

	// FTP history, oldest-first, capped to the last 12 changes so the payload
	// stays small. This is what lets the model separate "trained more" from
	// "got faster" in the performance section.
	var ftpHistory []aicoach.FTPPoint
	if hist, err := h.db.AllFTPHistory(); err == nil { // newest first
		if len(hist) > 12 {
			hist = hist[:12]
		}
		for i := len(hist) - 1; i >= 0; i-- {
			ftpHistory = append(ftpHistory, aicoach.FTPPoint{
				Date:     hist[i].EffectiveFrom.Format("2006-01-02"),
				FTPWatts: hist[i].FTPWatts,
			})
		}
	}

	return aicoach.BuildData(profile, fitnessPts, workouts, weekly, powerBests, zones, ftpHistory), nil
}

// round2/round1 use math.Round (not int truncation) so negative values — TSB
// during a training block, negative decoupling — round the same way as the
// REST endpoints instead of biasing toward zero.
func round2(f float64) float64 {
	return math.Round(f*100) / 100
}

func round1(f float64) float64 {
	return math.Round(f*10) / 10
}

// buildRiderProfileBlock renders the athlete's authoritative numbers as a
// system-prompt suffix the model must treat as ground truth (see the prompt's
// hard rules). Only fields with real values are emitted — a 0 here would
// otherwise read as "rider weighs 0 kg" rather than "not configured".
func buildRiderProfileBlock(a *models.Athlete) string {
	var b strings.Builder
	b.WriteString("Rider profile (authoritative — use these exact numbers, do not call get_athlete_profile to re-fetch them):\n")
	if a.FTPWatts > 0 {
		fmt.Fprintf(&b, "- FTP: %d W\n", a.FTPWatts)
	}
	if a.WeightKG > 0 {
		fmt.Fprintf(&b, "- Weight: %.1f kg\n", a.WeightKG)
		if a.FTPWatts > 0 {
			fmt.Fprintf(&b, "- FTP W/kg: %.2f\n", fitness.WPerKG(a.FTPWatts, a.WeightKG))
		}
	}
	if a.ThresholdHR > 0 {
		fmt.Fprintf(&b, "- Threshold HR (LTHR): %d bpm\n", a.ThresholdHR)
	}
	if a.MaxHR > 0 {
		fmt.Fprintf(&b, "- Max HR: %d bpm\n", a.MaxHR)
	}
	if a.RestingHR > 0 {
		fmt.Fprintf(&b, "- Resting HR: %d bpm\n", a.RestingHR)
	}
	if a.Age > 0 {
		fmt.Fprintf(&b, "- Age: %d\n", a.Age)
	}
	if a.Units != "" {
		fmt.Fprintf(&b, "- Preferred units: %s\n", a.Units)
	}
	return b.String()
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

// decouplingForWorkout returns a workout's aerobic decoupling, computing it
// from the streams on first use and caching it (workout_decoupling) — a
// finished ride's streams never change, so every later insights/chat call is
// an indexed read instead of a full stream fetch + resample.
func (h *CoachHandler) decouplingForWorkout(workoutID string) (float64, bool) {
	if val, ok, found, err := h.db.GetWorkoutDecoupling(workoutID); err == nil && found {
		return val, ok
	}
	streams, err := h.db.GetStreams(workoutID)
	if err != nil {
		return 0, false // transient failure: don't cache, retry next call
	}
	dec, ok := fitness.AerobicDecoupling(streams)
	var cached *float64
	if ok {
		cached = &dec
	}
	_ = h.db.SaveWorkoutDecoupling(workoutID, cached) // best-effort
	return dec, ok
}
