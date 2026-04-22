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
