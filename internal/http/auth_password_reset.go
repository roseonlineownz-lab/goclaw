package http

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/auth"
	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const (
	resetTokenBytes = 32        // raw entropy bytes → base64url; SHA-256 hex stored
	resetTokenTTL   = time.Hour // single TTL for v4 rc1; configurable post-rc1
)

// nowFn / resetTokenDuration are package-level for test injection. Tests
// override nowFn to advance time without sleeping.
var (
	nowFn               = time.Now
	resetTokenDuration  = func() time.Duration { return resetTokenTTL }
)

type passwordResetRequestBody struct {
	Email string `json:"email"`
}

type passwordResetConfirmBody struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

// handlePasswordResetRequest issues a single-use reset token to the email
// owner. Returns 204 unconditionally — same shape regardless of whether the
// email matches an active user (no enumeration). Constant-cost path on
// unknown email reuses the dummy Argon2id hash so VerifyPassword burns the
// same CPU as the lookup-then-issue path (no time.Sleep — sleep is detectable).
func (h *AuthHandler) handlePasswordResetRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	locale := extractLocale(r)
	var body passwordResetRequestBody
	if !bindJSON(w, r, locale, &body) {
		return
	}
	emailAddr := strings.ToLower(strings.TrimSpace(body.Email))

	user, err := h.users.GetByEmail(r.Context(), emailAddr)
	active := err == nil && user != nil && user.Status == "active" && user.DeletedAt == nil
	if !active {
		// Constant-cost dummy work. Mirrors handleLogin's enumeration guard.
		_, _ = auth.VerifyPassword("dummy", dummyArgon2idHash)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	rawToken, tokenHash, err := newResetToken()
	if err != nil {
		slog.Error("password_reset.token_gen_failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "token generation failed"})
		return
	}
	expiresAt := nowFn().Add(resetTokenDuration())
	if _, err := h.resetTokens.Insert(r.Context(), user.ID, tokenHash, expiresAt); err != nil {
		slog.Error("password_reset.insert_failed", "err", err, "user_id", user.ID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not issue token"})
		return
	}

	resetURL := buildResetURL(h.webURL, rawToken)
	if err := h.emailer.SendPasswordReset(r.Context(), user.Email, resetURL); err != nil {
		// Email delivery failure is logged but not surfaced — never tell the
		// caller whether the email matched an account.
		slog.Warn("password_reset.email_dispatch_failed", "err", err, "user_id", user.ID)
	}
	slog.Info("auth.password_reset_requested", "user_id", user.ID, "ip", r.RemoteAddr)
	w.WriteHeader(http.StatusNoContent)
}

// handlePasswordResetConfirm verifies the single-use token and atomically
// rotates the password + revokes every active refresh session inside one TX
// (composes Phase 02 MarkUsed + Phase 01 ChangePasswordAndRevokeSessions).
// 401 on invalid/expired/used token; 422 on weak password; 500 on store
// failure with rollback.
func (h *AuthHandler) handlePasswordResetConfirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	locale := extractLocale(r)
	var body passwordResetConfirmBody
	if !bindJSON(w, r, locale, &body) {
		return
	}
	body.Token = strings.TrimSpace(body.Token)
	if body.Token == "" || body.NewPassword == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing fields"})
		return
	}
	if err := auth.ValidatePasswordComplexity(body.NewPassword); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":   "weak_password",
			"message": i18n.T(locale, i18n.MsgWeakPassword),
		})
		return
	}
	newHash, err := auth.HashPassword(body.NewPassword)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "hash error"})
		return
	}
	codeHash := hashResetToken(body.Token)
	if err := h.users.ConfirmPasswordReset(r.Context(), codeHash, newHash); err != nil {
		if errors.Is(err, store.ErrPasswordResetNotFound) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error":   "invalid_token",
				"message": i18n.T(locale, i18n.MsgPasswordResetInvalidToken),
			})
			return
		}
		slog.Error("password_reset.confirm_failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "confirm failed"})
		return
	}
	slog.Info("auth.password_reset_confirmed", "ip", r.RemoteAddr)
	w.WriteHeader(http.StatusNoContent)
}

// newResetToken returns (rawTokenURL, sha256HexHash). Raw is sent to the user
// once via email; the hash is what we persist + look up.
func newResetToken() (string, string, error) {
	buf := make([]byte, resetTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("rand: %w", err)
	}
	raw := base64.RawURLEncoding.EncodeToString(buf)
	return raw, hashResetToken(raw), nil
}

func hashResetToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func buildResetURL(webURL, rawToken string) string {
	base := strings.TrimRight(webURL, "/")
	if base == "" {
		base = "/"
	}
	return base + "/reset-password?token=" + rawToken
}
