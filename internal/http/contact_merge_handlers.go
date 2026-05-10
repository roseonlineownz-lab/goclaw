package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/workspace"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// ContactMergeHandler exposes the v4 merge-contact endpoint. It is the ONLY
// sanctioned entry point for merging channel contacts into an authenticated
// user. The handler delegates to store.ContactStore.MergeUserAggregate which
// owns a single *sql.Tx covering channel_contacts + agent_sessions +
// user_context_files + memory_documents + traces.
type ContactMergeHandler struct {
	contactStore store.ContactStore
	usersStore   store.UsersStore
	msgBus       *bus.MessageBus
	workspaceDir string // base dir for FS relocation; may be empty (relocation skipped)
}

// NewContactMergeHandler constructs the handler. The users store is required
// for the target-user existence pre-check; it may be nil only in unit tests.
// workspaceDir is the base workspace root used for post-commit group FS
// relocation. Pass "" to skip relocation (e.g. in tests or lite builds that
// don't run channel chat).
func NewContactMergeHandler(cs store.ContactStore, us store.UsersStore, msgBus *bus.MessageBus, workspaceDir string) *ContactMergeHandler {
	return &ContactMergeHandler{contactStore: cs, usersStore: us, msgBus: msgBus, workspaceDir: workspaceDir}
}

// RegisterRoutes registers the merge endpoint. RoleAdmin is required: a member
// can never merge contacts on someone else's behalf.
func (h *ContactMergeHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/contacts/merge", h.adminAuth(h.handleMerge))
}

func (h *ContactMergeHandler) adminAuth(next http.HandlerFunc) http.HandlerFunc {
	return requireAuth(permissions.RoleAdmin, next)
}

// mergeRequest is the JSON payload accepted by POST /v1/contacts/merge.
type mergeRequest struct {
	ContactIDs   []string `json:"contact_ids"`
	TargetUserID string   `json:"target_user_id"`
}

