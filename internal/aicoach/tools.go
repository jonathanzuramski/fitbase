package aicoach

// Tool name constants. The api layer's executor switches on these, so the
// catalog and the executor can't drift on a typo.
const (
	ToolGetAthleteProfile   = "get_athlete_profile"
	ToolGetReadiness        = "get_readiness"
	ToolGetFitnessTrend     = "get_fitness_trend"
	ToolProposeSchedule     = "propose_schedule"
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
func numberProp(desc string) map[string]any {
	return map[string]any{"type": "number", "description": desc}
}
func arrayProp(items map[string]any, desc string) map[string]any {
	return map[string]any{"type": "array", "items": items, "description": desc}
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
			Description: "Deep detail for one workout: the summary metrics, full power/HR zone distribution, a separate Sweet Spot (88–94% FTP) time-in-band reading, variability index, efficiency factor, 90-day context for the same sport, and aerobic decoupling (Pw:HR drift) for rides long enough to compute it. SS overlaps Z3/Z4 — treat it as a parallel indicator, not an additional zone. Use when the rider asks about a specific session surfaced by list_recent_workouts.",
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
			Description: "Time spent in each power zone (7) and HR zone (5) over the last N days, plus a parallel Sweet Spot (88–94% FTP) total. SS overlaps Z3/Z4 — it is reported alongside, not as an 8th bucket, so 'did the rider do sweet spot work?' is answerable independently of how Z3/Z4 are split. Use for polarized-vs-threshold distribution and intensity-balance questions.",
			InputSchema: objSchema(map[string]any{
				"days": intProp("Look-back window in days, 1–365. Default 56 if omitted."),
			}),
		},
		{
			Name:        ToolProposeSchedule,
			Description: "Draft a structured training schedule for the rider to review. Pass one or more planned workouts (typically one per day for a week). The server stores them as a DRAFT and returns a preview id — the rider sees a preview card and clicks 'Add to calendar' to accept, or discards. Use after you've gathered enough context (readiness, recent load, power curve) to plan something specific; don't propose blindly. Use this when the rider asks for a plan/schedule — do not just describe the plan in prose, propose it through this tool so it can actually land on their calendar.",
			InputSchema: objSchema(map[string]any{
				"workouts": arrayProp(
					objSchema(map[string]any{
						"date":             stringProp("ISO date YYYY-MM-DD."),
						"sport":            stringProp("Defaults to 'cycling' if omitted."),
						"title":            stringProp("Short label, e.g. '2x20 sweet spot' or 'Endurance Z2'."),
						"description":      stringProp("Plain-English description of the session and its purpose."),
						"duration_secs":    intProp("Total target duration in seconds."),
						"intensity_factor": numberProp("Overall IF (e.g. 0.65 endurance, 0.85 threshold, 0.95 race). TSS is estimated as IF^2 * hours * 100 if tss is omitted."),
						"tss":              numberProp("Target TSS; estimated from IF + duration if omitted."),
						"intervals": arrayProp(objSchema(map[string]any{
							"kind":           stringProp("One of: warmup | work | recovery | cooldown | endurance | sweet_spot | threshold | vo2max."),
							"duration_secs":  intProp("Duration of this step in seconds."),
							"target_pct_ftp": intProp("0–200; takes precedence over target_zone if both are set."),
							"target_zone":    stringProp("Z1..Z7 when not specifying %FTP."),
							"note":           stringProp("Optional cue or prescription detail."),
							"repeats":        intProp("How many times to repeat this step back-to-back; default 1."),
						}, "kind", "duration_secs"), "Optional structured intervals — warmup, the work set, cooldown."),
					}, "date", "duration_secs"),
					"One or more planned workouts. Typically one entry per training day across a week.",
				),
			}, "workouts"),
		},
	}
}
