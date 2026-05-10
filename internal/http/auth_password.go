package http

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/auth"
	"github.com/nextlevelbuilder/goclaw/internal/email"
	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// dummyArgon2idHash is consumed when GetByEmail returns no row, so login
// timing remains constant regardless of whether the email exists. This is the
// PHC encoding of HashPassword("dummy-password-for-timing-equalization").
//
// Computed once at init() to avoid recomputing per request.
var dummyArgon2idHash string

func init() {
	h, err := auth.HashPassword("dummy-password-for-timing-equalization")
	if err == nil {
		dummyArgon2idHash = h
	}
}

// AuthHandler handles password-based auth lifecycle:
//
//	POST /v1/auth/login
//	POST /v1/auth/refresh
//	POST /v1/auth/logout
//	GET  /v1/auth/me
//	POST /v1/auth/password-reset/request
//	POST /v1/auth/password-reset/confirm
type AuthHandler struct {
	users       store.UsersStore
	sessions    store.UserSessionsStore
	resetTokens store.PasswordResetStore
	emailer     email.Dispatcher
	jwtKs       *auth.JWTKeyset
	accessTTL   time.Duration
	refreshTTL  time.Duration
	webURL      string // base URL the password-reset email links into; e.g. https://app.example.com
}

// NewAuthHandler constructs an AuthHandler. resetTokens / emailer / webURL
// are required by the password-reset endpoints; pass non-nil values when those
// routes are registered.
func NewAuthHandler(
	users store.UsersStore,
	sessions store.UserSessionsStore,
	resetTokens store.PasswordResetStore,
	emailer email.Dispatcher,
	jwtKs *auth.JWTKeyset,
	accessTTL, refreshTTL time.Duration,
	webURL string,
) *AuthHandler {
	return &AuthHandler{
		users:       users,
		sessions:    sessions,
		resetTokens: resetTokens,
		emailer:     emailer,
		jwtKs:       jwtKs,
		accessTTL:   accessTTL,
		refreshTTL:  refreshTTL,
		webURL:      webURL,
	}
}

// RegisterRoutes mounts the auth + self-management endpoints on mux.
func (h *AuthHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v1/auth/login", loginRateLimitMiddleware(h.handleLogin))
	mux.HandleFunc("/v1/auth/refresh", h.handleRefresh)
	mux.HandleFunc("/v1/auth/logout", requireAuth(permissions.RoleViewer, h.handleLogout))
	mux.HandleFunc("/v1/auth/me", requireAuth(permissions.RoleViewer, h.handleMe))
	mux.HandleFunc("/v1/auth/change-password", requireAuth(permissions.RoleViewer, h.handleChangePassword))
	mux.HandleFunc("/v1/auth/password-reset/request", loginRateLimitMiddleware(h.handlePasswordResetRequest))
	mux.HandleFunc("/v1/auth/password-reset/confirm", h.handlePasswordResetConfirm)
	mux.HandleFunc("/v1/users/me", requireAuth(permissions.RoleViewer, h.handleUpdateMe))
}

type loginBody struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *AuthHandler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	locale := extractLocale(r)
	var body loginBody
	if !bindJSON(w, r, locale, &body) {
		return
	}
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))

	// Constant-shape failure response — used for unknown email, wrong password,
	// suspended user. Hides which case triggered (no enumeration).
	failResp := func() {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error":   "invalid_credentials",
			"message": i18n.T(locale, i18n.MsgInvalidCredentials),
		})
	}

	user, err := h.users.GetByEmail(r.Context(), body.Email)
	// Always run VerifyPassword to equalize timing — even when user not found.
	hashToCheck := dummyArgon2idHash
	userActive := false
	if err == nil && user != nil {
		hashToCheck = user.PasswordHash
		userActive = user.Status == "active" && user.DeletedAt == nil
	}
	ok, _ := auth.VerifyPassword(body.Password, hashToCheck)
	if !ok || user == nil || !userActive {
		emailHash := sha256.Sum256([]byte(body.Email))
		slog.Warn("security.auth.login_failed",
			"email_hash", hex.EncodeToString(emailHash[:8]),
			"ip", r.RemoteAddr,
		)
		failResp()
		return
	}

	familyID, err := uuid.NewV7()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "uuid error"})
		return
	}
	rawRefresh, _, err := auth.IssueRefresh(r.Context(), h.sessions, user.ID, familyID, h.refreshTTL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "session error"})
		return
	}
	accessToken, err := auth.IssueAccess(h.jwtKs, user.ID.String(), permissions.Role(user.Role), h.accessTTL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "token error"})
		return
	}
	slog.Info("auth.login_success", "user_id", user.ID, "ip", r.RemoteAddr)

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  accessToken,
		"refresh_token": rawRefresh,
		"user_id":       user.ID.String(),
		"role":          user.Role,
	})
}

type refreshBody struct {
	RefreshToken string `json:"refresh_token"`
}

func (h *AuthHandler) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	locale := extractLocale(r)
	var body refreshBody
	if !bindJSON(w, r, locale, &body) {
		return
	}
	if body.RefreshToken == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":   "refresh_token_required",
			"message": i18n.T(locale, i18n.MsgRefreshTokenInvalid),
		})
		return
	}

	rawNew, newSess, err := auth.RotateRefresh(r.Context(), h.sessions, body.RefreshToken, h.refreshTTL)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error":   "refresh_failed",
			"message": refreshErrMessage(locale, err),
		})
		return
	}

	user, err := h.users.Get(r.Context(), newSess.UserID)
	if err != nil || user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error":   "user_not_found",
			"message": i18n.T(locale, i18n.MsgRefreshTokenInvalid),
		})
		return
	}
	accessToken, err := auth.IssueAccess(h.jwtKs, user.ID.String(), permissions.Role(user.Role), h.accessTTL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "token error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  accessToken,
		"refresh_token": rawNew,
	})
}

