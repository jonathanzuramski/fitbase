package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fitbase/fitbase/internal/aicoach"
	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/models"
)

// Weekly plan: the coach reviews how training has been going and proposes the
// next seven days. Started from the calendar page ("plan my week"), revised
// from there ("move the long ride to Sunday"), or run automatically on Sunday
// night when the rider turns that on. Every run goes through the same tool-
// using chat loop as the coach chat, with the weekly playbook appended to the
// system context, and is stored as a models.WeeklyPlan the rider accepts onto
// the calendar.

const (
	// weeklyJobKind is the sseJob slot for plan runs. Manual and scheduled runs
	// share it, so they can't overlap and the calendar page can attach to a
	// Sunday-night run that is still in progress.
	weeklyJobKind = "weekly"

	// weeklyPlanWeekday/Hour is when the auto-plan runs, rider-local:
	// Sunday 20:00, after the weekend's rides are in.
	weeklyPlanWeekday = time.Sunday
	weeklyPlanHour    = 20

	// A failing scheduled run (provider outage, bad key) retries at most this
	// many times per week, at least weeklyRetryAfter apart, so an outage can't
	// turn into a bill or a log flood.
	weeklyMaxScheduledFailures = 3
	weeklyRetryAfter           = time.Hour

	maxWeeklyNotes = 500
)

var errWeeklyNotConfigured = errors.New("AI coach not configured — add provider and API key in settings")

// weeklyPlanRun is one generation request.
type weeklyPlanRun struct {
	trigger string             // models.WeeklyPlanManual or models.WeeklyPlanScheduled
	notes   string             // rider constraints, or what to change on a revision
	revise  *models.WeeklyPlan // the plan being revised, if any
}

