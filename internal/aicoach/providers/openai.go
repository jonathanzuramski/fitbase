package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/fitbase/fitbase/internal/aicoach"
)

func init() { aicoach.Register(openaiProvider{}) }

type openaiProvider struct{}

func (openaiProvider) Name() string  { return "openai" }
func (openaiProvider) Label() string { return "ChatGPT" }

func (openaiProvider) Models() []aicoach.ModelOption {
	return []aicoach.ModelOption{
		{Value: "gpt-4o", Label: "GPT-4o (recommended)"},
		{Value: "gpt-4o-mini", Label: "GPT-4o mini (fast)"},
		{Value: "gpt-4-turbo", Label: "GPT-4 Turbo"},
	}
}

// openaiNonChatMarkers flag models that are returned by /v1/models but aren't
// general chat models (embeddings, audio, image, moderation, legacy completions).
var openaiNonChatMarkers = []string{
	"instruct", "embedding", "audio", "realtime", "transcribe", "tts",
	"whisper", "dall-e", "image", "moderation", "search", "codex",
	"davinci", "babbage", "ada", "curie",
}

// ListModels fetches the live model catalog from the OpenAI API. Unlike
// Anthropic's, /v1/models returns every model (embeddings, TTS, image, etc.)
// with no capability field, so we filter to chat-family ids by prefix and drop
// known non-chat variants. Newest models are listed first.
func (openaiProvider) ListModels(ctx context.Context, apiKey string) ([]aicoach.ModelOption, error) {
	headers := map[string]string{"Authorization": "Bearer " + apiKey}
	body, err := aicoach.GetJSON(ctx, "https://api.openai.com/v1/models", headers)
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	return parseOpenAIModels(body)
}

func parseOpenAIModels(body []byte) ([]aicoach.ModelOption, error) {
	var resp struct {
		Data []struct {
			ID      string `json:"id"`
			Created int64  `json:"created"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("openai: parse models: %w", err)
	}

	sort.Slice(resp.Data, func(i, j int) bool { return resp.Data[i].Created > resp.Data[j].Created })

	out := make([]aicoach.ModelOption, 0, len(resp.Data))
	for _, m := range resp.Data {
		id := strings.ToLower(m.ID)
		isChat := strings.HasPrefix(id, "gpt-") || strings.HasPrefix(id, "chatgpt") ||
			strings.HasPrefix(id, "o1") || strings.HasPrefix(id, "o3") || strings.HasPrefix(id, "o4")
		if !isChat {
			continue
		}
		skip := false
		for _, marker := range openaiNonChatMarkers {
			if strings.Contains(id, marker) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		out = append(out, aicoach.ModelOption{Value: m.ID, Label: m.ID})
	}
	return out, nil
}

func (openaiProvider) Stream(ctx context.Context, cfg aicoach.CallConfig, onChunk func(string) error) error {
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
	body, err := aicoach.PostStream(ctx, "https://api.openai.com/v1/chat/completions", headers, reqBody)
	if err != nil {
		return fmt.Errorf("openai: %w", err)
	}
	defer body.Close() //nolint:errcheck

	return aicoach.ScanSSE(body, func(data []byte) error {
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
