package providers

import "testing"

// TestParseAnthropicModels verifies the /v1/models response is mapped to
// ModelOptions with display_name as the label (falling back to the id).
func TestParseAnthropicModels(t *testing.T) {
	body := []byte(`{
		"data": [
			{"type": "model", "id": "claude-opus-4-8", "display_name": "Claude Opus 4.8", "created_at": "2026-01-01T00:00:00Z"},
			{"type": "model", "id": "claude-haiku-4-5", "display_name": ""}
		],
		"has_more": false
	}`)

	got, err := parseAnthropicModels(body)
	if err != nil {
		t.Fatalf("parseAnthropicModels: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 models, got %d: %+v", len(got), got)
	}
	if got[0].Value != "claude-opus-4-8" || got[0].Label != "Claude Opus 4.8" {
		t.Errorf("model[0] = %+v, want value/label claude-opus-4-8/Claude Opus 4.8", got[0])
	}
	// Empty display_name falls back to the id.
	if got[1].Value != "claude-haiku-4-5" || got[1].Label != "claude-haiku-4-5" {
		t.Errorf("model[1] = %+v, want label to fall back to id", got[1])
	}
}

// TestParseOpenAIModels verifies non-chat models are filtered out and the rest
// are ordered newest-first by created.
func TestParseOpenAIModels(t *testing.T) {
	body := []byte(`{
		"object": "list",
		"data": [
			{"id": "gpt-4o", "created": 100, "object": "model"},
			{"id": "text-embedding-3-small", "created": 200, "object": "model"},
			{"id": "gpt-4o-mini", "created": 300, "object": "model"},
			{"id": "whisper-1", "created": 50, "object": "model"},
			{"id": "gpt-3.5-turbo-instruct", "created": 400, "object": "model"},
			{"id": "o3-mini", "created": 500, "object": "model"},
			{"id": "dall-e-3", "created": 10, "object": "model"},
			{"id": "tts-1", "created": 20, "object": "model"}
		]
	}`)

	got, err := parseOpenAIModels(body)
	if err != nil {
		t.Fatalf("parseOpenAIModels: %v", err)
	}

	wantOrder := []string{"o3-mini", "gpt-4o-mini", "gpt-4o"} // by created desc; non-chat dropped
	if len(got) != len(wantOrder) {
		t.Fatalf("want %d chat models, got %d: %+v", len(wantOrder), len(got), got)
	}
	for i, want := range wantOrder {
		if got[i].Value != want {
			t.Errorf("model[%d] = %q, want %q (full: %+v)", i, got[i].Value, want, got)
		}
	}
}

// TestParseGeminiModels verifies only generateContent-capable models survive and
// the "models/" name prefix is stripped to the bare id.
func TestParseGeminiModels(t *testing.T) {
	body := []byte(`{
		"models": [
			{"name": "models/gemini-2.5-pro", "displayName": "Gemini 2.5 Pro", "supportedGenerationMethods": ["generateContent", "countTokens"]},
			{"name": "models/embedding-001", "displayName": "Embedding 001", "supportedGenerationMethods": ["embedContent"]},
			{"name": "models/gemini-2.0-flash", "displayName": "Gemini 2.0 Flash", "supportedGenerationMethods": ["generateContent"]}
		]
	}`)

	got, err := parseGeminiModels(body)
	if err != nil {
		t.Fatalf("parseGeminiModels: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 chat models, got %d: %+v", len(got), got)
	}
	if got[0].Value != "gemini-2.5-pro" || got[0].Label != "Gemini 2.5 Pro" {
		t.Errorf("model[0] = %+v, want gemini-2.5-pro / Gemini 2.5 Pro (prefix stripped)", got[0])
	}
	if got[1].Value != "gemini-2.0-flash" {
		t.Errorf("model[1] = %+v, want gemini-2.0-flash", got[1])
	}
}
