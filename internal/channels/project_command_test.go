package channels

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// fakeSessionStore is a minimal SessionCoreStore stub that records the last
// UpdateProject call. Only the methods the project handler invokes are
// implemented; the rest panic so tests fail loudly if the handler grows
// new dependencies on session state.
type fakeSessionStore struct {
	getResp *store.SessionData

	updateCalled    bool
	updateSessionKey string
	updateProjectID *uuid.UUID
	updateErr       error
}

func (f *fakeSessionStore) Get(_ context.Context, _ string) *store.SessionData { return f.getResp }
func (f *fakeSessionStore) GetOrCreate(_ context.Context, _ string) *store.SessionData {
	return f.getResp
}
func (f *fakeSessionStore) UpdateProject(_ context.Context, key string, pid *uuid.UUID) error {
	f.updateCalled = true
	f.updateSessionKey = key
	f.updateProjectID = pid
	return f.updateErr
}
func (f *fakeSessionStore) AddMessage(context.Context, string, providers.Message)        {}
func (f *fakeSessionStore) GetHistory(context.Context, string) []providers.Message       { return nil }
func (f *fakeSessionStore) GetSummary(context.Context, string) string                    { return "" }
func (f *fakeSessionStore) SetSummary(context.Context, string, string)                   {}
func (f *fakeSessionStore) GetLabel(context.Context, string) string                      { return "" }
func (f *fakeSessionStore) SetLabel(context.Context, string, string)                     {}
func (f *fakeSessionStore) SetAgentInfo(context.Context, string, uuid.UUID, string)      {}
func (f *fakeSessionStore) TruncateHistory(context.Context, string, int)                 {}
func (f *fakeSessionStore) SetHistory(context.Context, string, []providers.Message)      {}
func (f *fakeSessionStore) Reset(context.Context, string)                                {}
func (f *fakeSessionStore) Delete(context.Context, string) error                         { return nil }
func (f *fakeSessionStore) Save(context.Context, string) error                           { return nil }

// fakeProjectStore returns canned responses for slug + uuid lookups.
type fakeProjectStore struct {
	bySlug map[string]*store.Project
	byID   map[uuid.UUID]*store.Project
}

func (f *fakeProjectStore) Create(context.Context, *store.Project) error { return nil }
func (f *fakeProjectStore) Get(_ context.Context, id uuid.UUID) (*store.Project, error) {
	if p, ok := f.byID[id]; ok {
		return p, nil
	}
	return nil, sql.ErrNoRows
}
func (f *fakeProjectStore) GetBySlug(_ context.Context, slug string) (*store.Project, error) {
	if p, ok := f.bySlug[slug]; ok {
		return p, nil
	}
	return nil, sql.ErrNoRows
}
func (f *fakeProjectStore) List(context.Context, store.ListProjectsFilter) ([]*store.Project, error) {
	return nil, nil
}
func (f *fakeProjectStore) UpdateStatus(context.Context, uuid.UUID, string) error { return nil }
func (f *fakeProjectStore) UpdateMetadata(context.Context, uuid.UUID, json.RawMessage) error {
	return nil
}

// fakeProjectGrantStore returns canned access decisions.
type fakeProjectGrantStore struct {
	roleRank int
	isOwner  bool
	found    bool
	err      error

	listForUser []*store.ProjectGrant
}

func (f *fakeProjectGrantStore) Create(context.Context, *store.ProjectGrant) error { return nil }
func (f *fakeProjectGrantStore) Get(context.Context, string) (*store.ProjectGrant, error) {
	return nil, sql.ErrNoRows
}
func (f *fakeProjectGrantStore) List(context.Context, string) ([]*store.ProjectGrant, error) {
	return nil, nil
}
func (f *fakeProjectGrantStore) ListForUser(context.Context, string) ([]*store.ProjectGrant, error) {
	return f.listForUser, nil
}
func (f *fakeProjectGrantStore) ListForTeam(context.Context, string) ([]*store.ProjectGrant, error) {
	return nil, nil
}
func (f *fakeProjectGrantStore) Delete(context.Context, string) error { return nil }
func (f *fakeProjectGrantStore) ResolveProjectRole(_ context.Context, _, _ string) (int, bool, bool, error) {
	return f.roleRank, f.isOwner, f.found, f.err
}

// Compile-time interface checks — keep the fakes honest as the real
// interfaces evolve.
var _ store.SessionCoreStore = (*fakeSessionStore)(nil)
var _ store.ProjectStore = (*fakeProjectStore)(nil)
var _ store.ProjectGrantStore = (*fakeProjectGrantStore)(nil)