// refreshErrMessage maps auth.RotateRefresh errors to localized messages.
func refreshErrMessage(locale string, err error) string {
	switch {
	case errors.Is(err, auth.ErrRefreshExpired):
		return i18n.T(locale, i18n.MsgRefreshTokenExpired)
	case errors.Is(err, auth.ErrRefreshRevoked):
		return i18n.T(locale, i18n.MsgRefreshTokenRevoked)
	default:
		return i18n.T(locale, i18n.MsgRefreshTokenInvalid)
	}
}

func (h *AuthHandler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	uidStr := store.UserIDFromContext(r.Context())
	if uidStr == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no user"})
		return
	}
	uid, err := uuid.Parse(uidStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid user id"})
		return
	}
	if err := auth.RevokeAllForUser(r.Context(), h.sessions, uid); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "logout failed"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AuthHandler) handleMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	uidStr := store.UserIDFromContext(r.Context())
	if uidStr == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no user"})
		return
	}
	uid, err := uuid.Parse(uidStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid user id"})
		return
	}
	user, err := h.users.Get(r.Context(), uid)
	if err != nil || user == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	resp := map[string]any{
		"user_id":    user.ID.String(),
		"email":      user.Email,
		"role":       user.Role,
		"status":     user.Status,
		"created_at": user.CreatedAt,
	}
	if user.DisplayName != nil {
		resp["display_name"] = *user.DisplayName
	}
	writeJSON(w, http.StatusOK, resp)
}

// updateMeBody is the JSON body for PATCH /v1/users/me. Optional fields; only
// non-nil fields are persisted.
type updateMeBody struct {
	DisplayName *string `json:"display_name"`
}

// handleUpdateMe lets an authenticated user update their own profile.
// Currently only display_name is mutable; email + role + status remain
// admin-controlled and are intentionally not exposed here.
func (h *AuthHandler) handleUpdateMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	locale := extractLocale(r)
	uidStr := store.UserIDFromContext(r.Context())
	if uidStr == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no user"})
		return
	}
	uid, err := uuid.Parse(uidStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid user id"})
		return
	}
	var body updateMeBody
	if !bindJSON(w, r, locale, &body) {
		return
	}
	updates := map[string]any{}
	if body.DisplayName != nil {
		name := strings.TrimSpace(*body.DisplayName)
		// Mirror FE schema: 2..64 chars. Backend remains source of truth.
		if n := len(name); n < 2 || n > 64 {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error":   "display_name_invalid",
				"message": i18n.T(locale, i18n.MsgDisplayNameInvalid),
			})
			return
		}
		updates["display_name"] = name
	}
	if len(updates) > 0 {
		if err := h.users.Update(r.Context(), uid, updates); err != nil {
			slog.Error("user.update_failed", "err", err, "user_id", uid)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update failed"})
			return
		}
	}
	// Return the post-update row directly. Cannot delegate to handleMe — it
	// only accepts GET.
	user, err := h.users.Get(r.Context(), uid)
	if err != nil || user == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	resp := map[string]any{
		"user_id":    user.ID.String(),
		"email":      user.Email,
		"role":       user.Role,
		"status":     user.Status,
		"created_at": user.CreatedAt,
	}
	if user.DisplayName != nil {
		resp["display_name"] = *user.DisplayName
	}
	writeJSON(w, http.StatusOK, resp)
}

type changePasswordBody struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// handleChangePassword verifies the current password, validates the new one,
// hashes + writes it, and revokes ALL sessions (including the caller's). The
// client is expected to redirect to /login afterwards. Revoking everything is
// the simpler + safer choice over selective revocation: a stolen access token
// cannot ride out the rotation grace period.
func (h *AuthHandler) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	locale := extractLocale(r)
	uidStr := store.UserIDFromContext(r.Context())
	if uidStr == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no user"})
		return
	}
	uid, err := uuid.Parse(uidStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid user id"})
		return
	}
	var body changePasswordBody
	if !bindJSON(w, r, locale, &body) {
		return
	}
	if body.CurrentPassword == "" || body.NewPassword == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing fields"})
		return
	}
	if body.CurrentPassword == body.NewPassword {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":   "weak_password",
			"message": i18n.T(locale, i18n.MsgWeakPassword),
		})
		return
	}
	if err := auth.ValidatePasswordComplexity(body.NewPassword); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":   "weak_password",
			"message": i18n.T(locale, i18n.MsgWeakPassword),
		})
		return
	}
	user, err := h.users.Get(r.Context(), uid)
	if err != nil || user == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	ok, _ := auth.VerifyPassword(body.CurrentPassword, user.PasswordHash)
	if !ok {
		slog.Warn("security.change_password_wrong_current", "user_id", uid, "ip", r.RemoteAddr)
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error":   "current_password_wrong",
			"message": i18n.T(locale, i18n.MsgCurrentPasswordWrong),
		})
		return
	}
	newHash, err := auth.HashPassword(body.NewPassword)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "hash error"})
		return
	}
	// Atomic: password update + session revoke share one transaction. Any
	// failure rolls back both, so the client never gets a 200 response while
	// old refresh tokens remain valid.
	if err := h.users.ChangePasswordAndRevokeSessions(r.Context(), uid, newHash); err != nil {
		slog.Warn("security.password_change_atomic_failed", "err", err, "user_id", uid)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update failed"})
		return
	}
	slog.Info("auth.password_changed", "user_id", uid, "ip", r.RemoteAddr)
	w.WriteHeader(http.StatusNoContent)
}
