package providers

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fitbase/fitbase/internal/aicoach"
)

func init() { aicoach.Register(geminiProvider{}) }

type geminiProvider struct{}

func (geminiProvider) Name() string  { return "gemini" }
func (geminiProvider) Label() string { return "Gemini" }

func (geminiProvider) Models() []aicoach.ModelOption {
	return []aicoach.ModelOption{
		{Value: "gemini-2.0-flash", Label: "Gemini 2.0 Flash (recommended)"},
		{Value: "gemini-1.5-pro", Label: "Gemini 1.5 Pro"},
		{Value: "gemini-1.5-flash", Label: "Gemini 1.5 Flash (fast)"},
	}
}

func (geminiProvider) Stream(ctx context.Context, cfg aicoach.CallConfig, onChunk func(string) error) error {
	reqBody := map[string]any{
		"systemInstruction": map[string]any{
			"parts": []map[string]string{{"text": cfg.System}},
		},
		"contents": []map[string]any{
			{"parts": []map[string]string{{"text": cfg.User}}},
		},
		"generationConfig": map[string]any{"maxOutputTokens": cfg.MaxTokens},
	}
	url := "https://generativelanguage.googleapis.com/v1beta/models/" + cfg.Model +
		":streamGenerateContent?alt=sse&key=" + cfg.APIKey
	body, err := aicoach.PostStream(ctx, url, nil, reqBody)
	if err != nil {
		return fmt.Errorf("gemini: %w", err)
	}
	defer body.Close() //nolint:errcheck

	return aicoach.ScanSSE(body, func(data []byte) error {
		var evt struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &evt); err != nil {
			return nil
		}
		if evt.Error != nil {
			return fmt.Errorf("gemini API error: %s", evt.Error.Message)
		}
		if len(evt.Candidates) > 0 && len(evt.Candidates[0].Content.Parts) > 0 {
			if text := evt.Candidates[0].Content.Parts[0].Text; text != "" {
				return onChunk(text)
			}
		}
		return nil
	})
}
