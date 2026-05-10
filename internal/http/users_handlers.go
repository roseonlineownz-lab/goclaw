package http

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/auth"
	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// handleList handles GET /v1/users.
// Admin+ sees all users. Member/viewer sees only themselves in the result.
func (h *UsersHandler) handleList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	callerRole := permissions.Role(store.RoleFromContext(ctx))
	callerUID := store.UserIDFromContext(ctx)

	if !permissions.HasMinRole(callerRole, permissions.RoleAdmin) {
		// Non-admin: return only self (no enumeration of other users).
		if callerUID == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no user"})
			return
		}
		uid, err := uuid.Parse(callerUID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid user id"})
			return
		}
		user, err := h.users.Get(ctx, uid)
		if err != nil || user == nil {
			writeJSON(w, http.StatusOK, listUsersResp{Users: []UserResp{}})
			return
		}
		writeJSON(w, http.StatusOK, listUsersResp{Users: []UserResp{userToResp(*user)}})
		return
	}

	users, err := h.users.List(ctx, 1000, 0)
	if err != nil {
		locale := extractLocale(r)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": i18n.T(locale, i18n.MsgFailedToList, "users"),
		})
		return
	}
	resp := make([]UserResp, 0, len(users))
	for _, u := range users {
		resp = append(resp, userToResp(u))
	}
	writeJSON(w, http.StatusOK, listUsersResp{Users: resp})
}

// handleCreate handles POST /v1/users (RoleAdmin required — enforced at dispatch).
func (h *UsersHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	locale := extractLocale(r)
	callerRole := permissions.Role(store.RoleFromContext(ctx))

	var body createUserBody
	if !bindJSON(w, r, locale, &body) {
		return
	}

	// Normalize and validate email.
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))
	if !strings.Contains(body.Email, "@") || len(body.Email) > 320 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":   "invalid_email",
			"message": i18n.T(locale, i18n.MsgInvalidEmail),
		})
		return
	}

	// Validate password complexity (min 12 chars + letter + digit + symbol).
	if err := auth.ValidatePasswordComplexity(body.Password); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":   "weak_password",
			"message": i18n.T(locale, i18n.MsgWeakPassword),
		})
		return
	}

	// Validate role — root is bootstrap-only, never via API.
	validRoles := map[string]bool{"admin": true, "member": true, "viewer": true}
	if body.Role == "root" || !validRoles[body.Role] {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":   "invalid_role",
			"message": i18n.T(locale, i18n.MsgInvalidRequest, "role must be one of admin, member, viewer"),
		})
		return
	}

	// Admin can only create member/viewer; only root can create admin.
	if body.Role == "admin" && callerRole != permissions.RoleRoot {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":   "permission_denied",
			"message": i18n.T(locale, i18n.MsgPermissionDenied, "only root can create admin users"),
		})
		return
	}

	if body.DisplayName != nil && len(*body.DisplayName) > 200 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":   "invalid_display_name",
			"message": i18n.T(locale, i18n.MsgInvalidRequest, "display_name max 200 chars"),
		})
		return
	}

	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "hash error"})
		return
	}

	id, err := uuid.NewV7()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "uuid error"})
		return
	}

	u := &store.User{
		ID:           id,
		Email:        body.Email,
		DisplayName:  body.DisplayName,
		PasswordHash: hash,
		Role:         body.Role,
		Status:       "active",
	}
	if err := h.users.Create(ctx, u); err != nil {
		slog.Error("user.create_failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": i18n.T(locale, i18n.MsgFailedToCreate, "user", err.Error()),
		})
		return
	}

	created, err := h.users.Get(ctx, id)
	if err != nil || created == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "user created but fetch failed"})
		return
	}
	writeJSON(w, http.StatusCreated, userToResp(*created))
}

