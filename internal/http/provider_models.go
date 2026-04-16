package http

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// ModelInfo is a normalized model entry returned by the list-models endpoint.
type ModelInfo struct {
	ID        string                        `json:"id"`
	Name      string                        `json:"name,omitempty"`
	Reasoning *providers.ReasoningCapability `json:"reasoning,omitempty"`
}

type ProviderModelsResponse struct {
	Models            []ModelInfo                    `json:"models"`
	ReasoningDefaults *store.ProviderReasoningConfig `json:"reasoning_defaults,omitempty"`
}

// handleListProviderModels proxies to the upstream provider API to list
// available models for the given provider.
//
//	GET /v1/providers/{id}/models
func (h *ProvidersHandler) handleListProviderModels(w http.ResponseWriter, r *http.Request) {
	locale := extractLocale(r)
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidID, "provider")})
		return
	}

	p, err := h.store.GetProvider(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": i18n.T(locale, i18n.MsgNotFound, "provider", id.String())})
		return
	}

	respond := func(models []ModelInfo) {
		writeJSON(w, http.StatusOK, ProviderModelsResponse{
			Models:            models,
			ReasoningDefaults: reasoningDefaultsForModels(p.Settings, models),
		})
	}

	// Claude CLI doesn't need an API key — return hardcoded models
	if p.ProviderType == store.ProviderClaudeCLI {
		respond(claudeCLIModels())
		return
	}

	if p.ProviderType == store.ProviderChatGPTOAuth {
		respond(chatGPTOAuthModels())
		return
	}

	// ACP agents don't need an API key — return hardcoded models
	if p.ProviderType == store.ProviderACP {
		respond(acpModels())
		return
	}

	// Ollama: use native /api/tags for richer metadata (parameter size, quantization, family).
	// ProviderOllama has no API key; ProviderOllamaCloud requires one but both use the same endpoint.
	if p.ProviderType == store.ProviderOllama || p.ProviderType == store.ProviderOllamaCloud {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		apiBase := h.resolveAPIBase(p)
		if apiBase == "" {
			apiBase = "http://localhost:11434"
		}
		models, err := h.fetchOllamaModels(ctx, apiBase, p.APIKey)
		if err != nil {
			slog.Warn("providers.models.ollama", "provider", p.Name, "error", err)
			respond([]ModelInfo{})
			return
		}
		respond(models)
		return
	}

	if p.APIKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgRequired, "API key")})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	var models []ModelInfo

	switch p.ProviderType {
	case "anthropic_native":
		models, err = fetchAnthropicModels(ctx, p.APIKey, h.resolveAPIBase(p))
	case "gemini_native":
		models, err = fetchGeminiModels(ctx, p.APIKey)
	case "bailian":
		models = bailianModels()
	case "dashscope":
		models = dashScopeModels()
	case "minimax_native":
		models = minimaxModels()
	case "suno":
		models = sunoModels()
	default:
		// All other types use OpenAI-compatible /models endpoint
		apiBase := strings.TrimRight(h.resolveAPIBase(p), "/")
		if apiBase == "" {
			apiBase = "https://api.openai.com/v1"
		}
		models, err = fetchOpenAIModels(ctx, apiBase, p.APIKey)
	}

	if err != nil {
		slog.Warn("providers.models", "provider", p.Name, "error", err)
		// Return empty list instead of error — provider may not support /models
		respond([]ModelInfo{})
		return
	}

	respond(withReasoningCapabilities(models))
}

func reasoningDefaultsForModels(
	settings []byte,
	models []ModelInfo,
) *store.ProviderReasoningConfig {
	if len(models) == 0 {
		return nil
	}
	for _, model := range models {
		if model.Reasoning != nil {
			return store.ParseProviderReasoningConfig(settings)
		}
	}
	return nil
}

func withReasoningCapabilities(models []ModelInfo) []ModelInfo {
	result := make([]ModelInfo, 0, len(models))
	for _, model := range models {
		next := model
		next.Reasoning = providers.LookupReasoningCapability(model.ID)
		result = append(result, next)
	}
	return result
}
<<<<<<< HEAD
=======

// bailianModels returns a hardcoded list of models available on the
// Bailian Coding platform (coding-intl.dashscope.aliyuncs.com).
// The platform does not expose a /v1/models endpoint.
func bailianModels() []ModelInfo {
	return []ModelInfo{
		{ID: "qwen3.5-plus", Name: "Qwen 3.5 Plus"},
		{ID: "kimi-k2.5", Name: "Kimi K2.5"},
		{ID: "GLM-5.1", Name: "GLM-5.1 (202K ctx, thinking+tools)"},
		{ID: "GLM-5", Name: "GLM-5"},
		{ID: "MiniMax-M2.5", Name: "MiniMax M2.5"},
		{ID: "qwen3-max-2026-01-23", Name: "Qwen 3 Max (2026-01-23)"},
		{ID: "qwen3-coder-next", Name: "Qwen 3 Coder Next"},
		{ID: "qwen3-coder-plus", Name: "Qwen 3 Coder Plus"},
		{ID: "glm-4.7", Name: "GLM 4.7"},
	}
}

// minimaxModels returns a hardcoded list of MiniMax models.
// MiniMax does not expose a /v1/models endpoint.
func minimaxModels() []ModelInfo {
	return []ModelInfo{
		{ID: "MiniMax-Text-01", Name: "MiniMax Text 01"},
		{ID: "MiniMax-M1", Name: "MiniMax M1"},
		{ID: "MiniMax-M2.5", Name: "MiniMax M2.5"},
	}
}

// claudeCLIModels returns the model aliases accepted by the Claude CLI.
func claudeCLIModels() []ModelInfo {
	return []ModelInfo{
		{ID: "sonnet", Name: "Sonnet"},
		{ID: "opus", Name: "Opus"},
		{ID: "haiku", Name: "Haiku"},
	}
}

// fetchOpenAIModels calls an OpenAI-compatible /models endpoint.
func fetchOpenAIModels(ctx context.Context, apiBase, apiKey string) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", apiBase+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("provider API returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode provider response: %w", err)
	}

	models := make([]ModelInfo, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, ModelInfo{ID: m.ID, Name: m.ID})
	}
	return models, nil
}

>>>>>>> origin/main
