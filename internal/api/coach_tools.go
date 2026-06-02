package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/fitbase/fitbase/internal/aicoach"
	"github.com/fitbase/fitbase/internal/fitness"
	"github.com/fitbase/fitbase/internal/models"
)

// Execution side of the conversational coach's tools. Schemas live in
// aicoach.CoachTools(); each tool here maps to a DB-backed result.
//
// Tools reuse the same shaping cores as the agent-oriented REST API
// (agent_data.go) and models.Workout.ToSummary() — the "/summary" shape the
// README designates for function-calling — so the coach and the public API
// can never report different numbers. Windows are clamped so a bad argument
// can't pull unbounded data.

// briefFromWorkout enriches a stored workout with EF/VI and (for long rides)
// aerobic decoupling. This is the *insights* path's helper (collectCoachingData
// in coach.go) — it intentionally pays a per-ride stream fetch for decoupling.
// The chat list tool deliberately uses the cheaper ToSummary() instead and
// only computes decoupling for a single workout in get_workout_detail.
func (h *CoachHandler) briefFromWorkout(w models.Workout) aicoach.WorkoutBrief {
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
	if ef, ok := fitness.EfficiencyFactor(w.NormalizedPower, w.AvgHeartRate); ok {
		r := round2(ef)
		brief.EF = &r
	}
	if vi, ok := fitness.VariabilityIndex(w.NormalizedPower, w.AvgPowerWatts); ok {
		r := round2(vi)
		brief.VI = &r
	}
	if w.DurationSecs >= 3600 && w.AvgPowerWatts != nil && w.AvgHeartRate != nil {
		if dec, ok := h.decouplingForWorkout(w.ID); ok {
			r := round2(dec)
			brief.DecouplingPct = &r
		}
	}
	return brief
}

