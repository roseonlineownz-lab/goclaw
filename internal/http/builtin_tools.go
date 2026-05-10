package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// BuiltinToolsHandler handles built-in tool management endpoints.
// Built-in tools are seeded at startup; only enabled and settings are editable.
type BuiltinToolsHandler struct {
	store        store.BuiltinToolStore
	secretsStore store.ConfigSecretsStore
	msgBus       *bus.MessageBus
}

// NewBuiltinToolsHandler creates a handler for built-in tool management endpoints.
func NewBuiltinToolsHandler(s store.BuiltinToolStore, secretsStore store.ConfigSecretsStore, msgBus *bus.MessageBus) *BuiltinToolsHandler {
	return &BuiltinToolsHandler{store: s, secretsStore: secretsStore, msgBus: msgBus}
}

// toolSecretKeys maps (tool_name, settings field path) → config_secrets key.
// When saving settings, if a settings blob contains these fields, they are
// extracted, saved to config_secrets, and stripped from the persisted settings.
var toolSecretKeys = map[string]map[string]string{
	"web_search": {
		"exa.api_key":    "tools.web.exa.api_key",
		"tavily.api_key": "tools.web.tavily.api_key",
		"brave.api_key":  "tools.web.brave.api_key",
	},
}

// RegisterRoutes registers all built-in tool routes on the given mux.
func (h *BuiltinToolsHandler) RegisterRoutes(mux *http.ServeMux) {
	// Builtin tools (reads: viewer+, writes: admin+)
	mux.HandleFunc("GET /v1/tools/builtin", h.auth(h.handleList))
	mux.HandleFunc("GET /v1/tools/builtin/{name}", h.auth(h.handleGet))
	mux.HandleFunc("PUT /v1/tools/builtin/{name}", h.adminAuth(h.handleUpdate))
}

func (h *BuiltinToolsHandler) auth(next http.HandlerFunc) http.HandlerFunc {
	return requireAuth("", next)
}

func (h *BuiltinToolsHandler) adminAuth(next http.HandlerFunc) http.HandlerFunc {
	return requireAuth(permissions.RoleAdmin, next)
}

// emitCacheInvalidate broadcasts a cache invalidation event for a builtin tool.
func (h *BuiltinToolsHandler) emitCacheInvalidate(key string) {
	if h.msgBus == nil {
		return
	}
	h.msgBus.Broadcast(bus.Event{
		Name:    protocol.EventCacheInvalidate,
		Payload: bus.CacheInvalidatePayload{Kind: bus.CacheKindBuiltinTools, Key: key},
	})
}

// extractAndSaveSecrets extracts secret fields from a tool settings blob,
// saves them to config_secrets, and returns the cleaned settings with
// secrets stripped. Secret fields are identified by toolSecretKeys.
func (h *BuiltinToolsHandler) extractAndSaveSecrets(ctx context.Context, toolName string, raw json.RawMessage) json.RawMessage {
	if h.secretsStore == nil {
		return raw
	}
	mapping, ok := toolSecretKeys[toolName]
	if !ok || len(raw) == 0 {
		return raw
	}

	var settings map[string]json.RawMessage
	if err := json.Unmarshal(raw, &settings); err != nil {
		return raw
	}

	modified := false
	for settingsPath, secretKey := range mapping {
		parts := strings.SplitN(settingsPath, ".", 2)
		if len(parts) != 2 {
			continue
		}
		section, field := parts[0], parts[1]

		sectionRaw, ok := settings[section]
		if !ok {
			continue
		}

		var sectionMap map[string]json.RawMessage
		if err := json.Unmarshal(sectionRaw, &sectionMap); err != nil {
			continue
		}

		keyRaw, ok := sectionMap[field]
		if !ok {
			continue
		}

		var keyValue string
		if err := json.Unmarshal(keyRaw, &keyValue); err != nil {
			continue
		}

		// Strip field regardless; save only non-empty, non-masked values
		delete(sectionMap, field)
		if rebuilt, err := json.Marshal(sectionMap); err == nil {
			settings[section] = rebuilt
		}
		modified = true

		if keyValue == "" || keyValue == "***" {
			continue
		}

		if err := h.secretsStore.Set(ctx, secretKey, keyValue); err != nil {
			slog.Warn("failed to save tool secret", "tool", toolName, "key", secretKey, "error", err)
		}
	}

	if !modified {
		return raw
	}
	cleaned, err := json.Marshal(settings)
	if err != nil {
		return raw
	}
	return cleaned
}

