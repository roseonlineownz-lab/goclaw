//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/internal/gateway/methods"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	pgstore "github.com/nextlevelbuilder/goclaw/internal/store/pg"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

func projRPCSetup(t *testing.T) (*sql.DB, *methods.ProjectsMethods, *methods.ProjectGrantsMethods) {
	t.Helper()
	db := testDB(t)
	ps := pgstore.NewPGProjectStore(db)
	gs := pgstore.NewPGProjectGrantStore(db)
	cfg := &config.Config{}
	pm := methods.NewProjectsMethods(ps, gs, nil, cfg)
	gm := methods.NewProjectGrantsMethods(ps, gs, nil, cfg)
	return db, pm, gm
}

func rpcReq(t *testing.T, method string, params map[string]any) *protocol.RequestFrame {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &protocol.RequestFrame{ID: "rpc-" + uuid.NewString()[:8], Method: method, Params: raw}
}

func mustOK(t *testing.T, resp *protocol.ResponseFrame) map[string]any {
	t.Helper()
	if resp == nil {
		t.Fatal("no response captured")
	}
	if resp.Error != nil {
		t.Fatalf("expected OK, got error %s: %s", resp.Error.Code, resp.Error.Message)
	}
	payload, ok := resp.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload not map[string]any: %T", resp.Payload)
	}
	return payload
}

func mustErr(t *testing.T, resp *protocol.ResponseFrame, wantCode string) {
	t.Helper()
	if resp == nil {
		t.Fatal("no response captured")
	}
	if resp.Error == nil {
		t.Fatalf("expected error %s, got OK", wantCode)
	}
	if resp.Error.Code != wantCode {
		t.Fatalf("error code: got %s want %s (%s)", resp.Error.Code, wantCode, resp.Error.Message)
	}
}

// ─── happy paths ─────────────────────────────────────────────────────────────

// TestProjectsRPC_AdminLifecycle covers create → get → update_metadata → update_status → delete (archive).
func TestProjectsRPC_AdminLifecycle(t *testing.T) {
	db, pm, _ := projRPCSetup(t)
	owner := e2eUser(t, db)
	client, read := gateway.NewTestClientWithCapture(permissions.RoleAdmin, owner.String())

	slug := "lc-" + uuid.New().String()[:8]
	pm.HandleCreate(context.Background(), client, rpcReq(t, protocol.MethodProjectsCreate, map[string]any{
		"slug": slug, "ownerUserId": owner.String(),
	}))
	payload := mustOK(t, read())
	proj := payload["project"].(map[string]any)
	pid := proj["id"].(string)
	t.Cleanup(func() { db.Exec("DELETE FROM projects WHERE id = $1", pid) })

	pm.HandleGet(context.Background(), client, rpcReq(t, protocol.MethodProjectsGet, map[string]any{"id": pid}))
	mustOK(t, read())

	pm.HandleUpdateMetadata(context.Background(), client, rpcReq(t, protocol.MethodProjectsUpdateMetadata, map[string]any{
		"id":       pid,
		"metadata": map[string]any{"description": "lc"},
	}))
	mustOK(t, read())

	pm.HandleUpdateStatus(context.Background(), client, rpcReq(t, protocol.MethodProjectsUpdateStatus, map[string]any{
		"id": pid, "status": "archived",
	}))
	mustOK(t, read())

	// Re-create active sibling, then delete (soft-archive) it.
	slug2 := "lc-" + uuid.New().String()[:8]
	pm.HandleCreate(context.Background(), client, rpcReq(t, protocol.MethodProjectsCreate, map[string]any{
		"slug": slug2, "ownerUserId": owner.String(),
	}))
	payload2 := mustOK(t, read())
	pid2 := payload2["project"].(map[string]any)["id"].(string)
	t.Cleanup(func() { db.Exec("DELETE FROM projects WHERE id = $1", pid2) })

	pm.HandleDelete(context.Background(), client, rpcReq(t, protocol.MethodProjectsDelete, map[string]any{"id": pid2}))
	out := mustOK(t, read())
	if archived, _ := out["archived"].(bool); !archived {
		t.Errorf("delete: expected archived=true, got %v", out)
	}

	var status string
	if err := db.QueryRow("SELECT status FROM projects WHERE id = $1", pid2).Scan(&status); err != nil {
		t.Fatalf("status check: %v", err)
	}
	if status != "archived" {
		t.Errorf("status: got %q want archived", status)
	}
}

