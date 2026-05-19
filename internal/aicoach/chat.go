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

// ToolInvocation is a single tool call the model asked for. ID is the
// provider-assigned identifier that the matching result must echo back.
type ToolInvocation struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolOutcome is the result we return for one ToolInvocation. Result is a
// JSON (or plain text) string the model reads as the tool's output.
type ToolOutcome struct {
	ID     string
	Result string
}

// ToolRound pairs the calls the assistant requested with the results we
// produced, in the same order. Completed rounds are replayed to the provider
// on each subsequent turn so it sees the full reasoning trail. AssistantText
// is any prose the model emitted alongside the tool calls that turn (often
// empty) — replayed so the transcript stays faithful.
type ToolRound struct {
	AssistantText string
	Calls         []ToolInvocation
	Results       []ToolOutcome
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
	Calls []ToolInvocation
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
// is told to pull data with tools rather than guess.
const ChatSystemPrompt = `You are an experienced cycling coach having a conversation with a rider about their training. You have tools that read the rider's real training database — workouts, fitness trend (CTL/ATL/TSB), weekly load, power curve, zone distribution, and athlete profile.

Rules:
- Always ground answers in the rider's actual data. Call the tools to fetch what you need instead of guessing or asking the rider to paste numbers. Prefer the narrowest tool and shortest window that answers the question.
- You may call several tools before answering, and call more after seeing results if needed. Don't narrate tool use ("let me check…") — just fetch, then answer.
- Cite concrete numbers from the data ("CTL rose 52→61 over the block" beats "fitness improved"). If the data is thin or missing, say so plainly.
- Use cycling vocabulary the rider knows (FTP, IF, TSS, sweet spot, Z2, threshold, VO2, EF, decoupling).
- Be direct and concise — a few tight paragraphs, not an essay. Use Markdown. No medical, nutrition, weight, or injury advice.
- Answer the question the rider actually asked. Follow-ups build on the conversation.`

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
func (c *Coach) Chat(ctx context.Context, history []ChatMessage, tools []ToolSpec, exec ToolExecutor, cb ChatCallbacks) (string, error) {
	cp, ok := c.provider.(ChatProvider)
	if !ok {
		return "", fmt.Errorf("provider %q does not support chat", c.provider.Name())
	}

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
			System:    ChatSystemPrompt,
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

		results := make([]ToolOutcome, 0, len(out.Calls))
		for _, call := range out.Calls {
			if cb.OnToolStart != nil {
				cb.OnToolStart(call.Name)
			}
			res, err := exec(ctx, call.Name, call.Input)
			if cb.OnToolEnd != nil {
				cb.OnToolEnd(call.Name, err == nil)
			}
			if err != nil {
				// Surface the failure to the model rather than aborting — it
				// can apologize or try a different tool.
				res = fmt.Sprintf(`{"error":%q}`, err.Error())
			}
			results = append(results, ToolOutcome{ID: call.ID, Result: res})
		}
		rounds = append(rounds, ToolRound{
			AssistantText: out.Text,
			Calls:         out.Calls,
			Results:       results,
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
