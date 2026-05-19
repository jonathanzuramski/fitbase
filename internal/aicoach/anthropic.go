package aicoach

import (
	"context"
	"encoding/json"
	"fmt"
)

func init() { Register(anthropicProvider{}) }

type anthropicProvider struct{}

func (anthropicProvider) Name() string  { return "anthropic" }
func (anthropicProvider) Label() string { return "Claude" }

func (anthropicProvider) Models() []ModelOption {
	return []ModelOption{
		{Value: "claude-opus-4-7", Label: "Claude Opus 4.7 (best)"},
		{Value: "claude-sonnet-4-6", Label: "Claude Sonnet 4.6 (recommended)"},
		{Value: "claude-haiku-4-5-20251001", Label: "Claude Haiku 4.5 (fast)"},
	}
}

func (anthropicProvider) Stream(ctx context.Context, cfg CallConfig, onChunk func(string) error) error {
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
	body, err := postStream(ctx, "https://api.anthropic.com/v1/messages", headers, reqBody)
	if err != nil {
		return fmt.Errorf("anthropic: %w", err)
	}
	defer body.Close() //nolint:errcheck

	return scanSSE(body, func(data []byte) error {
		var evt struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
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
		if evt.Type == "content_block_delta" && evt.Delta.Type == "text_delta" && evt.Delta.Text != "" {
			return onChunk(evt.Delta.Text)
		}
		return nil
	})
}

// ChatTurn implements ChatProvider: one buffered (non-streamed) round-trip
// against the Messages API with tools enabled. It rebuilds the full provider
// transcript from the client history plus every resolved tool round, sends it,
// and returns either the model's final text or the tool calls it wants run.
func (anthropicProvider) ChatTurn(ctx context.Context, in ChatTurnInput) (*ChatTurnOutput, error) {
	messages := make([]map[string]any, 0, len(in.History)+2*len(in.Rounds))

	for _, m := range in.History {
		role := "user"
		if m.Role == ChatRoleAssistant {
			role = "assistant"
		}
		messages = append(messages, map[string]any{"role": role, "content": m.Content})
	}

	// Replay each completed tool round as the assistant's tool_use message
	// followed by our tool_result message, exactly as the API expects.
	for _, rnd := range in.Rounds {
		assistantBlocks := make([]map[string]any, 0, len(rnd.Calls)+1)
		if rnd.AssistantText != "" {
			assistantBlocks = append(assistantBlocks, map[string]any{
				"type": "text", "text": rnd.AssistantText,
			})
		}
		for _, c := range rnd.Calls {
			input := json.RawMessage(c.Input)
			if len(input) == 0 {
				input = json.RawMessage("{}")
			}
			assistantBlocks = append(assistantBlocks, map[string]any{
				"type":  "tool_use",
				"id":    c.ID,
				"name":  c.Name,
				"input": input,
			})
		}
		messages = append(messages, map[string]any{"role": "assistant", "content": assistantBlocks})

		resultBlocks := make([]map[string]any, 0, len(rnd.Results))
		for _, r := range rnd.Results {
			resultBlocks = append(resultBlocks, map[string]any{
				"type":        "tool_result",
				"tool_use_id": r.ID,
				"content":     r.Result,
			})
		}
		messages = append(messages, map[string]any{"role": "user", "content": resultBlocks})
	}

	reqBody := map[string]any{
		"model":      in.Model,
		"max_tokens": in.MaxTokens,
		"system":     in.System,
		"messages":   messages,
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
	}

	headers := map[string]string{
		"x-api-key":         in.APIKey,
		"anthropic-version": "2023-06-01",
	}
	raw, err := postJSON(ctx, "https://api.anthropic.com/v1/messages", headers, reqBody)
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}

	var resp struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
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

	out := &ChatTurnOutput{}
	for _, blk := range resp.Content {
		switch blk.Type {
		case "text":
			out.Text += blk.Text
		case "tool_use":
			out.Calls = append(out.Calls, ToolInvocation{
				ID:    blk.ID,
				Name:  blk.Name,
				Input: blk.Input,
			})
		}
	}
	return out, nil
}
