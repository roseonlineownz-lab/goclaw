package methods

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	httpapi "github.com/nextlevelbuilder/goclaw/internal/http"
	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/workspace"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// SessionsMethods handles sessions.list, sessions.preview, sessions.patch, sessions.delete, sessions.reset.
type SessionsMethods struct {
	sessions      store.SessionStore
	projects      store.ProjectStore
	projectGrants store.ProjectGrantStore
	episodics     store.EpisodicStore
	baseDir       string
	eventBus      bus.EventPublisher
	cfg           *config.Config
}

func NewSessionsMethods(sess store.SessionStore, projectGrants store.ProjectGrantStore, eventBus bus.EventPublisher, cfg *config.Config) *SessionsMethods {
	return &SessionsMethods{sessions: sess, projectGrants: projectGrants, eventBus: eventBus, cfg: cfg}
}

// SetProjectSwitchDeps wires the optional dependencies the
// sessions.updateProject path uses to keep the FS layout coherent on a
// project switch (relocate session subdir + retag session-scoped episodic
// memory). When unset, updateProject still works but only touches the DB
// row — any pre-existing files stay where they were written.
func (m *SessionsMethods) SetProjectSwitchDeps(projects store.ProjectStore, episodics store.EpisodicStore, baseDir string) {
	m.projects = projects
	m.episodics = episodics
	m.baseDir = baseDir
}

func (m *SessionsMethods) Register(router *gateway.MethodRouter) {
	router.Register(protocol.MethodSessionsList, m.handleList)
	router.Register(protocol.MethodSessionsPreview, m.handlePreview)
	router.Register(protocol.MethodSessionsPatch, m.handlePatch)
	router.Register(protocol.MethodSessionsDelete, m.handleDelete)
	router.Register(protocol.MethodSessionsReset, m.handleReset)
	router.Register(protocol.MethodSessionsCompact, m.handleCompact)
	router.Register(protocol.MethodSessionsUpdateProject, m.handleUpdateProject)
}

type sessionsListParams struct {
	AgentID string `json:"agentId"`
	Channel string `json:"channel"` // optional: filter by channel prefix ("ws", "telegram")
	Limit   int    `json:"limit"`
	Offset  int    `json:"offset"`
}

func (m *SessionsMethods) handleList(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var params sessionsListParams
	if req.Params != nil {
		json.Unmarshal(req.Params, &params)
	}

	if params.Limit <= 0 {
		params.Limit = 20
	}

	opts := store.SessionListOpts{
		AgentID: params.AgentID,
		Channel: params.Channel,
		Limit:   params.Limit,
		Offset:  params.Offset,
	}
	// Role-based filtering: admins/owners see all sessions; regular users see only their own.
	// Tenant scope is always applied above — admin sees all sessions within the tenant.
	if !canSeeAll(client.Role(), m.cfg.Gateway.OwnerIDs, client.UserID()) {
		opts.UserID = client.UserID()
	}

	result := m.sessions.ListPagedRich(ctx, opts)
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{
		"sessions": result.Sessions,
		"total":    result.Total,
		"limit":    params.Limit,
		"offset":   params.Offset,
	}))
}

type sessionKeyParams struct {
	Key string `json:"key"`
}

func (m *SessionsMethods) handlePreview(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	locale := store.LocaleFromContext(ctx)
	var params sessionKeyParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidJSON)))
		return
	}

	if !canSeeAll(client.Role(), m.cfg.Gateway.OwnerIDs, client.UserID()) {
		sess := m.sessions.Get(ctx, params.Key)
		if sess == nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrNotFound, i18n.T(locale, i18n.MsgNotFound, "session", params.Key)))
			return
		}
		if sess.UserID != client.UserID() {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, i18n.T(locale, i18n.MsgPermissionDenied, "session")))
			return
		}
	}

	history := m.sessions.GetHistory(ctx, params.Key)
	summary := m.sessions.GetSummary(ctx, params.Key)

	// Sign file URLs before delivery — sessions store clean paths.
	secret := httpapi.FileSigningKey()
	for i := range history {
		history[i].Content = httpapi.SignFileURLs(history[i].Content, secret)
		for j := range history[i].MediaRefs {
			history[i].MediaRefs[j].Path = httpapi.SignMediaPath(history[i].MediaRefs[j].Path, secret)
		}
	}
	summary = httpapi.SignFileURLs(summary, secret)

	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{
		"key":      params.Key,
		"messages": history,
		"summary":  summary,
	}))
}

