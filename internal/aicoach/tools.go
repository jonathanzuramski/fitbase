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
	ToolGetFTPHistory       = "get_ftp_history"
	ToolGetPerformanceTrend = "get_performance_trend"
	ToolGetGoalProgress     = "get_goal_progress"
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

// enumProp constrains a string property to a fixed value set. Enums beat prose
// ("one of: a | b | c") because the provider enforces them at generation time.
func enumProp(desc string, values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values, "description": desc}
}

// CoachTools is the catalog the conversational coach may call. Specs only —
// execution lives in the api layer where the DB and data-shaping helpers are.
// Descriptions are written for the model: they say what the tool returns and
// when to reach for it.
func CoachTools() []ToolSpec {
	return []ToolSpec{
		{
			Name:        ToolGetAthleteProfile,
			Label:       "your profile",
			Description: "The rider's profile: FTP (watts), weight (kg), threshold HR and max HR. These numbers are normally already in your system prompt as the 'Rider profile' block — call this only if that block is missing or the rider asks you to verify their profile.",
			InputSchema: objSchema(nil),
		},
		{
			Name:        ToolGetReadiness,
			Label:       "your current form",
			Description: "Today's readiness snapshot: CTL (fitness), ATL (fatigue), TSB (form), days since last workout, 28-day CTL ramp rate, and a form-based recommendation. The right first call for 'how fresh am I', 'should I rest', or 'am I ready to race' questions.",
			InputSchema: objSchema(nil),
		},
		{
			Name:        ToolGetFitnessTrend,
			Label:       "your fitness trend",
			Description: "Daily CTL/ATL/TSB series over the last N days (sampled for long windows). Use to discuss how fitness or form has trended, ramp rate, or build/taper trajectory.",
			InputSchema: objSchema(map[string]any{
				"days": intProp("Look-back window in days, 7–365. Default 90 if omitted."),
			}),
		},
		{
			Name:        ToolListRecentWorkouts,
			Label:       "your recent workouts",
			Description: "Recent workouts newest-first with summary metrics (date, sport, duration, distance, elevation, avg/normalized power, avg HR, TSS, IF). Use to review training, find a session, or summarize a block. Each item has an 'id' — call get_workout_detail with it for zone distribution, EF/VI, and decoupling on a specific ride.",
			InputSchema: objSchema(map[string]any{
				"days":  intProp("Look-back window in days, 1–365. Default 56 if omitted."),
				"sport": stringProp("Optional sport filter, e.g. 'cycling' or 'running'. Omit for all sports."),
				"limit": intProp("Max workouts to return, 1–100. Default 40 if omitted."),
			}),
		},
		{
			Name:        ToolGetWorkoutDetail,
			Label:       "that workout",
			Description: "Deep detail for one workout: the summary metrics, full power/HR zone distribution, a separate Sweet Spot (88–94% FTP) time-in-band reading, variability index, efficiency factor, 90-day context for the same sport, and aerobic decoupling (Pw:HR drift) for rides long enough to compute it. SS overlaps Z3/Z4 — treat it as a parallel indicator, not an additional zone. Use when the rider asks about a specific session surfaced by list_recent_workouts.",
			InputSchema: objSchema(map[string]any{
				"id": stringProp("The workout id from list_recent_workouts."),
			}, "id"),
		},
		{
			Name:        ToolGetWeeklyBreakdown,
			Label:       "your weekly load",
			Description: "Per-week training summary for the last N weeks: total TSS, duration, distance, elevation, workout count, and a load-type label (build/taper/recovery/etc). Use for weekly volume, consistency, and periodization questions.",
			InputSchema: objSchema(map[string]any{
				"weeks": intProp("Number of weeks back, 1–52. Default 8 if omitted."),
			}),
		},
		{
			Name:        ToolGetPowerCurve,
			Label:       "your power curve",
			Description: "Best power at key durations (5s, 30s, 1min, 5min, 20min, 60min) with watts, W/kg, and % of FTP. Without arguments returns all-time bests — use for overall sprint/threshold/endurance strengths. Pass days to also get the best within that recent window compared against all-time (recent_pct_of_all_time) — use this when judging current form or whether the rider is actually getting stronger, since an all-time PR from last season can hide present detraining.",
			InputSchema: objSchema(map[string]any{
				"days": intProp("Optional recent window in days, 7–365 (42–90 is typical for current form). Omit for all-time only."),
			}),
		},
		{
			Name:        ToolGetFTPHistory,
			Label:       "your FTP history",
			Description: "The rider's FTP setting over time: every recorded FTP change with its effective date and the delta from the previous value. The most direct record of performance progression — call it when discussing whether the rider is getting faster, how training translated into results, or when a question spans a period where FTP may have changed (older workouts' %FTP and TSS were computed against the FTP of their day).",
			InputSchema: objSchema(nil),
		},
		{
			Name:        ToolGetPerformanceTrend,
			Label:       "your performance trend",
			Description: "Week-by-week performance signals over the last N days: rides with power, average IF, average EF (normalized power ÷ avg HR — the primary aerobic-fitness marker), and average aerobic decoupling for long rides, plus any FTP changes inside the window. Use this to answer 'am I getting fitter' questions properly: rising EF at similar IF, falling decoupling, or an FTP bump are real fitness gains; rising CTL alone is just more load. Compare EF only between weeks with similar average IF.",
			InputSchema: objSchema(map[string]any{
				"days": intProp("Look-back window in days, 14–365. Default 90 if omitted."),
			}),
		},
		{
			Name:        ToolGetGoalProgress,
			Label:       "your goal progress",
			Description: "The rider's self-set weekly and yearly distance goals per sport, with current progress (this week's total and daily split, year-to-date total). Call before proposing a schedule when volume matters, and when the rider asks about goals, targets, or whether they're on pace — plans should respect the targets the rider set for themselves.",
			InputSchema: objSchema(nil),
		},
		{
			Name:        ToolGetZoneDistribution,
			Label:       "your zone distribution",
			Description: "Time spent in each power zone (7) and HR zone (5) over the last N days, plus a parallel Sweet Spot (88–94% FTP) total. SS overlaps Z3/Z4 — it is reported alongside, not as an 8th bucket, so 'did the rider do sweet spot work?' is answerable independently of how Z3/Z4 are split. Use for polarized-vs-threshold distribution and intensity-balance questions.",
			InputSchema: objSchema(map[string]any{
				"days": intProp("Look-back window in days, 1–365. Default 56 if omitted."),
			}),
		},
		{
			Name:        ToolProposeSchedule,
			Label:       "a schedule draft",
			Description: "Draft a training schedule for the rider to review — call this whenever the rider asks for a plan, a week, or 'what should I do next'; a plan described only in prose never reaches their calendar. The server stores the batch as a DRAFT and returns a preview id; the rider sees a preview card and clicks 'Add to calendar' to accept, or discards. Pass one entry per training day (max 21) with dates today or later; skip rest days rather than sending empty workouts. Every workout needs a title — it's the session's name on the rider's preview card and calendar. Gather context first (readiness and weekly load at minimum) so the plan fits current form. Give key sessions structured intervals — warmup, work set with recoveries, cooldown — and use a repeat group (steps + repeat) for patterns like 4x(8min sweet spot + 3min recovery).",
			InputSchema: objSchema(map[string]any{
				"workouts": arrayProp(
					objSchema(map[string]any{
						"date":             stringProp("ISO date YYYY-MM-DD. Must be today or later — never schedule into the past."),
						"sport":            stringProp("Defaults to 'cycling' if omitted."),
						"title":            stringProp("Required. The workout's name as shown to the rider on the preview card and calendar — a missing title renders as '(untitled)'. Keep it a short session label, e.g. '4x8 sweet spot', '2x20 threshold', 'Endurance Z2'."),
						"description":      stringProp("Plain-English description of the session and its purpose."),
						"duration_secs":    intProp("Total target duration in seconds. When intervals are given, their summed duration is used instead."),
						"intensity_factor": numberProp("Overall IF (e.g. 0.65 endurance, 0.85 threshold, 0.95 race). TSS is estimated as IF^2 * hours * 100 if tss is omitted."),
						"tss":              numberProp("Target TSS; estimated from IF + duration if omitted."),
						"intervals": arrayProp(objSchema(map[string]any{
							"kind":                 enumProp("Step type. Required on a plain step; omit on a repeat group.", "warmup", "work", "recovery", "cooldown", "endurance", "sweet_spot", "threshold", "vo2max"),
							"duration_secs":        intProp("Duration of this step in seconds. Required on a plain step; omit on a repeat group (the group's length comes from its steps)."),
							"target_pct_ftp":       intProp("Flat power target as %FTP, 0-200. Set at most one of target_pct_ftp, target_watts, target_zone, or a ramp."),
							"target_pct_ftp_start": intProp("Ramp start %FTP — sweeps power linearly to target_pct_ftp_end across the step (good for warmups/cooldowns on smart trainers). Both ramp ends required together; mutually exclusive with a flat target."),
							"target_pct_ftp_end":   intProp("Ramp end %FTP. See target_pct_ftp_start."),
							"target_watts":         intProp("Flat power target in absolute watts, as an alternative to %FTP."),
							"target_zone":          enumProp("Flat target as a power zone, as an alternative to %FTP/watts.", "Z1", "Z2", "Z3", "Z4", "Z5", "Z6", "Z7"),
							"note":                 stringProp("Optional cue or prescription detail, e.g. 'high cadence' or 'ride to feel'."),
							"repeat":               intProp("Repeat count. On a plain step: repeat it back-to-back. On a repeat group: run the whole steps sequence this many times. 0 or 1 means once."),
							"steps": arrayProp(objSchema(map[string]any{
								"kind":           enumProp("Step type.", "warmup", "work", "recovery", "cooldown", "endurance", "sweet_spot", "threshold", "vo2max"),
								"duration_secs":  intProp("Duration of this step in seconds."),
								"target_pct_ftp": intProp("Flat power target as %FTP, 0-200."),
								"target_watts":   intProp("Flat power target in absolute watts."),
								"target_zone":    enumProp("Flat target as a power zone.", "Z1", "Z2", "Z3", "Z4", "Z5", "Z6", "Z7"),
								"note":           stringProp("Optional cue."),
							}, "kind", "duration_secs"), "Makes this entry a repeat group: these steps run in order, `repeat` times. Use for work/recovery patterns, e.g. 4x(8min sweet_spot + 3min recovery). A group must not set kind, duration_secs, or a target of its own."),
						}), "Optional structured intervals in ride order — typically a warmup, the work (often a repeat group), and a cooldown. A step with no target means ride to feel."),
					}, "date", "title", "duration_secs"),
					"One or more planned workouts, one entry per training day. Max 21.",
				),
			}, "workouts"),
		},
	}
}
