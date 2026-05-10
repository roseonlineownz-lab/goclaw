package methods

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// ProjectGrantsMethods owns project_grants.* RPC handlers.
// Permissions: only admin or project owner can list/create/delete grants on a project.
type ProjectGrantsMethods struct {
	projects store.ProjectStore
	grants   store.ProjectGrantStore
	eventBus bus.EventPublisher
	cfg      *config.Config
}

func NewProjectGrantsMethods(projects store.ProjectStore, grants store.ProjectGrantStore, eventBus bus.EventPublisher, cfg *config.Config) *ProjectGrantsMethods {
	return &ProjectGrantsMethods{projects: projects, grants: grants, eventBus: eventBus, cfg: cfg}
}

func (m *ProjectGrantsMethods) Register(router *gateway.MethodRouter) {
	router.Register(protocol.MethodProjectGrantsList, m.HandleList)
	router.Register(protocol.MethodProjectGrantsListInherited, m.HandleListInherited)
	router.Register(protocol.MethodProjectGrantsCreate, m.HandleCreate)
	router.Register(protocol.MethodProjectGrantsDelete, m.HandleDelete)
}

type grantView struct {
	ID        string  `json:"id"`
	ProjectID string  `json:"projectId"`
	UserID    *string `json:"userId,omitempty"`
	TeamID    *string `json:"teamId,omitempty"`
	Role      string  `json:"role"`
	GrantedBy *string `json:"grantedBy,omitempty"`
	CreatedAt string  `json:"createdAt"`
}

func toGrantView(g *store.ProjectGrant) grantView {
	return grantView{
		ID:        g.ID,
		ProjectID: g.ProjectID,
		UserID:    g.UserID,
		TeamID:    g.TeamID,
		Role:      g.Role,
		GrantedBy: g.GrantedBy,
		CreatedAt: g.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

// requireProjectOwnerOrAdmin loads the project and gates by admin OR owner.
// Returns the project (and true) when authorised; otherwise sends an error and returns false.
func (m *ProjectGrantsMethods) requireProjectOwnerOrAdmin(ctx context.Context, client *gateway.Client, reqID, projectID string) (*store.Project, bool) {
	locale := store.LocaleFromContext(ctx)
	pid, err := uuid.Parse(projectID)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(reqID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidID, "projectId")))
		return nil, false
	}
	p, err := m.projects.Get(ctx, pid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			client.SendResponse(protocol.NewErrorResponse(reqID, protocol.ErrNotFound, i18n.T(locale, i18n.MsgNotFound, "project", projectID)))
			return nil, false
		}
		client.SendResponse(protocol.NewErrorResponse(reqID, protocol.ErrInternal, err.Error()))
		return nil, false
	}
	if canSeeAll(client.Role(), m.cfg.Gateway.OwnerIDs, client.UserID()) {
		return p, true
	}
	if client.UserID() != "" && p.OwnerUserID.String() == client.UserID() {
		return p, true
	}
	client.SendResponse(protocol.NewErrorResponse(reqID, protocol.ErrUnauthorized, i18n.T(locale, i18n.MsgPermissionDenied, "project")))
	return nil, false
}

// ---- handleList (direct user grants only) -----------------------------------

type grantsListParams struct {
	ProjectID string `json:"projectId"`
}

func (m *ProjectGrantsMethods) HandleList(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	locale := store.LocaleFromContext(ctx)
	var params grantsListParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidJSON)))
		return
	}
	if params.ProjectID == "" {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgRequired, "projectId")))
		return
	}
	if _, ok := m.requireProjectOwnerOrAdmin(ctx, client, req.ID, params.ProjectID); !ok {
		return
	}
	rows, err := m.grants.List(ctx, params.ProjectID)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	out := make([]grantView, 0, len(rows))
	for _, g := range rows {
		if g.UserID == nil {
			continue // team grants surface via list_inherited
		}
		out = append(out, toGrantView(g))
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"grants": out}))
}

// ---- handleListInherited (team grants only) ---------------------------------

func (m *ProjectGrantsMethods) HandleListInherited(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	locale := store.LocaleFromContext(ctx)
	var params grantsListParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidJSON)))
		return
	}
	if params.ProjectID == "" {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgRequired, "projectId")))
		return
	}
	if _, ok := m.requireProjectOwnerOrAdmin(ctx, client, req.ID, params.ProjectID); !ok {
		return
	}
	rows, err := m.grants.List(ctx, params.ProjectID)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	out := make([]grantView, 0, len(rows))
	for _, g := range rows {
		if g.TeamID == nil {
			continue
		}
		out = append(out, toGrantView(g))
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"grants": out}))
}

// ---- handleCreate -----------------------------------------------------------

type grantCreateParams struct {
	ProjectID string  `json:"projectId"`
	UserID    *string `json:"userId"`
	TeamID    *string `json:"teamId"`
	Role      string  `json:"role"`
}

func validRole(r string) bool {
	return r == "viewer" || r == "member" || r == "editor"
}

func (m *ProjectGrantsMethods) HandleCreate(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	locale := store.LocaleFromContext(ctx)
	var params grantCreateParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidJSON)))
		return
	}
	if params.ProjectID == "" {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgRequired, "projectId")))
		return
	}
	if !validRole(params.Role) {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgProjectGrantInvalidRole)))
		return
	}
	// XOR: exactly one of userId / teamId is required.
	hasUser := params.UserID != nil && *params.UserID != ""
	hasTeam := params.TeamID != nil && *params.TeamID != ""
	if hasUser == hasTeam {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgProjectGrantInvalid)))
		return
	}
	if hasUser {
		if _, err := uuid.Parse(*params.UserID); err != nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidID, "userId")))
			return
		}
	} else {
		if _, err := uuid.Parse(*params.TeamID); err != nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidID, "teamId")))
			return
		}
	}
	if _, ok := m.requireProjectOwnerOrAdmin(ctx, client, req.ID, params.ProjectID); !ok {
		return
	}
	g := &store.ProjectGrant{
		ProjectID: params.ProjectID,
		UserID:    params.UserID,
		TeamID:    params.TeamID,
		Role:      params.Role,
	}
	if granter := client.UserID(); granter != "" {
		g.GrantedBy = &granter
	}
	if err := m.grants.Create(ctx, g); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	emitAudit(m.eventBus, client, "project_grant.created", "project_grant", g.ID)
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"grant": toGrantView(g)}))
}

// ---- handleDelete -----------------------------------------------------------

type grantDeleteParams struct {
	ID string `json:"id"`
}

func (m *ProjectGrantsMethods) HandleDelete(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	locale := store.LocaleFromContext(ctx)
	var params grantDeleteParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidJSON)))
		return
	}
	if params.ID == "" {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgRequired, "id")))
		return
	}
	g, err := m.grants.Get(ctx, params.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrNotFound, i18n.T(locale, i18n.MsgNotFound, "grant", params.ID)))
			return
		}
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	if _, ok := m.requireProjectOwnerOrAdmin(ctx, client, req.ID, g.ProjectID); !ok {
		return
	}
	if err := m.grants.Delete(ctx, params.ID); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	emitAudit(m.eventBus, client, "project_grant.deleted", "project_grant", params.ID)
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"ok": true}))
}
