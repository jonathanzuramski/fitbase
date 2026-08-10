package api

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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

// toolEvents carries typed UI side effects out of tool execution, so the HTTP
// layer never has to reverse-engineer them from model-facing result JSON.
// Any callback may be nil.
type toolEvents struct {
	// OnPreview fires when propose_schedule stores a draft the rider should
	// see as a preview card.
	OnPreview func(id string, count int)
}

// execTool runs one tool call from the conversational coach and returns its
// result as a JSON string. Unknown tools and bad input return an error, which
// Coach.Chat relays to the model as a tool error rather than aborting the
// conversation.
func (h *CoachHandler) execTool(_ context.Context, name string, input json.RawMessage, ev toolEvents) (string, error) {
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
		return h.toolPowerCurve(input)
	case aicoach.ToolGetZoneDistribution:
		return h.toolZoneDistribution(input)
	case aicoach.ToolGetFTPHistory:
		return h.toolFTPHistory()
	case aicoach.ToolGetPerformanceTrend:
		return h.toolPerformanceTrend(input)
	case aicoach.ToolGetGoalProgress:
		return h.toolGoalProgress()
	case aicoach.ToolProposeSchedule:
		return h.toolProposeSchedule(input, ev)
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
	// Sport filter and limit run in SQL so the DB never hydrates rows this
	// tool would discard.
	rows, err := h.db.ListWorkoutsSince(since, sport, limit)
	if err != nil {
		return "", err
	}
	out := make([]models.WorkoutSummary, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].ToSummary())
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

// toolPowerCurve reuses the same core as GET /api/athlete/power-curve for the
// all-time bests. When the model passes a days window it additionally fetches
// the best efforts inside that window and reports them against the all-time
// numbers, so "current form vs lifetime PR" is answerable in one call.
func (h *CoachHandler) toolPowerCurve(input json.RawMessage) (string, error) {
	var args struct {
		Days int `json:"days"`
	}
	_ = json.Unmarshal(input, &args)

	report, err := powerCurveReport(h.db)
	if err != nil {
		return "", err
	}
	if args.Days == 0 {
		return jsonResult(report)
	}

	days := clampInt(args.Days, 90, 7, 365)
	since := time.Now().UTC().AddDate(0, 0, -days)
	recent, err := h.db.GetPowerCurveSince(since)
	if err != nil {
		return "", err
	}

	type recentEntry struct {
		Duration           string   `json:"duration"`
		AllTimeWatts       int      `json:"all_time_watts"`
		RecentWatts        *int     `json:"recent_watts,omitempty"`
		RecentPctOfAllTime *float64 `json:"recent_pct_of_all_time,omitempty"`
	}
	entries := make([]recentEntry, 0, len(report.Entries))
	for _, e := range report.Entries {
		re := recentEntry{Duration: e.DurationLabel, AllTimeWatts: e.Watts}
		if best, ok := recent[e.DurationSecs]; ok && e.Watts > 0 {
			w := best.Watts
			pct := round1(float64(w) / float64(e.Watts) * 100)
			re.RecentWatts = &w
			re.RecentPctOfAllTime = &pct
		}
		entries = append(entries, re)
	}
	return jsonResult(map[string]any{
		"all_time":           report,
		"window_days":        days,
		"recent_vs_all_time": entries,
		"note":               "recent_pct_of_all_time near 100 means current form matches lifetime bests; missing recent_watts means no effort at that duration in the window.",
	})
}