// clampInt bounds v to [lo, hi], substituting def when v is 0 (unset).
func clampInt(v, def, lo, hi int) int {
	if v == 0 {
		v = def
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// execTool runs one tool call from the conversational coach and returns its
// result as a JSON string. Satisfies aicoach.ToolExecutor. Unknown tools and
// bad input return an error, which Coach.Chat relays to the model as a tool
// error rather than aborting the conversation.
func (h *CoachHandler) execTool(_ context.Context, name string, input json.RawMessage) (string, error) {
	switch name {
	case aicoach.ToolGetAthleteProfile:
		return h.toolAthleteProfile()
	case aicoach.ToolGetReadiness:
		return h.toolReadiness()
	case aicoach.ToolGetFitnessTrend:
		return h.toolFitnessTrend(input)
	case aicoach.ToolListRecentWorkouts:
		return h.toolListRecentWorkouts(input)
	case aicoach.ToolGetWorkoutDetail:
		return h.toolWorkoutDetail(input)
	case aicoach.ToolGetWeeklyBreakdown:
		return h.toolWeeklyBreakdown(input)
	case aicoach.ToolGetPowerCurve:
		return h.toolPowerCurve()
	case aicoach.ToolGetZoneDistribution:
		return h.toolZoneDistribution(input)
	case aicoach.ToolProposeSchedule:
		return h.toolProposeSchedule(input)
	default:
		return "", fmt.Errorf("unknown tool %q", name)
	}
}

// jsonResult marshals v to a compact string for the model.
func jsonResult(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("marshal tool result: %w", err)
	}
	return string(b), nil
}

func (h *CoachHandler) toolAthleteProfile() (string, error) {
	a, err := h.db.GetAthlete()
	if err != nil {
		return "", err
	}
	return jsonResult(aicoach.AthleteProfile{
		FTPWatts:    a.FTPWatts,
		WeightKG:    a.WeightKG,
		ThresholdHR: a.ThresholdHR,
		MaxHR:       a.MaxHR,
	})
}

// toolReadiness reuses the same core as GET /api/athlete/readiness — current
// CTL/ATL/TSB, days since last workout, 28-day ramp rate, and a recommendation.
func (h *CoachHandler) toolReadiness() (string, error) {
	report, err := readinessReport(h.db)
	if err != nil {
		return "", err
	}
	return jsonResult(report)
}

func (h *CoachHandler) toolFitnessTrend(input json.RawMessage) (string, error) {
	var args struct {
		Days int `json:"days"`
	}
	_ = json.Unmarshal(input, &args)
	days := clampInt(args.Days, 90, 7, 365)

	hist, err := h.db.GetFitnessHistory(days)
	if err != nil {
		return "", err
	}
	// Sample long windows so the model gets the shape without a wall of rows,
	// using the same sampler as the insights payload.
	step := 1
	if len(hist) > 60 {
		step = len(hist) / 45
	}
	sampled := aicoach.SampleEvery(hist, step)
	pts := make([]aicoach.FitnessPoint, len(sampled))
	for i, fp := range sampled {
		pts[i] = aicoach.FitnessPoint{
			Date:    fp.Date.Format("2006-01-02"),
			Fitness: round1(fp.Fitness),
			Fatigue: round1(fp.Fatigue),
			Form:    round1(fp.Form),
		}
	}
	return jsonResult(map[string]any{"window_days": days, "points": pts})
}

// toolListRecentWorkouts returns the README's curated /summary shape
// (models.Workout.ToSummary) for each workout. Deliberately no per-ride stream
// fetch — get_workout_detail is where the model drills in for decoupling.
func (h *CoachHandler) toolListRecentWorkouts(input json.RawMessage) (string, error) {
	var args struct {
		Days  int    `json:"days"`
		Sport string `json:"sport"`
		Limit int    `json:"limit"`
	}
	_ = json.Unmarshal(input, &args)
	days := clampInt(args.Days, 56, 1, 365)
	limit := clampInt(args.Limit, 40, 1, 100)
	sport := strings.ToLower(strings.TrimSpace(args.Sport))

	since := time.Now().UTC().AddDate(0, 0, -days)
	rows, err := h.db.ListWorkoutsSince(since)
	if err != nil {
		return "", err
	}
	out := make([]models.WorkoutSummary, 0, limit)
	for i := range rows {
		if sport != "" && strings.ToLower(rows[i].Sport) != sport {
			continue
		}
		out = append(out, rows[i].ToSummary())
		if len(out) >= limit {
			break
		}
	}
	return jsonResult(map[string]any{
		"window_days": days,
		"count":       len(out),
		"workouts":    out,
	})
}

// toolWorkoutDetail reuses the workoutAnalysis core (zone distribution, VI/EF,
// 90-day context) shared with GET /api/workouts/{id}/analysis, plus the base
// summary and — only here, for a single ride — aerobic decoupling.
func (h *CoachHandler) toolWorkoutDetail(input json.RawMessage) (string, error) {
	var args struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(input, &args)
	id := strings.TrimSpace(args.ID)
	if id == "" {
		return "", fmt.Errorf("id is required")
	}

	w, err := h.db.GetWorkout(id)
	if err != nil {
		return "", err
	}
	if w == nil {
		return jsonResult(map[string]any{"error": "no workout with that id"})
	}
	analysis, err := workoutAnalysis(h.db, id)
	if err != nil {
		return "", err
	}

	res := map[string]any{
		"workout":  w.ToSummary(),
		"analysis": analysis,
	}
	if w.DurationSecs >= 3600 && w.AvgPowerWatts != nil && w.AvgHeartRate != nil {
		if dec, ok := h.decouplingForWorkout(w.ID); ok {
			res["aerobic_decoupling_pct"] = round2(dec)
		}
	}
	return jsonResult(res)
}

// toolWeeklyBreakdown returns the same rows as GET /api/training/weekly.
func (h *CoachHandler) toolWeeklyBreakdown(input json.RawMessage) (string, error) {
	var args struct {
		Weeks int `json:"weeks"`
	}
	_ = json.Unmarshal(input, &args)
	weeks := clampInt(args.Weeks, 8, 1, 52)

	rows, err := h.db.GetWeeklyBreakdown(weeks)
	if err != nil {
		return "", err
	}
	if rows == nil {
		rows = []models.WeeklyLoad{}
	}
	return jsonResult(map[string]any{"weeks": rows})
}

// toolPowerCurve reuses the same core as GET /api/athlete/power-curve.
func (h *CoachHandler) toolPowerCurve() (string, error) {
	report, err := powerCurveReport(h.db)
	if err != nil {
		return "", err
	}
	return jsonResult(report)
}

// toolProposeSchedule is the only write tool. It validates each proposed
// workout (via the same plannedRequest.toModel path the manual quick-add uses)
// and stores the batch as a coach draft in planned_workout_drafts. The
// returned preview_id is sniffed by the Chat HTTP handler, which forwards it
// to the browser over SSE so the UI can render a preview card. Nothing lands
// on the calendar until the rider commits the draft.
func (h *CoachHandler) toolProposeSchedule(input json.RawMessage) (string, error) {
	var args struct {
		Workouts []plannedRequest `json:"workouts"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if len(args.Workouts) == 0 {
		return "", fmt.Errorf("workouts is empty — propose at least one")
	}
	// Validate every item before persisting so a malformed entry doesn't leave
	// the model with a draft id pointing at unreviewable content.
	for i, req := range args.Workouts {
		if _, err := req.toModel("coach"); err != nil {
			return "", fmt.Errorf("workouts[%d]: %s", i, err.Error())
		}
	}
	payload, err := json.Marshal(draftPayload{Workouts: args.Workouts})
	if err != nil {
		return "", err
	}
	id, err := h.db.SavePlannedDraft(string(payload))
	if err != nil {
		return "", err
	}
	return jsonResult(map[string]any{
		"preview_id": id,
		"count":      len(args.Workouts),
		"message":    "Saved as a draft. The rider sees a preview card; tell them to review it and click 'Add to calendar' to accept.",
	})
}

// toolZoneDistribution has no REST equivalent (the analysis endpoint is
// per-workout; this aggregates time-in-zone across a recent window).
func (h *CoachHandler) toolZoneDistribution(input json.RawMessage) (string, error) {
	var args struct {
		Days int `json:"days"`
	}
	_ = json.Unmarshal(input, &args)
	days := clampInt(args.Days, 56, 1, 365)

	power, hr, ssSecs, err := h.db.GetRecentZoneTotals(days)
	if err != nil {
		return "", err
	}
	// SS is a parallel band (88–94% FTP) that overlaps Z3/Z4 — surface as a
	// single value alongside the partitioned zones so the model treats it as a
	// quality indicator, not as an 8th bucket.
	ss := formatZoneValues([]int{ssSecs})
	return jsonResult(map[string]any{
		"window_days": days,
		"power_zones": formatZoneValues(power[:]),
		"hr_zones":    formatZoneValues(hr[:]),
		"sweet_spot": map[string]any{
			"unit":  ss.Unit,
			"value": ss.Values[0],
			"note":  "Sweet Spot (88–94% FTP) overlaps Z3/Z4 — counted in parallel, not subtracted from the 7-zone totals.",
		},
	})
}
