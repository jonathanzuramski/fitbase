package aicoach

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ── Conversational coach with tool use ───────────────────────────────────────
//
// The insights path (StreamInsights) hands the model one big pre-assembled blob
// and asks for a fixed report. Chat is the opposite: the model holds a
// conversation and pulls only the data it needs by calling tools, which the
// server executes against the DB and feeds back. The loop runs buffered (no
// token streaming) so tool_use parsing stays simple; the final answer is
// re-chunked to the browser by the HTTP layer to preserve the live-typing feel.

// ChatRole is the author of a client-visible message. Tool-use exchanges are
// internal to a single Chat call and never appear in this history — the client
// only ever sends and receives plain user/assistant text.
type ChatRole string

const (
	ChatRoleUser      ChatRole = "user"
	ChatRoleAssistant ChatRole = "assistant"
)

// ChatMessage is one user/assistant text turn. The browser owns the full
// history and posts it on every request; the server stays stateless.
type ChatMessage struct {
	Role    ChatRole `json:"role"`
	Content string   `json:"content"`
}

// ToolSpec describes a tool the model may call. InputSchema is a JSON Schema
// object passed verbatim to the provider's tool-definition format. Label is a
// short user-facing phrase for progress UI ("looking up <label>…") — it never
// reaches the model.
type ToolSpec struct {
	Name        string
	Label       string
	Description string
	InputSchema map[string]any
}

// ToolExchange is one tool call paired with its result. ID is the
// provider-assigned identifier replayed on the result block. Result is filled
// in by the coach after the executor runs — empty in ChatTurnOutput, populated
// by the time the exchange is stored in a ToolRound.
type ToolExchange struct {
	ID     string
	Name   string
	Input  json.RawMessage
	Result string
}

// ToolRound is the assistant's tool-use turn plus the results we produced.
// Completed rounds are replayed to the provider on each subsequent turn so it
// sees the full reasoning trail. AssistantText is any prose the model emitted
// alongside the tool calls (often empty) — replayed so the transcript stays
// faithful.
type ToolRound struct {
	AssistantText string
	Exchanges     []ToolExchange
}

// ToolChoiceNone forces a text answer: the tools stay defined (required by
// providers when the transcript replays tool_use blocks) but the model may not
// call them this turn.
const ToolChoiceNone = "none"

// ChatTurnInput is one provider round-trip: the client history plus every
// tool round resolved so far in this Chat call. ToolChoice is "" (model
// decides) or ToolChoiceNone.
type ChatTurnInput struct {
	Model      string
	APIKey     string
	System     string
	History    []ChatMessage
	Rounds     []ToolRound
	Tools      []ToolSpec
	ToolChoice string
	MaxTokens  int
}

// ChatTurnOutput is the assistant's response for one turn. When Calls is
// empty, Text is the final answer; otherwise the caller must execute Calls,
// append a ToolRound, and call ChatTurn again.
type ChatTurnOutput struct {
	Text  string
	Calls []ToolExchange
}

// ChatProvider is an optional capability: providers that support multi-turn,
// tool-augmented conversation implement it in addition to Provider. Providers
// that only implement Provider still serve one-shot insights — chat callers
// must type-assert and degrade gracefully when it is absent.
type ChatProvider interface {
	Provider
	ChatTurn(ctx context.Context, in ChatTurnInput) (*ChatTurnOutput, error)
}

// ChatCapableLabels returns the display labels of every registered provider
// that supports chat, sorted by provider name. Used for user-facing "chat
// needs …" messages so they never hardcode a provider.
func ChatCapableLabels() []string {
	var out []string
	for _, p := range All() {
		if _, ok := p.(ChatProvider); ok {
			out = append(out, p.Label())
		}
	}
	return out
}

// SupportsChat reports whether the registered provider implements ChatProvider.
// Used by the UI to decide whether to show the chat box.
func SupportsChat(name string) bool {
	p, ok := Get(name)
	if !ok {
		return false
	}
	_, ok = p.(ChatProvider)
	return ok
}

// maxToolRounds caps the agentic loop so a misbehaving model can't fetch data
// forever. Six rounds is generous — most questions resolve in one or two.
const maxToolRounds = 6

