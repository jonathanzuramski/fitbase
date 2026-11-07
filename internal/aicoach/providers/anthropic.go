// Package providers registers every concrete LLM provider with the aicoach
// registry. Importing it (typically as a blank import in main) is what wires
// the providers up — aicoach itself has no compile-time knowledge of which
// backends exist.
package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/fitbase/fitbase/internal/aicoach"
)

func init() { aicoach.Register(anthropicProvider{}) }

type anthropicProvider struct{}

func (anthropicProvider) Name() string  { return "anthropic" }
func (anthropicProvider) Label() string { return "Claude" }

func (anthropicProvider) Models() []aicoach.ModelOption {
	return []aicoach.ModelOption{
		{Value: "claude-opus-4-7", Label: "Claude Opus 4.7 (best)"},
		{Value: "claude-sonnet-4-6", Label: "Claude Sonnet 4.6 (recommended)"},
		{Value: "claude-haiku-4-5-20251001", Label: "Claude Haiku 4.5 (fast)"},
	}
}

// ListModels fetches the live model catalog from the Anthropic API. The
// /v1/models endpoint returns only chat-capable Claude models, newest first,
// so no filtering is needed.
func (anthropicProvider) ListModels(ctx context.Context, apiKey string) ([]aicoach.ModelOption, error) {
	headers := map[string]string{
		"x-api-key":         apiKey,
		"anthropic-version": "2023-06-01",
	}
	body, err := aicoach.GetJSON(ctx, "https://api.anthropic.com/v1/models?limit=100", headers)
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	return parseAnthropicModels(body)
}

func parseAnthropicModels(body []byte) ([]aicoach.ModelOption, error) {
	var resp struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("anthropic: parse models: %w", err)
	}
	out := make([]aicoach.ModelOption, 0, len(resp.Data))
	for _, m := range resp.Data {
		label := m.DisplayName
		if label == "" {
			label = m.ID
		}
		out = append(out, aicoach.ModelOption{Value: m.ID, Label: label})
	}
	return out, nil
}