func (h *ContactMergeHandler) handleMerge(w http.ResponseWriter, r *http.Request) {
	locale := extractLocale(r)
	ctx := r.Context()

	var req mergeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidJSON))
		return
	}
	if len(req.ContactIDs) == 0 {
		writeError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgContactIDsRequired))
		return
	}
	if req.TargetUserID == "" {
		writeError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgMergeTargetRequired))
		return
	}

	var body struct {
		ContactIDs   []uuid.UUID `json:"contact_ids"`
		TenantUserID *uuid.UUID  `json:"tenant_user_id"`
		CreateUser   *struct {
			UserID      string `json:"user_id"`
			DisplayName string `json:"display_name"`
		} `json:"create_user"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidJSON)})
		return
	}

	if len(body.ContactIDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgContactIDsRequired)})
		return
	}

	mergeAudit := buildMergeAudit(r, fromChannel, fromSender)
	auditBytes, _ := json.Marshal(mergeAudit) // map[string]any is always marshallable

	mergeReq := store.MergeUserAggregateRequest{
		ContactIDs:    contactIDs,
		SourceUserIDs: sourceUserIDs,
		TargetUserID:  targetID,
		MergeAudit:    auditBytes,
	}
	// Wire FS relocation for group contacts when workspace is configured.
	// Runs post-commit outside the TX — failure must never surface as a merge error.
	if h.workspaceDir != "" && h.usersStore != nil {
		baseDir := h.workspaceDir
		cs := h.contactStore
		us := h.usersStore
		tgtID := targetID
		mergeReq.OnGroupContactsMerged = func(groupContactIDs []uuid.UUID) {
			relocateMergedGroupContacts(context.Background(), cs, us, baseDir, tgtID, groupContactIDs)
		}
	}

	if err := h.contactStore.MergeUserAggregate(ctx, mergeReq); err != nil {
		writeMergeError(w, locale, err)
		return
	}

	var targetID uuid.UUID
	var targetUserID string // tenant_user.user_id string for context file migration

	if hasTU {
		// Link to existing tenant_user — verify same tenant.
		tu, err := h.tenantStore.GetTenantUser(r.Context(), *body.TenantUserID)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": i18n.T(locale, i18n.MsgTenantUserNotFound)})
			return
		}
		if tu.TenantID != tid {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(locale, i18n.MsgTenantMismatch)})
			return
		}
		targetID = tu.ID
		targetUserID = tu.UserID
	} else {
		// Create new tenant_user.
		userID := body.CreateUser.UserID
		displayName := body.CreateUser.DisplayName
		if userID == "" {
			// Fallback: derive from first contact's username.
			userID = h.deriveUserIDFromContacts(r.Context(), body.ContactIDs)
		}
		if userID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgRequired, "user_id")})
			return
		}
		tu, err := h.tenantStore.CreateTenantUserReturning(r.Context(), tid, userID, displayName, store.TenantRoleMember)
		if err != nil {
			slog.Error("contacts.merge.create_user", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(locale, i18n.MsgFailedToCreate, "tenant user", err.Error())})
			return
		}
		targetID = tu.ID
		targetUserID = tu.UserID
	}

	if err := h.contactStore.MergeContacts(r.Context(), body.ContactIDs, targetID); err != nil {
		slog.Error("contacts.merge", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(locale, i18n.MsgFailedToUpdate, "contacts", err.Error())})
		return
	}

	// Migrate user_context_files from old sender_ids to new tenant_user_id.
	h.migrateContextFilesOnMerge(r.Context(), body.ContactIDs, targetUserID)

	emitAudit(h.msgBus, r, "contacts.merged", "tenant_user", targetID.String())
	writeJSON(w, http.StatusOK, map[string]any{
		"merged_id":    targetID,
		"merged_count": len(body.ContactIDs),
	})
}

// handleUnmergeContacts removes merged_id from selected contacts.
// POST /v1/contacts/unmerge
func (h *ChannelInstancesHandler) handleUnmergeContacts(w http.ResponseWriter, r *http.Request) {
	locale := store.LocaleFromContext(r.Context())
	tid := store.TenantIDFromContext(r.Context())
	if tid == uuid.Nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgTenantScopeRequired)})
		return
	}

	var body struct {
		ContactIDs []uuid.UUID `json:"contact_ids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidJSON)})
		return
	}
	if len(body.ContactIDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgContactIDsRequired)})
		return
	}
	if len(body.ContactIDs) > 500 {
		body.ContactIDs = body.ContactIDs[:500]
	}

	if err := h.contactStore.UnmergeContacts(r.Context(), body.ContactIDs); err != nil {
		slog.Error("contacts.unmerge", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(locale, i18n.MsgFailedToUpdate, "contacts", err.Error())})
		return
	}

	emitAudit(h.msgBus, r, "contacts.unmerged", "contacts", "")
	writeJSON(w, http.StatusOK, map[string]any{"unmerged_count": len(body.ContactIDs)})
}

// handleListMergedContacts returns contacts linked to a tenant_user.
// GET /v1/contacts/merged/{tenantUserId}
func (h *ChannelInstancesHandler) handleListMergedContacts(w http.ResponseWriter, r *http.Request) {
	locale := store.LocaleFromContext(r.Context())
	tid := store.TenantIDFromContext(r.Context())
	if tid == uuid.Nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgTenantScopeRequired)})
		return
	}

	mergedID, err := uuid.Parse(r.PathValue("tenantUserId"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidID, "tenantUserId")})
		return
	}

	contacts, err := h.contactStore.GetContactsByMergedID(r.Context(), mergedID)
	if err != nil {
		slog.Error("contacts.merged.list", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(locale, i18n.MsgFailedToList, "contacts")})
		return
	}
	if contacts == nil {
		contacts = []store.ChannelContact{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"contacts": contacts})
}