// handlePatch updates session metadata fields.
// Matching TS sessions.patch (src/gateway/server-methods/sessions.ts:237-287).
func (m *SessionsMethods) handlePatch(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	locale := store.LocaleFromContext(ctx)
	var params struct {
		Key      string            `json:"key"`
		Label    *string           `json:"label,omitempty"`
		Model    *string           `json:"model,omitempty"`
		Metadata map[string]string `json:"metadata,omitempty"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidJSON)))
		return
	}

	if params.Key == "" {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgRequired, "key")))
		return
	}

	if !canSeeAll(client.Role(), m.cfg.Gateway.OwnerIDs, client.UserID()) {
		sess := m.sessions.Get(ctx, params.Key)
		if sess == nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrNotFound, i18n.T(locale, i18n.MsgNotFound, "session", params.Key)))
			return
		}
		if sess.UserID != client.UserID() {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, i18n.T(locale, i18n.MsgPermissionDenied, "session")))
			return
		}
	}

	// Apply label patch
	if params.Label != nil {
		m.sessions.SetLabel(ctx, params.Key, *params.Label)
	}

	// Apply model patch
	if params.Model != nil {
		m.sessions.UpdateMetadata(ctx, params.Key, *params.Model, "", "")
	}

	// Apply metadata patch
	if len(params.Metadata) > 0 {
		m.sessions.SetSessionMetadata(ctx, params.Key, params.Metadata)
	}

	// Save changes to DB
	m.sessions.Save(ctx, params.Key)

	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{
		"ok":  true,
		"key": params.Key,
	}))
	emitAudit(m.eventBus, client, "session.patched", "session", params.Key)
}

func (m *SessionsMethods) handleDelete(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	locale := store.LocaleFromContext(ctx)
	var params sessionKeyParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidJSON)))
		return
	}

	if !canSeeAll(client.Role(), m.cfg.Gateway.OwnerIDs, client.UserID()) {
		sess := m.sessions.Get(ctx, params.Key)
		if sess == nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrNotFound, i18n.T(locale, i18n.MsgNotFound, "session", params.Key)))
			return
		}
		if sess.UserID != client.UserID() {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, i18n.T(locale, i18n.MsgPermissionDenied, "session")))
			return
		}
	}

	if err := m.sessions.Delete(ctx, params.Key); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}

	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{
		"ok": true,
	}))
	emitAudit(m.eventBus, client, "session.deleted", "session", params.Key)
}

func (m *SessionsMethods) handleReset(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	locale := store.LocaleFromContext(ctx)
	var params sessionKeyParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidJSON)))
		return
	}

	if !canSeeAll(client.Role(), m.cfg.Gateway.OwnerIDs, client.UserID()) {
		sess := m.sessions.Get(ctx, params.Key)
		if sess == nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrNotFound, i18n.T(locale, i18n.MsgNotFound, "session", params.Key)))
			return
		}
		if sess.UserID != client.UserID() {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, i18n.T(locale, i18n.MsgPermissionDenied, "session")))
			return
		}
	}

	m.sessions.Reset(ctx, params.Key)

	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{
		"ok": true,
	}))
	emitAudit(m.eventBus, client, "session.reset", "session", params.Key)
}

type sessionCompactParams struct {
	Key      string `json:"key"`
	KeepLast int    `json:"keepLast,omitempty"` // default 4
}

// handleCompact truncates session history to the last N messages.
// Issue 958: Manual session compaction API (truncate-only, no LLM summarization).
func (m *SessionsMethods) handleCompact(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	locale := store.LocaleFromContext(ctx)
	var params sessionCompactParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidJSON)))
		return
	}

	if params.Key == "" {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, "key is required"))
		return
	}

	keepLast := params.KeepLast
	if keepLast <= 0 {
		keepLast = 4 // default: keep last 2 exchanges
	}

	// Auth check
	sess := m.sessions.Get(ctx, params.Key)
	if sess == nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrNotFound, i18n.T(locale, i18n.MsgNotFound, "session", params.Key)))
		return
	}
	if !canSeeAll(client.Role(), m.cfg.Gateway.OwnerIDs, client.UserID()) {
		if sess.UserID != client.UserID() {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, i18n.T(locale, i18n.MsgPermissionDenied, "session")))
			return
		}
	}

	history := m.sessions.GetHistory(ctx, params.Key)
	originalLen := len(history)
	if originalLen < 6 {
		client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{
			"ok":      true,
			"message": "session too short to compact",
			"kept":    originalLen,
		}))
		return
	}

	// Truncate history to last N messages
	m.sessions.TruncateHistory(ctx, params.Key, keepLast)
	m.sessions.IncrementCompaction(ctx, params.Key)
	m.sessions.Save(ctx, params.Key)

	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{
		"ok":       true,
		"original": originalLen,
		"kept":     keepLast,
	}))
	emitAudit(m.eventBus, client, "session.compacted", "session", params.Key)
}

// handleUpdateProject binds or unbinds a session from a project.
// Callers must hold at least member role on the target project.
// Send projectId="" or omit it to clear the binding.
func (m *SessionsMethods) handleUpdateProject(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	locale := store.LocaleFromContext(ctx)
	var params struct {
		Key       string `json:"key"`
		ProjectID string `json:"projectId"` // empty string → clear binding
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidJSON)))
		return
	}

	if params.Key == "" {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgRequired, "key")))
		return
	}

	// Session ownership check for non-admin callers.
	if !canSeeAll(client.Role(), m.cfg.Gateway.OwnerIDs, client.UserID()) {
		sess := m.sessions.Get(ctx, params.Key)
		if sess == nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrNotFound, i18n.T(locale, i18n.MsgNotFound, "session", params.Key)))
			return
		}
		if sess.UserID != client.UserID() {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, i18n.T(locale, i18n.MsgPermissionDenied, "session")))
			return
		}
	}

	// Resolve target project_id (nil = clear binding).
	var projectID *uuid.UUID
	if params.ProjectID != "" {
		pid, err := uuid.Parse(params.ProjectID)
		if err != nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgRequired, "projectId")))
			return
		}

		// Caller must have at least member access to bind a session to a project.
		ok, err := permissions.CanAccessProject(ctx, m.projectGrants, client.UserID(), params.ProjectID, permissions.ProjectRoleMember)
		if err != nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
			return
		}
		if !ok {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, i18n.T(locale, i18n.MsgPermissionDenied, "project")))
			return
		}
		projectID = &pid
	}

	// Route through SwitchSessionProject when FS-side deps are wired so the
	// session subdir relocates atomically with the DB binding flip. When
	// unset (e.g. legacy callers, in-memory tests), fall back to the bare
	// DB UpdateProject — same behaviour as before this method was added.
	var switchErr error
	if m.projects != nil && m.episodics != nil && m.baseDir != "" {
		switchErr = workspace.SwitchSessionProject(ctx, workspace.ProjectSwitchDeps{
			Sessions:  m.sessions,
			Projects:  m.projects,
			Episodics: m.episodics,
			BaseDir:   m.baseDir,
		}, params.Key, projectID)
	} else {
		switchErr = m.sessions.UpdateProject(ctx, params.Key, projectID)
	}
	if switchErr != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, switchErr.Error()))
		return
	}

	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{
		"ok":        true,
		"key":       params.Key,
		"projectId": params.ProjectID,
	}))
	emitAudit(m.eventBus, client, "session.project_updated", "session", params.Key)
}