// ChatSystemPrompt steers the conversational coach. Unlike SystemPrompt it has
// no fixed output structure: the model answers the rider's actual question and
// is told to pull data with tools rather than guess. The rider profile is
// appended as a dynamic suffix by the caller so the model never needs to call
// get_athlete_profile to know FTP/HR — eliminating the most common drift mode.
const ChatSystemPrompt = `You are an experienced cycling coach talking with a rider about their training. You have tools that read the rider's real training database — readiness, fitness trend (CTL/ATL/TSB), recent workouts, weekly load, power curve (all-time and recent), FTP history, weekly EF/decoupling trends, zone distribution, and mileage-goal progress — plus one write tool, propose_schedule, that drafts future workouts for the rider to review.

Grounding:
- Every specific number you state (FTP, threshold HR, max HR, weight, CTL, ATL, TSB, TSS, IF, NP, watts, W/kg, %FTP, durations, dates) must come from a tool result in this conversation or from the "Rider profile" block below. If you don't have the number, call a tool to get it — never substitute a plausible value, because the rider acts on these numbers.
- The Rider profile block is authoritative for FTP, weight, threshold HR, and max HR — use those numbers as-is without re-fetching them.
- Questions about recent training (workouts, fitness, form, ramp, zone time, power curve, weekly load) need current data: call the matching tool before answering. Prefer the narrowest tool and the shortest window that answers the question; call more tools after seeing results when needed.
- Fetch silently — no "let me check…" narration. If the data is thin or missing, say so plainly rather than filling the gap.

Talking about fitness — load is not fitness:
- CTL, ATL, and TSB measure training load and freshness, not performance. Never tell the rider they "got fitter" from rising CTL alone — that only proves they trained more.
- To judge real fitness change, triangulate the performance signals: FTP history (get_ftp_history), recent-vs-all-time power (get_power_curve with days set), and weekly EF/decoupling trends (get_performance_trend). Rising EF at similar IF, power bests approaching or beating all-time in the recent window, or an FTP increase are real gains. Falling decoupling on long rides is durability progress even when peak power is flat.
- Rising CTL with flat EF and no recent power progress means the load hasn't converted into fitness yet — say that honestly, and look at intensity distribution (get_zone_distribution) for why.
- The full picture spans load (CTL, ramp), performance (FTP, power curve, EF), durability (decoupling), freshness (TSB), and intensity balance (zones). Pull the pieces the question needs before concluding.

Style:
- Cite concrete numbers ("CTL rose 52→61 over the block" beats "fitness improved").
- Use the vocabulary riders know: FTP, IF, TSS, sweet spot, Z2, threshold, VO2, EF, decoupling. Use the rider's preferred units when the profile states them; default to metric.
- Direct and concise — a few tight paragraphs of Markdown, not an essay. Answer the question the rider actually asked; follow-ups build on the conversation.
- No medical, nutrition, weight-loss, or injury advice — for those, suggest the right professional.

Planning — when the rider asks for a plan, a week, a schedule, or "what should I do" across multiple days:
- Gather context first: readiness and weekly load at minimum; recent workouts, power curve, zone distribution, or goal progress (get_goal_progress) when they would change the plan. If the rider has set mileage goals, the plan should respect them.
- Then call propose_schedule with one entry per training day. The UI turns that call into a preview card the rider can accept onto their calendar — a plan that stays in prose never reaches the calendar.
- Dates must be today or later (the current date is given below); start tomorrow unless the rider names a start date. Skip rest days instead of scheduling empty workouts.
- Build against current form and ramp rate: recover when TSB is deeply negative, build when it's near neutral, sharpen when positive. Alternate hard and easy days. Give key sessions structured intervals — warmup, work set with recoveries (use a repeat group), cooldown — and give endurance rides a simple Z2 target.
- Afterwards, summarize the plan in one short paragraph and point the rider to the preview card to accept or discard.`

// GetSystemPrompt returns the chat system prompt with an optional rider-profile
// suffix appended verbatim. Callers build profileBlock from current athlete
// data (FTP, weight, LTHR, MaxHR) so the model has authoritative numbers in
// context and never needs to fetch them via the athlete-profile tool.
func GetSystemPrompt(profileBlock string) string {
	if profileBlock == "" {
		return ChatSystemPrompt
	}
	return ChatSystemPrompt + "\n\n" + profileBlock
}

// ToolExecutor runs one tool call and returns its result as a string the model
// will read (JSON preferred). An error aborts the whole chat turn.
type ToolExecutor func(ctx context.Context, name string, input json.RawMessage) (string, error)

