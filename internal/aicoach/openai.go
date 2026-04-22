package aicoach

import (
	"context"
	"encoding/json"
	"fmt"
)

func init() { Register(openaiProvider{}) }

type openaiProvider struct{}

func (openaiProvider) Name() string  { return "openai" }
func (openaiProvider) Label() string { return "ChatGPT" }

func (openaiProvider) Models() []ModelOption {
	return []ModelOption{
		{Value: "gpt-4o", Label: "GPT-4o (recommended)"},
		{Value: "gpt-4o-mini", Label: "GPT-4o mini (fast)"},
		{Value: "gpt-4-turbo", Label: "GPT-4 Turbo"},
	}
}

func (openaiProvider) Stream(ctx context.Context, cfg CallConfig, onChunk func(string) error) error {
	reqBody := map[string]any{
		"model": cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": cfg.System},
			{"role": "user", "content": cfg.User},
		},
		"max_tokens": cfg.MaxTokens,
		"stream":     true,
	}
	headers := map[string]string{"Authorization": "Bearer " + cfg.APIKey}
	body, err := postStream(ctx, "https://api.openai.com/v1/chat/completions", headers, reqBody)
	if err != nil {
		return fmt.Errorf("openai: %w", err)
	}
	defer body.Close() //nolint:errcheck

	return scanSSE(body, func(data []byte) error {
		var evt struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &evt); err != nil {
			return nil
		}
		if evt.Error != nil {
			return fmt.Errorf("openai API error: %s", evt.Error.Message)
		}
		if len(evt.Choices) > 0 && evt.Choices[0].Delta.Content != "" {
			return onChunk(evt.Choices[0].Delta.Content)
		}
		return nil
	})
}