func newDeps(s *fakeSessionStore, p *fakeProjectStore, g *fakeProjectGrantStore) ProjectCommandDeps {
	return ProjectCommandDeps{Sessions: s, Projects: p, ProjectGrants: g}
}

func TestHandleProjectCommand_HelpOnEmptyOrUnknown(t *testing.T) {
	deps := newDeps(&fakeSessionStore{}, &fakeProjectStore{}, &fakeProjectGrantStore{})

	for _, raw := range []string{"/project", "/project help", "/project   "} {
		got := HandleProjectCommand(context.Background(), deps, ProjectCommandRequest{RawText: raw})
		if !strings.Contains(got, "/project switch") {
			t.Errorf("raw=%q expected help text, got %q", raw, got)
		}
	}

	got := HandleProjectCommand(context.Background(), deps, ProjectCommandRequest{RawText: "/project bogus"})
	if !strings.Contains(got, "Unknown subcommand") {
		t.Errorf("expected unknown-subcommand reply, got %q", got)
	}
}

func TestHandleProjectCommand_NotConfigured(t *testing.T) {
	got := HandleProjectCommand(context.Background(), ProjectCommandDeps{}, ProjectCommandRequest{
		RawText: "/project switch foo",
	})
	if !strings.Contains(got, "not configured") {
		t.Errorf("expected not-configured reply, got %q", got)
	}
}

func TestHandleProjectCommand_SwitchSuccess(t *testing.T) {
	pid := uuid.New()
	sess := &fakeSessionStore{}
	proj := &fakeProjectStore{
		bySlug: map[string]*store.Project{"alpha": {ID: pid, Slug: "alpha"}},
	}
	grants := &fakeProjectGrantStore{roleRank: 2, found: true}

	got := HandleProjectCommand(context.Background(), newDeps(sess, proj, grants),
		ProjectCommandRequest{
			SessionKey: "agent:a:tg:direct:42",
			UserID:     "user-uuid",
			RawText:    "/project switch alpha",
		})

	if !sess.updateCalled {
		t.Fatal("expected UpdateProject to be called")
	}
	if sess.updateProjectID == nil || *sess.updateProjectID != pid {
		t.Errorf("UpdateProject got pid=%v, want %v", sess.updateProjectID, pid)
	}
	if !strings.Contains(got, "Switched to project") {
		t.Errorf("expected switch confirmation, got %q", got)
	}
}

func TestHandleProjectCommand_SwitchPermissionDenied(t *testing.T) {
	pid := uuid.New()
	sess := &fakeSessionStore{}
	proj := &fakeProjectStore{
		bySlug: map[string]*store.Project{"alpha": {ID: pid, Slug: "alpha"}},
	}
	// found=true but rank=0 and not owner → deny.
	grants := &fakeProjectGrantStore{roleRank: 0, isOwner: false, found: true}

	got := HandleProjectCommand(context.Background(), newDeps(sess, proj, grants),
		ProjectCommandRequest{UserID: "u", SessionKey: "k", RawText: "/project switch alpha"})

	if sess.updateCalled {
		t.Fatal("UpdateProject must not be called on permission deny")
	}
	if !strings.Contains(got, "do not have access") {
		t.Errorf("expected permission-denied reply, got %q", got)
	}
}

func TestHandleProjectCommand_SwitchOwnerAllowed(t *testing.T) {
	pid := uuid.New()
	sess := &fakeSessionStore{}
	proj := &fakeProjectStore{
		bySlug: map[string]*store.Project{"alpha": {ID: pid, Slug: "alpha"}},
	}
	// Owner with rank=0 (no explicit grant row) — must still be allowed.
	grants := &fakeProjectGrantStore{roleRank: 0, isOwner: true, found: true}

	got := HandleProjectCommand(context.Background(), newDeps(sess, proj, grants),
		ProjectCommandRequest{UserID: "u", SessionKey: "k", RawText: "/project switch alpha"})

	if !sess.updateCalled {
		t.Fatalf("expected UpdateProject to be called for owner; reply=%q", got)
	}
}

func TestHandleProjectCommand_SwitchNoUserID(t *testing.T) {
	deps := newDeps(&fakeSessionStore{}, &fakeProjectStore{}, &fakeProjectGrantStore{})
	got := HandleProjectCommand(context.Background(), deps,
		ProjectCommandRequest{UserID: "", SessionKey: "k", RawText: "/project switch foo"})
	if !strings.Contains(got, "user identity") {
		t.Errorf("expected pairing hint, got %q", got)
	}
}

