package methods

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// ProjectsMethods owns projects.* RPC handlers.
// Permissions: admin sees/edits all; non-admin sees own + grants; owner/admin can mutate.
type ProjectsMethods struct {
	projects store.ProjectStore
	grants   store.ProjectGrantStore
	eventBus bus.EventPublisher
	cfg      *config.Config
}

func NewProjectsMethods(projects store.ProjectStore, grants store.ProjectGrantStore, eventBus bus.EventPublisher, cfg *config.Config) *ProjectsMethods {
	return &ProjectsMethods{projects: projects, grants: grants, eventBus: eventBus, cfg: cfg}
}

func (m *ProjectsMethods) Register(router *gateway.MethodRouter) {
	router.Register(protocol.MethodProjectsList, m.HandleList)
	router.Register(protocol.MethodProjectsGet, m.HandleGet)
	router.Register(protocol.MethodProjectsCreate, m.HandleCreate)
	router.Register(protocol.MethodProjectsUpdateMetadata, m.HandleUpdateMetadata)
	router.Register(protocol.MethodProjectsUpdateStatus, m.HandleUpdateStatus)
	router.Register(protocol.MethodProjectsDelete, m.HandleDelete)
}

// slugRE enforces kebab-case lowercase per project requirement (matches BE G3/G7 invariant).
var slugRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// projectView is the JSON-serialised shape returned to clients (camelCase).
type projectView struct {
	ID          string          `json:"id"`
	Slug        string          `json:"slug"`
	OwnerUserID string          `json:"ownerUserId"`
	Status      string          `json:"status"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
	CreatedAt   string          `json:"createdAt"`
	UpdatedAt   string          `json:"updatedAt"`
}

func toProjectView(p *store.Project) projectView {
	return projectView{
		ID:          p.ID.String(),
		Slug:        p.Slug,
		OwnerUserID: p.OwnerUserID.String(),
		Status:      p.Status,
		Metadata:    p.Metadata,
		CreatedAt:   p.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		UpdatedAt:   p.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

// ---- handleList ----------------------------------------------------------

type projectsListParams struct {
	OwnerUserID string `json:"ownerUserId"`
	Status      string `json:"status"`
}

func (m *ProjectsMethods) HandleList(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	locale := store.LocaleFromContext(ctx)
	var params projectsListParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidJSON)))
			return
		}
	}

	filter := store.ListProjectsFilter{Status: params.Status}
	admin := canSeeAll(client.Role(), m.cfg.Gateway.OwnerIDs, client.UserID())
	if !admin {
		// Non-admin: union of (owned projects) + (projects with direct grants).
		ownerID, err := uuid.Parse(client.UserID())
		if err != nil {
			client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"projects": []projectView{}}))
			return
		}
		filter.OwnerUserID = ownerID
	} else if params.OwnerUserID != "" {
		oid, err := uuid.Parse(params.OwnerUserID)
		if err != nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidID, "ownerUserId")))
			return
		}
		filter.OwnerUserID = oid
	}

	owned, err := m.projects.List(ctx, filter)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}

	views := make([]projectView, 0, len(owned))
	seen := map[string]struct{}{}
	for _, p := range owned {
		seen[p.ID.String()] = struct{}{}
		views = append(views, toProjectView(p))
	}

	if !admin {
		// Append projects accessible via direct grant (owned set already covers owner role).
		grants, gerr := m.grants.ListForUser(ctx, client.UserID())
		if gerr == nil {
			for _, g := range grants {
				if _, dup := seen[g.ProjectID]; dup {
					continue
				}
				pid, perr := uuid.Parse(g.ProjectID)
				if perr != nil {
					continue
				}
				p, perr := m.projects.Get(ctx, pid)
				if perr != nil {
					continue
				}
				if params.Status != "" && p.Status != params.Status {
					continue
				}
				seen[p.ID.String()] = struct{}{}
				views = append(views, toProjectView(p))
			}
		}
	}

	sort.Slice(views, func(i, j int) bool { return views[i].CreatedAt > views[j].CreatedAt })
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"projects": views}))
}

// ---- handleGet -----------------------------------------------------------

type projectIDParam struct {
	ID string `json:"id"`
}

func (m *ProjectsMethods) HandleGet(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	locale := store.LocaleFromContext(ctx)
	var params projectIDParam
	if err := json.Unmarshal(req.Params, &params); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidJSON)))
		return
	}
	pid, err := uuid.Parse(params.ID)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidID, "id")))
		return
	}
	p, err := m.projects.Get(ctx, pid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrNotFound, i18n.T(locale, i18n.MsgNotFound, "project", params.ID)))
			return
		}
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	if !m.canRead(ctx, client, p) {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, i18n.T(locale, i18n.MsgPermissionDenied, "project")))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"project": toProjectView(p)}))
}

// canRead — admin OR owner OR any direct grant on the project.
func (m *ProjectsMethods) canRead(ctx context.Context, client *gateway.Client, p *store.Project) bool {
	if canSeeAll(client.Role(), m.cfg.Gateway.OwnerIDs, client.UserID()) {
		return true
	}
	if client.UserID() == "" {
		return false
	}
	if p.OwnerUserID.String() == client.UserID() {
		return true
	}
	ok, _ := permissions.CanAccessProject(ctx, m.grants, client.UserID(), p.ID.String(), permissions.ProjectRoleViewer)
	return ok
}

// canMutate — admin OR project owner only.
func (m *ProjectsMethods) canMutate(client *gateway.Client, p *store.Project) bool {
	if canSeeAll(client.Role(), m.cfg.Gateway.OwnerIDs, client.UserID()) {
		return true
	}
	return client.UserID() != "" && p.OwnerUserID.String() == client.UserID()
}

// ---- handleCreate --------------------------------------------------------

type projectCreateParams struct {
	Slug        string          `json:"slug"`
	OwnerUserID string          `json:"ownerUserId"`
	Metadata    json.RawMessage `json:"metadata"`
}

func (m *ProjectsMethods) HandleCreate(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	locale := store.LocaleFromContext(ctx)
	var params projectCreateParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidJSON)))
		return
	}
	slug := strings.TrimSpace(params.Slug)
	if slug == "" {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgRequired, "slug")))
		return
	}
	if !slugRE.MatchString(slug) || len(slug) < 3 || len(slug) > 64 {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidSlug, "slug")))
		return
	}

	// Determine owner: admin can specify any; non-admin → self.
	var ownerID uuid.UUID
	admin := canSeeAll(client.Role(), m.cfg.Gateway.OwnerIDs, client.UserID())
	switch {
	case admin && params.OwnerUserID != "":
		oid, err := uuid.Parse(params.OwnerUserID)
		if err != nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidID, "ownerUserId")))
			return
		}
		ownerID = oid
	case client.UserID() != "":
		oid, err := uuid.Parse(client.UserID())
		if err != nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, i18n.T(locale, i18n.MsgUserCtxRequired)))
			return
		}
		ownerID = oid
	default:
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, i18n.T(locale, i18n.MsgUserCtxRequired)))
		return
	}

	p := &store.Project{Slug: slug, OwnerUserID: ownerID, Status: "active", Metadata: params.Metadata}
	if err := m.projects.Create(ctx, p); err != nil {
		// Slug uniqueness violation surfaces as DB error; map to ALREADY_EXISTS for known patterns.
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") || strings.Contains(strings.ToLower(err.Error()), "unique") {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrAlreadyExists, i18n.T(locale, i18n.MsgAlreadyExists, "project", slug)))
			return
		}
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}

	emitAudit(m.eventBus, client, "project.created", "project", p.ID.String())
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"project": toProjectView(p)}))
}

// ---- handleUpdateMetadata ------------------------------------------------

type projectUpdateMetadataParams struct {
	ID       string          `json:"id"`
	Slug     *string         `json:"slug"` // presence rejected — slug is immutable
	Metadata json.RawMessage `json:"metadata"`
}

func (m *ProjectsMethods) HandleUpdateMetadata(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	locale := store.LocaleFromContext(ctx)
	var params projectUpdateMetadataParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidJSON)))
		return
	}
	if params.Slug != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrFailedPrecondition, i18n.T(locale, i18n.MsgProjectSlugImmutable)))
		return
	}
	pid, err := uuid.Parse(params.ID)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidID, "id")))
		return
	}
	p, err := m.projects.Get(ctx, pid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrNotFound, i18n.T(locale, i18n.MsgNotFound, "project", params.ID)))
			return
		}
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	if !m.canMutate(client, p) {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, i18n.T(locale, i18n.MsgPermissionDenied, "project")))
		return
	}
	if err := m.projects.UpdateMetadata(ctx, pid, params.Metadata); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	updated, _ := m.projects.Get(ctx, pid)
	emitAudit(m.eventBus, client, "project.metadata_updated", "project", pid.String())
	if updated != nil {
		client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"ok": true, "project": toProjectView(updated)}))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"ok": true}))
}

// ---- handleUpdateStatus --------------------------------------------------

type projectUpdateStatusParams struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func (m *ProjectsMethods) HandleUpdateStatus(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	locale := store.LocaleFromContext(ctx)
	var params projectUpdateStatusParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidJSON)))
		return
	}
	if params.Status != "active" && params.Status != "archived" {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgProjectInvalidStatus)))
		return
	}
	pid, err := uuid.Parse(params.ID)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidID, "id")))
		return
	}
	p, err := m.projects.Get(ctx, pid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrNotFound, i18n.T(locale, i18n.MsgNotFound, "project", params.ID)))
			return
		}
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	if !m.canMutate(client, p) {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, i18n.T(locale, i18n.MsgPermissionDenied, "project")))
		return
	}
	if err := m.projects.UpdateStatus(ctx, pid, params.Status); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	emitAudit(m.eventBus, client, "project.status_updated", "project", pid.String())
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"ok": true}))
}

// ---- handleDelete (soft-delete via status=archived) ----------------------

func (m *ProjectsMethods) HandleDelete(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	locale := store.LocaleFromContext(ctx)
	var params projectIDParam
	if err := json.Unmarshal(req.Params, &params); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidJSON)))
		return
	}
	pid, err := uuid.Parse(params.ID)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidID, "id")))
		return
	}
	p, err := m.projects.Get(ctx, pid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrNotFound, i18n.T(locale, i18n.MsgNotFound, "project", params.ID)))
			return
		}
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	if !m.canMutate(client, p) {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, i18n.T(locale, i18n.MsgPermissionDenied, "project")))
		return
	}
	// Soft-delete: rc1 store has no Delete, transition to archived instead.
	if err := m.projects.UpdateStatus(ctx, pid, "archived"); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	emitAudit(m.eventBus, client, "project.archived", "project", pid.String())
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"ok": true, "archived": true}))
}
