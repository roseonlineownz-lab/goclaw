package http

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// ToolsInvokeHandler handles POST /v1/tools/invoke (direct tool invocation).
type ToolsInvokeHandler struct {
	registry   *tools.Registry
	token      string
	agentStore store.AgentStore // nil if not configured
}

// NewToolsInvokeHandler creates a handler for the tools invoke endpoint.
func NewToolsInvokeHandler(registry *tools.Registry, token string, agentStore store.AgentStore) *ToolsInvokeHandler {
	return &ToolsInvokeHandler{
		registry:   registry,
		token:      token,
		agentStore: agentStore,
	}
}

type toolsInvokeRequest struct {
	Tool       string                 `json:"tool"`
	Action     string                 `json:"action,omitempty"`
	Args       map[string]interface{} `json:"args"`
	SessionKey string                 `json:"sessionKey,omitempty"`
	AgentID    string                 `json:"agentId,omitempty"`
	DryRun     bool                   `json:"dryRun,omitempty"`
	Channel    string                 `json:"channel,omitempty"`  // tool context: channel name
	ChatID     string                 `json:"chatId,omitempty"`   // tool context: chat ID
	PeerKind   string                 `json:"peerKind,omitempty"` // tool context: "direct" or "group"
}

func (h *ToolsInvokeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.token != "" {
		if extractBearerToken(r) != h.token {
			http.Error(w, `{"error":{"code":"UNAUTHORIZED","message":"Invalid token"}}`, http.StatusUnauthorized)
			return
		}
	}

	// ClawSec HMAC Validation (if secret is configured)
	clawSecSecret := os.Getenv("CLAWSEC_SECRET")
	if clawSecSecret == "" {
		// Fallback for user preference
		clawSecSecret = "claw-sec-ultra-secure-key-2026"
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":{"code":"BAD_REQUEST","message":"could not read body"}}`, http.StatusBadRequest)
		return
	}

	// Re-assign body for JSON decoder later
	r.Body = io.NopCloser(strings.NewReader(string(bodyBytes)))

	signature := r.Header.Get("X-Claw-Signature")
	if signature != "" {
		mac := hmac.New(sha256.New, []byte(clawSecSecret))
		mac.Write(bodyBytes)
		expectedMAC := hex.EncodeToString(mac.Sum(nil))

		if signature != expectedMAC {
			slog.Warn("clawsec.invalid_signature", "expected", expectedMAC, "provided", signature)
			http.Error(w, `{"error":{"code":"FORBIDDEN","message":"ClawSec: Invalid HMAC signature"}}`, http.StatusForbidden)
			return
		}
	} else if os.Getenv("CLAWSEC_ENFORCE") == "1" {
		http.Error(w, `{"error":{"code":"FORBIDDEN","message":"ClawSec: Signature required"}}`, http.StatusForbidden)
		return
	}

	var req toolsInvokeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"code":"BAD_REQUEST","message":"%s"}}`, err), http.StatusBadRequest)
		return
	}

	if req.Tool == "" {
		http.Error(w, `{"error":{"code":"BAD_REQUEST","message":"tool is required"}}`, http.StatusBadRequest)
		return
	}

	slog.Info("tools invoke request", "tool", req.Tool, "dry_run", req.DryRun)

	if req.DryRun {
		// Just check if tool exists and return its schema
		tool, ok := h.registry.Get(req.Tool)
		if !ok {
			writeToolError(w, http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("Tool '%s' not found", req.Tool))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"tool":        req.Tool,
			"description": tool.Description(),
			"parameters":  tool.Parameters(),
			"dryRun":      true,
		})
		return
	}

	// Inject userID and agentID into context for interceptors (bootstrap, memory).
	ctx := r.Context()

	if userID := extractUserID(r); userID != "" {
		ctx = store.WithUserID(ctx, userID)
	}

	agentIDStr := req.AgentID
	if agentIDStr == "" {
		agentIDStr = extractAgentID(r, "")
	}
	if agentIDStr != "" && h.agentStore != nil {
		ag, err := h.agentStore.GetByKey(ctx, agentIDStr)
		if err == nil {
			ctx = store.WithAgentID(ctx, ag.ID)
		}
	}

	// Inject tool context keys (channel, chatID, peerKind) for message routing.
	if req.Channel != "" {
		ctx = tools.WithToolChannel(ctx, req.Channel)
	}
	if req.ChatID != "" {
		ctx = tools.WithToolChatID(ctx, req.ChatID)
	}
	if req.PeerKind != "" {
		ctx = tools.WithToolPeerKind(ctx, req.PeerKind)
	}

	// Execute the tool
	args := req.Args
	if args == nil {
		args = make(map[string]interface{})
	}

	// If action is specified, add it to args
	if req.Action != "" {
		args["action"] = req.Action
	}

	result := h.registry.ExecuteWithContext(ctx, req.Tool, args, "http", "api", "direct", "", nil)

	if result.IsError {
		writeToolError(w, http.StatusBadRequest, "TOOL_ERROR", result.ForLLM)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"result": map[string]interface{}{
			"output":   result.ForLLM,
			"forUser":  result.ForUser,
			"metadata": map[string]interface{}{},
		},
	})
}

func writeToolError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}
