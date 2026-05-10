package testutil

import (
	"context"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// TenantCtx returns a background context (tenant scope removed in v4 single-tenant).
func TenantCtx(_ uuid.UUID) context.Context {
	return context.Background()
}

// UserCtx returns a context with user identity set.
func UserCtx(_ uuid.UUID, userID string) context.Context {
	return store.WithUserID(context.Background(), userID)
}

// AgentCtx returns a context with agent identity set.
func AgentCtx(_ uuid.UUID, agentID uuid.UUID) context.Context {
	return store.WithAgentID(context.Background(), agentID)
}

// FullCtx returns a context with user + agent identities set.
func FullCtx(_ uuid.UUID, userID string, agentID uuid.UUID) context.Context {
	ctx := store.WithUserID(context.Background(), userID)
	return store.WithAgentID(ctx, agentID)
}

// MustParseUUID is a helper for tests to turn a literal into uuid.UUID.
// Tests die loudly on malformed input; production code should never touch this.
func MustParseUUID(s string) uuid.UUID {
	id, err := uuid.Parse(s)
	if err != nil {
		panic("testutil.MustParseUUID: " + err.Error())
	}
	return id
}
