package aicoach

// Tool name constants. The api layer's executor switches on these, so the
// catalog and the executor can't drift on a typo.
const (
	ToolGetAthleteProfile   = "get_athlete_profile"
	ToolGetReadiness        = "get_readiness"
	ToolGetFitnessTrend     = "get_fitness_trend"
	ToolListRecentWorkouts  = "list_recent_workouts"
	ToolGetWorkoutDetail    = "get_workout_detail"
	ToolGetWeeklyBreakdown  = "get_weekly_breakdown"
	ToolGetPowerCurve       = "get_power_curve"
	ToolGetZoneDistribution = "get_zone_distribution"
)

// objSchema builds a JSON Schema object with the given properties. required
// lists property names the model must supply.
func objSchema(props map[string]any, required ...string) map[string]any {
	if props == nil {
		props = map[string]any{} // never marshal "properties": null — providers reject it
	}
	s := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		s["required"] = required
	} else {
		s["required"] = []string{}
	}
	return s
}

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func stringProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// CoachTools is the catalog the conversational coach may call. Specs only —
// execution lives in the api layer where the DB and data-shaping helpers are.
// Descriptions are written for the model: they say what the tool returns and
// when to reach for it.
func CoachTools() []ToolSpec {
	return []ToolSpec{
		{
			Name:        ToolGetAthleteProfile,
			Description: "The rider's profile: FTP (watts), weight (kg), threshold HR and max HR. Call this first when a question depends on FTP, W/kg, or HR zones.",
			InputSchema: objSchema(nil),
		},
		{
			Name:        ToolGetReadiness,
			Description: "Today's readiness snapshot: CTL (fitness), ATL (fatigue), TSB (form), days since last workout, 28-day CTL ramp rate, and a form-based recommendation. The right first call for 'how fresh am I', 'should I rest', or 'am I ready to race' questions.",
			InputSchema: objSchema(nil),
		},
		{
			Name:        ToolGetFitnessTrend,
			Description: "Daily CTL/ATL/TSB series over the last N days (sampled for long windows). Use to discuss how fitness or form has trended, ramp rate, or build/taper trajectory.",
			InputSchema: objSchema(map[string]any{
				"days": intProp("Look-back window in days, 7–365. Default 90 if omitted."),
			}),
		},
		{
			Name:        ToolListRecentWorkouts,
			Description: "Recent workouts newest-first with summary metrics (date, sport, duration, distance, elevation, avg/normalized power, avg HR, TSS, IF). Use to review training, find a session, or summarize a block. Each item has an 'id' — call get_workout_detail with it for zone distribution, EF/VI, and decoupling on a specific ride.",
			InputSchema: objSchema(map[string]any{
				"days":  intProp("Look-back window in days, 1–365. Default 56 if omitted."),
				"sport": stringProp("Optional sport filter, e.g. 'cycling' or 'running'. Omit for all sports."),
				"limit": intProp("Max workouts to return, 1–100. Default 40 if omitted."),
			}),
		},
		{
			Name:        ToolGetWorkoutDetail,
			Description: "Deep detail for one workout: the summary metrics, full power/HR zone distribution, variability index, efficiency factor, 90-day context for the same sport, and aerobic decoupling (Pw:HR drift) for rides long enough to compute it. Use when the rider asks about a specific session surfaced by list_recent_workouts.",
			InputSchema: objSchema(map[string]any{
				"id": stringProp("The workout id from list_recent_workouts."),
			}, "id"),
		},
		{
			Name:        ToolGetWeeklyBreakdown,
			Description: "Per-week training summary for the last N weeks: total TSS, duration, distance, elevation, workout count, and a load-type label (build/taper/recovery/etc). Use for weekly volume, consistency, and periodization questions.",
			InputSchema: objSchema(map[string]any{
				"weeks": intProp("Number of weeks back, 1–52. Default 8 if omitted."),
			}),
		},
		{
			Name:        ToolGetPowerCurve,
			Description: "All-time best power at key durations (5s, 30s, 1min, 5min, 20min, 60min) with watts, W/kg, and % of FTP. Use for sprint/threshold/endurance strengths and weaknesses.",
			InputSchema: objSchema(nil),
		},
		{
			Name:        ToolGetZoneDistribution,
			Description: "Time spent in each power zone (7) and HR zone (5) over the last N days. Use for polarized-vs-threshold distribution and intensity-balance questions.",
			InputSchema: objSchema(map[string]any{
				"days": intProp("Look-back window in days, 1–365. Default 56 if omitted."),
			}),
		},
	}
}
