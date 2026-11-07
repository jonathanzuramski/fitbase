package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/fitbase/fitbase/internal/aicoach"
	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/models"
)

// capturingProvider is a fake chat provider that records the system prompt of
// each turn and answers immediately with text, so a Chat request runs end to
// end without a network call.
type capturingProvider struct {
	mu      sync.Mutex
	systems []string
}

func (*capturingProvider) Name() string  { return "capture-test" }
func (*capturingProvider) Label() string { return "Capture" }
func (*capturingProvider) Models() []aicoach.ModelOption {
	return []aicoach.ModelOption{{Value: "m", Label: "m"}}
}
func (*capturingProvider) Stream(context.Context, aicoach.CallConfig, func(string) error) error {
	return nil
}
func (p *capturingProvider) ChatTurn(_ context.Context, in aicoach.ChatTurnInput) (*aicoach.ChatTurnOutput, error) {
	p.mu.Lock()
	// The weekly playbook is per-request context, so it rides in
	// SystemContext — never in the cached static System prompt.
	p.systems = append(p.systems, in.System+"\n\n"+in.SystemContext)
	p.mu.Unlock()
	return &aicoach.ChatTurnOutput{Text: "ok"}, nil
}

func (p *capturingProvider) lastSystem() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.systems) == 0 {
		return ""
	}
	return p.systems[len(p.systems)-1]
}

var captureProvider = &capturingProvider{}

func init() { aicoach.Register(captureProvider) }

func chatFixture(t *testing.T) *CoachHandler {
	t.Helper()
	h := zoneDistFixture(t, &models.Athlete{FTPWatts: 250, WeightKG: 75})
	if err := h.db.SaveAISettings(db.AISettings{Provider: "capture-test", Model: "m", APIKey: "k"}); err != nil {
		t.Fatalf("SaveAISettings: %v", err)
	}
	return h
}

func postChat(h *CoachHandler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/coach/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Chat(rr, req)
	return rr
}

// The "plan my week" button only differs from a typed question by its mode,
// so the playbook must actually reach the model's system prompt — and only on
// that turn.
func TestChat_WeeklyPlanModeAppendsPlaybook(t *testing.T) {
	h := chatFixture(t)

	rr := postChat(h, `{"messages":[{"role":"user","content":"Plan my week."}],"mode":"weekly_plan"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "event: done") {
		t.Fatalf("stream did not finish: %s", rr.Body.String())
	}
	sys := captureProvider.lastSystem()
	if !strings.Contains(sys, "Weekly plan request") || !strings.Contains(sys, aicoach.ToolGetWorkoutIntervals) {
		t.Errorf("weekly_plan turn's system prompt lacks the playbook:\n%s", sys)
	}

	rr = postChat(h, `{"messages":[{"role":"user","content":"How fresh am I?"}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(captureProvider.lastSystem(), "Weekly plan request") {
		t.Error("a normal question carried the weekly-plan playbook")
	}
}

func TestChat_UnknownModeRejected(t *testing.T) {
	h := chatFixture(t)
	rr := postChat(h, `{"messages":[{"role":"user","content":"hi"}],"mode":"monthly_plan"}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an unknown mode (body=%s)", rr.Code, rr.Body.String())
	}
}