// generateWeeklyPlan runs the weekly playbook through the coach and stores the
// result. emit receives the same tool/chunk events as a chat turn, so the
// calendar page can show progress live. A failed run is stored too (status
// failed) — the scheduler counts those to back off.
func (h *CoachHandler) generateWeeklyPlan(ctx context.Context, run weeklyPlanRun, emit func(event string, data any)) (models.WeeklyPlan, error) {
	now := time.Now().In(h.db.AthleteLocation())
	start, end := aicoach.WeeklyPlanWindow(now)

	// A revision changes the week it revises: what is left of that window,
	// from today when it comes on a later day, so today's session stays in
	// the plan as today's. It gets the revision brief, and its text is a
	// change note added under the review the plan already has. A week that
	// is over (or can't be read back) is planned afresh instead, with the
	// full brief and a full review.
	revising, week := false, ""
	if run.revise != nil {
		if s, e, ok := revisionWindow(run.revise, now); ok {
			if ws, err := planWorkouts(run.revise); err != nil {
				slog.Warn("weekly plan: could not replay the proposed week", "plan", run.revise.ID, "err", err)
			} else {
				start, end, revising = s, e, true
				week = proposedWeekText(ws, start, end, now)
			}
		}
	}
	plan := models.WeeklyPlan{
		Trigger:     run.trigger,
		WindowStart: start.Format("2006-01-02"),
		WindowEnd:   end.Format("2006-01-02"),
	}

	// The request the coach answers. A revision replays the plan it revises —
	// the rider's earlier constraints and the coach's review — then asks for
	// the change, and the new plan's Request carries every constraint so far.
	//
	// The review never lists the workouts and an unaccepted plan isn't on the
	// calendar, so the change message carries the proposed week itself:
	// without it "move the long ride to Sunday" rebuilt the week from scratch.
	// It stays out of Request, which would otherwise accumulate every week.
	history := []aicoach.ChatMessage{{Role: aicoach.ChatRoleUser}}
	switch {
	case run.revise != nil:
		change := "Change the plan: " + run.notes
		msg := change
		if revising {
			if week == "" {
				week = "(no sessions left in it)"
			}
			msg += "\n\nThe week you proposed, " + start.Format("Mon 2006-01-02") + " to " + end.Format("Mon 2006-01-02") + ":\n" + week
		}
		history = []aicoach.ChatMessage{
			{Role: aicoach.ChatRoleUser, Content: run.revise.Request},
			{Role: aicoach.ChatRoleAssistant, Content: run.revise.ReviewMD},
			{Role: aicoach.ChatRoleUser, Content: msg},
		}
		plan.Request = run.revise.Request + "\n\n" + change
	case run.notes != "":
		plan.Request = "Plan my week.\n\nConstraints: " + run.notes
		history[0].Content = plan.Request
	default:
		plan.Request = "Plan my week."
		history[0].Content = plan.Request
	}

	fail := func(err error) (models.WeeklyPlan, error) {
		plan.Status = models.WeeklyPlanFailed
		plan.Error = err.Error()
		if _, saveErr := h.db.SaveWeeklyPlan(plan); saveErr != nil {
			slog.Warn("weekly plan: could not record failure", "err", saveErr)
		}
		return plan, err
	}

	settings, err := h.db.GetAISettings()
	if err != nil {
		return fail(fmt.Errorf("load AI settings: %w", err))
	}
	if settings.Provider == "" || settings.APIKey == "" {
		return fail(errWeeklyNotConfigured)
	}
	coach, err := aicoach.New(settings.Provider, settings.Model, settings.APIKey)
	if err != nil {
		return fail(err)
	}

	// The rider's standing availability (calendar page): stated in the prompt
	// as dates, and enforced on propose_schedule.
	prefs, err := h.db.GetWeeklyPlanPrefs()
	if err != nil {
		return fail(fmt.Errorf("load weekly plan preferences: %w", err))
	}
	var budget float64
	if prefs.WeeklyHours != nil {
		budget = *prefs.WeeklyHours
	}
	brief := aicoach.WeeklyPlanBrief(start, end)
	if revising {
		brief = aicoach.WeeklyPlanRevisionBrief(start, end)
	}
	systemContext := h.chatSystemContext(now) + "\n\n" + brief +
		"\n\n" + aicoach.WeeklyPlanAvailability(start, end, prefs.Allows, budget)

	// propose_schedule is captured into the plan rather than written to the
	// single-slot draft table (see toolEvents.Capture).
	var proposed []plannedRequest
	ev := toolEvents{
		Capture: func(ws []plannedRequest) string {
			proposed = ws
			return "weekly-plan"
		},
		Availability: &planAvailability{prefs: prefs},
		Window:       &planWindow{Start: plan.WindowStart, End: plan.WindowEnd},
	}
	exec := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		return h.execTool(ctx, name, input, ev)
	}
	tools := aicoach.CoachTools()
	labels := make(map[string]string, len(tools))
	for _, t := range tools {
		labels[t.Name] = t.Label
	}

	review, err := coach.Chat(ctx, history, systemContext, tools, exec, aicoach.ChatCallbacks{
		OnToolStart: func(name string) {
			emit("tool", map[string]string{"name": name, "label": labels[name], "status": "running"})
		},
		OnToolEnd: func(name string, ok bool) {
			status := "done"
			if !ok {
				status = "error"
			}
			emit("tool", map[string]string{"name": name, "label": labels[name], "status": status})
		},
		OnText: func(chunk string) error {
			emit("chunk", map[string]string{"text": chunk})
			return nil
		},
	})
	if err != nil {
		return fail(err)
	}

	plan.Status = models.WeeklyPlanReady
	plan.ReviewMD = review
	if revising {
		plan.ReviewMD = revisedReview(run.revise.ReviewMD, review)
	}
	if len(proposed) > 0 {
		js, err := json.Marshal(draftPayload{Workouts: proposed})
		if err != nil {
			return fail(fmt.Errorf("encode proposed week: %w", err))
		}
		plan.WorkoutsJSON = string(js)
	}
	saved, err := h.db.SaveWeeklyPlan(plan)
	if err != nil {
		return plan, err
	}
	return saved, nil
}

// planWorkouts decodes a plan's proposed week into PlannedWorkout shape.
func planWorkouts(p *models.WeeklyPlan) ([]models.PlannedWorkout, error) {
	out := []models.PlannedWorkout{}
	if p.WorkoutsJSON == "" {
		return out, nil
	}
	var d draftPayload
	if err := json.Unmarshal([]byte(p.WorkoutsJSON), &d); err != nil {
		return nil, fmt.Errorf("proposed week is corrupt: %w", err)
	}
	for i, req := range d.Workouts {
		w, err := req.toModel("coach")
		if err != nil {
			return nil, fmt.Errorf("proposed workout %d: %w", i, err)
		}
		out = append(out, w)
	}
	return out, nil
}

