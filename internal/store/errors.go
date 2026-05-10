package store

import "errors"

// ErrNotFound is returned by store methods when the requested row does not exist.
// Callers compare with errors.Is(err, store.ErrNotFound) — never with ==.
var ErrNotFound = errors.New("store: not found")

// ErrInvalidShareRole is returned when an agent_shares insert is attempted
// with a role outside the {viewer, member, editor} enum.
var ErrInvalidShareRole = errors.New("store: invalid share role")

// ErrInvalidShareTarget is returned when an agent_shares insert is attempted
// with neither or both of (shared_with_user_id, shared_with_team_id).
var ErrInvalidShareTarget = errors.New("store: invalid share target (must be exactly one of user_id, team_id)")
