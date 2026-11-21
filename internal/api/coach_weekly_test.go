package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fitbase/fitbase/internal/aicoach"
	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/models"
)

// scriptedCoach is a fake chat provider: each test sets turn to script the
// model, and every request it receives is recorded.
type scriptedCoach struct {
	mu   sync.Mutex
	seen []aicoach.ChatTurnInput
	turn func(in aicoach.ChatTurnInput) (*aicoach.ChatTurnOutput, error)
}

func (*scriptedCoach) Name() string  { return "scripted-coach" }
func (*scriptedCoach) Label() string { return "Scripted" }
func (*scriptedCoach) Models() []aicoach.ModelOption {
	return []aicoach.ModelOption{{Value: "m", Label: "m"}}
}
func (*scriptedCoach) Stream(context.Context, aicoach.CallConfig, func(string) error) error {
	return nil
}
func (p *scriptedCoach) ChatTurn(_ context.Context, in aicoach.ChatTurnInput) (*aicoach.ChatTurnOutput, error) {
	p.mu.Lock()
	p.seen = append(p.seen, in)
	turn := p.turn
	p.mu.Unlock()
	if turn == nil {
		return &aicoach.ChatTurnOutput{Text: "ok"}, nil
	}
	return turn(in)
}

func (p *scriptedCoach) requests() []aicoach.ChatTurnInput {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]aicoach.ChatTurnInput(nil), p.seen...)
}

var fakeCoach = &scriptedCoach{}

func init() { aicoach.Register(fakeCoach) }

func coachFixture(t *testing.T) *CoachHandler {
	t.Helper()
	fakeCoach.mu.Lock()
	fakeCoach.seen, fakeCoach.turn = nil, nil
	fakeCoach.mu.Unlock()
	h := zoneDistFixture(t, &models.Athlete{FTPWatts: 250, WeightKG: 75})
	if err := h.db.SaveAISettings(db.AISettings{Provider: "scripted-coach", Model: "m", APIKey: "k"}); err != nil {
		t.Fatalf("SaveAISettings: %v", err)
	}
	return h
}

// firstPlanDay is the first day of a weekly plan generated now, the way the
// handler computes it: tomorrow in the athlete's timezone, which is UTC for
// the fixture (no timezone set). Using the machine's local "tomorrow" instead
// fails every evening west of UTC, when UTC has already rolled over.
func firstPlanDay() time.Time {
	start, _ := aicoach.WeeklyPlanWindow(time.Now().UTC())
	return start
}

// proposingWeek scripts the weekly playbook's shape: the model writes its
// review and calls propose_schedule in one turn, then closes in the next.
func proposingWeek(review, closing string) func(in aicoach.ChatTurnInput) (*aicoach.ChatTurnOutput, error) {
	return func(in aicoach.ChatTurnInput) (*aicoach.ChatTurnOutput, error) {
		if len(in.Rounds) > 0 {
			return &aicoach.ChatTurnOutput{Text: closing}, nil
		}
		tomorrow := firstPlanDay().Format("2006-01-02")
		args := `{"workouts":[{"date":"` + tomorrow + `","title":"3x10 tempo","duration_secs":3900,
			"intervals":[{"repeat":3,"steps":[{"kind":"work","duration_secs":600,"target_pct_ftp":86},
			{"kind":"recovery","duration_secs":300,"target_pct_ftp":50}]}]}]}`
		return &aicoach.ChatTurnOutput{
			Text:  review,
			Calls: []aicoach.ToolExchange{{ID: "p1", Name: aicoach.ToolProposeSchedule, Input: json.RawMessage(args)}},
		}, nil
	}
}

func discardEvents(string, any) {}

// The weekly playbook reaches the model; the proposed week is captured into
// the plan — not into the single-slot draft table, where a Sunday-night run
// would wipe out a preview card the rider has open in chat; and the stored
// review is everything the rider saw, review plus closing.
func TestGenerateWeeklyPlan_CapturesWeekWithoutTouchingDrafts(t *testing.T) {
	h := coachFixture(t)
	openDraft, err := h.db.SavePlannedDraft(`{"workouts":[]}`)
	if err != nil {
		t.Fatalf("SavePlannedDraft: %v", err)
	}
	fakeCoach.turn = proposingWeek("## How the last week went\nTwo clean reps.", "The proposed week is below.")

	plan, err := h.generateWeeklyPlan(context.Background(), weeklyPlanRun{trigger: models.WeeklyPlanScheduled}, discardEvents)
	if err != nil {
		t.Fatalf("generateWeeklyPlan: %v", err)
	}
	if plan.Status != models.WeeklyPlanReady || plan.Trigger != models.WeeklyPlanScheduled {
		t.Errorf("plan = %s/%s, want ready/scheduled", plan.Status, plan.Trigger)
	}
	if !strings.Contains(plan.ReviewMD, "How the last week went") || !strings.Contains(plan.ReviewMD, "proposed week is below") {
		t.Errorf("review = %q, want the review and the closing paragraph", plan.ReviewMD)
	}
	ws, err := planWorkouts(&plan)
	if err != nil || len(ws) != 1 || ws[0].Title != "3x10 tempo" || ws[0].Source != "coach" {
		t.Fatalf("proposed workouts = %+v (err %v), want the captured 3x10 tempo", ws, err)
	}
	if payload, _ := h.db.GetPlannedDraft(openDraft); payload == "" {
		t.Error("the chat's open draft was swept by a weekly-plan run")
	}

	// The playbook is per-request context: it rides in SystemContext, never
	// in the static (cached) System prompt.
	first := fakeCoach.requests()[0]
	if !strings.Contains(first.SystemContext, "Weekly plan request") || !strings.Contains(first.SystemContext, plan.WindowStart) {
		t.Errorf("system context lacks the playbook for window %s:\n%s", plan.WindowStart, first.SystemContext)
	}
	if strings.Contains(first.System, "Weekly plan request") {
		t.Error("weekly playbook leaked into the static system prompt — it would split the prompt cache by mode")
	}
}

