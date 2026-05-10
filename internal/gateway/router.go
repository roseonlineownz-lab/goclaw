package gateway

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"slices"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/edition"
	httpapi "github.com/nextlevelbuilder/goclaw/internal/http"
	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// MethodHandler processes a single RPC method request.
type MethodHandler func(ctx context.Context, client *Client, req *protocol.RequestFrame)

// MethodRouter maps method names to handlers.
type MethodRouter struct {
	handlers map[string]MethodHandler
	server   *Server
}

func NewMethodRouter(server *Server) *MethodRouter {
	r := &MethodRouter{
		handlers: make(map[string]MethodHandler),
		server:   server,
	}
	r.registerDefaults()
	return r
}

// Register adds a method handler.
func (r *MethodRouter) Register(method string, handler MethodHandler) {
	r.handlers[method] = handler
}

// Handle dispatches a request to the appropriate handler.
func (r *MethodRouter) Handle(ctx context.Context, client *Client, req *protocol.RequestFrame) {
	handler, ok := r.handlers[req.Method]
	if !ok {
		slog.Warn("unknown method", "method", req.Method, "client", client.id)
		locale := i18n.Normalize(client.locale)
		client.SendResponse(protocol.NewErrorResponse(
			req.ID,
			protocol.ErrInvalidRequest,
			i18n.T(locale, i18n.MsgUnknownMethod, req.Method),
		))
		return
	}

	// Permission check: skip for connect, health, and browser pairing status (used by unauthenticated clients)
	if req.Method != protocol.MethodConnect && req.Method != protocol.MethodHealth && req.Method != protocol.MethodBrowserPairingStatus {
		if pe := r.server.policyEngine; pe != nil {
			if !pe.CanAccess(client.role, req.Method) {
				required := permissions.MethodRole(req.Method)
				slog.Warn("security.permission_denied",
					"method", req.Method,
					"role", client.role,
					"required", string(required),
					"client", client.id,
				)
				locale := i18n.Normalize(client.locale)
				var msg string
				if required == permissions.RoleNone {
					// Unclassified method — fail-closed (issue #866 fix).
					msg = i18n.T(locale, i18n.MsgPermissionDenied, req.Method+" is not permitted for this session")
				} else {
					msg = i18n.T(locale, i18n.MsgPermissionDenied, req.Method+" requires "+string(required)+" role")
				}
				client.SendResponse(protocol.NewErrorResponse(
					req.ID,
					protocol.ErrUnauthorized,
					msg,
				))
				return
			}
		}
	}

	// Inject locale + role into context.
	// Role injection is required so store.IsRootRole / store.IsMasterScope work
	// from WS handlers — without it, ctx-based permission helpers silently
	// evaluate as non-owner. HTTP layer does the same via enrichContext.
	ctx = store.WithLocale(ctx, i18n.Normalize(client.locale))
	if role := client.Role(); role != "" {
		ctx = store.WithRole(ctx, string(role))
	}

	slog.Debug("handling method", "method", req.Method, "client", client.id, "req_id", req.ID)
	handler(ctx, client, req)
}

// registerDefaults registers built-in Phase 1 method handlers.
func (r *MethodRouter) registerDefaults() {
	// System
	r.Register(protocol.MethodConnect, r.handleConnect)
	r.Register(protocol.MethodHealth, r.handleHealth)
	r.Register(protocol.MethodStatus, r.handleStatus)
}

// --- Built-in handlers ---