// TestProjectsRPC_NonAdminOwnerCreatesAndAccesses asserts a non-admin user can create their own project + read it.
func TestProjectsRPC_NonAdminOwnerCreatesAndAccesses(t *testing.T) {
	db, pm, _ := projRPCSetup(t)
	owner := e2eUser(t, db)
	client, read := gateway.NewTestClientWithCapture(permissions.RoleMember, owner.String())

	slug := "noa-" + uuid.New().String()[:8]
	pm.HandleCreate(context.Background(), client, rpcReq(t, protocol.MethodProjectsCreate, map[string]any{"slug": slug}))
	out := mustOK(t, read())
	pid := out["project"].(map[string]any)["id"].(string)
	t.Cleanup(func() { db.Exec("DELETE FROM projects WHERE id = $1", pid) })

	pm.HandleList(context.Background(), client, rpcReq(t, protocol.MethodProjectsList, map[string]any{}))
	listOut := mustOK(t, read())
	projs := listOut["projects"].([]any)
	found := false
	for _, p := range projs {
		if pp, ok := p.(map[string]any); ok && pp["id"] == pid {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("non-admin owner should see their project in list")
	}
}

// TestProjectGrantsRPC_OwnerListsDirectAndInherited asserts split between user vs team grants.
func TestProjectGrantsRPC_OwnerListsDirectAndInherited(t *testing.T) {
	db, _, gm := projRPCSetup(t)
	owner := e2eUser(t, db)
	member := e2eUser(t, db)
	teamID := e2eTeam(t, db, owner)
	ctx := context.Background()
	p := e2eCreateProject(t, ctx, db, owner)

	client, read := gateway.NewTestClientWithCapture(permissions.RoleAdmin, owner.String())

	// Direct user grant.
	gm.HandleCreate(ctx, client, rpcReq(t, protocol.MethodProjectGrantsCreate, map[string]any{
		"projectId": p.ID.String(),
		"userId":    member.String(),
		"role":      "viewer",
	}))
	mustOK(t, read())

	// Team grant.
	gm.HandleCreate(ctx, client, rpcReq(t, protocol.MethodProjectGrantsCreate, map[string]any{
		"projectId": p.ID.String(),
		"teamId":    teamID.String(),
		"role":      "member",
	}))
	mustOK(t, read())

	// list → only direct (user) grant.
	gm.HandleList(ctx, client, rpcReq(t, protocol.MethodProjectGrantsList, map[string]any{"projectId": p.ID.String()}))
	directOut := mustOK(t, read())
	directs := directOut["grants"].([]any)
	if len(directs) != 1 {
		t.Fatalf("direct grants: got %d want 1", len(directs))
	}
	if directs[0].(map[string]any)["userId"] != member.String() {
		t.Errorf("direct grant userId mismatch")
	}

	// list_inherited → only team grant.
	gm.HandleListInherited(ctx, client, rpcReq(t, protocol.MethodProjectGrantsListInherited, map[string]any{"projectId": p.ID.String()}))
	inhOut := mustOK(t, read())
	inhs := inhOut["grants"].([]any)
	if len(inhs) != 1 {
		t.Fatalf("inherited grants: got %d want 1", len(inhs))
	}
	if inhs[0].(map[string]any)["teamId"] != teamID.String() {
		t.Errorf("inherited grant teamId mismatch")
	}
}

// TestProjectsRPC_NonOwnerWithGrantSeesProjectInList asserts grant-based visibility.
func TestProjectsRPC_NonOwnerWithGrantSeesProjectInList(t *testing.T) {
	db, pm, gm := projRPCSetup(t)
	owner := e2eUser(t, db)
	other := e2eUser(t, db)
	ctx := context.Background()
	p := e2eCreateProject(t, ctx, db, owner)

	// Owner grants viewer access to other.
	ownerClient, ownerRead := gateway.NewTestClientWithCapture(permissions.RoleAdmin, owner.String())
	gm.HandleCreate(ctx, ownerClient, rpcReq(t, protocol.MethodProjectGrantsCreate, map[string]any{
		"projectId": p.ID.String(), "userId": other.String(), "role": "viewer",
	}))
	mustOK(t, ownerRead())

	// Other lists — should see the project via grant.
	otherClient, otherRead := gateway.NewTestClientWithCapture(permissions.RoleMember, other.String())
	pm.HandleList(ctx, otherClient, rpcReq(t, protocol.MethodProjectsList, map[string]any{}))
	out := mustOK(t, otherRead())
	projs := out["projects"].([]any)
	found := false
	for _, x := range projs {
		if pp, ok := x.(map[string]any); ok && pp["id"] == p.ID.String() {
			found = true
		}
	}
	if !found {
		t.Errorf("grant-holder should see project in list")
	}
}

// TestProjectsRPC_GetByGrantedViewer asserts viewer-only grant satisfies canRead.
func TestProjectsRPC_GetByGrantedViewer(t *testing.T) {
	db, pm, gm := projRPCSetup(t)
	owner := e2eUser(t, db)
	other := e2eUser(t, db)
	ctx := context.Background()
	p := e2eCreateProject(t, ctx, db, owner)

	ownerClient, ownerRead := gateway.NewTestClientWithCapture(permissions.RoleAdmin, owner.String())
	gm.HandleCreate(ctx, ownerClient, rpcReq(t, protocol.MethodProjectGrantsCreate, map[string]any{
		"projectId": p.ID.String(), "userId": other.String(), "role": "viewer",
	}))
	mustOK(t, ownerRead())

	otherClient, otherRead := gateway.NewTestClientWithCapture(permissions.RoleMember, other.String())
	pm.HandleGet(ctx, otherClient, rpcReq(t, protocol.MethodProjectsGet, map[string]any{"id": p.ID.String()}))
	mustOK(t, otherRead())
}

// ─── deny paths ──────────────────────────────────────────────────────────────

// TestProjectsRPC_UpdateSlugRejectedAsImmutable asserts FAILED_PRECONDITION on slug field presence.
func TestProjectsRPC_UpdateSlugRejectedAsImmutable(t *testing.T) {
	db, pm, _ := projRPCSetup(t)
	owner := e2eUser(t, db)
	ctx := context.Background()
	p := e2eCreateProject(t, ctx, db, owner)

	client, read := gateway.NewTestClientWithCapture(permissions.RoleAdmin, owner.String())
	newSlug := "new-slug-x"
	pm.HandleUpdateMetadata(ctx, client, rpcReq(t, protocol.MethodProjectsUpdateMetadata, map[string]any{
		"id":   p.ID.String(),
		"slug": newSlug,
	}))
	mustErr(t, read(), protocol.ErrFailedPrecondition)
}

// TestProjectsRPC_NonOwnerCannotUpdate asserts a viewer-grant holder cannot mutate metadata.
func TestProjectsRPC_NonOwnerCannotUpdate(t *testing.T) {
	db, pm, gm := projRPCSetup(t)
	owner := e2eUser(t, db)
	other := e2eUser(t, db)
	ctx := context.Background()
	p := e2eCreateProject(t, ctx, db, owner)

	ownerClient, ownerRead := gateway.NewTestClientWithCapture(permissions.RoleAdmin, owner.String())
	gm.HandleCreate(ctx, ownerClient, rpcReq(t, protocol.MethodProjectGrantsCreate, map[string]any{
		"projectId": p.ID.String(), "userId": other.String(), "role": "editor",
	}))
	mustOK(t, ownerRead())

	otherClient, otherRead := gateway.NewTestClientWithCapture(permissions.RoleMember, other.String())
	pm.HandleUpdateMetadata(ctx, otherClient, rpcReq(t, protocol.MethodProjectsUpdateMetadata, map[string]any{
		"id":       p.ID.String(),
		"metadata": map[string]any{"description": "should fail"},
	}))
	mustErr(t, otherRead(), protocol.ErrUnauthorized)
}

// TestProjectsRPC_NonOwnerCannotDelete asserts a non-owner cannot archive the project.
func TestProjectsRPC_NonOwnerCannotDelete(t *testing.T) {
	db, pm, _ := projRPCSetup(t)
	owner := e2eUser(t, db)
	other := e2eUser(t, db)
	ctx := context.Background()
	p := e2eCreateProject(t, ctx, db, owner)

	otherClient, otherRead := gateway.NewTestClientWithCapture(permissions.RoleMember, other.String())
	pm.HandleDelete(ctx, otherClient, rpcReq(t, protocol.MethodProjectsDelete, map[string]any{"id": p.ID.String()}))
	mustErr(t, otherRead(), protocol.ErrUnauthorized)
}

// TestProjectGrantsRPC_NonOwnerCannotCreateGrant asserts grant-create requires admin or owner.
func TestProjectGrantsRPC_NonOwnerCannotCreateGrant(t *testing.T) {
	db, _, gm := projRPCSetup(t)
	owner := e2eUser(t, db)
	other := e2eUser(t, db)
	target := e2eUser(t, db)
	ctx := context.Background()
	p := e2eCreateProject(t, ctx, db, owner)

	otherClient, otherRead := gateway.NewTestClientWithCapture(permissions.RoleMember, other.String())
	gm.HandleCreate(ctx, otherClient, rpcReq(t, protocol.MethodProjectGrantsCreate, map[string]any{
		"projectId": p.ID.String(), "userId": target.String(), "role": "viewer",
	}))
	mustErr(t, otherRead(), protocol.ErrUnauthorized)
}

// TestProjectsRPC_InvalidSlugRejected asserts kebab-case enforcement.
func TestProjectsRPC_InvalidSlugRejected(t *testing.T) {
	_, pm, _ := projRPCSetup(t)
	client, read := gateway.NewTestClientWithCapture(permissions.RoleAdmin, uuid.New().String())
	pm.HandleCreate(context.Background(), client, rpcReq(t, protocol.MethodProjectsCreate, map[string]any{
		"slug": "Bad_Slug",
	}))
	mustErr(t, read(), protocol.ErrInvalidRequest)
}