// handleListTenantUsers returns users for the current tenant (for merge dialog dropdown).
// GET /v1/tenant-users
func (h *ChannelInstancesHandler) handleListTenantUsers(w http.ResponseWriter, r *http.Request) {
	locale := store.LocaleFromContext(r.Context())
	tid := store.TenantIDFromContext(r.Context())
	if tid == uuid.Nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgTenantScopeRequired)})
		return
	}

	users, err := h.tenantStore.ListUsers(r.Context(), tid)
	if err != nil {
		slog.Error("tenant_users.list", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(locale, i18n.MsgFailedToList, "tenant users")})
		return
	}
	if users == nil {
		users = []store.TenantUserData{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

// migrateContextFilesOnMerge moves user_context_files from old sender_ids to the new tenant_user_id.
// Best-effort: log errors but don't fail the merge.
func (h *ChannelInstancesHandler) migrateContextFilesOnMerge(ctx context.Context, contactIDs []uuid.UUID, newUserID string) {
	// Batch-fetch sender_ids from merged contacts in one query.
	oldUserIDs, err := h.contactStore.GetSenderIDsByContactIDs(ctx, contactIDs)
	if err != nil {
		slog.Warn("contacts.merge.get_sender_ids", "error", err)
		return
	}
	// Filter out the target user_id itself (no self-migration needed).
	filtered := oldUserIDs[:0]
	for _, id := range oldUserIDs {
		if id != newUserID {
			filtered = append(filtered, id)
		}
	}
	if len(filtered) == 0 {
		return
	}
	if err := h.agentStore.MigrateUserDataOnMerge(ctx, filtered, newUserID); err != nil {
		slog.Warn("contacts.merge.migrate_context_files", "error", err, "old_ids", filtered, "new_id", newUserID)
	}
}

// deriveUserIDFromContacts returns the first contact's username or sender_id as fallback user_id.
func (h *ChannelInstancesHandler) deriveUserIDFromContacts(ctx context.Context, contactIDs []uuid.UUID) string {
	if len(contactIDs) == 0 {
		return ""
	}
	c, err := h.contactStore.GetContactByID(ctx, contactIDs[0])
	if err != nil {
		return ""
	}
	if c.Username != nil && *c.Username != "" {
		return *c.Username
	}
	return c.SenderID
}

// relocateMergedGroupContacts performs best-effort FS workspace relocation for
// group contacts that have been merged into a canonical user account. Called
// post-commit; errors are logged but never propagate to the merge response.
//
// Strategy: for each group contact, look up its channel_type and sender_id, then
// glob for any agent or team group directory matching that segment under baseDir.
// Matching directories are relocated to the canonical users/{user_key}/groups/ zone.
func relocateMergedGroupContacts(
	ctx context.Context,
	cs store.ContactStore,
	us store.UsersStore,
	baseDir string,
	targetUserID uuid.UUID,
	groupContactIDs []uuid.UUID,
) {
	if len(groupContactIDs) == 0 || baseDir == "" {
		return
	}

	// Resolve target user_key for the canonical destination path.
	targetUser, err := us.Get(ctx, targetUserID)
	if err != nil {
		slog.Warn("merge.fs_relocate_failed", "reason", "target user lookup", "error", err, "target_user_id", targetUserID)
		return
	}
	if targetUser.UserKey == "" {
		slog.Warn("merge.fs_relocate_failed", "reason", "target user_key empty", "target_user_id", targetUserID)
		return
	}

	for _, cid := range groupContactIDs {
		contact, getErr := cs.GetContactByID(ctx, cid)
		if getErr != nil {
			slog.Warn("merge.fs_relocate_failed", "reason", "contact lookup", "contact_id", cid, "error", getErr)
			continue
		}
		if contact.ChannelType == "" || contact.SenderID == "" {
			continue
		}

		// Sanitize all segments before path construction. The resolver
		// always sanitizes; this handler must stay aligned or a malicious
		// channel_type/sender_id/user_key value (e.g. "../escape") would
		// break out of baseDir on a relocation move.
		groupSeg := workspace.SanitizeSegment(contact.ChannelType) + "-" + workspace.SanitizeSegment(contact.SenderID)
		userSeg := workspace.SanitizeSegment(targetUser.UserKey)

		// Find all group directories for this contact across all agents and teams.
		// Pattern covers: agents/{any}/groups/{seg} and teams/{any}/groups/{seg}.
		patterns := []string{
			filepath.Join(baseDir, "agents", "*", "groups", groupSeg),
			filepath.Join(baseDir, "teams", "*", "groups", groupSeg),
		}
		newPath := filepath.Join(baseDir, "users", userSeg, "groups", groupSeg)

		for _, pattern := range patterns {
			matches, globErr := filepath.Glob(pattern)
			if globErr != nil {
				slog.Warn("merge.fs_relocate_failed", "reason", "glob error", "pattern", pattern, "error", globErr)
				continue
			}
			for _, oldPath := range matches {
				if relocErr := workspace.RelocateOnMerge(oldPath, newPath); relocErr != nil {
					slog.Warn("merge.fs_relocate_failed", "old_path", oldPath, "new_path", newPath, "error", relocErr)
				}
			}
		}
	}
}
