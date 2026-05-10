package permissions

import (
	"context"

	"github.com/google/uuid"
)

// ResolverConfig wires the four dependency functions that feed the AND-intersect
// resolver. Each function returns a share-role string and a found bool. When
// found is false the layer is treated as "no grant" (deny unless admin bypass).
//
// Using functions instead of interfaces keeps the resolver test-friendly: tests
// pass inline closures; production wires real store adapters.
type ResolverConfig struct {
	// UserRole returns the platform-level role for the user (e.g. ShareOwner for
	// admin/root). Used for admin bypass and as the final user-level cap.
	UserRole func(ctx context.Context, userID uuid.UUID) string

	// ProjectGrant returns the user's role on the project. found=false when no
	// project is bound to the session (project layer skipped) or when the user
	// has no grant / project is archived.
	ProjectGrant func(ctx context.Context, userID, projectID uuid.UUID) (role string, found bool)

	// AgentShare returns the user's share role on the agent (explicit grant).
	// Includes implicit team grant if the sibling agent-access resolver provides it.
	AgentShare func(ctx context.Context, userID, agentID uuid.UUID) (role string, found bool)

	// TeamMembership returns the implicit team-membership role: the user is a
	// member of the team that owns the agent. found=false when no team owns the
	// agent or the user is not a member.
	TeamMembership func(ctx context.Context, userID, agentID uuid.UUID) (role string, found bool)
}

// Resolver computes effective access by AND-intersecting four independent layers:
//
//  1. User platform role (admin/root bypass; viewer caps everything)
//  2. Project grant     (skipped when projectID == nil)
//  3. Agent share       (explicit grant or implicit via team-target share)
//  4. Team membership   (implicit grant via owning team of the agent)
//
// All active layers must grant at least the required role for the action. A
// missing grant on any active layer is an implicit deny.
type Resolver struct {
	cfg ResolverConfig
}

// NewResolver constructs a Resolver from the given dependency config.
func NewResolver(cfg ResolverConfig) *Resolver {
	return &Resolver{cfg: cfg}
}

// CheckAccess returns true when the user is permitted to perform action on
// the agent (optionally scoped to a project). Returns false on any deny.
//
// Strict AND-intersect across four layers — ALL active layers must grant at
// least the required role. A missing or insufficient grant on any active layer
// is an implicit deny regardless of other layers.
//
// Layer evaluation order:
//  1. Admin bypass: userRole ∈ {owner/root} → allow immediately (no lower layers).
//  2. Project layer: when projectID != nil, user must hold project grant ≥ required.
//     When projectID == nil the project layer is inactive (returns true).
//  3. Agent layer: user must hold agent share ≥ required (explicit grant) OR
//     team membership ≥ required (implicit grant via owning team). At least one
//     must satisfy the requirement; both present → weaker (min) governs.
//  4. Team layer: if the owning team grants the user membership, that membership
//     must also be ≥ required. When no team owns the agent the layer is inactive.
//  5. User role cap: platform role must be ≥ required (viewer cannot write).
func (r *Resolver) CheckAccess(ctx context.Context, userID, agentID uuid.UUID, projectID *uuid.UUID, action Action) bool {
	// Layer 1: admin bypass — owner/root skips all grant checks.
	userRole := r.cfg.UserRole(ctx, userID)
	if isAdminShare(userRole) {
		return true
	}

	// Layer 2: project grant (skipped when no project is bound to the session).
	if !r.checkProjectAxis(ctx, userID, projectID, action) {
		return false
	}

	// Layers 3 + 4: agent share and team membership must both pass (AND-intersect).
	if !r.checkAgentTeamAxes(ctx, userID, agentID, action) {
		return false
	}

	// Layer 5: user platform role cap (viewer cannot write even with editor grants).
	return roleAtLeast(userRole, action)
}

// checkProjectAxis returns true when the project layer is inactive (nil projectID)
// or when the user holds a project grant that meets the required action level.
func (r *Resolver) checkProjectAxis(ctx context.Context, userID uuid.UUID, projectID *uuid.UUID, action Action) bool {
	if projectID == nil {
		return true // layer not applicable
	}
	pRole, found := r.cfg.ProjectGrant(ctx, userID, *projectID)
	return found && roleAtLeast(pRole, action)
}

// checkAgentTeamAxes enforces the AND-intersect of the agent-share and
// team-membership layers. The agent axis is satisfied when the user holds an
// explicit agent share OR a team membership grant, each ≥ required. When both
// exist the weaker (min) governs so neither can be bypassed by the stronger.
func (r *Resolver) checkAgentTeamAxes(ctx context.Context, userID, agentID uuid.UUID, action Action) bool {
	aRole, aFound := r.cfg.AgentShare(ctx, userID, agentID)
	tRole, tFound := r.cfg.TeamMembership(ctx, userID, agentID)

	// Determine the effective agent-layer grant.
	var agentOk bool
	switch {
	case aFound && tFound:
		// Both present: AND-intersect — weaker role must satisfy the action.
		agentOk = roleAtLeast(minShareRole(aRole, tRole), action)
	case aFound:
		agentOk = roleAtLeast(aRole, action)
	case tFound:
		agentOk = roleAtLeast(tRole, action)
	default:
		// No explicit grant and no team membership — deny.
		return false
	}

	if !agentOk {
		return false
	}

	// Team membership additional guard: when a team owns the agent, membership
	// must independently meet the required level (both axes must pass).
	if tFound && !roleAtLeast(tRole, action) {
		return false
	}

	return true
}