// handleGet handles GET /v1/users/{id}.
// Admin+ can fetch any user. Non-admin gets 404 for any id that is not self (no enumeration).
func (h *UsersHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	callerRole := permissions.Role(store.RoleFromContext(ctx))
	callerUID := store.UserIDFromContext(ctx)
	targetID := r.PathValue("id")

	if !permissions.HasMinRole(callerRole, permissions.RoleAdmin) && targetID != callerUID {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	uid, err := uuid.Parse(targetID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	user, err := h.users.Get(ctx, uid)
	if err != nil || user == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, http.StatusOK, userToResp(*user))
}

// handlePatch handles PATCH /v1/users/{id}.
//   - Any authenticated user can change their own display_name.
//   - Root only can change role.
//   - Admin+ can change status (active/suspended) of non-root users.
func (h *UsersHandler) handlePatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	locale := extractLocale(r)
	callerRole := permissions.Role(store.RoleFromContext(ctx))
	callerUID := store.UserIDFromContext(ctx)
	targetID := r.PathValue("id")

	isSelf := targetID == callerUID
	isAdmin := permissions.HasMinRole(callerRole, permissions.RoleAdmin)

	if !isSelf && !isAdmin {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": i18n.T(locale, i18n.MsgPermissionDenied, "not authorized to update this user"),
		})
		return
	}

	uid, err := uuid.Parse(targetID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	target, err := h.users.Get(ctx, uid)
	if err != nil || target == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	var body patchUserBody
	if !bindJSON(w, r, locale, &body) {
		return
	}

	updates := map[string]any{}

	if body.DisplayName != nil {
		name := strings.TrimSpace(*body.DisplayName)
		if len(name) > 200 {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error":   "invalid_display_name",
				"message": i18n.T(locale, i18n.MsgInvalidRequest, "display_name max 200 chars"),
			})
			return
		}
		updates["display_name"] = name
	}

	if body.Role != nil {
		if callerRole != permissions.RoleRoot {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":   "permission_denied",
				"message": i18n.T(locale, i18n.MsgPermissionDenied, "only root can change role"),
			})
			return
		}
		validRoles := map[string]bool{"admin": true, "member": true, "viewer": true}
		if !validRoles[*body.Role] {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error":   "invalid_role",
				"message": i18n.T(locale, i18n.MsgInvalidRequest, "role must be one of admin, member, viewer"),
			})
			return
		}
		updates["role"] = *body.Role
	}

	if body.Status != nil {
		if !isAdmin {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":   "permission_denied",
				"message": i18n.T(locale, i18n.MsgPermissionDenied, "only admin+ can change status"),
			})
			return
		}
		if target.Role == "root" && callerRole != permissions.RoleRoot {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":   "permission_denied",
				"message": i18n.T(locale, i18n.MsgPermissionDenied, "cannot change status of root user"),
			})
			return
		}
		if *body.Status != "active" && *body.Status != "suspended" {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error":   "invalid_status",
				"message": i18n.T(locale, i18n.MsgInvalidRequest, "status must be active or suspended"),
			})
			return
		}
		updates["status"] = *body.Status
	}

	if len(updates) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": i18n.T(locale, i18n.MsgNoUpdatesProvided),
		})
		return
	}

	if err := h.users.Update(ctx, uid, updates); err != nil {
		slog.Error("user.update_failed", "err", err, "user_id", uid)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": i18n.T(locale, i18n.MsgFailedToUpdate, "user", err.Error()),
		})
		return
	}

	updated, err := h.users.Get(ctx, uid)
	if err != nil || updated == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, http.StatusOK, userToResp(*updated))
}

// handleDelete handles DELETE /v1/users/{id} (RoleAdmin required — enforced at dispatch).
// Root users cannot be deleted. Admins cannot delete admin or root users.
func (h *UsersHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	locale := extractLocale(r)
	callerRole := permissions.Role(store.RoleFromContext(ctx))
	targetID := r.PathValue("id")

	uid, err := uuid.Parse(targetID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	target, err := h.users.Get(ctx, uid)
	if err != nil || target == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	if target.Role == "root" {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":   "permission_denied",
			"message": i18n.T(locale, i18n.MsgPermissionDenied, "root user cannot be deleted"),
		})
		return
	}

	if callerRole == permissions.RoleAdmin && target.Role == "admin" {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":   "permission_denied",
			"message": i18n.T(locale, i18n.MsgPermissionDenied, "admin cannot delete other admin users"),
		})
		return
	}

	if err := h.users.Delete(ctx, uid); err != nil {
		slog.Error("user.delete_failed", "err", err, "user_id", uid)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": i18n.T(locale, i18n.MsgFailedToDelete, "user", err.Error()),
		})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