// ChatCallbacks receive progress as a Chat call resolves. Any may be nil.
type ChatCallbacks struct {
	// OnToolStart fires before a tool executes (UI shows "fetching…").
	OnToolStart func(name string)
	// OnToolEnd fires after a tool executes, ok=false if it errored.
	OnToolEnd func(name string, ok bool)
	// OnText receives the final answer in chunks for incremental rendering.
	OnText func(chunk string) error
}

// SupportsChat reports whether this Coach's provider can hold a tool-augmented
// conversation. False for OpenAI/Gemini today.
func (c *Coach) SupportsChat() bool {
	_, ok := c.provider.(ChatProvider)
	return ok
}

// Chat runs the agentic conversation loop: ask the provider, execute any tool
// calls it requests against exec, feed the results back, and repeat until the
// model returns a final text answer (or the round cap is hit). The final
// answer is delivered through cb.OnText in chunks and also returned whole.
// systemContext is appended to ChatSystemPrompt — used by callers to inject
// the rider profile so the model has authoritative FTP/HR numbers in context.
func (c *Coach) Chat(ctx context.Context, history []ChatMessage, systemContext string, tools []ToolSpec, exec ToolExecutor, cb ChatCallbacks) (string, error) {
	// assert the provider supports ChatProvider interface.
	cp, ok := c.provider.(ChatProvider)
	if !ok {
		return "", fmt.Errorf("provider %q does not support chat", c.provider.Name())
	}
	// get the system prompt with profile context appended.
	system := GetSystemPrompt(systemContext)

	var rounds []ToolRound
	for attempt := 0; attempt <= maxToolRounds; attempt++ {
		// On the final allowed attempt, forbid tool calls (ToolChoiceNone) so the
		// model is forced to answer with what it already gathered instead of
		// looping forever. The tools themselves stay defined — providers reject a
		// transcript containing tool_use blocks when tools are absent.
		toolChoice := ""
		if attempt == maxToolRounds {
			toolChoice = ToolChoiceNone
		}

		out, err := cp.ChatTurn(ctx, ChatTurnInput{
			Model:      c.model,
			APIKey:     c.apiKey,
			System:     system,
			History:    history,
			Rounds:     rounds,
			Tools:      tools,
			ToolChoice: toolChoice,
			MaxTokens:  MaxTokens,
		})
		if err != nil {
			return "", err
		}

		if len(out.Calls) == 0 {
			text := strings.TrimSpace(out.Text)
			if text == "" {
				text = "I couldn't find enough data to answer that."
			}
			if cb.OnText != nil {
				for _, chunk := range chunkText(text) {
					if err := cb.OnText(chunk); err != nil {
						return "", err
					}
				}
			}
			return text, nil
		}

		exchanges := out.Calls
		for i := range exchanges {
			ex := &exchanges[i]
			if cb.OnToolStart != nil {
				cb.OnToolStart(ex.Name)
			}
			res, err := exec(ctx, ex.Name, ex.Input)
			if cb.OnToolEnd != nil {
				cb.OnToolEnd(ex.Name, err == nil)
			}
			if err != nil {
				// Surface the failure to the model rather than aborting — it
				// can apologize or try a different tool.
				res = fmt.Sprintf(`{"error":%q}`, err.Error())
			}
			ex.Result = res
		}
		rounds = append(rounds, ToolRound{
			AssistantText: out.Text,
			Exchanges:     exchanges,
		})
	}

	// Unreachable: the attempt == maxToolRounds turn forbids tool calls, so it
	// must return text and return above. Kept as a defensive fallback.
	return "", fmt.Errorf("chat did not converge after %d tool rounds", maxToolRounds)
}

// chunkText splits the buffered final answer into small pieces so the HTTP
// layer can stream it and the UI types it out. A piece is flushed only at a
// whitespace boundary once it passes the target size, so no chunk lands
// mid-word or mid-Markdown-token. Concatenating the result reproduces s.
func chunkText(s string) []string {
	const target = 24 // chars per chunk; small enough to feel live
	var out []string
	var b strings.Builder
	for _, r := range s {
		b.WriteRune(r)
		if b.Len() >= target && (r == ' ' || r == '\n' || r == '\t') {
			out = append(out, b.String())
			b.Reset()
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	if len(out) == 0 {
		return []string{s}
	}
	return out
}