// getSecretsStatus returns which secret keys are set for a tool (boolean only, never raw values).
func (h *BuiltinToolsHandler) getSecretsStatus(ctx context.Context, toolName string) map[string]bool {
	if h.secretsStore == nil {
		return nil
	}
	mapping, ok := toolSecretKeys[toolName]
	if !ok {
		return nil
	}

	status := make(map[string]bool, len(mapping))
	for _, secretKey := range mapping {
		val, err := h.secretsStore.Get(ctx, secretKey)
		status[secretKey] = err == nil && val != ""
	}
	return status
}

func (h *BuiltinToolsHandler) handleList(w http.ResponseWriter, r *http.Request) {
	result, err := h.store.List(r.Context())
	if err != nil {
		slog.Error("builtin_tools.list", "error", err)
		locale := extractLocale(r)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(locale, i18n.MsgFailedToList, "tools")})
		return
	}

	// Include secrets_set for tools that have secrets.
	type toolWithSecrets struct {
		store.BuiltinToolDef
		SecretsSet map[string]bool `json:"secrets_set,omitempty"`
	}
	enriched := make([]toolWithSecrets, len(result))
	for i, t := range result {
		enriched[i] = toolWithSecrets{BuiltinToolDef: t}
		if _, hasSecrets := toolSecretKeys[t.Name]; hasSecrets {
			enriched[i].SecretsSet = h.getSecretsStatus(r.Context(), t.Name)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tools": enriched})
}

func (h *BuiltinToolsHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	locale := extractLocale(r)
	name := r.PathValue("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgRequired, "name")})
		return
	}

	def, err := h.store.Get(r.Context(), name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": i18n.T(locale, i18n.MsgNotFound, "tool", name)})
		return
	}

	writeJSON(w, http.StatusOK, def)
}

func (h *BuiltinToolsHandler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	// builtin_tools is a global table; writes must be restricted to master-scope callers.
	// Non-owner users must go through PUT /v1/tools/builtin/{name}/tenant-config instead.
	// See plans/reports/debugger-260412-0922-tenant-scope-audit.md CRITICAL-1.
	if !requireMasterScope(w, r) {
		return
	}
	locale := extractLocale(r)
	name := r.PathValue("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgRequired, "name")})
		return
	}

	var updates map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&updates); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidJSON)})
		return
	}

	// Only allow enabled and settings to be updated
	allowed := make(map[string]any)
	if v, ok := updates["enabled"]; ok {
		allowed["enabled"] = v
	}
	if v, ok := updates["settings"]; ok {
		// Extract secrets before saving settings
		if settingsRaw, err := json.Marshal(v); err == nil {
			cleaned := h.extractAndSaveSecrets(r.Context(), name, settingsRaw)
			var cleanedMap any
			if err2 := json.Unmarshal(cleaned, &cleanedMap); err2 == nil {
				allowed["settings"] = cleanedMap
			} else {
				allowed["settings"] = v
			}
		} else {
			allowed["settings"] = v
		}
	}

	if len(allowed) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidUpdates)})
		return
	}

	if err := h.store.Update(r.Context(), name, allowed); err != nil {
		slog.Error("builtin_tools.update", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	emitAudit(h.msgBus, r, "builtin_tool.updated", "builtin_tool", name)
	h.emitCacheInvalidate(name)
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

