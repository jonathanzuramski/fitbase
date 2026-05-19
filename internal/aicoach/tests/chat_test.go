package aicoach_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fitbase/fitbase/internal/aicoach"
)

// scriptedProvider is a fake ChatProvider whose behavior each test sets via
// fn. Tests run sequentially (no t.Parallel), so mutating the shared instance
// between tests is safe; it's registered once in init.
type scriptedProvider struct {
	fn func(in aicoach.ChatTurnInput) (*aicoach.ChatTurnOutput, error)
}

func (*scriptedProvider) Name() string  { return "scripted" }
func (*scriptedProvider) Label() string { return "Scripted" }
func (*scriptedProvider) Models() []aicoach.ModelOption {
	return []aicoach.ModelOption{{Value: "m", Label: "m"}}
}
func (*scriptedProvider) Stream(context.Context, aicoach.CallConfig, func(string) error) error {
	return nil
}
func (p *scriptedProvider) ChatTurn(_ context.Context, in aicoach.ChatTurnInput) (*aicoach.ChatTurnOutput, error) {
	return p.fn(in)
}

var fake = &scriptedProvider{}

func init() { aicoach.Register(fake) }

func newCoach(t *testing.T) *aicoach.Coach {
	t.Helper()
	c, err := aicoach.New("scripted", "m", "k")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestChatExecutesToolThenAnswers(t *testing.T) {
	const finalAnswer = "Your CTL is 61 — a solid aerobic base built over the last block of training, nice work."

	// Turn 1: ask for a tool. Turn 2 (after results present): final text.
	fake.fn = func(in aicoach.ChatTurnInput) (*aicoach.ChatTurnOutput, error) {
		if len(in.Rounds) == 0 {
			return &aicoach.ChatTurnOutput{
				Calls: []aicoach.ToolInvocation{{
					ID:    "call_1",
					Name:  aicoach.ToolGetReadiness,
					Input: json.RawMessage(`{}`),
				}},
			}, nil
		}
		// The result we returned must be replayed back to us.
		if got := in.Rounds[0].Results[0].Result; got != `{"fitness_ctl":61}` {
			t.Errorf("tool result not threaded back: got %q", got)
		}
		return &aicoach.ChatTurnOutput{Text: finalAnswer}, nil
	}

	var execName string
	var started, ended []string
	endedOK := map[string]bool{}
	var streamed strings.Builder

	c := newCoach(t)
	out, err := c.Chat(context.Background(),
		[]aicoach.ChatMessage{{Role: aicoach.ChatRoleUser, Content: "how's my fitness?"}},
		aicoach.CoachTools(),
		func(_ context.Context, name string, _ json.RawMessage) (string, error) {
			execName = name
			return `{"fitness_ctl":61}`, nil
		},
		aicoach.ChatCallbacks{
			OnToolStart: func(n string) { started = append(started, n) },
			OnToolEnd:   func(n string, ok bool) { ended = append(ended, n); endedOK[n] = ok },
			OnText:      func(chunk string) error { streamed.WriteString(chunk); return nil },
		},
	)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if out != finalAnswer {
		t.Errorf("final answer = %q, want %q", out, finalAnswer)
	}
	if execName != aicoach.ToolGetReadiness {
		t.Errorf("executor called with %q, want %q", execName, aicoach.ToolGetReadiness)
	}
	if len(started) != 1 || len(ended) != 1 || !endedOK[aicoach.ToolGetReadiness] {
		t.Errorf("tool callbacks off: started=%v ended=%v ok=%v", started, ended, endedOK)
	}
	// Re-chunked streaming must reconstruct the answer exactly (no lost or
	// reordered bytes).
	if streamed.String() != finalAnswer {
		t.Errorf("streamed chunks = %q, want %q", streamed.String(), finalAnswer)
	}
}

func TestChatToolErrorSurfacesToModelAndContinues(t *testing.T) {
	fake.fn = func(in aicoach.ChatTurnInput) (*aicoach.ChatTurnOutput, error) {
		if len(in.Rounds) == 0 {
			return &aicoach.ChatTurnOutput{
				Calls: []aicoach.ToolInvocation{{ID: "c1", Name: aicoach.ToolGetPowerCurve, Input: json.RawMessage(`{}`)}},
			}, nil
		}
		if !strings.Contains(in.Rounds[0].Results[0].Result, "boom") {
			t.Errorf("tool error not surfaced to model: %q", in.Rounds[0].Results[0].Result)
		}
		return &aicoach.ChatTurnOutput{Text: "Couldn't read your power curve right now."}, nil
	}

	var endedOK bool
	c := newCoach(t)
	out, err := c.Chat(context.Background(),
		[]aicoach.ChatMessage{{Role: aicoach.ChatRoleUser, Content: "power curve?"}},
		aicoach.CoachTools(),
		func(context.Context, string, json.RawMessage) (string, error) {
			return "", errBoom
		},
		aicoach.ChatCallbacks{OnToolEnd: func(_ string, ok bool) { endedOK = ok }},
	)
	if err != nil {
		t.Fatalf("Chat should not abort on tool error: %v", err)
	}
	if endedOK {
		t.Error("OnToolEnd ok should be false when the tool errored")
	}
	if !strings.Contains(out, "Couldn't read") {
		t.Errorf("unexpected answer: %q", out)
	}
}

type boomError struct{}

func (boomError) Error() string { return "boom" }

var errBoom = boomError{}

func TestChatConvergesWhenModelKeepsCallingTools(t *testing.T) {
	// The model asks for a tool every turn it has tools. The loop must drop
	// tools on the final attempt, forcing a text answer instead of looping.
	calls := 0
	fake.fn = func(in aicoach.ChatTurnInput) (*aicoach.ChatTurnOutput, error) {
		if len(in.Tools) == 0 {
			return &aicoach.ChatTurnOutput{Text: "Best read I can give with what I have."}, nil
		}
		return &aicoach.ChatTurnOutput{
			Calls: []aicoach.ToolInvocation{{ID: "x", Name: aicoach.ToolGetWeeklyBreakdown, Input: json.RawMessage(`{}`)}},
		}, nil
	}

	c := newCoach(t)
	out, err := c.Chat(context.Background(),
		[]aicoach.ChatMessage{{Role: aicoach.ChatRoleUser, Content: "loop forever?"}},
		aicoach.CoachTools(),
		func(context.Context, string, json.RawMessage) (string, error) {
			calls++
			return `{}`, nil
		},
		aicoach.ChatCallbacks{},
	)
	if err != nil {
		t.Fatalf("Chat did not converge: %v", err)
	}
	if !strings.Contains(out, "Best read") {
		t.Errorf("expected forced final answer, got %q", out)
	}
	if calls == 0 {
		t.Error("expected tools to have been executed before convergence")
	}
}

func TestSupportsChat(t *testing.T) {
	if !aicoach.SupportsChat("anthropic") {
		t.Error("anthropic should support chat")
	}
	for _, n := range []string{"openai", "gemini", "does-not-exist"} {
		if aicoach.SupportsChat(n) {
			t.Errorf("%s should not support chat", n)
		}
	}

	cClaude, err := aicoach.New("anthropic", "claude-sonnet-4-6", "k")
	if err != nil {
		t.Fatalf("New anthropic: %v", err)
	}
	if !cClaude.SupportsChat() {
		t.Error("Coach over anthropic should support chat")
	}
	cGPT, err := aicoach.New("openai", "gpt-4o", "k")
	if err != nil {
		t.Fatalf("New openai: %v", err)
	}
	if cGPT.SupportsChat() {
		t.Error("Coach over openai should not support chat")
	}
}

func TestCoachToolsCatalogWellFormed(t *testing.T) {
	tools := aicoach.CoachTools()
	if len(tools) == 0 {
		t.Fatal("no tools defined")
	}
	seen := map[string]bool{}
	for _, tl := range tools {
		if tl.Name == "" || tl.Description == "" {
			t.Errorf("tool %+v missing name or description", tl)
		}
		if seen[tl.Name] {
			t.Errorf("duplicate tool name %q", tl.Name)
		}
		seen[tl.Name] = true
		if tl.InputSchema["type"] != "object" {
			t.Errorf("tool %q schema type = %v, want object", tl.Name, tl.InputSchema["type"])
		}
		if tl.InputSchema["properties"] == nil {
			t.Errorf("tool %q has nil properties (providers reject null)", tl.Name)
		}
	}
	// Every named constant must be present in the catalog.
	for _, name := range []string{
		aicoach.ToolGetAthleteProfile, aicoach.ToolGetReadiness,
		aicoach.ToolGetFitnessTrend, aicoach.ToolListRecentWorkouts,
		aicoach.ToolGetWorkoutDetail, aicoach.ToolGetWeeklyBreakdown,
		aicoach.ToolGetPowerCurve, aicoach.ToolGetZoneDistribution,
	} {
		if !seen[name] {
			t.Errorf("catalog missing tool %q", name)
		}
	}
}
