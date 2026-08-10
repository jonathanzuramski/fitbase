package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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

// ListModels fetches the live model catalog from the Gemini API and keeps only
// models that support generateContent (the endpoint also returns embedding,
// aqa, and other non-chat models). The "models/" name prefix is stripped to the
// bare id the Stream URL expects. The key is passed via header rather than the
// URL query so it doesn't leak into proxy/redirect logs.
func (geminiProvider) ListModels(ctx context.Context, apiKey string) ([]aicoach.ModelOption, error) {
	headers := map[string]string{"x-goog-api-key": apiKey}
	body, err := aicoach.GetJSON(ctx, "https://generativelanguage.googleapis.com/v1beta/models?pageSize=1000", headers)
	if err != nil {
		return nil, fmt.Errorf("gemini: %w", err)
	}
	return parseGeminiModels(body)
}

func parseGeminiModels(body []byte) ([]aicoach.ModelOption, error) {
	var resp struct {
		Models []struct {
			Name                       string   `json:"name"`
			DisplayName                string   `json:"displayName"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("gemini: parse models: %w", err)
	}
	out := make([]aicoach.ModelOption, 0, len(resp.Models))
	for _, m := range resp.Models {
		supportsChat := false
		for _, method := range m.SupportedGenerationMethods {
			if method == "generateContent" {
				supportsChat = true
				break
			}
		}
		if !supportsChat {
			continue
		}
		id := strings.TrimPrefix(m.Name, "models/")
		label := m.DisplayName
		if label == "" {
			label = id
		}
		out = append(out, aicoach.ModelOption{Value: id, Label: label})
	}
	return out, nil
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
	// Key goes in the header, not the URL query: transport errors embed the full
	// URL in their message (and it lands in proxy/server logs), which would leak
	// the key — same reason ListModels uses the header.
	url := "https://generativelanguage.googleapis.com/v1beta/models/" + cfg.Model +
		":streamGenerateContent?alt=sse"
	headers := map[string]string{"x-goog-api-key": cfg.APIKey}
	body, err := aicoach.PostStream(ctx, url, headers, reqBody)
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
		if len(evt.Candidates) > 0 {
			// A chunk can carry several parts (e.g. around thought parts or part
			// splits) — concatenate them all so no text is silently dropped.
			var text strings.Builder
			for _, p := range evt.Candidates[0].Content.Parts {
				text.WriteString(p.Text)
			}
			if text.Len() > 0 {
				return onChunk(text.String())
			}
		}
		return nil
	})
}
