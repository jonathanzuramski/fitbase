package aicoach_test

import (
	"strings"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/aicoach"
)

// The window is the seven days starting tomorrow in the rider's own timezone:
// late on Sunday evening in Denver is already Monday in UTC, and planning from
// the UTC date would silently drop the rider's Monday.
func TestWeeklyPlanWindow_StartsTomorrowInRiderTimezone(t *testing.T) {
	denver, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	now := time.Date(2026, 11, 8, 22, 30, 0, 0, denver) // Sunday 22:30 MST = Monday 05:30 UTC
	start, end := aicoach.WeeklyPlanWindow(now)
	if got := start.Format("2006-01-02 Mon"); got != "2026-11-09 Mon" {
		t.Errorf("start = %s, want 2026-11-09 Mon", got)
	}
	if got := end.Format("2006-01-02 Mon"); got != "2026-11-15 Sun" {
		t.Errorf("end = %s, want 2026-11-15 Sun", got)
	}
}

func TestWeeklyPlanWindow_CrossesMonthEnd(t *testing.T) {
	start, end := aicoach.WeeklyPlanWindow(time.Date(2026, 12, 29, 9, 0, 0, 0, time.UTC))
	if start.Format("2006-01-02") != "2026-12-30" || end.Format("2006-01-02") != "2027-01-05" {
		t.Errorf("window = %s..%s, want 2026-12-30..2027-01-05", start.Format("2006-01-02"), end.Format("2006-01-02"))
	}
}

// The brief must pin the exact dates (propose_schedule rejects dates outside
// the window, so a model that inferred the wrong week would burn rounds
// re-planning) and must send the model to
// get_workout_intervals — grading sessions from averages is the failure mode
// this feature exists to avoid.
func TestWeeklyPlanBrief_PinsWindowAndIntervalGrading(t *testing.T) {
	start, end := aicoach.WeeklyPlanWindow(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	brief := aicoach.WeeklyPlanBrief(start, end)

	for _, want := range []string{
		"2026-11-06 to 2026-11-12",
		"Friday, 2026-11-06",
		aicoach.ToolGetWorkoutIntervals,
		aicoach.ToolProposeSchedule,
		aicoach.ToolListPlannedWorkouts,
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("brief is missing %q", want)
		}
	}
	if strings.Contains(brief, "%!") {
		t.Errorf("brief has a formatting error:\n%s", brief)
	}
}

// A revision changes the rest of the proposed week. Its brief pins that
// window, which starts today when the revision comes mid-week, and asks for a
// change note, never a second review: the server keeps the plan's review and
// adds the note under it.
func TestWeeklyPlanRevisionBrief(t *testing.T) {
	start := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	brief := aicoach.WeeklyPlanRevisionBrief(start, start.AddDate(0, 0, 4))
	for _, want := range []string{
		"dates 2026-11-11 to 2026-11-15 only",
		"(5 days)",
		"(today)",
		"Don't write the review again",
		"exactly as listed: same date, title, description",
		"Planning rules",
		aicoach.ToolProposeSchedule,
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("revision brief is missing %q:\n%s", want, brief)
		}
	}
	if strings.Contains(brief, "%!") || strings.Contains(brief, "## How the last week went") {
		t.Errorf("revision brief is malformed or asks for the review:\n%s", brief)
	}
}

