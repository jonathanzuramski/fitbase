package providers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fitbase/fitbase/internal/aicoach"
)

// A caching regression never errors — requests keep succeeding and the bill
// quietly goes up (measured: ~40% on a weekly plan). These tests pin the
// layout that made the cache hit: an explicit breakpoint on the static system
// block, the per-request context after it and unmarked, and top-level
// automatic caching for the growing tool-round tail.

func chatRequestJSON(t *testing.T, in aicoach.ChatTurnInput) map[string]any {
	t.Helper()
	b, err := json.Marshal(anthropicChatRequest(in))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

func sampleTurn() aicoach.ChatTurnInput {
	return aicoach.ChatTurnInput{
		Model:         "claude-sonnet-5-5",
		MaxTokens:     4096,
		System:        aicoach.ChatSystemPrompt,
		SystemContext: "Today is Sunday, 2026-11-08.\n\nRider profile: FTP 245 W",
		History:       []aicoach.ChatMessage{{Role: aicoach.ChatRoleUser, Content: "How did my workout go?"}},
		Rounds: []aicoach.ToolRound{{Exchanges: []aicoach.ToolExchange{{
			ID: "t1", Name: aicoach.ToolListRecentWorkouts, Input: json.RawMessage(`{}`), Result: `{"workouts":[]}`,
		}}}},
		Tools: aicoach.CoachTools(),
	}
}

func TestAnthropicChatRequest_CachesStaticPrefix(t *testing.T) {
	req := chatRequestJSON(t, sampleTurn())

	system, ok := req["system"].([]any)
	if !ok || len(system) != 2 {
		t.Fatalf("system = %#v, want two text blocks (static prompt, per-request context)", req["system"])
	}
	static := system[0].(map[string]any)
	if static["text"] != aicoach.ChatSystemPrompt {
		t.Error("first system block is not the static coach prompt")
	}
	if cc, _ := static["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Errorf("static block cache_control = %v, want ephemeral breakpoint", static["cache_control"])
	}
	dynamic := system[1].(map[string]any)
	if _, marked := dynamic["cache_control"]; marked {
		t.Error("per-request context carries a breakpoint; it changes daily and must sit after the cached prefix")
	}
	if !strings.Contains(dynamic["text"].(string), "Today is") {
		t.Errorf("second block = %q, want the per-request context", dynamic["text"])
	}

	if cc, _ := req["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Errorf("top-level cache_control = %v, want automatic caching for the tool-round tail", req["cache_control"])
	}
}

// The static block must not vary with the day, the rider, or the mode — that
// is what lets one question read the cache another wrote.
func TestAnthropicChatRequest_StaticPrefixIgnoresContext(t *testing.T) {
	a, b := sampleTurn(), sampleTurn()
	b.SystemContext = "Today is Monday, 2026-11-09.\n\nWeekly plan request — ..."
	sa := chatRequestJSON(t, a)["system"].([]any)[0]
	sb := chatRequestJSON(t, b)["system"].([]any)[0]
	ja, _ := json.Marshal(sa)
	jb, _ := json.Marshal(sb)
	if string(ja) != string(jb) {
		t.Error("static system block changed with the per-request context — cross-turn cache hits would be lost")
	}
	ta, _ := json.Marshal(chatRequestJSON(t, a)["tools"])
	tb, _ := json.Marshal(chatRequestJSON(t, b)["tools"])
	if string(ta) != string(tb) {
		t.Error("tool definitions serialized differently between requests — they render first, so nothing would cache")
	}
}