func (r *MethodRouter) handleConnect(ctx context.Context, client *Client, req *protocol.RequestFrame) {
	// Parse connect params. AccessToken is the v4 password-auth path (HS256
	// JWT issued by /v1/auth/login). Token remains for gateway-token /
	// API-key compatibility. tenant_* params kept as no-ops for backward
	// compat during the desktop/web client rollout — silently ignored.
	var params struct {
		Token       string `json:"token"`
		AccessToken string `json:"accessToken"`  // v4 JWT access token from /v1/auth/login
		UserID      string `json:"user_id"`
		SenderID    string `json:"sender_id"`    // browser pairing: stored sender ID for reconnect
		Locale      string `json:"locale"`       // user's preferred locale (en, vi, zh)
		TenantHint  string `json:"tenant_hint"`  // ignored in v4 single-tenant
		TenantScope string `json:"tenant_scope"` // ignored in v4 single-tenant
	}
	if req.Params != nil {
		json.Unmarshal(req.Params, &params)
	}

	// Set locale on client (persists across all requests for this connection)
	client.locale = i18n.Normalize(params.Locale)

	configToken := r.server.cfg.Gateway.Token

	// Path 1: Valid gateway token → admin (constant-time comparison)
	if configToken != "" && subtle.ConstantTimeCompare([]byte(params.Token), []byte(configToken)) == 1 {
		client.role = permissions.RoleAdmin
		client.authenticated = true
		client.userID = params.UserID

		// Owner IDs get RoleRoot; others keep RoleAdmin. Single-tenant model
		// (v4): always master scope.
		if isOwnerID(params.UserID, r.server.cfg.Gateway.OwnerIDs) {
			client.role = permissions.RoleRoot
		}
		r.sendConnectResponse(ctx, client, req.ID)
		return
	}

	// Path 1a: JWT access token (v4 multi-user auth). Issued by
	// /v1/auth/login or /v1/bootstrap/init. Carries Sub (user UUID) + Role.
	if params.AccessToken != "" {
		if claims, ok := httpapi.ResolveJWTAccess(params.AccessToken); ok {
			client.role = permissions.Role(claims.Role)
			client.authenticated = true
			client.userID = claims.Sub
			slog.Debug("security.ws_connect_resolved_jwt",
				"client", client.id,
				"role", string(client.role),
				"sub", claims.Sub,
			)
			r.sendConnectResponse(ctx, client, req.ID)
			return
		}
		// Invalid JWT → reject (do NOT fall through to API-key path; the param
		// name is explicit so a wrong/expired JWT must fail closed).
		locale := i18n.Normalize(client.locale)
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized,
			i18n.T(locale, i18n.MsgAccessTokenInvalid)))
		return
	}

	// Path 1b: API key → role derived from scopes (uses shared cache)
	if params.Token != "" {
		if keyData, role := httpapi.ResolveAPIKey(ctx, params.Token); keyData != nil {
			scopes := make([]permissions.Scope, len(keyData.Scopes))
			for i, s := range keyData.Scopes {
				scopes[i] = permissions.Scope(s)
			}
			client.role = role
			client.scopes = scopes
			client.authenticated = true
			// If the key has a bound owner, force user_id to owner_id.
			if keyData.OwnerID != "" {
				if params.UserID != "" && params.UserID != keyData.OwnerID {
					slog.Warn("security.ws_api_key_owner_override",
						"param_user_id", params.UserID,
						"owner_id", keyData.OwnerID,
					)
				}
				client.userID = keyData.OwnerID
			} else {
				client.userID = params.UserID
			}
			slog.Debug("security.ws_connect_resolved",
				"client", client.id,
				"role", string(client.role),
			)
			r.sendConnectResponse(ctx, client, req.ID)
			return
		}
	}

	// Path 2: No token configured → operator (backward compat)
	if configToken == "" {
		client.role = permissions.RoleMember
		client.authenticated = true
		client.userID = params.UserID
		r.sendConnectResponse(ctx, client, req.ID)
		return
	}

	// Path 3: Token configured but not provided/wrong → check browser pairing
	ps := r.server.pairingService

	// Path 3a: Reconnecting with a previously-paired sender_id
	if ps != nil && params.SenderID != "" {
		paired, pairErr := ps.IsPaired(ctx, params.SenderID, "browser")
		if pairErr != nil {
			slog.Warn("security.pairing_check_failed",
				"sender_id", params.SenderID, "error", pairErr)
			// Fail-closed: deny access on DB error instead of granting operator role.
			locale := i18n.Normalize(client.locale)
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal,
				i18n.T(locale, i18n.MsgInternalError, pairErr.Error())))
			return
		}
		if paired {
			client.role = permissions.RoleMember
			client.authenticated = true
			client.userID = params.UserID
			client.pairedSenderID = params.SenderID
			client.pairedChannel = "browser"
			slog.Info("browser pairing authenticated", "sender_id", params.SenderID, "client", client.id)
			r.sendConnectResponse(ctx, client, req.ID)
			return
		}
	}

	// Path 3b: No token, no valid pairing → initiate browser pairing (if service available)
	if ps != nil && params.Token == "" {
		code, err := ps.RequestPairing(ctx, client.id, "browser", "", "default", nil)
		if err != nil {
			slog.Warn("browser pairing request failed", "error", err, "client", client.id)
			// Fall through to viewer role
		} else {
			client.pairingCode = code
			client.pairingPending = true
			// Not authenticated — can only call browser.pairing.status
			client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{
				"protocol":     protocol.ProtocolVersion,
				"status":       "pending_pairing",
				"pairing_code": code,
				"sender_id":    client.id,
				"server": map[string]any{
					"name":    "goclaw",
					"version": r.server.version,
				},
			}))
			return
		}
	}

	// Path 4: Fail-closed — no valid token, no valid pairing → reject.
	// Previously fell back to viewer+authenticated=true, which allowed any
	// unauthenticated client to exercise the default-permit policy (CVE #866).
	slog.Warn("security.ws_connect_rejected",
		"reason", "no_valid_credentials",
		"client", client.id,
		"has_token", params.Token != "",
		"has_sender_id", params.SenderID != "",
	)
	client.authenticated = false
	locale := i18n.Normalize(client.locale)
	client.SendResponse(protocol.NewErrorResponse(
		req.ID,
		protocol.ErrUnauthorized,
		i18n.T(locale, i18n.MsgPermissionDenied, "valid token or active pairing required"),
	))
}

