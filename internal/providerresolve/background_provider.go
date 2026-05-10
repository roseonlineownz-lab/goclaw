package providerresolve

import (
	"context"
	"log/slog"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// ResolveBackgroundProvider resolves the LLM provider for background workers.
// Fallback chain: background.provider → agent.default_provider → first registered.
// Used by vault enrichment, episodic summarization, dreaming consolidation.
func ResolveBackgroundProvider(
	ctx context.Context,
	registry *providers.Registry,
	systemConfigs store.SystemConfigStore,
) (providers.Provider, string) {
	if registry == nil {
		return nil, ""
	}

	// Load system configs
	var configs map[string]string
	if systemConfigs != nil {
		var err error
		configs, err = systemConfigs.List(ctx)
		if err != nil {
			slog.Warn("background: failed to load system_configs", "error", err)
		} else {
			slog.Debug("background: loaded system_configs",
				"background.provider", configs["background.provider"],
				"agent.default_provider", configs["agent.default_provider"])
		}
	}

	// tryResolve attempts to get a provider by name
	tryResolve := func(name, model, source string) (providers.Provider, string, bool) {
		if name == "" {
			return nil, "", false
		}
		p, err := registry.GetByName(name)
		if err != nil || p == nil {
			slog.Debug("background: provider not found", "source", source, "name", name, "error", err)
			return nil, "", false
		}
		if model == "" {
			model = p.DefaultModel()
		}
		slog.Debug("background: resolved provider", "source", source, "name", name, "model", model)
		return p, model, true
	}

	// 1. Explicit background config
	if p, m, ok := tryResolve(configs["background.provider"], configs["background.model"], "background.provider"); ok {
		return p, m
	}
	// 2. Agent default provider
	if p, m, ok := tryResolve(configs["agent.default_provider"], configs["agent.default_model"], "agent.default_provider"); ok {
		return p, m
	}
	// 3. First registered provider (fallback)
	names := registry.List()
	if len(names) == 0 {
		slog.Warn("background: no providers available")
		return nil, ""
	}
	p, err := registry.GetByName(names[0])
	if err != nil {
		slog.Warn("background: fallback provider failed", "name", names[0], "error", err)
		return nil, ""
	}
	slog.Warn("background: using fallback provider (no explicit config)",
		"provider", names[0], "available", names,
		"background.provider", configs["background.provider"],
		"agent.default_provider", configs["agent.default_provider"])
	return p, p.DefaultModel()
}