// toolProposeSchedule is the only write tool. It validates each proposed
// workout (via the same plannedRequest.toModel path the manual quick-add uses)
// and stores the batch as a coach draft in planned_workout_drafts. The draft's
// preview id is reported through ev.OnPreview — the Chat handler forwards it
// to the browser over SSE so the UI can render a preview card. Nothing lands
// on the calendar until the rider commits the draft.
func (h *CoachHandler) toolProposeSchedule(input json.RawMessage, ev toolEvents) (string, error) {
	var args struct {
		Workouts []plannedRequest `json:"workouts"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if len(args.Workouts) == 0 {
		return "", fmt.Errorf("workouts is empty — propose at least one")
	}
	// Cap matches the "max 21" in the tool description: three weeks of daily
	// workouts is more than any sane proposal, and the cap keeps a confused
	// model from staging an unbounded draft blob.
	if len(args.Workouts) > 21 {
		return "", fmt.Errorf("too many workouts (%d) — propose at most 21 (three weeks); split longer plans into blocks", len(args.Workouts))
	}
	// Validate every item before persisting so a malformed entry doesn't leave
	// the model with a draft id pointing at unreviewable content.
	for i, req := range args.Workouts {
		if _, err := req.toModel("coach"); err != nil {
			return "", fmt.Errorf("workouts[%d]: %s", i, err.Error())
		}
		// Titles are only schema-required (provider-enforced), so an empty
		// string can still arrive; the preview card would render '(untitled)'.
		if strings.TrimSpace(req.Title) == "" {
			return "", fmt.Errorf("workouts[%d]: title is required — a short session label the rider sees on the card, e.g. '2x20 threshold'", i)
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
	if ev.OnPreview != nil {
		ev.OnPreview(id, len(args.Workouts))
	}
	return jsonResult(map[string]any{
		"preview_id": id,
		"count":      len(args.Workouts),
		"message":    "Saved as a draft. The rider sees a preview card; tell them to review it and click 'Add to calendar' to accept.",
	})
}

// toolFTPHistory returns every recorded FTP change oldest-first with the delta
// from the previous setting, so the progression reads chronologically.
func (h *CoachHandler) toolFTPHistory() (string, error) {
	entries, err := h.db.AllFTPHistory() // newest first
	if err != nil {
		return "", err
	}
	type ftpChange struct {
		Date       string `json:"date"`
		FTPWatts   int    `json:"ftp_watts"`
		DeltaWatts *int   `json:"delta_watts,omitempty"`
	}
	out := make([]ftpChange, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		c := ftpChange{Date: e.EffectiveFrom.Format("2006-01-02"), FTPWatts: e.FTPWatts}
		if i < len(entries)-1 {
			d := e.FTPWatts - entries[i+1].FTPWatts
			c.DeltaWatts = &d
		}
		out = append(out, c)
	}
	return jsonResult(map[string]any{
		"count":   len(out),
		"changes": out,
		"note":    "Oldest first. delta_watts is the change from the previous setting. Workouts' %FTP/TSS were computed against the FTP in effect on their date.",
	})
}

// toolPerformanceTrend aggregates the per-week markers that distinguish real
// fitness change from mere load accumulation: EF (NP÷avgHR), average IF (so EF
// comparisons can be intensity-matched), and aerobic decoupling on long rides,
// plus FTP changes inside the window. Decoupling pays a stream fetch per long
// ride — same cost the insights path already accepts in briefFromWorkout.
func (h *CoachHandler) toolPerformanceTrend(input json.RawMessage) (string, error) {
	var args struct {
		Days int `json:"days"`
	}
	_ = json.Unmarshal(input, &args)
	days := clampInt(args.Days, 90, 14, 365)
	since := time.Now().UTC().AddDate(0, 0, -days)

	rows, err := h.db.ListWorkoutsSince(since, "", 0)
	if err != nil {
		return "", err
	}

	type weekAgg struct {
		rides                int
		efSum, ifSum, decSum float64
		efN, ifN, decN       int
	}
	weeks := map[string]*weekAgg{}
	for i := range rows {
		w := rows[i]
		// Label by the Monday of the recorded week (UTC) — consistent bucketing
		// is what matters for a trend, not calendar-perfect local weeks.
		wd := (int(w.RecordedAt.UTC().Weekday()) + 6) % 7
		monday := w.RecordedAt.UTC().AddDate(0, 0, -wd).Format("2006-01-02")
		agg := weeks[monday]
		if agg == nil {
			agg = &weekAgg{}
			weeks[monday] = agg
		}
		agg.rides++
		if ef, ok := fitness.EfficiencyFactor(w.NormalizedPower, w.AvgHeartRate); ok {
			agg.efSum += ef
			agg.efN++
		}
		if w.IntensityFactor != nil {
			agg.ifSum += *w.IntensityFactor
			agg.ifN++
		}
		if w.DurationSecs >= 3600 && w.AvgPowerWatts != nil && w.AvgHeartRate != nil {
			if dec, ok := h.decouplingForWorkout(w.ID); ok {
				agg.decSum += dec
				agg.decN++
			}
		}
	}

	type weekRow struct {
		WeekStart        string   `json:"week_start"`
		Rides            int      `json:"rides"`
		AvgIF            *float64 `json:"avg_if,omitempty"`
		AvgEF            *float64 `json:"avg_ef,omitempty"`
		AvgDecouplingPct *float64 `json:"avg_decoupling_pct,omitempty"`
		LongRides        int      `json:"long_rides_with_decoupling,omitempty"`
	}
	keys := make([]string, 0, len(weeks))
	for k := range weeks {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]weekRow, 0, len(keys))
	for _, k := range keys {
		agg := weeks[k]
		row := weekRow{WeekStart: k, Rides: agg.rides, LongRides: agg.decN}
		if agg.ifN > 0 {
			v := round2(agg.ifSum / float64(agg.ifN))
			row.AvgIF = &v
		}
		if agg.efN > 0 {
			v := round2(agg.efSum / float64(agg.efN))
			row.AvgEF = &v
		}
		if agg.decN > 0 {
			v := round2(agg.decSum / float64(agg.decN))
			row.AvgDecouplingPct = &v
		}
		out = append(out, row)
	}

	type ftpChange struct {
		Date     string `json:"date"`
		FTPWatts int    `json:"ftp_watts"`
	}
	ftpChanges := []ftpChange{}
	if hist, err := h.db.AllFTPHistory(); err == nil {
		for i := len(hist) - 1; i >= 0; i-- {
			if hist[i].EffectiveFrom.After(since) {
				ftpChanges = append(ftpChanges, ftpChange{
					Date:     hist[i].EffectiveFrom.Format("2006-01-02"),
					FTPWatts: hist[i].FTPWatts,
				})
			}
		}
	}

	return jsonResult(map[string]any{
		"window_days":           days,
		"weeks":                 out,
		"ftp_changes_in_window": ftpChanges,
		"note":                  "EF = normalized power ÷ avg HR. Compare EF only across weeks with similar avg_if — rising EF at matched IF is real aerobic gain. Decoupling under 5% on long rides indicates durability.",
	})
}

// toolGoalProgress reports the rider's self-set weekly/yearly distance goals
// and progress toward them, in km. Sports without a goal are omitted.
func (h *CoachHandler) toolGoalProgress() (string, error) {
	goals, err := h.db.GetAllMileageGoals()
	if err != nil {
		return "", err
	}
	tz := h.db.AthleteLocation()
	type sportGoal struct {
		Sport    string   `json:"sport"`
		WeeklyKM float64  `json:"weekly_goal_km,omitempty"`
		YearlyKM float64  `json:"yearly_goal_km,omitempty"`
		WeekKM   float64  `json:"this_week_km"`
		YearKM   float64  `json:"year_to_date_km"`
		WeekPct  *float64 `json:"week_pct,omitempty"`
		YearPct  *float64 `json:"year_pct,omitempty"`
	}
	out := []sportGoal{}
	for _, sport := range []string{"cycling", "running", "swimming"} {
		g, ok := goals[sport]
		if !ok || (g.WeeklyMeters == 0 && g.YearlyMeters == 0) {
			continue
		}
		prog, err := h.db.GetMileageProgress(sport, tz)
		if err != nil {
			continue
		}
		sg := sportGoal{
			Sport:    sport,
			WeeklyKM: round1(g.WeeklyMeters / 1000),
			YearlyKM: round1(g.YearlyMeters / 1000),
			WeekKM:   round1(prog.WeekMeters / 1000),
			YearKM:   round1(prog.YearMeters / 1000),
		}
		if g.WeeklyMeters > 0 {
			p := round1(prog.WeekMeters / g.WeeklyMeters * 100)
			sg.WeekPct = &p
		}
		if g.YearlyMeters > 0 {
			p := round1(prog.YearMeters / g.YearlyMeters * 100)
			sg.YearPct = &p
		}
		out = append(out, sg)
	}
	if len(out) == 0 {
		return jsonResult(map[string]any{
			"goals": []any{},
			"note":  "The rider has not set any mileage goals — don't invent targets; plan from form and load instead.",
		})
	}
	return jsonResult(map[string]any{"goals": out})
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