// Plain chat must not carry the weekly playbook.
func TestChat_HasNoWeeklyPlaybook(t *testing.T) {
	h := coachFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/api/coach/chat",
		strings.NewReader(`{"messages":[{"role":"user","content":"How fresh am I?"}]}`))
	rr := httptest.NewRecorder()
	h.Chat(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "event: done") {
		t.Fatalf("chat failed: %d %s", rr.Code, rr.Body.String())
	}
	if reqs := fakeCoach.requests(); len(reqs) == 0 || strings.Contains(reqs[0].System+reqs[0].SystemContext, "Weekly plan request") {
		t.Error("a plain chat question carried the weekly playbook")
	}
}

// postChat sends one plain chat question and waits for the turn to finish.
func postChat(t *testing.T, h *CoachHandler) aicoach.ChatTurnInput {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/coach/chat",
		strings.NewReader(`{"messages":[{"role":"user","content":"Plan my next few days"}]}`))
	rr := httptest.NewRecorder()
	h.Chat(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "event: done") {
		t.Fatalf("chat failed: %d %s", rr.Code, rr.Body.String())
	}
	reqs := fakeCoach.requests()
	if len(reqs) == 0 {
		t.Fatal("the chat never reached the model")
	}
	return reqs[0]
}

// A plan made in chat sees the training days and hours the rider saved, as
// a weekly run does.
func TestChat_CarriesStandingAvailability(t *testing.T) {
	h := coachFixture(t)
	six := 6.0
	if err := h.db.SaveWeeklyPlanPrefs(models.WeeklyPlanPrefs{Days: []string{"mon", "wed", "sat"}, WeeklyHours: &six}); err != nil {
		t.Fatal(err)
	}
	ctx := postChat(t, h).SystemContext
	if !strings.Contains(ctx, "Mon, Wed, Sat") || !strings.Contains(ctx, "about 6 hours") {
		t.Errorf("chat context lacks the rider's availability:\n%s", ctx)
	}
}

func TestChat_NoAvailabilityWithDefaults(t *testing.T) {
	h := coachFixture(t)
	if ctx := postChat(t, h).SystemContext; strings.Contains(ctx, "standing availability") {
		t.Errorf("default availability (every day, no budget) still stated:\n%s", ctx)
	}
}

// A weekly run states availability by date and enforces it; the chat line,
// by weekday and advisory, must not ride along.
func TestGenerateWeeklyPlan_AvailabilityByDateOnly(t *testing.T) {
	h := coachFixture(t)
	six := 6.0
	if err := h.db.SaveWeeklyPlanPrefs(models.WeeklyPlanPrefs{Days: append([]string(nil), models.WeekdayKeys...), WeeklyHours: &six}); err != nil {
		t.Fatal(err)
	}
	fakeCoach.turn = proposingWeek("Review.", "Done.")
	if _, err := h.generateWeeklyPlan(context.Background(), weeklyPlanRun{trigger: models.WeeklyPlanManual}, discardEvents); err != nil {
		t.Fatalf("generate: %v", err)
	}
	ctx := fakeCoach.requests()[0].SystemContext
	if strings.Contains(ctx, "standing availability") || !strings.Contains(ctx, "Rider availability —") {
		t.Errorf("weekly context should state availability by date only:\n%s", ctx)
	}
}

// A revision replays the plan it revises and accumulates the rider's
// constraints, so "no riding Wednesday" isn't forgotten by the next change.
func TestGenerateWeeklyPlan_RevisionReplaysPlan(t *testing.T) {
	h := coachFixture(t)
	fakeCoach.turn = proposingWeek("Review one.", "Done.")
	first, err := h.generateWeeklyPlan(context.Background(),
		weeklyPlanRun{trigger: models.WeeklyPlanManual, notes: "no riding Wednesday"}, discardEvents)
	if err != nil {
		t.Fatalf("first plan: %v", err)
	}

	fakeCoach.mu.Lock()
	fakeCoach.seen = nil
	fakeCoach.mu.Unlock()
	fakeCoach.turn = proposingWeek("Moved the long ride to Sunday.", "Nothing else changed.")
	second, err := h.generateWeeklyPlan(context.Background(),
		weeklyPlanRun{trigger: models.WeeklyPlanManual, notes: "move the long ride to Sunday", revise: &first}, discardEvents)
	if err != nil {
		t.Fatalf("revision: %v", err)
	}

	req := fakeCoach.requests()[0]
	hist := req.History
	if len(hist) != 3 || hist[0].Content != first.Request || hist[1].Content != first.ReviewMD ||
		!strings.Contains(hist[2].Content, "move the long ride to Sunday") {
		t.Fatalf("revision history = %+v, want [first request, first review, the change]", hist)
	}
	// The change carries the week it changes: the review never lists the
	// workouts, so without them the coach rebuilt the week from scratch.
	for _, want := range []string{
		firstPlanDay().Format("Mon 2006-01-02"),
		"3x10 tempo",
		"45 min", // toModel sums the intervals: 3×(10+5) min
		"3×(work 10:00 @ 86% + recovery 5:00 @ 50%)",
	} {
		if !strings.Contains(hist[2].Content, want) {
			t.Errorf("revision message lacks %q:\n%s", want, hist[2].Content)
		}
	}
	// The instructions agree with the user turn: change only what was asked,
	// and no second review (the weekly brief's full gather and three sections).
	if !strings.Contains(req.SystemContext, "Weekly plan revision") || strings.Contains(req.SystemContext, "Weekly plan request") ||
		!strings.Contains(req.SystemContext, "Copy every session the change doesn't touch exactly as listed") {
		t.Errorf("revision system context should carry the revision brief only:\n%s", req.SystemContext)
	}
	// The static prompt's Planning section, which weekly plans follow too,
	// gathers recent rides "at minimum" and starts tomorrow; the revision
	// context settles that its own gather and window win. Revised the day it
	// was made, the week still starts tomorrow, so today isn't in it.
	if !strings.Contains(req.System, "Gather context first") ||
		!strings.Contains(req.SystemContext, `- "Gather context first": the week was built on that gather`) ||
		!strings.Contains(req.SystemContext, `- "start tomorrow unless the rider names a start date": the plan covers what is left of the week, `+
			first.WindowStart+" to "+first.WindowEnd+".") {
		t.Errorf("revision context doesn't replace the Planning section's gather and start date:\n%s", req.SystemContext)
	}
	if !strings.Contains(second.Request, "no riding Wednesday") || !strings.Contains(second.Request, "long ride to Sunday") {
		t.Errorf("revised request = %q, want both constraints", second.Request)
	}
	if strings.Contains(second.Request, "3×(") {
		t.Errorf("revised request = %q — the replayed week must not accumulate into Request", second.Request)
	}
	// The stored review is the one revised, with the change note under it:
	// the calendar shows it, and the next revision replays it.
	if want := first.ReviewMD + "\n\n**Revised:** Moved the long ride to Sunday.\n\nNothing else changed."; second.ReviewMD != want {
		t.Errorf("revised review = %q, want %q", second.ReviewMD, want)
	}
	if second.WindowStart != first.WindowStart || second.WindowEnd != first.WindowEnd {
		t.Errorf("revised window = %s..%s, want the week revised (%s..%s)",
			second.WindowStart, second.WindowEnd, first.WindowStart, first.WindowEnd)
	}
}

