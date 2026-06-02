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
// object passed verbatim to the provider's tool-definition format.
type ToolSpec struct {
	Name        string
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

// ChatTurnInput is one provider round-trip: the client history plus every
// tool round resolved so far in this Chat call.
type ChatTurnInput struct {
	Model     string
	APIKey    string
	System    string
	History   []ChatMessage
	Rounds    []ToolRound
	Tools     []ToolSpec
	MaxTokens int
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
const ChatSystemPrompt = `You are an experienced cycling coach having a conversation with a rider about their training. You have tools that read the rider's real training database — workouts, readiness, fitness trend (CTL/ATL/TSB), weekly load, power curve, zone distribution, and athlete profile — and one tool that drafts a future schedule for them to review.

Hard rules — these are not suggestions:
- NEVER cite a specific number (FTP, threshold HR, max HR, weight, CTL, ATL, TSB, TSS, IF, NP, watts, bpm, W/kg, %FTP, workout duration, dates) unless you got it from a tool result this turn OR from the "Rider profile" block in this system prompt. If you don't have the number, call a tool first. Plausible-sounding guesses are forbidden — readers act on these numbers.
- The "Rider profile" block below is authoritative for FTP, weight, threshold HR, and max HR. Use those numbers verbatim. Do not call get_athlete_profile unless the rider explicitly asks to verify the profile.
- For anything about recent training (workouts, fitness, form, ramp, zone time, power curve, weekly load): CALL the matching tool before answering. Prefer the narrowest tool and shortest window. You may call several tools before answering, and more after seeing results.
- Don't narrate tool use ("let me check…") — fetch silently, then answer.
- If a tool returns thin/missing data, say so plainly. Don't fill the gap with a guess.

Style:
- Cite concrete numbers from the data ("CTL rose 52→61 over the block" beats "fitness improved").
- Use cycling vocabulary the rider knows (FTP, IF, TSS, sweet spot, Z2, threshold, VO2, EF, decoupling).
- Be direct and concise — a few tight paragraphs, not an essay. Use Markdown. No medical, nutrition, weight, or injury advice.
- Answer the question the rider actually asked. Follow-ups build on the conversation.

When the rider asks for a plan, week, schedule, or "what should I do" over multiple days:
- Gather context first (readiness + weekly load at minimum; power curve and recent workouts if relevant).
- Then CALL propose_schedule with one workout per day. Do not just describe the plan in prose — the UI shows a preview card from that tool call so the rider can accept it onto their calendar. After it's drafted, summarize the plan in one short paragraph and tell the rider to review the preview.
- Build the schedule against current form (TSB) and ramp rate: recover when form is deeply negative, build when neutral, sharpen when positive.`

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
		// On the final allowed attempt, drop tools so the model is forced to
		// answer with what it already gathered instead of looping forever.
		turnTools := tools
		if attempt == maxToolRounds {
			turnTools = nil
		}

		out, err := cp.ChatTurn(ctx, ChatTurnInput{
			Model:     c.model,
			APIKey:    c.apiKey,
			System:    system,
			History:   history,
			Rounds:    rounds,
			Tools:     turnTools,
			MaxTokens: MaxTokens,
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

	// Unreachable: the attempt == maxToolRounds turn has no tools, so it must
	// return text and return above. Kept as a defensive fallback.
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