// revisionWindow returns what is left of a plan's window at now: from its
// first day, or from today once that has passed (a session dated today is
// still to be ridden), to its last day. ok is false once the week is over.
func revisionWindow(p *models.WeeklyPlan, now time.Time) (start, end time.Time, ok bool) {
	loc := now.Location()
	start, err1 := time.ParseInLocation("2006-01-02", p.WindowStart, loc)
	end, err2 := time.ParseInLocation("2006-01-02", p.WindowEnd, loc)
	if err1 != nil || err2 != nil {
		return start, end, false
	}
	y, m, d := now.Date()
	if today := time.Date(y, m, d, 0, 0, 0, 0, loc); today.After(start) {
		start = today
	}
	return start, end, !start.After(end)
}

// revisedReview is the stored review of a revised plan: the review it
// revises, which the calendar shows and the next revision replays, with the
// revision's change note under it.
func revisedReview(previous, note string) string {
	return strings.TrimSpace(previous) + "\n\n**Revised:** " + strings.TrimSpace(note)
}

// proposedWeekText renders a plan's proposed week for the coach, one line per
// workout in [start, end] with everything propose_schedule needs to send it
// again unchanged: date (marked when it is today), title, sport (when not
// cycling), duration, TSS and IF when set, the interval structure, and the
// description on a line of its own, quoted. Workouts before start (a
// revision made on a later day) are left out and counted. "" when no workout
// falls in the window.
func proposedWeekText(ws []models.PlannedWorkout, start, end, now time.Time) string {
	from, to := start.Format("2006-01-02"), end.Format("2006-01-02")
	today := now.Format("2006-01-02")
	var lines []string
	past := 0
	for _, w := range ws {
		day := w.PlannedDate.Format("2006-01-02")
		if day < from {
			past++
			continue
		}
		if day > to {
			continue
		}
		line := "- " + w.PlannedDate.Format("Mon 2006-01-02")
		if day == today {
			line += " (today)"
		}
		line += " · " + w.Title
		if w.Sport != "cycling" {
			line += " · " + w.Sport
		}
		if w.DurationSecs%60 == 0 {
			line += fmt.Sprintf(" · %d min", w.DurationSecs/60)
		} else {
			line += fmt.Sprintf(" · %d min %d s", w.DurationSecs/60, w.DurationSecs%60)
		}
		if w.TSS != nil {
			line += fmt.Sprintf(" · ~%.0f TSS", *w.TSS)
		}
		if w.IntensityFactor != nil {
			line += fmt.Sprintf(" · IF %.2f", *w.IntensityFactor)
		}
		if s := models.PlannedStructure(w.Intervals); s != "" {
			line += " · " + s
		}
		if w.Description != "" {
			line += "\n  description: " + strconv.Quote(w.Description)
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return ""
	}
	if past > 0 {
		lines = append(lines, fmt.Sprintf("(%d earlier session(s) are now in the past and not part of this plan.)", past))
	}
	return strings.Join(lines, "\n")
}

// ── Schedule ──────────────────────────────────────────────────────────────────

// weeklySlot returns the most recent auto-plan time (Sunday 20:00 in now's
// location) at or before now, and the Monday after it — the week that slot
// plans for.
func weeklySlot(now time.Time) (slot time.Time, monday string) {
	back := (int(now.Weekday()) - int(weeklyPlanWeekday) + 7) % 7
	y, m, d := now.AddDate(0, 0, -back).Date()
	slot = time.Date(y, m, d, weeklyPlanHour, 0, 0, 0, now.Location())
	if slot.After(now) { // it's Sunday, before 20:00: the slot is last week's
		y, m, d = slot.AddDate(0, 0, -7).Date()
		slot = time.Date(y, m, d, weeklyPlanHour, 0, 0, 0, now.Location())
	}
	y, m, d = slot.Date()
	return slot, time.Date(y, m, d+1, 0, 0, 0, 0, now.Location()).Format("2006-01-02")
}

// nextWeeklySlot returns the first auto-plan time strictly after now.
func nextWeeklySlot(now time.Time) time.Time {
	slot, _ := weeklySlot(now)
	y, m, d := slot.AddDate(0, 0, 7).Date()
	return time.Date(y, m, d, weeklyPlanHour, 0, 0, 0, now.Location())
}

// weeklyPlanDue decides whether the scheduler should run now. A slot that was
// missed (server off on Sunday night) is caught up on the next check; a week
// the rider already planned — any non-failed plan starting on or after that
// Monday — is skipped.
func (h *CoachHandler) weeklyPlanDue(now time.Time) bool {
	if v, _ := h.db.GetMeta(db.MetaWeeklyPlanAuto); v != "1" {
		return false
	}
	settings, err := h.db.GetAISettings()
	if err != nil || settings.Provider == "" || settings.APIKey == "" || !aicoach.SupportsChat(settings.Provider) {
		return false
	}
	slot, monday := weeklySlot(now)
	if covered, err := h.db.WeeklyPlanCovers(monday); err != nil || covered {
		return false
	}
	fails, last, err := h.db.ScheduledFailuresSince(slot)
	if err != nil || fails >= weeklyMaxScheduledFailures {
		return false
	}
	return last.IsZero() || now.Sub(last) >= weeklyRetryAfter
}

// RunWeeklyScheduler checks every interval whether the Sunday-night auto-plan
// is due and runs it. Blocks until ctx is cancelled; call it in a goroutine.
// The first check waits one interval so boot-time work (archive restore,
// rebuilds) settles before the coach reads the data.
func (h *CoachHandler) RunWeeklyScheduler(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.runScheduledPlanIfDue(ctx)
		}
	}
}

