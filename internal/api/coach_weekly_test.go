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

// proposingWeek scripts the weekly playbook's shape: the model writes its
// review and calls propose_schedule in one turn, then closes in the next.
func proposingWeek(review, closing string) func(in aicoach.ChatTurnInput) (*aicoach.ChatTurnOutput, error) {
	return func(in aicoach.ChatTurnInput) (*aicoach.ChatTurnOutput, error) {
		if len(in.Rounds) > 0 {
			return &aicoach.ChatTurnOutput{Text: closing}, nil
		}
		tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
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

	sys := fakeCoach.requests()[0].System
	if !strings.Contains(sys, "Weekly plan request") || !strings.Contains(sys, plan.WindowStart) {
		t.Errorf("system prompt lacks the playbook for window %s:\n%s", plan.WindowStart, sys)
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
	if reqs := fakeCoach.requests(); len(reqs) == 0 || strings.Contains(reqs[0].System, "Weekly plan request") {
		t.Error("a plain chat question carried the weekly playbook")
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
	fakeCoach.turn = proposingWeek("Review two.", "Done.")
	second, err := h.generateWeeklyPlan(context.Background(),
		weeklyPlanRun{trigger: models.WeeklyPlanManual, notes: "move the long ride to Sunday", revise: &first}, discardEvents)
	if err != nil {
		t.Fatalf("revision: %v", err)
	}

	hist := fakeCoach.requests()[0].History
	if len(hist) != 3 || hist[0].Content != first.Request || hist[1].Content != first.ReviewMD ||
		!strings.Contains(hist[2].Content, "move the long ride to Sunday") {
		t.Fatalf("revision history = %+v, want [first request, first review, the change]", hist)
	}
	if !strings.Contains(second.Request, "no riding Wednesday") || !strings.Contains(second.Request, "long ride to Sunday") {
		t.Errorf("revised request = %q, want both constraints", second.Request)
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
	day := time.Now().AddDate(0, 0, 1)
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
