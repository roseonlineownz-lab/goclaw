package memory

// IntersectScopes returns the most restrictive common scope from a slice of
// ScopeKey values. For each dimension (TeamID, UserID, ContactID, ProjectID):
//   - If all input scopes share the same non-empty value → keep that value
//   - If any input differs (including empty vs non-empty) → use "" (agent-broad)
//
// AgentID is always taken from the first element (consolidation runs per-agent;
// all chunks in a session share the same agent).
//
// An empty input slice returns a zero ScopeKey.
//
// This implements the L31 privacy invariant: user-private chunks (UserID = U)
// consolidate into a user-private summary. Mixed-user chunks produce a summary
// with UserID = "" (agent-broad), which is accessible to all users of that
// agent but not to other agents.
func IntersectScopes(scopes []ScopeKey) ScopeKey {
	if len(scopes) == 0 {
		return ScopeKey{}
	}

	out := scopes[0]
	for _, s := range scopes[1:] {
		if out.TeamID != s.TeamID {
			out.TeamID = ""
		}
		if out.UserID != s.UserID {
			out.UserID = ""
		}
		if out.ContactID != s.ContactID {
			out.ContactID = ""
		}
		if out.ProjectID != s.ProjectID {
			out.ProjectID = ""
		}
		// AgentID always preserved — consolidation runs within a single agent.
	}
	return out
}