func (anthropicProvider) Stream(ctx context.Context, cfg aicoach.CallConfig, onChunk func(string) error) error {
	reqBody := map[string]any{
		"model":      cfg.Model,
		"max_tokens": cfg.MaxTokens,
		"system":     cfg.System,
		"messages":   []map[string]string{{"role": "user", "content": cfg.User}},
		"stream":     true,
	}
	headers := map[string]string{
		"x-api-key":         cfg.APIKey,
		"anthropic-version": "2023-06-01",
	}
	body, err := aicoach.PostStream(ctx, "https://api.anthropic.com/v1/messages", headers, reqBody)
	if err != nil {
		return fmt.Errorf("anthropic: %w", err)
	}
	defer body.Close() //nolint:errcheck

	var usage anthropicUsage
	return aicoach.ScanSSE(body, func(data []byte) error {
		var evt struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
			// message_start carries the input side of usage, message_delta the
			// final output count.
			Message struct {
				Usage anthropicUsage `json:"usage"`
			} `json:"message"`
			Usage anthropicUsage `json:"usage"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		// Skip unparseable events (e.g. `ping`) rather than failing the stream.
		if err := json.Unmarshal(data, &evt); err != nil {
			return nil
		}
		if evt.Error != nil {
			return fmt.Errorf("anthropic API error: %s", evt.Error.Message)
		}
		switch evt.Type {
		case "message_start":
			usage = evt.Message.Usage
		case "message_delta":
			usage.OutputTokens = evt.Usage.OutputTokens
		case "message_stop":
			usage.log(cfg.Model, "insights")
		}
		if evt.Type == "content_block_delta" && evt.Delta.Type == "text_delta" && evt.Delta.Text != "" {
			return onChunk(evt.Delta.Text)
		}
		return nil
	})
}

// ChatTurn implements aicoach.ChatProvider: one buffered (non-streamed)
// round-trip against the Messages API with tools enabled. It rebuilds the full
// provider transcript from the client history plus every resolved tool round,
// sends it, and returns either the model's final text or the tool calls it
// wants run.
func (anthropicProvider) ChatTurn(ctx context.Context, in aicoach.ChatTurnInput) (*aicoach.ChatTurnOutput, error) {
	reqBody := anthropicChatRequest(in)
	headers := map[string]string{
		"x-api-key":         in.APIKey,
		"anthropic-version": "2023-06-01",
	}
	raw, err := aicoach.PostJSON(ctx, "https://api.anthropic.com/v1/messages", headers, reqBody)
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	return parseAnthropicChatResponse(raw, in.Model)
}

// anthropicChatRequest builds the Messages API body for one chat round. Kept
// separate from the HTTP call so the prompt-caching layout can be tested: a
// caching regression never errors, it only shows up on the bill.
func anthropicChatRequest(in aicoach.ChatTurnInput) map[string]any {
	messages := make([]map[string]any, 0, len(in.History)+2*len(in.Rounds))

	for _, m := range in.History {
		role := "user"
		if m.Role == aicoach.ChatRoleAssistant {
			role = "assistant"
		}
		messages = append(messages, map[string]any{"role": role, "content": m.Content})
	}

	// Replay each completed tool round as the assistant's tool_use message
	// followed by our tool_result message, exactly as the API expects.
	for _, rnd := range in.Rounds {
		assistantBlocks := make([]map[string]any, 0, len(rnd.Exchanges)+1)
		if rnd.AssistantText != "" {
			assistantBlocks = append(assistantBlocks, map[string]any{
				"type": "text", "text": rnd.AssistantText,
			})
		}
		for _, ex := range rnd.Exchanges {
			input := json.RawMessage(ex.Input)
			if len(input) == 0 {
				input = json.RawMessage("{}")
			}
			assistantBlocks = append(assistantBlocks, map[string]any{
				"type":  "tool_use",
				"id":    ex.ID,
				"name":  ex.Name,
				"input": input,
			})
		}
		messages = append(messages, map[string]any{"role": "assistant", "content": assistantBlocks})

		resultBlocks := make([]map[string]any, 0, len(rnd.Exchanges))
		for _, ex := range rnd.Exchanges {
			resultBlocks = append(resultBlocks, map[string]any{
				"type":        "tool_result",
				"tool_use_id": ex.ID,
				"content":     ex.Result,
			})
		}
		messages = append(messages, map[string]any{"role": "user", "content": resultBlocks})
	}

	// Prompt caching. A chat turn is a loop of requests that each re-send the
	// tool definitions, the system prompt, and every earlier tool result, so
	// without caching a 4-round weekly plan pays for its ~10k-token prefix four
	// times at full price. Two breakpoints (of the API's four):
	//   - explicit, on the static system block: caches tools + prompt, which
	//     are byte-identical across rounds, turns, riders, and days;
	//   - automatic (top-level cache_control): the API places it on the last
	//     block and moves it forward, so each round reads everything earlier
	//     rounds wrote and pays full price only for what it appends.
	// Default 5-minute TTL: rounds start seconds apart, and a follow-up
	// question inside 5 minutes still reads the tools + prompt entry.
	ephemeral := map[string]string{"type": "ephemeral"}
	var system []map[string]any
	if in.System != "" {
		system = append(system, map[string]any{"type": "text", "text": in.System, "cache_control": ephemeral})
	}
	if in.SystemContext != "" {
		system = append(system, map[string]any{"type": "text", "text": in.SystemContext})
	}
	reqBody := map[string]any{
		"model":         in.Model,
		"max_tokens":    in.MaxTokens,
		"messages":      messages,
		"cache_control": ephemeral,
	}
	if len(system) > 0 {
		reqBody["system"] = system
	}
	if len(in.Tools) > 0 {
		tools := make([]map[string]any, len(in.Tools))
		for i, t := range in.Tools {
			tools[i] = map[string]any{
				"name":         t.Name,
				"description":  t.Description,
				"input_schema": t.InputSchema,
			}
		}
		reqBody["tools"] = tools
		if in.ToolChoice == aicoach.ToolChoiceNone {
			// Forbid tool calls without dropping the definitions — the API
			// rejects a transcript containing tool_use blocks if tools are absent.
			reqBody["tool_choice"] = map[string]any{"type": "none"}
		}
	}
	return reqBody
}

// parseAnthropicChatResponse maps a Messages API response to the model's text
// and tool calls, logging the call's token usage.
func parseAnthropicChatResponse(raw []byte, model string) (*aicoach.ChatTurnOutput, error) {

	var resp struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string         `json:"stop_reason"`
		Usage      anthropicUsage `json:"usage"`
		Error      *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("anthropic: parse response: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("anthropic API error: %s", resp.Error.Message)
	}
	resp.Usage.log(model, "chat")

	out := &aicoach.ChatTurnOutput{}
	for _, blk := range resp.Content {
		switch blk.Type {
		case "text":
			out.Text += blk.Text
		case "tool_use":
			out.Calls = append(out.Calls, aicoach.ToolExchange{
				ID:    blk.ID,
				Name:  blk.Name,
				Input: blk.Input,
			})
		}
	}
	return out, nil
}

// anthropicUsage is the Messages API usage block. Total prompt size is
// InputTokens + CacheCreationInputTokens + CacheReadInputTokens — InputTokens
// alone is only the uncached remainder.
type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	OutputTokens             int `json:"output_tokens"`
}

// log records one API call's token usage. One line per call is the only way
// to see what the coach costs and whether prompt caching is actually hitting.
func (u anthropicUsage) log(model, kind string) {
	slog.Info("anthropic usage", "kind", kind, "model", model,
		"input_tokens", u.InputTokens,
		"cache_creation_input_tokens", u.CacheCreationInputTokens,
		"cache_read_input_tokens", u.CacheReadInputTokens,
		"output_tokens", u.OutputTokens)
}
