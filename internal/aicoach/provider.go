package aicoach

import (
	"context"
	"sort"
)

// ModelOption is one selectable model for a provider, exposed to the settings UI.
type ModelOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// CallConfig carries per-invocation parameters to a provider's Call method.
// Providers are stateless — all config flows through here.
type CallConfig struct {
	Model     string
	APIKey    string
	System    string
	User      string
	MaxTokens int
}

// Provider is implemented by each supported LLM backend. Providers self-register
// in init() so adding a new one is a single new file with no edits elsewhere.
type Provider interface {
	Name() string          // stable identifier persisted in DB, e.g. "anthropic"
	Label() string         // user-facing display name, e.g. "Claude"
	Models() []ModelOption // models offered in settings, ordered best-first
	// Stream invokes onChunk for each text delta as the model generates. It
	// returns when the stream ends or the first error occurs.
	Stream(ctx context.Context, cfg CallConfig, onChunk func(string) error) error
}

// ProviderInfo is the JSON-serializable view of a Provider, used to pass
// provider+model metadata to the settings template.
type ProviderInfo struct {
	Name   string        `json:"name"`
	Label  string        `json:"label"`
	Models []ModelOption `json:"models"`
}

var registry = make(map[string]Provider)

// Register adds a provider to the registry. Call from init() in provider files.
// Panics on duplicate registration — a programming error, not runtime condition.
func Register(p Provider) {
	if _, dup := registry[p.Name()]; dup {
		panic("aicoach: duplicate provider registration: " + p.Name())
	}
	registry[p.Name()] = p
}

// Get returns the provider registered under name.
func Get(name string) (Provider, bool) {
	p, ok := registry[name]
	return p, ok
}

// All returns every registered provider, sorted by name for stable ordering.
func All() []Provider {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Provider, len(names))
	for i, n := range names {
		out[i] = registry[n]
	}
	return out
}

// AllInfo returns JSON-serializable metadata for every registered provider,
// suitable for passing directly to templates or API responses.
func AllInfo() []ProviderInfo {
	ps := All()
	out := make([]ProviderInfo, len(ps))
	for i, p := range ps {
		out[i] = ProviderInfo{Name: p.Name(), Label: p.Label(), Models: p.Models()}
	}
	return out
}

// ProviderLabel returns the display name for a provider id, or "" if unknown.
func ProviderLabel(name string) string {
	if p, ok := Get(name); ok {
		return p.Label()
	}
	return ""
}