func (h *CoachHandler) runScheduledPlanIfDue(ctx context.Context) {
	if !h.weeklyPlanDue(time.Now().In(h.db.AthleteLocation())) {
		return
	}
	job, ok := h.startJob(weeklyJobKind)
	if !ok {
		return // a manual run is in flight; it covers the week
	}
	job.emit("job", map[string]string{"id": job.ID})
	runCtx, cancel := context.WithTimeout(ctx, llmRunTimeout)
	defer cancel()

	slog.Info("weekly plan: scheduled run starting")
	plan, err := h.generateWeeklyPlan(runCtx, weeklyPlanRun{trigger: models.WeeklyPlanScheduled}, job.emit)
	if err != nil {
		slog.Warn("weekly plan: scheduled run failed", "err", err)
		job.emit("error", map[string]string{"error": err.Error()})
	} else {
		slog.Info("weekly plan: scheduled run done", "plan", plan.ID, "window_start", plan.WindowStart)
		job.emit("done", map[string]string{"plan_id": plan.ID})
	}
	job.finish()
}

// ── HTTP ──────────────────────────────────────────────────────────────────────

type weeklyScheduleStatus struct {
	Enabled bool   `json:"enabled"`
	NextRun string `json:"next_run,omitempty"` // RFC3339, rider-local; only when enabled
}

func (h *CoachHandler) weeklySchedule() weeklyScheduleStatus {
	v, _ := h.db.GetMeta(db.MetaWeeklyPlanAuto)
	s := weeklyScheduleStatus{Enabled: v == "1"}
	if s.Enabled {
		s.NextRun = nextWeeklySlot(time.Now().In(h.db.AthleteLocation())).Format(time.RFC3339)
	}
	return s
}

// WeeklyPlanLatest returns the most recent plan (null when none), its proposed
// workouts, and the auto-plan setting.
//
// GET /api/coach/weekly-plan
func (h *CoachHandler) WeeklyPlanLatest(w http.ResponseWriter, r *http.Request) {
	p, err := h.db.LatestWeeklyPlan()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	prefs, err := h.db.GetWeeklyPlanPrefs()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	resp := map[string]any{
		"plan":        p,
		"workouts":    []models.PlannedWorkout{},
		"schedule":    h.weeklySchedule(),
		"preferences": prefs.Normalized(),
	}
	if p != nil {
		ws, err := planWorkouts(p)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp["workouts"] = ws
	}
	writeJSON(w, http.StatusOK, resp)
}