// A revision on a later day changes what is left of the week: today's
// session is presented as today's and stays in the plan (the window used to
// restart tomorrow, which called it "in the past" and dropped it), and the
// window keeps its last day. Descriptions are replayed so the model can send
// them back unchanged.
func TestGenerateWeeklyPlan_RevisionOnALaterDayKeepsToday(t *testing.T) {
	h := coachFixture(t)
	now := time.Now().In(h.db.AthleteLocation())
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	day := func(offset int) string { return today.AddDate(0, 0, offset).Format("2006-01-02") }
	week, err := json.Marshal(draftPayload{Workouts: []plannedRequest{
		{Date: day(-1), Title: "Yesterday's VO2", DurationSecs: 3600},
		{Date: day(0), Title: "Sweet spot 3x12", Description: "Seated, 90 rpm.\nFuel before the second rep.", DurationSecs: 4500},
		{Date: day(2), Title: "Long ride", DurationSecs: 10800},
	}})
	if err != nil {
		t.Fatal(err)
	}
	prev := models.WeeklyPlan{
		ID: "prev", Status: models.WeeklyPlanReady, Trigger: models.WeeklyPlanManual,
		WindowStart: day(-2), WindowEnd: day(4),
		Request: "Plan my week.", ReviewMD: "## How the last week went\nSolid.", WorkoutsJSON: string(week),
	}

	fakeCoach.turn = func(in aicoach.ChatTurnInput) (*aicoach.ChatTurnOutput, error) {
		if len(in.Rounds) > 0 {
			return &aicoach.ChatTurnOutput{Text: "Moved the long ride to the weekend."}, nil
		}
		args := `{"workouts":[{"date":"` + day(0) + `","title":"Sweet spot 3x12","description":"Seated, 90 rpm.\nFuel before the second rep.","duration_secs":4500},
			{"date":"` + day(4) + `","title":"Long ride","duration_secs":10800}]}`
		return &aicoach.ChatTurnOutput{Calls: []aicoach.ToolExchange{{ID: "p", Name: aicoach.ToolProposeSchedule, Input: json.RawMessage(args)}}}, nil
	}
	plan, err := h.generateWeeklyPlan(context.Background(),
		weeklyPlanRun{trigger: models.WeeklyPlanManual, notes: "move the long ride to the weekend", revise: &prev}, discardEvents)
	if err != nil {
		t.Fatalf("revision: %v", err)
	}

	req := fakeCoach.requests()[0]
	msg := req.History[2].Content
	for _, want := range []string{
		"- " + today.Format("Mon 2006-01-02") + " (today) · Sweet spot 3x12 · 75 min",
		`description: "Seated, 90 rpm.\nFuel before the second rep."`,
		"Long ride",
		"Earlier in the week, now past and not part of this plan",
		"- " + today.AddDate(0, 0, -1).Format("Mon 2006-01-02") + " · Yesterday's VO2 · 60 min",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("revision message lacks %q:\n%s", want, msg)
		}
	}
	if !strings.Contains(req.SystemContext, "dates "+day(0)+" to "+day(4)+" only") ||
		!strings.Contains(req.SystemContext, day(0)+" to "+day(4)+", today included.") {
		t.Errorf("revision brief should cover today through the week's last day, in place of the start-tomorrow default:\n%s", req.SystemContext)
	}
	if plan.WindowStart != day(0) || plan.WindowEnd != day(4) {
		t.Errorf("revised window = %s..%s, want %s..%s", plan.WindowStart, plan.WindowEnd, day(0), day(4))
	}
	ws, err := planWorkouts(&plan)
	if err != nil || len(ws) != 2 || ws[0].PlannedDate.Format("2006-01-02") != day(0) {
		t.Fatalf("revised week = %+v (err %v), want today's session kept and the long ride moved", ws, err)
	}
	if !strings.HasPrefix(plan.ReviewMD, prev.ReviewMD) || !strings.HasSuffix(plan.ReviewMD, "Moved the long ride to the weekend.") {
		t.Errorf("revised review = %q, want the earlier review with the change note under it", plan.ReviewMD)
	}
}

// A revision of a week that is already over plans a new week: the full brief
// and a full review, not a change note on a review of the past week.
func TestGenerateWeeklyPlan_RevisionOfAFinishedWeekPlansAfresh(t *testing.T) {
	h := coachFixture(t)
	now := time.Now().In(h.db.AthleteLocation())
	prev := models.WeeklyPlan{
		ID: "old", Status: models.WeeklyPlanReady, Trigger: models.WeeklyPlanManual,
		WindowStart: now.AddDate(0, 0, -9).Format("2006-01-02"), WindowEnd: now.AddDate(0, 0, -3).Format("2006-01-02"),
		Request: "Plan my week.", ReviewMD: "Old review.",
	}
	fakeCoach.turn = proposingWeek("## How the last week went\nNew review.", "Done.")
	plan, err := h.generateWeeklyPlan(context.Background(),
		weeklyPlanRun{trigger: models.WeeklyPlanManual, notes: "lighter please", revise: &prev}, discardEvents)
	if err != nil {
		t.Fatalf("revision: %v", err)
	}
	if ctx := fakeCoach.requests()[0].SystemContext; !strings.Contains(ctx, "Weekly plan request") || strings.Contains(ctx, "Weekly plan revision") {
		t.Errorf("a finished week should get the full brief:\n%s", ctx)
	}
	if strings.Contains(plan.ReviewMD, "Old review.") || !strings.Contains(plan.ReviewMD, "New review.") {
		t.Errorf("review = %q, want the new full review only", plan.ReviewMD)
	}
	if plan.WindowStart != firstPlanDay().Format("2006-01-02") {
		t.Errorf("window start = %s, want tomorrow", plan.WindowStart)
	}
}