func TestHandleProjectCommand_SwitchProjectNotFound(t *testing.T) {
	sess := &fakeSessionStore{}
	proj := &fakeProjectStore{bySlug: map[string]*store.Project{}}
	grants := &fakeProjectGrantStore{}

	got := HandleProjectCommand(context.Background(), newDeps(sess, proj, grants),
		ProjectCommandRequest{UserID: "u", SessionKey: "k", RawText: "/project switch nope"})

	if sess.updateCalled {
		t.Fatal("UpdateProject must not be called when slug is unknown")
	}
	if !strings.Contains(got, `"nope" not found`) {
		t.Errorf("expected not-found reply, got %q", got)
	}
}

func TestHandleProjectCommand_Clear(t *testing.T) {
	sess := &fakeSessionStore{}
	got := HandleProjectCommand(context.Background(),
		newDeps(sess, &fakeProjectStore{}, &fakeProjectGrantStore{}),
		ProjectCommandRequest{SessionKey: "k", RawText: "/project clear"})

	if !sess.updateCalled || sess.updateProjectID != nil {
		t.Fatalf("expected UpdateProject(nil); called=%v pid=%v", sess.updateCalled, sess.updateProjectID)
	}
	if !strings.Contains(got, "Cleared") {
		t.Errorf("expected clear confirmation, got %q", got)
	}
}

func TestHandleProjectCommand_CurrentBound(t *testing.T) {
	pid := uuid.New()
	sess := &fakeSessionStore{getResp: &store.SessionData{ProjectID: &pid}}
	proj := &fakeProjectStore{byID: map[uuid.UUID]*store.Project{pid: {ID: pid, Slug: "alpha"}}}

	got := HandleProjectCommand(context.Background(), newDeps(sess, proj, &fakeProjectGrantStore{}),
		ProjectCommandRequest{SessionKey: "k", RawText: "/project current"})

	if !strings.Contains(got, "alpha") {
		t.Errorf("expected slug in reply, got %q", got)
	}
}

func TestHandleProjectCommand_CurrentUnbound(t *testing.T) {
	got := HandleProjectCommand(context.Background(),
		newDeps(&fakeSessionStore{}, &fakeProjectStore{}, &fakeProjectGrantStore{}),
		ProjectCommandRequest{SessionKey: "k", RawText: "/project current"})

	if !strings.Contains(got, "No project is bound") {
		t.Errorf("expected unbound reply, got %q", got)
	}
}

func TestHandleProjectCommand_List(t *testing.T) {
	pid := uuid.New()
	pidStr := pid.String()
	sess := &fakeSessionStore{}
	proj := &fakeProjectStore{byID: map[uuid.UUID]*store.Project{pid: {ID: pid, Slug: "alpha"}}}
	grants := &fakeProjectGrantStore{
		listForUser: []*store.ProjectGrant{
			{ProjectID: pidStr, Role: "member"},
		},
	}

	got := HandleProjectCommand(context.Background(), newDeps(sess, proj, grants),
		ProjectCommandRequest{UserID: "u", RawText: "/project list"})

	if !strings.Contains(got, "alpha") || !strings.Contains(got, "member") {
		t.Errorf("expected slug+role in list reply, got %q", got)
	}
}

func TestHandleProjectCommand_SwitchSlugWhitespaceTrim(t *testing.T) {
	pid := uuid.New()
	sess := &fakeSessionStore{}
	proj := &fakeProjectStore{
		bySlug: map[string]*store.Project{"alpha": {ID: pid, Slug: "alpha"}},
	}
	grants := &fakeProjectGrantStore{roleRank: 1, found: true}

	got := HandleProjectCommand(context.Background(), newDeps(sess, proj, grants),
		ProjectCommandRequest{UserID: "u", SessionKey: "k", RawText: "/project switch alpha extra ignored"})

	if !sess.updateCalled {
		t.Errorf("expected UpdateProject called for slug with trailing args; reply=%q", got)
	}
}

func TestHandleProjectCommand_SwitchUpdateError(t *testing.T) {
	pid := uuid.New()
	sess := &fakeSessionStore{updateErr: errors.New("boom")}
	proj := &fakeProjectStore{
		bySlug: map[string]*store.Project{"alpha": {ID: pid, Slug: "alpha"}},
	}
	grants := &fakeProjectGrantStore{roleRank: 1, found: true}

	got := HandleProjectCommand(context.Background(), newDeps(sess, proj, grants),
		ProjectCommandRequest{UserID: "u", SessionKey: "k", RawText: "/project switch alpha"})

	if !strings.Contains(got, "Failed to switch") {
		t.Errorf("expected error reply, got %q", got)
	}
}
