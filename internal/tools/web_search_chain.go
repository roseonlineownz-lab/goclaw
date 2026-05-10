package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// web_search_chain.go — provider chain resolution.
//
// On every Execute() call, resolveChain() returns the ordered slice of
// SearchProviders. A 60-second TTL cache amortizes DB reads
// (≈1 round-trip/min on cache miss).
//
// Chain construction rules (buildChainFromStorage):
//  1. Parse settings from ctx (builtin_tool_configs.settings).
//  2. Use NormalizeWebSearchProviderOrder to determine iteration order.
//  3. DDG is always appended last — force-enabled, no API key required.
//  4. For other providers: skip if explicitly disabled, or if no API
//     key found in config_secrets.
//
// Settings schema (stored in builtin_tool_configs.settings):
//
//	{
//	  "provider_order": ["brave", "exa"],     // optional reorder
//	  "brave":      { "enabled": false },     // optional per-provider disable
//	  "duckduckgo": { "enabled": true }
//	}

// chainCacheTTL is the cache TTL for the provider chain.
const chainCacheTTL = 60 * time.Second

// webSearchChainCache holds a single resolved provider chain with TTL.
type webSearchChainCache struct {
	mu       sync.Mutex
	chain    []SearchProvider
	hasEntry bool
	expires  time.Time
	now      func() time.Time // injected for testing; defaults to time.Now
}

func newWebSearchChainCache() *webSearchChainCache {
	return &webSearchChainCache{now: time.Now}
}

// Get returns the cached chain if it exists and has not expired.
func (c *webSearchChainCache) Get() ([]SearchProvider, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.hasEntry || c.now().After(c.expires) {
		return nil, false
	}
	return c.chain, true
}

// Set stores the chain with the configured TTL.
func (c *webSearchChainCache) Set(chain []SearchProvider) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.chain = chain
	c.hasEntry = true
	c.expires = c.now().Add(chainCacheTTL)
}

// Invalidate drops the cached chain. Called on admin writes that may shift
// provider availability (e.g. API-key rotation).
func (c *webSearchChainCache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.chain = nil
	c.hasEntry = false
}

// WebSearchProviderOverride is the per-provider override envelope. Only
// non-nil fields override the default. Unknown fields in the JSON blob are
// ignored to stay forward-compatible with future tuning knobs.
type WebSearchProviderOverride struct {
	Enabled    *bool `json:"enabled,omitempty"`
	MaxResults int   `json:"max_results,omitempty"`
}

// WebSearchChainOverride is the full tenant settings shape for web_search.
// All fields optional — an empty/nil override results in the default chain.
type WebSearchChainOverride struct {
	ProviderOrder []string                             `json:"provider_order,omitempty"`
	Providers     map[string]WebSearchProviderOverride `json:"-"`
	// Per-provider sections are unmarshaled into Providers via custom logic
	// below so admins can keep the natural JSON shape:
	//   { "brave": {...}, "duckduckgo": {...} }
}

// UnmarshalJSON accepts the flat admin-facing shape:
//
//	{ "provider_order": [...], "brave": {...}, "duckduckgo": {...} }
//
// Keeps ProviderOrder top-level and collects every other object field into
// the Providers map keyed by provider name.
func (w *WebSearchChainOverride) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if orderRaw, ok := raw["provider_order"]; ok {
		if err := json.Unmarshal(orderRaw, &w.ProviderOrder); err != nil {
			return err
		}
		delete(raw, "provider_order")
	}
	if len(raw) > 0 {
		w.Providers = make(map[string]WebSearchProviderOverride, len(raw))
		for name, blob := range raw {
			var po WebSearchProviderOverride
			if err := json.Unmarshal(blob, &po); err != nil {
				// Skip unknown keys that aren't provider overrides (forward-compat).
				slog.Debug("web_search: skipping unrecognized override key", "key", name, "error", err)
				continue
			}
			w.Providers[name] = po
		}
	}
	return nil
}

// resolveChain returns the ordered provider slice.
// It checks the TTL cache first; on miss it calls BuildChainFromStorage.
func (t *WebSearchTool) resolveChain(ctx context.Context) []SearchProvider {
	if chain, ok := t.chainCache.Get(); ok {
		return chain
	}

	chain := BuildChainFromStorage(ctx, t.secrets)
	t.chainCache.Set(chain)
	return chain
}

// BuildChainFromStorage constructs the provider chain for the current
// request by combining settings from ctx with API keys from secrets.
// Exported for testing.
func BuildChainFromStorage(ctx context.Context, secrets store.ConfigSecretsStore) []SearchProvider {
	// Parse tenant override (may be nil/empty → all defaults).
	var override WebSearchChainOverride
	settings := BuiltinToolSettingsFromCtx(ctx)
	if raw, ok := settings["web_search"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &override); err != nil {
			slog.Warn("web_search: failed to parse tenant override, using defaults", "error", err)
		}
	}

	// isDisabled returns true if the tenant explicitly disabled a provider.
	isDisabled := func(name string) bool {
		if po, ok := override.Providers[name]; ok {
			return po.Enabled != nil && !*po.Enabled
		}
		return false
	}

	order := NormalizeWebSearchProviderOrder(override.ProviderOrder)

	var chain []SearchProvider
	for _, name := range order {
		if name == searchProviderDuckDuckGo {
			// DDG is force-enabled — always last, no API key needed.
			maxResults := defaultSearchCount
			if po, ok := override.Providers[name]; ok && po.MaxResults > 0 {
				maxResults = po.MaxResults
			}
			chain = append(chain, buildProviderByName(name, "", maxResults))
			continue
		}

		if isDisabled(name) {
			continue
		}

		key, err := secrets.Get(ctx, "tools.web."+name+".api_key")
		if err != nil || key == "" {
			// No key → provider not configured for this tenant; skip silently.
			continue
		}

		maxResults := defaultSearchCount
		if po, ok := override.Providers[name]; ok && po.MaxResults > 0 {
			maxResults = po.MaxResults
		}

		p := buildProviderByName(name, key, maxResults)
		if p == nil {
			slog.Warn("web_search: unknown provider name in chain", "name", name)
			continue
		}
		chain = append(chain, p)
	}

	return chain
}