// A revision made on a later day replays the sessions still ahead, with what
// propose_schedule needs to send each one back unchanged, and then lists the
// week's earlier sessions apart, as context: past, not to be proposed again,
// but counted in the weekly total and for key sessions on consecutive days.
func TestProposedWeekText_ListsEarlierSessionsApart(t *testing.T) {
	start := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	now := start.Add(9 * time.Hour)
	tss, ifactor := 55.0, 0.72
	hard, easy := 115, 50
	vo2 := []models.PlannedInterval{{Repeat: 5, Steps: []models.PlannedInterval{
		{Kind: "work", DurationSecs: 240, TargetPctFTP: &hard}, {Kind: "recovery", DurationSecs: 240, TargetPctFTP: &easy}}}}
	wk := planWeek{
		ahead: []models.PlannedWorkout{
			{PlannedDate: start, Title: "Endurance Z2", Sport: "cycling", DurationSecs: 5400, TSS: &tss, IntensityFactor: &ifactor,
				Description: `Keep it "conversational".`,
				Intervals:   []models.PlannedInterval{{Kind: "endurance", DurationSecs: 5400, TargetZone: "Z2"}}},
			{PlannedDate: start.AddDate(0, 0, 2), Title: "Easy run", Sport: "running", DurationSecs: 1830},
		},
		earlier: []models.PlannedWorkout{
			{PlannedDate: start.AddDate(0, 0, -1), Title: "Yesterday's VO2", Sport: "cycling", DurationSecs: 2400,
				Description: "Not replayed.", Intervals: vo2},
		},
	}
	got := proposedWeekText(wk, now)
	const header = "Earlier in the week, now past and not part of this plan. Don't propose them again"
	for _, want := range []string{
		"- Tue 2026-11-10 (today) · Endurance Z2 · 90 min · ~55 TSS · IF 0.72 · endurance 90:00 @ Z2\n" +
			`  description: "Keep it \"conversational\"."`,
		"- Thu 2026-11-12 · Easy run · running · 30 min 30 s",
		header,
		"count them in the weekly total and when checking for key sessions on consecutive days",
		"- Mon 2026-11-09 · Yesterday's VO2 · 40 min · 5×(work 4:00 @ 115% + recovery 4:00 @ 50%)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("proposed week lacks %q:\n%s", want, got)
		}
	}
	if i := strings.Index(got, header); i < 0 || strings.Index(got, "Yesterday's VO2") < i || strings.Index(got, "Easy run") > i {
		t.Errorf("earlier sessions should follow the sessions ahead, under their own header:\n%s", got)
	}
	if strings.Contains(got, "Not replayed.") {
		t.Errorf("an earlier session's description was replayed, as if to be sent back:\n%s", got)
	}

	onlyEarlier := proposedWeekText(planWeek{earlier: wk.earlier}, now)
	if !strings.HasPrefix(onlyEarlier, "(no sessions left in it)\n\n"+header) {
		t.Errorf("only earlier sessions: got\n%s", onlyEarlier)
	}
	if got := proposedWeekText(planWeek{}, now); got != "(no sessions left in it)" {
		t.Errorf("empty week: got %q", got)
	}
}