// WeeklyPlanGenerate starts a plan run (or a revision of an existing plan) and
// streams it over SSE: job, tool, chunk, done {"plan_id"}, error. Like chat,
// the run is detached — navigating away doesn't cancel it.
//
// POST /api/coach/weekly-plan   body: {"notes": "...", "revise_id": "..."}
func (h *CoachHandler) WeeklyPlanGenerate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Notes    string `json:"notes"`
		ReviseID string `json:"revise_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Notes = strings.TrimSpace(req.Notes)
	if len(req.Notes) > maxWeeklyNotes {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("notes are limited to %d characters", maxWeeklyNotes))
		return
	}

	settings, err := h.db.GetAISettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if settings.Provider == "" || settings.APIKey == "" {
		writeError(w, http.StatusServiceUnavailable, errWeeklyNotConfigured.Error())
		return
	}
	if !aicoach.SupportsChat(settings.Provider) {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("weekly plans need a provider that supports tool use — switch to %s in settings",
				strings.Join(aicoach.ChatCapableLabels(), " or ")))
		return
	}

	run := weeklyPlanRun{trigger: models.WeeklyPlanManual, notes: req.Notes}
	if req.ReviseID != "" {
		prev, err := h.db.GetWeeklyPlan(req.ReviseID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "database error")
			return
		}
		if prev == nil || prev.Status == models.WeeklyPlanFailed || prev.ReviewMD == "" {
			writeError(w, http.StatusNotFound, "no plan to revise with that id")
			return
		}
		if req.Notes == "" {
			writeError(w, http.StatusBadRequest, "say what to change")
			return
		}
		run.revise = prev
	}

	job, ok := h.startJob(weeklyJobKind)
	if !ok {
		writeError(w, http.StatusConflict, "a weekly plan is already being generated")
		return
	}
	job.emit("job", map[string]string{"id": job.ID})

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), llmRunTimeout)
		defer cancel()
		plan, err := h.generateWeeklyPlan(ctx, run, job.emit)
		if err != nil {
			job.emit("error", map[string]string{"error": err.Error()})
		} else {
			job.emit("done", map[string]string{"plan_id": plan.ID})
		}
		job.finish()
	}()

	setupSSE(w)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	job.streamTo(w, r.Context())
}

// WeeklyPlanActive reports the current plan run's {id, done}, or 204.
//
// GET /api/coach/weekly-plan/active
func (h *CoachHandler) WeeklyPlanActive(w http.ResponseWriter, r *http.Request) {
	h.jobStatus(w, weeklyJobKind)
}

// WeeklyPlanEvents re-attaches to a plan run's SSE stream.
//
// GET /api/coach/weekly-plan/runs/{id}/events
func (h *CoachHandler) WeeklyPlanEvents(w http.ResponseWriter, r *http.Request) {
	h.attachJob(w, r, weeklyJobKind, chi.URLParam(r, "id"))
}

// WeeklyPlanAccept adds a ready plan's workouts to the calendar.
//
// POST /api/coach/weekly-plan/{id}/accept
func (h *CoachHandler) WeeklyPlanAccept(w http.ResponseWriter, r *http.Request) {
	p, err := h.db.GetWeeklyPlan(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if p == nil {
		writeError(w, http.StatusNotFound, "weekly plan not found")
		return
	}
	if p.Status != models.WeeklyPlanReady {
		writeError(w, http.StatusConflict, "this plan was already "+p.Status)
		return
	}
	ws, err := planWorkouts(p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(ws) == 0 {
		writeError(w, http.StatusConflict, "this plan has no workouts to add")
		return
	}
	saved, err := h.db.AcceptWeeklyPlan(p.ID, ws)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	writeJSON(w, http.StatusCreated, saved)
}

// WeeklyPlanDiscard dismisses a plan without touching the calendar.
//
// POST /api/coach/weekly-plan/{id}/discard
func (h *CoachHandler) WeeklyPlanDiscard(w http.ResponseWriter, r *http.Request) {
	err := h.db.SetWeeklyPlanStatus(chi.URLParam(r, "id"), models.WeeklyPlanDiscarded)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "weekly plan not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// WeeklyPlanSetPreferences saves the rider's training days and optional
// weekly time budget, used by every plan run including the auto-plan.
//
// PUT /api/coach/weekly-plan/preferences   body: {"days": ["mon","wed"], "weekly_hours": 8}
func (h *CoachHandler) WeeklyPlanSetPreferences(w http.ResponseWriter, r *http.Request) {
	var prefs models.WeeklyPlanPrefs
	if err := decodeJSON(r, &prefs); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := prefs.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.db.SaveWeeklyPlanPrefs(prefs); err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	writeJSON(w, http.StatusOK, prefs.Normalized())
}

// WeeklyPlanSetSchedule turns the Sunday-night auto-plan on or off.
//
// PUT /api/coach/weekly-plan/schedule   body: {"enabled": true}
func (h *CoachHandler) WeeklyPlanSetSchedule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Enabled == nil {
		writeError(w, http.StatusBadRequest, `body must be {"enabled": true|false}`)
		return
	}
	var err error
	if *req.Enabled {
		err = h.db.SetMeta(db.MetaWeeklyPlanAuto, "1")
	} else {
		err = h.db.DeleteMeta(db.MetaWeeklyPlanAuto)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	writeJSON(w, http.StatusOK, h.weeklySchedule())
}