// planningRules is every planning rule the model sees on a weekly run: the
// static chat prompt plus the per-run brief. Rule tests assert on both
// together so they hold wherever a rule lives.
func planningRules() string {
	start, end := aicoach.WeeklyPlanWindow(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	return aicoach.ChatSystemPrompt + aicoach.WeeklyPlanBrief(start, end)
}

// get_readiness states the ramp per week (ctl_ramp_per_week). The old rule
// read a bare 28-day ramp_rate against "~8 CTL/week", so a steady 3/week
// build (12 over 28 days) forced a recovery week.
func TestPlanningRules_RampIsPerWeek(t *testing.T) {
	rules := planningRules()
	if !strings.Contains(rules, "ctl_ramp_per_week") {
		t.Error("planning rules don't name ctl_ramp_per_week")
	}
	if strings.Contains(rules, "ramp rate above ~8 CTL/week") {
		t.Error("planning rules still judge a bare ramp rate against a per-week line")
	}
	for _, tool := range aicoach.CoachTools() {
		if tool.Name == aicoach.ToolGetReadiness && !strings.Contains(tool.Description, "per week") {
			t.Errorf("get_readiness description doesn't say the ramp is per week: %s", tool.Description)
		}
	}
}

// The rider's rpe and notes reach the coach; the rules must use them to
// change the plan, not just mention them in the review.
func TestPlanningRules_WeighRPEAndNotes(t *testing.T) {
	rules := planningRules()
	for _, want := range []string{"rpe 9–10", "never assume one", "illness", "rpe / rider_note"} {
		if !strings.Contains(rules, want) {
			t.Errorf("planning rules lack %q", want)
		}
	}
	start, end := aicoach.WeeklyPlanWindow(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	brief := aicoach.WeeklyPlanBrief(start, end)
	if strings.Contains(brief, "%!") || strings.Contains(brief, "%%") {
		t.Errorf("brief has a formatting error:\n%s", brief)
	}
}

// Both coaches that read rider notes say what to do about cardiac-type
// symptoms: the no-medical-advice rule alone left the model to keep
// prescribing intensity.
func TestPrompts_RedFlagSymptoms(t *testing.T) {
	for name, prompt := range map[string]string{"chat": aicoach.ChatSystemPrompt, "insights": aicoach.SystemPrompt} {
		if !strings.Contains(prompt, "chest pain") || !strings.Contains(prompt, "doctor") {
			t.Errorf("%s prompt has no red-flag symptom rule", name)
		}
	}
}

// Every planning rule is in %FTP: an FTP no recent ride backs must change
// how the week is built, in the weekly plan and in insights alike.
func TestPlanningRules_FTPCheck(t *testing.T) {
	rules := planningRules()
	for _, want := range []string{"FTP check", "target_watts"} {
		if !strings.Contains(rules, want) {
			t.Errorf("planning rules lack %q", want)
		}
	}
	if !strings.Contains(aicoach.SystemPrompt, "ftp_check") {
		t.Error("the insights prompt doesn't use ftp_check")
	}
	start, end := aicoach.WeeklyPlanWindow(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	if brief := aicoach.WeeklyPlanBrief(start, end); strings.Contains(brief, "%!") {
		t.Errorf("brief has a formatting error:\n%s", brief)
	}
}

// The rules for building a plan live in the static chat prompt, so chat plans
// by the same rules as the weekly run and they sit in the cached prefix; the
// per-run brief only points at them.
func TestChatSystemPrompt_CarriesPlanningRules(t *testing.T) {
	for _, want := range []string{"last_vs_first_rep_pct", "consecutive days", "sweet spot 88–94%", "ctl_ramp_per_week", "rpe 9–10"} {
		if !strings.Contains(aicoach.ChatSystemPrompt, want) {
			t.Errorf("chat prompt lacks the planning rule %q", want)
		}
	}
	if strings.Contains(aicoach.ChatSystemPrompt, "%%") {
		t.Error("chat prompt is a raw string, not a format: it must not escape %")
	}
	start, end := aicoach.WeeklyPlanWindow(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	brief := aicoach.WeeklyPlanBrief(start, end)
	if strings.Contains(brief, "consecutive days") {
		t.Error("the weekly brief still carries the rules: they belong in the cached chat prompt")
	}
	if !strings.Contains(brief, "Planning rules") || strings.Contains(brief, "%!") {
		t.Errorf("brief doesn't point at the planning rules, or is malformed:\n%s", brief)
	}
}

// The planning rules serve chat as well as the weekly plan. Chat has no
// review for a rule to point at, and the rules that read rpe and rider_note
// need chat's planning gather to fetch the rides that carry them.
func TestChatSystemPrompt_PlanningWorksInChat(t *testing.T) {
	p := aicoach.ChatSystemPrompt
	i := strings.Index(p, "Planning —")
	if i < 0 {
		t.Fatal("chat prompt has no planning section")
	}
	planning := p[i:]
	if strings.Contains(planning, "in the review") {
		t.Error("a planning rule points at a review, which chat doesn't write")
	}
	var gather string
	for _, line := range strings.Split(planning, "\n") {
		if strings.HasPrefix(line, "- Gather context first") {
			gather = line
		}
	}
	if !strings.Contains(gather, aicoach.ToolListRecentWorkouts) || !strings.Contains(gather, "at minimum") ||
		strings.Index(gather, aicoach.ToolListRecentWorkouts) > strings.Index(gather, "at minimum") {
		t.Errorf("chat's planning gather doesn't require %s, which carries rpe and rider_note:\n%s",
			aicoach.ToolListRecentWorkouts, gather)
	}
}

func TestStandingAvailability(t *testing.T) {
	monWedSat := func(wd time.Weekday) bool {
		return wd == time.Monday || wd == time.Wednesday || wd == time.Saturday
	}
	got := aicoach.StandingAvailability(monWedSat, 6)
	for _, want := range []string{"Mon, Wed, Sat", "about 6 hours"} {
		if !strings.Contains(got, want) {
			t.Errorf("availability lacks %q: %s", want, got)
		}
	}
	everyDay := func(time.Weekday) bool { return true }
	if got := aicoach.StandingAvailability(everyDay, 0); got != "" {
		t.Errorf("defaults (every day, no budget) = %q, want empty", got)
	}
	if got := aicoach.StandingAvailability(everyDay, 8); !strings.Contains(got, "any day") || !strings.Contains(got, "about 8 hours") {
		t.Errorf("every day with a budget = %q", got)
	}
}

func TestWeeklyPlanAvailability(t *testing.T) {
	start := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC) // Monday
	end := start.AddDate(0, 0, 6)
	noWedFri := func(wd time.Weekday) bool { return wd != time.Wednesday && wd != time.Friday }

	got := aicoach.WeeklyPlanAvailability(start, end, noWedFri, 8)
	for _, want := range []string{"Schedule nothing on Wed 2026-11-11, Fri 2026-11-13", "Mon 2026-11-09", "about 8 hours"} {
		if !strings.Contains(got, want) {
			t.Errorf("availability text missing %q:\n%s", want, got)
		}
	}

	open := aicoach.WeeklyPlanAvailability(start, end, func(time.Weekday) bool { return true }, 0)
	if !strings.Contains(open, "any day") || !strings.Contains(open, "not set") {
		t.Errorf("open availability text:\n%s", open)
	}
}