// readPlanWeek splits the revised plan at the window, and keeps the earlier
// sessions a revision of its own carried: a second later-day revision of the
// same week still sees, and counts, the week's first days.
func TestReadPlanWeek_KeepsEarlierSessionsAcrossRevisions(t *testing.T) {
	js, err := json.Marshal(weeklyPlanPayload{
		Workouts: []plannedRequest{
			{Date: "2026-11-11", Title: "Tempo", DurationSecs: 3600},
			{Date: "2026-11-10", Title: "VO2", DurationSecs: 2700},
			{Date: "2026-11-13", Title: "Long ride", DurationSecs: 10800},
		},
		Earlier: []plannedRequest{{Date: "2026-11-09", Title: "Endurance", DurationSecs: 5400}},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := &models.WeeklyPlan{WorkoutsJSON: string(js)}
	start := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	wk, err := readPlanWeek(p, start, start.AddDate(0, 0, 4))
	if err != nil {
		t.Fatal(err)
	}
	titles := func(ws []models.PlannedWorkout) string {
		var out []string
		for _, w := range ws {
			out = append(out, w.Title)
		}
		return strings.Join(out, ", ")
	}
	if got := titles(wk.ahead); got != "Tempo, Long ride" {
		t.Errorf("ahead = %s, want Tempo, Long ride", got)
	}
	if got := titles(wk.earlier); got != "Endurance, VO2" {
		t.Errorf("earlier = %s, want Endurance, VO2 (oldest first)", got)
	}
	if len(wk.carry) != 2 || wk.earlierSecs() != 5400+2700 {
		t.Errorf("carry = %+v, earlierSecs = %d; want both earlier sessions, 8100 s", wk.carry, wk.earlierSecs())
	}
	// What the calendar shows and accepting adds is the proposed week only.
	if ws, err := planWorkouts(p); err != nil || len(ws) != 3 {
		t.Errorf("planWorkouts = %d workouts (err %v), want the 3 proposed, not the carried one", len(ws), err)
	}
}

// A provider failure is stored as a failed plan (the scheduler counts those to
// back off) and never becomes the plan the calendar shows.
func TestGenerateWeeklyPlan_FailureIsRecorded(t *testing.T) {
	h := coachFixture(t)
	fakeCoach.turn = func(aicoach.ChatTurnInput) (*aicoach.ChatTurnOutput, error) {
		return nil, context.DeadlineExceeded
	}
	if _, err := h.generateWeeklyPlan(context.Background(), weeklyPlanRun{trigger: models.WeeklyPlanScheduled}, discardEvents); err == nil {
		t.Fatal("want the provider error")
	}
	n, last, err := h.db.ScheduledFailuresSince(time.Now().Add(-time.Minute))
	if err != nil || n != 1 || last.IsZero() {
		t.Errorf("failures = %d (last %v, err %v), want 1", n, last, err)
	}
	if latest, _ := h.db.LatestWeeklyPlan(); latest != nil {
		t.Errorf("latest plan = %+v, want none (failures are hidden)", latest)
	}
}

func TestWeeklyPlanAccept_AddsWeekOnce(t *testing.T) {
	h := coachFixture(t)
	fakeCoach.turn = proposingWeek("Review.", "Done.")
	plan, err := h.generateWeeklyPlan(context.Background(), weeklyPlanRun{trigger: models.WeeklyPlanManual}, discardEvents)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	accept := func() *httptest.ResponseRecorder {
		req := withChiParam(httptest.NewRequest(http.MethodPost, "/api/coach/weekly-plan/"+plan.ID+"/accept", nil), "id", plan.ID)
		rr := httptest.NewRecorder()
		h.WeeklyPlanAccept(rr, req)
		return rr
	}
	if rr := accept(); rr.Code != http.StatusCreated {
		t.Fatalf("accept: %d %s", rr.Code, rr.Body.String())
	}
	day := firstPlanDay()
	planned, err := h.db.ListPlannedWorkoutsBetween(day, day)
	if err != nil || len(planned) != 1 || planned[0].Source != "coach" {
		t.Fatalf("calendar after accept = %+v (err %v), want the one coach workout", planned, err)
	}
	if rr := accept(); rr.Code != http.StatusConflict {
		t.Errorf("second accept: %d, want 409 — a plan is added once", rr.Code)
	}
	if got, _ := h.db.GetWeeklyPlan(plan.ID); got.Status != models.WeeklyPlanAccepted {
		t.Errorf("status = %s, want accepted", got.Status)
	}
}

func TestWeeklyPlanGenerate_Validation(t *testing.T) {
	h := coachFixture(t)
	post := func(body string) int {
		rr := httptest.NewRecorder()
		h.WeeklyPlanGenerate(rr, httptest.NewRequest(http.MethodPost, "/api/coach/weekly-plan", strings.NewReader(body)))
		return rr.Code
	}
	if code := post(`{"revise_id":"nope","notes":"x"}`); code != http.StatusNotFound {
		t.Errorf("revise unknown plan: %d, want 404", code)
	}
	if code := post(`{"notes":"` + strings.Repeat("x", maxWeeklyNotes+1) + `"}`); code != http.StatusBadRequest {
		t.Errorf("oversized notes: %d, want 400", code)
	}
	if err := h.db.SaveAISettings(db.AISettings{}); err != nil {
		t.Fatal(err)
	}
	if code := post(`{}`); code != http.StatusServiceUnavailable {
		t.Errorf("unconfigured AI: %d, want 503", code)
	}
}

// ── Scheduler ─────────────────────────────────────────────────────────────────

func TestWeeklySlot(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	cases := []struct {
		now        time.Time
		slot, week string
	}{
		// Sunday before 20:00 still belongs to last week's slot.
		{time.Date(2026, 11, 8, 19, 59, 0, 0, ny), "2026-11-01 20:00", "2026-11-02"},
		{time.Date(2026, 11, 8, 20, 0, 0, 0, ny), "2026-11-08 20:00", "2026-11-09"},
		{time.Date(2026, 11, 11, 9, 0, 0, 0, ny), "2026-11-08 20:00", "2026-11-09"},
		{time.Date(2026, 11, 14, 23, 0, 0, 0, ny), "2026-11-08 20:00", "2026-11-09"},
		// DST ended at 02:00 on Sunday 2026-11-01: the slot stays 20:00 local.
		{time.Date(2026, 11, 2, 7, 0, 0, 0, ny), "2026-11-01 20:00", "2026-11-02"},
	}
	for _, c := range cases {
		slot, monday := weeklySlot(c.now)
		if got := slot.Format("2006-01-02 15:04"); got != c.slot || monday != c.week {
			t.Errorf("weeklySlot(%s) = %s / %s, want %s / %s", c.now.Format(time.RFC3339), got, monday, c.slot, c.week)
		}
	}
	if next := nextWeeklySlot(time.Date(2026, 11, 8, 20, 0, 0, 0, ny)); next.Format("2006-01-02 15:04") != "2026-11-15 20:00" {
		t.Errorf("nextWeeklySlot = %s, want 2026-11-15 20:00", next)
	}
}

// weeklyPlanDue is the whole safety story of the scheduler: off unless turned
// on, skips a week the rider already planned, and backs off on failures so an
// outage can't run up a bill.
func TestWeeklyPlanDue(t *testing.T) {
	h := coachFixture(t)
	loc := h.db.AthleteLocation()
	// A fixed mid-week instant: with the real clock, "two hours later" on a
	// Sunday evening would cross into the next week's slot.
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, loc)
	_, monday := weeklySlot(now)

	if h.weeklyPlanDue(now) {
		t.Error("due while the auto-plan is off")
	}
	if err := h.db.SetMeta(db.MetaWeeklyPlanAuto, "1"); err != nil {
		t.Fatal(err)
	}
	if !h.weeklyPlanDue(now) {
		t.Fatal("not due with the auto-plan on and no plan for this week")
	}

	// Two recent failures: inside the retry back-off.
	for i := 0; i < 2; i++ {
		if _, err := h.db.SaveWeeklyPlan(models.WeeklyPlan{Trigger: models.WeeklyPlanScheduled,
			Status: models.WeeklyPlanFailed, WindowStart: monday, WindowEnd: monday, CreatedAt: now.Add(-10 * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	if h.weeklyPlanDue(now) {
		t.Error("due again within the retry back-off after a failure")
	}
	if !h.weeklyPlanDue(now.Add(2 * time.Hour)) {
		t.Error("not due once the back-off has passed")
	}
	// A third failure hits the weekly cap.
	if _, err := h.db.SaveWeeklyPlan(models.WeeklyPlan{Trigger: models.WeeklyPlanScheduled,
		Status: models.WeeklyPlanFailed, WindowStart: monday, WindowEnd: monday, CreatedAt: now.Add(-3 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if h.weeklyPlanDue(now.Add(2 * time.Hour)) {
		t.Error("still due after the weekly failure cap")
	}
}

func TestWeeklyPlanDue_SkipsWeekAlreadyPlanned(t *testing.T) {
	h := coachFixture(t)
	if err := h.db.SetMeta(db.MetaWeeklyPlanAuto, "1"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(h.db.AthleteLocation())
	_, monday := weeklySlot(now)
	if _, err := h.db.SaveWeeklyPlan(models.WeeklyPlan{Trigger: models.WeeklyPlanManual,
		Status: models.WeeklyPlanReady, WindowStart: monday, WindowEnd: monday}); err != nil {
		t.Fatal(err)
	}
	if h.weeklyPlanDue(now) {
		t.Error("due although the rider already planned this week by hand")
	}
}

// End to end: the scheduler runs the plan when due, and not twice.
func TestRunScheduledPlanIfDue(t *testing.T) {
	h := coachFixture(t)
	if err := h.db.SetMeta(db.MetaWeeklyPlanAuto, "1"); err != nil {
		t.Fatal(err)
	}
	fakeCoach.turn = proposingWeek("Scheduled review.", "Done.")

	h.runScheduledPlanIfDue(context.Background())
	latest, err := h.db.LatestWeeklyPlan()
	if err != nil || latest == nil || latest.Trigger != models.WeeklyPlanScheduled {
		t.Fatalf("latest plan = %+v (err %v), want the scheduled run", latest, err)
	}
	calls := len(fakeCoach.requests())
	h.runScheduledPlanIfDue(context.Background())
	if len(fakeCoach.requests()) != calls {
		t.Error("scheduler ran a second plan for a week it already planned")
	}
}

func TestWeeklyPlanSetSchedule(t *testing.T) {
	h := coachFixture(t)
	put := func(body string) (int, weeklyScheduleStatus) {
		rr := httptest.NewRecorder()
		h.WeeklyPlanSetSchedule(rr, httptest.NewRequest(http.MethodPut, "/api/coach/weekly-plan/schedule", strings.NewReader(body)))
		var env struct {
			Data weeklyScheduleStatus `json:"data"`
		}
		_ = json.Unmarshal(rr.Body.Bytes(), &env)
		return rr.Code, env.Data
	}
	if code, s := put(`{"enabled":true}`); code != http.StatusOK || !s.Enabled || s.NextRun == "" {
		t.Errorf("enable: %d %+v, want enabled with a next run", code, s)
	}
	if code, s := put(`{"enabled":false}`); code != http.StatusOK || s.Enabled {
		t.Errorf("disable: %d %+v", code, s)
	}
	if code, _ := put(`{}`); code != http.StatusBadRequest {
		t.Errorf("missing enabled: %d, want 400", code)
	}
}

// withChiParam sets a chi URL parameter on a request for direct handler calls.
func withChiParam(r *http.Request, key, val string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, val)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// ── Availability (training days, weekly hours) ───────────────────────────────

func TestPlanAvailabilityCheck(t *testing.T) {
	eight := 8.0
	a := &planAvailability{prefs: models.WeeklyPlanPrefs{Days: []string{"mon", "tue", "thu", "sat", "sun"}, WeeklyHours: &eight}}
	wed := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	thu := wed.AddDate(0, 0, 1)

	err := a.check([]models.PlannedWorkout{{Title: "VO2", PlannedDate: wed, DurationSecs: 3600}})
	if err == nil || !strings.Contains(err.Error(), "Wednesday") || !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("Wednesday workout: err = %v, want an unavailable-day rejection naming the day", err)
	}
	if err := a.check([]models.PlannedWorkout{{Title: "Z2", PlannedDate: thu, DurationSecs: 8 * 3600}}); err != nil {
		t.Errorf("exactly on budget rejected: %v", err)
	}
	// Within the 10% slack passes; past it fails.
	if err := a.check([]models.PlannedWorkout{{Title: "Z2", PlannedDate: thu, DurationSecs: int(8.5 * 3600)}}); err != nil {
		t.Errorf("8.5h on an 8h budget (inside slack) rejected: %v", err)
	}
	if err := a.check([]models.PlannedWorkout{{Title: "Z2", PlannedDate: thu, DurationSecs: 9 * 3600}}); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Errorf("9h on an 8h budget: err = %v, want a budget rejection", err)
	}

	// A revision made on a later day proposes only the days left; the budget
	// is the week's, so the earlier sessions' time counts.
	revising := &planAvailability{prefs: a.prefs, earlierSecs: 6 * 3600}
	if err := revising.check([]models.PlannedWorkout{{Title: "Z2", PlannedDate: thu, DurationSecs: 2 * 3600}}); err != nil {
		t.Errorf("a week exactly on budget, earlier sessions included, rejected: %v", err)
	}
	err = revising.check([]models.PlannedWorkout{{Title: "Z2", PlannedDate: thu, DurationSecs: 3 * 3600}})
	if err == nil || !strings.Contains(err.Error(), "the week comes to 9.0 h") || !strings.Contains(err.Error(), "budget is 8 h") {
		t.Errorf("3h left in a week with 6h behind it, on an 8h budget: err = %v, want a rejection counting the week", err)
	}
	spent := &planAvailability{prefs: a.prefs, earlierSecs: 9 * 3600}
	err = spent.check([]models.PlannedWorkout{{Title: "Z2", PlannedDate: thu, DurationSecs: 1800}})
	if err == nil || !strings.Contains(err.Error(), "no session fits") {
		t.Errorf("earlier sessions over the budget: err = %v, want a rejection that says trimming can't fit", err)
	}
	noBudget := &planAvailability{prefs: models.WeeklyPlanPrefs{Days: models.WeekdayKeys}, earlierSecs: 9 * 3600}
	if err := noBudget.check([]models.PlannedWorkout{{Title: "Z2", PlannedDate: thu, DurationSecs: 3600}}); err != nil {
		t.Errorf("no budget set: err = %v, want none", err)
	}
}

// End to end, the reviewer's case: a 3 h week whose first day held a 2 h
// ride, revised later in the week to add 90 minutes. The days left came to
// 2.5 h, which fit the whole budget on their own, so the stored week was
// 4.5 h. propose_schedule now counts the earlier session, the prompt says
// what it leaves, the message lists it, and the revised plan keeps it for
// the next revision.
func TestGenerateWeeklyPlan_RevisionCountsTheWeeksEarlierSessions(t *testing.T) {
	h := coachFixture(t)
	three := 3.0
	if err := h.db.SaveWeeklyPlanPrefs(models.WeeklyPlanPrefs{Days: append([]string(nil), models.WeekdayKeys...), WeeklyHours: &three}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(h.db.AthleteLocation())
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	day := func(offset int) string { return today.AddDate(0, 0, offset).Format("2006-01-02") }
	week, err := json.Marshal(draftPayload{Workouts: []plannedRequest{
		{Date: day(-2), Title: "Long ride", DurationSecs: 7200},
		{Date: day(0), Title: "Spin", DurationSecs: 1800},
		{Date: day(2), Title: "Spin", DurationSecs: 1800},
	}})
	if err != nil {
		t.Fatal(err)
	}
	prev := models.WeeklyPlan{
		ID: "prev", Status: models.WeeklyPlanReady, Trigger: models.WeeklyPlanManual,
		WindowStart: day(-2), WindowEnd: day(4),
		Request: "Plan my week.", ReviewMD: "Review.", WorkoutsJSON: string(week),
	}

	propose := func(sessions string) *aicoach.ChatTurnOutput {
		return &aicoach.ChatTurnOutput{Calls: []aicoach.ToolExchange{{ID: "p", Name: aicoach.ToolProposeSchedule,
			Input: json.RawMessage(`{"workouts":[` + sessions + `]}`)}}}
	}
	spin := func(offset int) string {
		return `{"date":"` + day(offset) + `","title":"Spin","duration_secs":1800}`
	}
	var firstResult string
	fakeCoach.turn = func(in aicoach.ChatTurnInput) (*aicoach.ChatTurnOutput, error) {
		switch len(in.Rounds) {
		case 0: // the days left as asked: 2.5 h, under 3 h on their own
			return propose(spin(0) + `,{"date":"` + day(1) + `","title":"Added ride","duration_secs":5400},` + spin(2)), nil
		case 1:
			firstResult = in.Rounds[0].Exchanges[0].Result
			return propose(spin(0) + `,{"date":"` + day(1) + `","title":"Added ride","duration_secs":2700}`), nil
		default:
			return &aicoach.ChatTurnOutput{Text: "Added 45 minutes tomorrow and dropped the second spin: 3.25 h for the week."}, nil
		}
	}
	plan, err := h.generateWeeklyPlan(context.Background(),
		weeklyPlanRun{trigger: models.WeeklyPlanManual, notes: "add a 90 min ride tomorrow", revise: &prev}, discardEvents)
	if err != nil {
		t.Fatalf("revision: %v", err)
	}

	if !strings.Contains(firstResult, "the week comes to 4.5 h") || !strings.Contains(firstResult, "budget is 3 h") {
		t.Errorf("the 4.5 h week passed propose_schedule; first result = %s", firstResult)
	}
	req := fakeCoach.requests()[0]
	if !strings.Contains(req.SystemContext, "earlier sessions take 2 h") || !strings.Contains(req.SystemContext, "Plan up to 1 h for these days") {
		t.Errorf("the availability should say what the earlier session leaves of the budget:\n%s", req.SystemContext)
	}
	if msg := req.History[2].Content; !strings.Contains(msg, "- "+today.AddDate(0, 0, -2).Format("Mon 2006-01-02")+" · Long ride · 120 min") {
		t.Errorf("the revision message should list the earlier session with its time:\n%s", msg)
	}
	ws, err := planWorkouts(&plan)
	if err != nil || len(ws) != 2 || ws[1].DurationSecs != 2700 {
		t.Fatalf("revised week = %+v (err %v), want the trimmed proposal", ws, err)
	}
	// The next revision of this plan still counts the long ride.
	next, err := readPlanWeek(&plan, today.AddDate(0, 0, 1), today.AddDate(0, 0, 4))
	if err != nil || next.earlierSecs() != 7200+1800 || len(next.ahead) != 1 {
		t.Errorf("a revision tomorrow sees earlier %+v (%d s, err %v), want the long ride and today's spin", next.earlier, next.earlierSecs(), err)
	}
}

// End to end: the model books a day the rider unchecked, propose_schedule
// rejects it with a reason, and the model re-plans onto an open day. The
// stored plan never contains the forbidden day.
func TestGenerateWeeklyPlan_EnforcesTrainingDays(t *testing.T) {
	h := coachFixture(t)
	now := time.Now().In(h.db.AthleteLocation())
	blocked := now.AddDate(0, 0, 1) // tomorrow: the first day of the window
	var days []string
	for _, k := range models.WeekdayKeys {
		if k != models.WeekdayKey(blocked.Weekday()) {
			days = append(days, k)
		}
	}
	if err := h.db.SaveWeeklyPlanPrefs(models.WeeklyPlanPrefs{Days: days}); err != nil {
		t.Fatal(err)
	}

	propose := func(day time.Time) *aicoach.ChatTurnOutput {
		args := `{"workouts":[{"date":"` + day.Format("2006-01-02") + `","title":"Endurance Z2","duration_secs":3600}]}`
		return &aicoach.ChatTurnOutput{Calls: []aicoach.ToolExchange{{ID: "p", Name: aicoach.ToolProposeSchedule, Input: json.RawMessage(args)}}}
	}
	var firstResult string
	fakeCoach.turn = func(in aicoach.ChatTurnInput) (*aicoach.ChatTurnOutput, error) {
		switch len(in.Rounds) {
		case 0:
			return propose(blocked), nil
		case 1:
			firstResult = in.Rounds[0].Exchanges[0].Result
			return propose(blocked.AddDate(0, 0, 1)), nil
		default:
			return &aicoach.ChatTurnOutput{Text: "Moved it."}, nil
		}
	}

	plan, err := h.generateWeeklyPlan(context.Background(), weeklyPlanRun{trigger: models.WeeklyPlanManual}, discardEvents)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.Contains(firstResult, "unavailable") {
		t.Errorf("first proposal's tool result = %s, want an unavailable-day error for the model", firstResult)
	}
	ws, _ := planWorkouts(&plan)
	if len(ws) != 1 || ws[0].PlannedDate.Format("2006-01-02") == blocked.Format("2006-01-02") {
		t.Errorf("stored week = %+v, want one workout off the blocked day", ws)
	}
	first := fakeCoach.requests()[0]
	ctx := first.System + first.SystemContext // availability is per-request context
	if !strings.Contains(ctx, "Schedule nothing on "+blocked.Format("Mon 2006-01-02")) {
		t.Errorf("prompt doesn't name the blocked date:\n%s", ctx)
	}
}

// ── Proposal dates ───────────────────────────────────────────────────────────

// proposeOne is propose_schedule's input for a single one-hour ride on day.
func proposeOne(day time.Time) json.RawMessage {
	return json.RawMessage(`{"workouts":[{"date":"` + day.Format("2006-01-02") + `","title":"Z2","duration_secs":3600}]}`)
}

// Accepting a proposal deletes coach entries on every proposed date, so a
// past date would erase the plan a finished ride is graded against.
func TestToolProposeSchedule_RejectsPastDates(t *testing.T) {
	h := coachFixture(t)
	now := time.Now().In(h.db.AthleteLocation())
	captured := 0
	ev := toolEvents{Capture: func([]plannedRequest) string { captured++; return "x" }}

	_, err := h.toolProposeSchedule(proposeOne(now.AddDate(0, 0, -1)), ev)
	if err == nil || !strings.Contains(err.Error(), "before today") || !strings.Contains(err.Error(), now.Format("2006-01-02")) {
		t.Errorf("yesterday: err = %v, want a before-today rejection naming today", err)
	}
	if captured != 0 {
		t.Errorf("a rejected proposal was captured %d time(s)", captured)
	}
	if _, err := h.toolProposeSchedule(proposeOne(now), ev); err != nil {
		t.Errorf("today rejected: %v", err)
	}
	if _, err := h.toolProposeSchedule(proposeOne(now.AddDate(0, 0, 1)), ev); err != nil {
		t.Errorf("tomorrow rejected: %v", err)
	}
	if captured != 2 {
		t.Errorf("captured = %d, want 2 (today and tomorrow)", captured)
	}
}

func TestToolProposeSchedule_EnforcesPlanWindow(t *testing.T) {
	h := coachFixture(t)
	now := time.Now().In(h.db.AthleteLocation())
	start, end := aicoach.WeeklyPlanWindow(now)
	ev := toolEvents{
		Capture: func([]plannedRequest) string { return "x" },
		Window:  &planWindow{Start: start.Format("2006-01-02"), End: end.Format("2006-01-02")},
	}

	_, err := h.toolProposeSchedule(proposeOne(end.AddDate(0, 0, 1)), ev)
	if err == nil || !strings.Contains(err.Error(), "outside the plan window") ||
		!strings.Contains(err.Error(), ev.Window.Start) || !strings.Contains(err.Error(), ev.Window.End) {
		t.Errorf("day after the window: err = %v, want a window rejection naming both ends", err)
	}
	if _, err := h.toolProposeSchedule(proposeOne(start), ev); err != nil {
		t.Errorf("first day of the window rejected: %v", err)
	}
}

// End to end: a weekly run that proposes a day past its window gets the
// reason back, re-plans, and stores only the in-window day.
func TestGenerateWeeklyPlan_EnforcesWindow(t *testing.T) {
	h := coachFixture(t)
	start, end := aicoach.WeeklyPlanWindow(time.Now().In(h.db.AthleteLocation()))
	propose := func(day time.Time) *aicoach.ChatTurnOutput {
		return &aicoach.ChatTurnOutput{Calls: []aicoach.ToolExchange{{ID: "p", Name: aicoach.ToolProposeSchedule, Input: proposeOne(day)}}}
	}
	var firstResult string
	fakeCoach.turn = func(in aicoach.ChatTurnInput) (*aicoach.ChatTurnOutput, error) {
		switch len(in.Rounds) {
		case 0:
			return propose(end.AddDate(0, 0, 1)), nil
		case 1:
			firstResult = in.Rounds[0].Exchanges[0].Result
			return propose(start), nil
		default:
			return &aicoach.ChatTurnOutput{Text: "Fixed the dates."}, nil
		}
	}

	plan, err := h.generateWeeklyPlan(context.Background(), weeklyPlanRun{trigger: models.WeeklyPlanManual}, discardEvents)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.Contains(firstResult, "outside the plan window") {
		t.Errorf("first proposal's tool result = %s, want a window error for the model", firstResult)
	}
	ws, err := planWorkouts(&plan)
	if err != nil || len(ws) != 1 || ws[0].PlannedDate.Format("2006-01-02") != start.Format("2006-01-02") {
		t.Errorf("stored week = %+v (err %v), want one workout on %s", ws, err, start.Format("2006-01-02"))
	}
}

func TestWeeklyPlanSetPreferences(t *testing.T) {
	h := coachFixture(t)
	put := func(body string) (int, models.WeeklyPlanPrefs) {
		rr := httptest.NewRecorder()
		h.WeeklyPlanSetPreferences(rr, httptest.NewRequest(http.MethodPut, "/api/coach/weekly-plan/preferences", strings.NewReader(body)))
		var env struct {
			Data models.WeeklyPlanPrefs `json:"data"`
		}
		_ = json.Unmarshal(rr.Body.Bytes(), &env)
		return rr.Code, env.Data
	}
	code, p := put(`{"days":["sat","mon","wed"],"weekly_hours":7.5}`)
	if code != http.StatusOK || strings.Join(p.Days, ",") != "mon,wed,sat" || p.WeeklyHours == nil || *p.WeeklyHours != 7.5 {
		t.Fatalf("save: %d %+v, want mon,wed,sat at 7.5h (calendar order)", code, p)
	}
	stored, err := h.db.GetWeeklyPlanPrefs()
	if err != nil || strings.Join(stored.Days, ",") != "mon,wed,sat" || stored.WeeklyHours == nil {
		t.Errorf("stored = %+v (err %v)", stored, err)
	}
	if code, p := put(`{"days":["mon"],"weekly_hours":null}`); code != http.StatusOK || p.WeeklyHours != nil {
		t.Errorf("clearing hours: %d %+v", code, p)
	}
	for _, bad := range []string{`{"days":[]}`, `{"days":["funday"]}`, `{"days":["mon"],"weekly_hours":0.5}`, `{"days":["mon","mon"]}`} {
		if code, _ := put(bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, code)
		}
	}
}

func TestWeeklyPlanPrefsDefaultToEveryDay(t *testing.T) {
	h := coachFixture(t)
	p, err := h.db.GetWeeklyPlanPrefs()
	if err != nil || len(p.Days) != 7 || p.WeeklyHours != nil {
		t.Errorf("defaults = %+v (err %v), want all 7 days and no budget", p, err)
	}
}
