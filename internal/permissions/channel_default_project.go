package permissions

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// ChannelDefaultProjectDeps holds the stores required to evaluate the
// SetGroupDefaultProject permission gate. Kept minimal to avoid coupling
// to the full Stores aggregate.
type ChannelDefaultProjectDeps struct {
	Contacts         store.ContactStore
	ChannelInstances store.ChannelInstanceStore
	Agents           store.AgentStore
	Projects         store.ProjectStore
	ProjectGrants    store.ProjectGrantStore
}

// CanSetChannelDefaultProject reports whether callerUserID is allowed to set
// (or clear) the default project on a channel contact.
//
// Decision tree:
//
//	isAdmin         = callerRole >= admin
//	isAgentOwner    = channel contact's bound agent has owner_user_id == callerUserID
//	isProjectOwner  = project.owner_user_id == callerUserID
//
//	When projectID == nil (clear default):
//	  allow iff isAdmin || isAgentOwner
//
//	When projectID != nil:
//	  allow iff (isAdmin || isAgentOwner || isProjectOwner) && CanAccessProject(caller, projectID, viewer)
//
// Error messages are intentionally generic to avoid revealing project existence.
func CanSetChannelDefaultProject(
	ctx context.Context,
	deps ChannelDefaultProjectDeps,
	callerRole Role,
	callerUserID string,
	contactID uuid.UUID,
	projectID *uuid.UUID,
) (bool, error) {
	isAdmin := HasMinRole(callerRole, RoleAdmin)

	// Load the contact to resolve its channel instance (needed for agent-owner check).
	contact, err := deps.Contacts.GetContactByID(ctx, contactID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("load contact: %w", err)
	}

	isAgentOwner, err := checkAgentOwner(ctx, deps, callerUserID, contact)
	if err != nil {
		return false, err
	}

	// Clearing the default requires no project access check — no target project.
	if projectID == nil {
		return isAdmin || isAgentOwner, nil
	}

	// Check project ownership (fast path — avoids grant store query when owner).
	isProjectOwner, err := checkProjectOwner(ctx, deps, callerUserID, *projectID)
	if err != nil {
		return false, err
	}

	// Caller must hold at least one qualifying role.
	if !isAdmin && !isAgentOwner && !isProjectOwner {
		return false, nil
	}

	// Caller must also be able to read the target project.
	// Using generic denial to avoid revealing project existence to unauthorised callers.
	canRead, err := CanAccessProject(ctx, deps.ProjectGrants, callerUserID, projectID.String(), ProjectRoleViewer)
	if err != nil {
		return false, fmt.Errorf("project access check: %w", err)
	}
	return canRead, nil
}

// checkAgentOwner returns true when the contact's bound channel instance is
// owned by callerUserID. Returns false (nil) when the contact has no instance
// binding or when the agent record cannot be resolved.
func checkAgentOwner(
	ctx context.Context,
	deps ChannelDefaultProjectDeps,
	callerUserID string,
	contact *store.ChannelContact,
) (bool, error) {
	if contact.ChannelInstance == nil || *contact.ChannelInstance == "" {
		return false, nil
	}
	inst, err := deps.ChannelInstances.GetByName(ctx, *contact.ChannelInstance)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("load channel instance: %w", err)
	}
	agent, err := deps.Agents.GetByIDUnscoped(ctx, inst.AgentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("load agent: %w", err)
	}
	if agent.OwnerUserID == nil {
		return false, nil
	}
	return agent.OwnerUserID.String() == callerUserID, nil
}

// checkProjectOwner returns true when project.owner_user_id == callerUserID.
func checkProjectOwner(
	ctx context.Context,
	deps ChannelDefaultProjectDeps,
	callerUserID string,
	projectID uuid.UUID,
) (bool, error) {
	project, err := deps.Projects.Get(ctx, projectID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("load project: %w", err)
	}
	return project.OwnerUserID.String() == callerUserID, nil
}
