package aicoach

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// WeeklyPlanDays is the length of the window a weekly plan covers.
const WeeklyPlanDays = 7

// WeeklyPlanWindow returns the first and last day a weekly plan covers: the
// seven days starting tomorrow in the rider's timezone. Starting tomorrow
// (rather than next Monday) means the button is useful on any day of the week
// and never leaves a gap between today and the plan.
func WeeklyPlanWindow(now time.Time) (start, end time.Time) {
	y, m, d := now.Date()
	start = time.Date(y, m, d+1, 0, 0, 0, 0, now.Location())
	end = start.AddDate(0, 0, WeeklyPlanDays-1)
	return start, end
}

// WeeklyPlanBrief is the playbook for one weekly review-and-plan run (the
// calendar's "plan my week" button, its revisions, and the Sunday-night
// auto-plan). It is appended to the per-request system context. It names the
// exact window so the model never has to infer "next week" from a weekday, and
// spells out the data to gather — in particular grading last week's key
// sessions with get_workout_intervals, because progressing a plan from
// whole-ride averages alone is exactly the guesswork it replaces.
func WeeklyPlanBrief(start, end time.Time) string {
	return fmt.Sprintf(`Weekly plan request — review how the rider's training has been going, then plan %s through %s (%d days, starting tomorrow). The rider may not be watching (this can run automatically on Sunday night); their message holds any constraints they gave.

Gather first — request independent tools together in one round:
- get_readiness, get_weekly_breakdown (weeks: 6), get_fitness_trend (days: 42), get_performance_trend (days: 90), get_power_curve (days: 42), get_goal_progress.
- list_planned_workouts — it includes the past 7 days, so you can compare what was planned with what was done, and it shows manual entries in the new window that the plan must work around.
- list_recent_workouts (days: 42). Its structure line shows which rides were interval sessions — use that, not IF, to pick the key sessions. Then call get_workout_intervals on every key session from the last 7–10 days. If that window has fewer than two (a break, illness, travel), also take the two or three most recent structured sessions before it, so the plan progresses from what the rider last proved they can do. This is how you know whether they hit their targets — never grade intervals from IF or average power.

Then write, in Markdown:
## How the last week went
Volume (TSS, hours, rides) against the previous weeks and against what was planned. Grade each key session on execution — reps completed (a short last rep usually means they stopped early), power vs target, last_vs_first_rep_pct, HR creep — and name specific sessions and numbers. If the last week was thin, say so and grade the most recent structured sessions you pulled instead, with their dates.
## Progress
What is improving and what is stalling, from performance signals (FTP history, recent-vs-all-time power, EF at matched IF, decoupling) — not from CTL alone. Be honest about plateaus.
## Focus for the week
One or two priorities and why, tied to form (TSB), ramp rate, and what is underdeveloped.

Then call propose_schedule for the window — one entry per training day, rest days skipped, dates %s to %s only. Build the key sessions from what the rider actually executed:
- Reps completed fading less than ~3%% (last_vs_first_rep_pct above -3) and HR settling between reps → progress one step: one more rep, +2–3%% target, or longer reps — not all three.
- Fading more than ~5%%, missed reps, or a session cut short → repeat the same session, or cut a rep.
- Deeply negative TSB (below about -25) or a ramp rate above ~8 CTL/week → make it a recovery or consolidation week regardless.
- After a break of a week or more, restart a step below the last proven session (fewer reps or a few %% lower), not at it.
Name sessions and step kinds by their targets: tempo 76–87%% FTP, sweet spot 88–94%%, threshold 95–105%%, VO2 106–120%%. Don't call an 85%% session sweet spot.
Respect the rider's mileage goals when set, and any constraints they mention in their message or earlier in the conversation.

Finish with a short paragraph summarizing the week and one thing to watch. The rider sees the proposed week directly under your review, with buttons to add it to their calendar or ask for changes — refer to it as "the proposed week below", not a preview card. Keep the prose to roughly 250–400 words — the schedule itself goes in the tool call, not in prose.`,
		start.Format("Monday, 2006-01-02"), end.Format("Monday, 2006-01-02"), WeeklyPlanDays,
		start.Format("2006-01-02"), end.Format("2006-01-02"))
}

// WeeklyPlanAvailability renders the rider's standing availability for the
// plan window, appended after WeeklyPlanBrief. Days are listed as dates, not
// weekday names, so the model never has to map "Wednesday" onto the window.
// weeklyHours <= 0 means no budget: the coach chooses the volume.
// propose_schedule enforces both, so this text is guidance, not the guard.
func WeeklyPlanAvailability(start, end time.Time, allowed func(time.Weekday) bool, weeklyHours float64) string {
	var open, closed []string
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		label := d.Format("Mon 2006-01-02")
		if allowed(d.Weekday()) {
			open = append(open, label)
		} else {
			closed = append(closed, label)
		}
	}
	var b strings.Builder
	b.WriteString("Rider availability — set by the rider on the calendar page. propose_schedule rejects a plan that breaks it.\n")
	if len(closed) == 0 {
		b.WriteString("- Training days: any day in the window.\n")
	} else {
		fmt.Fprintf(&b, "- Training days: %s. Schedule nothing on %s — those are rest days.\n",
			strings.Join(open, ", "), strings.Join(closed, ", "))
	}
	if weeklyHours > 0 {
		fmt.Fprintf(&b, "- Weekly time: about %s hours of training in total. Plan up to that, not over it — a lighter week is right when form or a comeback calls for it. Mention the total you planned.",
			strconv.FormatFloat(weeklyHours, 'f', -1, 64))
	} else {
		b.WriteString("- Weekly time: not set — choose the volume from their recent weeks and current form, and state the total hours you planned.")
	}
	return b.String()
}
