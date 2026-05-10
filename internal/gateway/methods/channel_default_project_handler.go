package methods

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// ChannelContactsMethods handles channel contact mutations (set/clear default project).
type ChannelContactsMethods struct {
	contacts  store.ContactStore
	instances store.ChannelInstanceStore
	agents    store.AgentStore
	projects  store.ProjectStore
	grants    store.ProjectGrantStore
	eventBus  bus.EventPublisher
	cfg       *config.Config
}

// NewChannelContactsMethods constructs the handler with the stores it needs.
func NewChannelContactsMethods(
	contacts store.ContactStore,
	instances store.ChannelInstanceStore,
	agents store.AgentStore,
	projects store.ProjectStore,
	grants store.ProjectGrantStore,
	eventBus bus.EventPublisher,
	cfg *config.Config,
) *ChannelContactsMethods {
	return &ChannelContactsMethods{
		contacts:  contacts,
		instances: instances,
		agents:    agents,
		projects:  projects,
		grants:    grants,
		eventBus:  eventBus,
		cfg:       cfg,
	}
}

// Register wires the handler into the method router.
func (m *ChannelContactsMethods) Register(router *gateway.MethodRouter) {
	router.Register(protocol.MethodChannelsContactsSetDefaultProject, m.handleSetDefaultProject)
}

type setDefaultProjectParams struct {
	ChannelContactID string  `json:"channelContactId"`
	ProjectID        *string `json:"projectId"` // omit or null → clear binding
}

// handleSetDefaultProject sets or clears the default project on a channel contact.
// Params (camelCase JSON): channelContactId (required), projectId (nullable — omit to clear).
func (m *ChannelContactsMethods) handleSetDefaultProject(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	locale := store.LocaleFromContext(ctx)

	var params setDefaultProjectParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidJSON)))
		return
	}

	if params.ChannelContactID == "" {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgRequired, "channelContactId")))
		return
	}

	contactUUID, err := uuid.Parse(params.ChannelContactID)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidID, "channelContactId")))
		return
	}

	// Resolve optional target project UUID.
	var projectID *uuid.UUID
	if params.ProjectID != nil && *params.ProjectID != "" {
		pid, err := uuid.Parse(*params.ProjectID)
		if err != nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidID, "projectId")))
			return
		}
		projectID = &pid
	}

	deps := permissions.ChannelDefaultProjectDeps{
		Contacts:         m.contacts,
		ChannelInstances: m.instances,
		Agents:           m.agents,
		Projects:         m.projects,
		ProjectGrants:    m.grants,
	}

	ok, err := permissions.CanSetChannelDefaultProject(
		ctx, deps,
		client.Role(), client.UserID(),
		contactUUID, projectID,
	)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	if !ok {
		// Generic message — must not reveal whether the project exists.
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, i18n.T(locale, i18n.MsgChannelDefaultProjectDenied)))
		return
	}

	if err := m.contacts.UpdateDefaultProject(ctx, contactUUID, projectID); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}

	projectIDStr := ""
	if projectID != nil {
		projectIDStr = projectID.String()
	}

	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{
		"ok":               true,
		"channelContactId": params.ChannelContactID,
		"projectId":        projectIDStr,
	}))
	emitAudit(m.eventBus, client, "channel_contact.default_project_updated", "channel_contact", params.ChannelContactID)
}
