package workspace

import (
	"context"
	"fmt"
	"path/filepath"
)

// Production callers still use Resolve(); ResolveChannel is reserved for a
// forthcoming refactor that wires the 12-scenario matrix into the agent pipeline.

// ResolveChannel resolves the workspace path for a channel-originated session.
//
// Decision tree:
//  1. Merged → canonical user zone (privacy hard rule: merged contact MUST
//     never write to agent/team-shared paths — cross-channel leak risk).
//  2. web sender → user-scoped zone (personal or team).
//  3. channel DM → agent or team contact zone (unmerged).
//  4. channel group → agent or team group zone (unmerged).
//  5. system run (Web sender, empty UserKey) → agent/team root zone — covers
//     heartbeat / delegate / system flows where no user identity exists.
func (r *defaultResolver) ResolveChannel(_ context.Context, c ChannelResolveCtx) (string, ChannelScope, error) {
	if c.BaseDir == "" {
		return "", ChannelScope{}, fmt.Errorf("workspace: base dir is required")
	}

	// Merged → canonical user zone regardless of sender kind.
	// Why: merged contact = canonical user; routing to agent/team-shared zone
	// would leak across user identity boundary (Plan #3 privacy hard rule).
	if c.Merged && c.UserKey != "" {
		return channelUserZone(c)
	}

	switch c.SenderKind {
	case SenderWeb:
		// System run fallback: a Web sender with no user identity (heartbeat,
		// delegate, cron-without-user) cannot route to users/{user_key}/.
		// Drop into the agent/team system zone instead.
		if c.UserKey == "" {
			return channelSystemZone(c)
		}
		return channelUserZone(c)
	case SenderChannelDM:
		return channelDMZone(c)
	case SenderChannelGroup:
		return channelGroupZone(c)
	default:
		return channelUserZone(c)
	}
}

// channelSystemZone handles system-initiated runs (no user identity).
//   - solo: agents/{agent_key}/
//   - team: teams/{team_key}/
//
// Used by heartbeat, delegate, and cron-without-user flows where SenderKind is
// classified as Web but no UserKey can be resolved.
func channelSystemZone(c ChannelResolveCtx) (string, ChannelScope, error) {
	if c.TeamKey != "" {
		p := filepath.Join(c.BaseDir, "teams", sanitizeSegment(c.TeamKey))
		ensureDirTeam(p)
		return p, ChannelScope{SenderKind: c.SenderKind, ZoneKind: "team-system"}, nil
	}
	p := filepath.Join(c.BaseDir, "agents", sanitizeSegment(c.AgentKey))
	ensureDir(p)
	return p, ChannelScope{SenderKind: c.SenderKind, ZoneKind: "agent-system"}, nil
}

// channelUserZone handles web sender paths and merged-contact canonical paths.
//   - solo / predefined: users/{user_key}/agents/{agent_key}/  (scenarios 1, 3, 5, 10)
//   - team:              users/{user_key}/teams/{team_key}/    (scenarios 2, 7, 11)
func channelUserZone(c ChannelResolveCtx) (string, ChannelScope, error) {
	if c.TeamKey != "" {
		p := filepath.Join(c.BaseDir, "users", sanitizeSegment(c.UserKey), "teams", sanitizeSegment(c.TeamKey))
		ensureDirTeam(p)
		return p, ChannelScope{SenderKind: c.SenderKind, ZoneKind: "user-team", Merged: c.Merged}, nil
	}
	p := filepath.Join(c.BaseDir, "users", sanitizeSegment(c.UserKey), "agents", sanitizeSegment(c.AgentKey))
	ensureDir(p)
	return p, ChannelScope{SenderKind: c.SenderKind, ZoneKind: "user-agent", Merged: c.Merged}, nil
}

// channelDMZone handles channel direct-message paths (unmerged).
//   - solo / predefined: agents/{agent_key}/contacts/{channel}-{sender_id}/  (scenarios 4, 12)
//   - team:              teams/{team_key}/contacts/{channel}-{sender_id}/    (scenario 6)
func channelDMZone(c ChannelResolveCtx) (string, ChannelScope, error) {
	contactSeg := sanitizeSegment(c.ChannelType) + "-" + sanitizeSegment(c.SenderID)
	if c.TeamKey != "" {
		p := filepath.Join(c.BaseDir, "teams", sanitizeSegment(c.TeamKey), "contacts", contactSeg)
		ensureDirTeam(p)
		return p, ChannelScope{SenderKind: c.SenderKind, ZoneKind: "team-contact"}, nil
	}
	p := filepath.Join(c.BaseDir, "agents", sanitizeSegment(c.AgentKey), "contacts", contactSeg)
	ensureDir(p)
	return p, ChannelScope{SenderKind: c.SenderKind, ZoneKind: "agent-contact"}, nil
}

// channelGroupZone handles channel group-chat paths (unmerged).
//   - solo:  agents/{agent_key}/groups/{channel}-{chat_id}/  (scenario 8)
//   - team:  teams/{team_key}/groups/{channel}-{chat_id}/    (scenario 9)
//
// Channel prefix is included in the group segment to disambiguate identical
// chat_id values across different channel platforms (L-1 fix).
func channelGroupZone(c ChannelResolveCtx) (string, ChannelScope, error) {
	groupSeg := sanitizeSegment(c.ChannelType) + "-" + sanitizeSegment(c.ChatID)
	if c.TeamKey != "" {
		p := filepath.Join(c.BaseDir, "teams", sanitizeSegment(c.TeamKey), "groups", groupSeg)
		ensureDirTeam(p)
		return p, ChannelScope{SenderKind: c.SenderKind, ZoneKind: "team-group"}, nil
	}
	p := filepath.Join(c.BaseDir, "agents", sanitizeSegment(c.AgentKey), "groups", groupSeg)
	ensureDir(p)
	return p, ChannelScope{SenderKind: c.SenderKind, ZoneKind: "agent-group"}, nil
}