func (r *MethodRouter) sendConnectResponse(ctx context.Context, client *Client, reqID string) {
	// Build scoped ctx so store.IsMasterScope can check the role.
	scopedCtx := ctx
	if client.IsOwner() {
		scopedCtx = store.WithRole(scopedCtx, store.RoleRoot)
	}
	resp := map[string]any{
		"protocol":        protocol.ProtocolVersion,
		"role":            string(client.role),
		"user_id":         client.userID,
		"is_owner":        client.IsOwner(),
		"is_master_scope": store.IsMasterScope(scopedCtx),
		"edition":         edition.Current().Name,
		"server": map[string]any{
			"name":    "goclaw",
			"version": r.server.version,
		},
	}

	client.SendResponse(protocol.NewOKResponse(reqID, resp))
}

// isOwnerID checks if the given user ID is in the configured owner list.
// If no owner IDs configured, only "system" is treated as owner (fail-closed).
func isOwnerID(userID string, ownerIDs []string) bool {
	if userID == "" {
		return false
	}
	if len(ownerIDs) == 0 {
		return userID == "system"
	}
	return slices.Contains(ownerIDs, userID)
}

func (r *MethodRouter) handleHealth(ctx context.Context, client *Client, req *protocol.RequestFrame) {
	s := r.server
	uptimeMs := time.Since(s.startedAt).Milliseconds()

	mode := "managed"

	// Database status (real ping)
	dbStatus := "n/a"
	if s.db != nil {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := s.db.PingContext(pingCtx); err != nil {
			dbStatus = "error"
		} else {
			dbStatus = "ok"
		}
	}

	// Connected clients list
	type clientInfo struct {
		ID          string `json:"id"`
		RemoteAddr  string `json:"remoteAddr"`
		UserID      string `json:"userId"`
		Role        string `json:"role"`
		ConnectedAt string `json:"connectedAt"`
	}
	clients := s.ClientList()
	clientList := make([]clientInfo, 0, len(clients))
	for _, c := range clients {
		clientList = append(clientList, clientInfo{
			ID:          c.ID(),
			RemoteAddr:  c.RemoteAddr(),
			UserID:      c.UserID(),
			Role:        string(c.Role()),
			ConnectedAt: c.ConnectedAt().UTC().Format(time.RFC3339),
		})
	}

	// Tool count
	toolCount := 0
	if s.tools != nil {
		toolCount = s.tools.Count()
	}

	resp := map[string]any{
		"status":    "ok",
		"version":   s.version,
		"uptime":    uptimeMs,
		"mode":      mode,
		"database":  dbStatus,
		"tools":     toolCount,
		"clients":   clientList,
		"currentId": client.ID(),
	}
	if s.updateChecker != nil {
		if info := s.updateChecker.Info(); info != nil {
			resp["latestVersion"] = info.LatestVersion
			resp["updateAvailable"] = info.UpdateAvailable
			resp["updateUrl"] = info.UpdateURL
			resp["releaseNotes"] = info.ReleaseNotes
		}
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, resp))
}

func (r *MethodRouter) handleStatus(ctx context.Context, client *Client, req *protocol.RequestFrame) {
	agents := r.server.agents.ListInfo()

	sessionCount := 0
	if r.server.sessions != nil {
		sessionCount = len(r.server.sessions.List(ctx, ""))
	}

	// Agents are lazily resolved — router only has loaded agents.
	// Query the DB store for the real total count.
	agentTotal := len(agents)
	if r.server.agentStore != nil {
		if dbAgents, err := r.server.agentStore.List(ctx, ""); err == nil && len(dbAgents) > agentTotal {
			agentTotal = len(dbAgents)
		}
	}

	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{
		"agents":     agents,
		"agentTotal": agentTotal,
		"clients":    len(r.server.clients),
		"sessions":   sessionCount,
	}))
}
