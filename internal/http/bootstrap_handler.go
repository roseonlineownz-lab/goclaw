package http

import (
	"database/sql"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/auth"
	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// BootstrapHandler handles the two bootstrap lifecycle endpoints:
//
//	GET  /v1/bootstrap/status
//	POST /v1/bootstrap/init
type BootstrapHandler struct {
	users      store.UsersStore
	sessions   store.UserSessionsStore
	db         *sql.DB
	jwtKs      *auth.JWTKeyset
	accessTTL  time.Duration
	refreshTTL time.Duration
	isPG       bool // true when the underlying driver is PostgreSQL
}

// NewBootstrapHandler constructs a BootstrapHandler.
// db is used only for the pg_advisory_xact_lock race guard; nil is safe (SQLite skips the lock).
func NewBootstrapHandler(
	users store.UsersStore,
	sessions store.UserSessionsStore,
	db *sql.DB,
	jwtKs *auth.JWTKeyset,
	accessTTL, refreshTTL time.Duration,
) *BootstrapHandler {
	h := &BootstrapHandler{
		users:      users,
		sessions:   sessions,
		db:         db,
		jwtKs:      jwtKs,
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
	}
	if db != nil {
		h.isPG = detectPostgres(db)
	}
	return h
}

// detectPostgres probes the DB for a PG-only function to determine dialect.
func detectPostgres(db *sql.DB) bool {
	row := db.QueryRow("SELECT current_setting('server_version_num')")
	var v string
	return row.Scan(&v) == nil
}

// RegisterRoutes mounts the two bootstrap endpoints on mux.
func (h *BootstrapHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v1/bootstrap/status", h.handleStatus)
	mux.HandleFunc("/v1/bootstrap/init", h.handleInit)
}

// handleStatus returns {"bootstrapped": <bool>}.
// No auth required — clients need this to decide whether to show the setup wizard.
func (h *BootstrapHandler) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{
		"bootstrapped": !IsBootstrapRequired(),
	})
}

// bootstrapInitBody is the expected JSON payload for POST /v1/bootstrap/init.
type bootstrapInitBody struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

// handleInit creates the first root user and returns JWT tokens.
func (h *BootstrapHandler) handleInit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	locale := extractLocale(r)

	// 1. Already bootstrapped → 409.
	if !IsBootstrapRequired() {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":   "bootstrap_already_done",
			"message": i18n.T(locale, i18n.MsgBootstrapAlreadyDone),
		})
		return
	}

	// 2. Loopback-only. Defense-in-depth alongside the bootstrap token.
	// KISS: we do not spin up a separate listener; this check prevents remote
	// callers from reaching the endpoint even if they have the token.
	if !isLoopback(r) {
		slog.Warn("security.bootstrap_remote_attempt", "ip", r.RemoteAddr)
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}

	// 3. Validate X-Bootstrap-Token (constant-time compare against in-memory token).
	if !validateBootstrapToken(r.Header.Get("X-Bootstrap-Token")) {
		slog.Warn("security.bootstrap_invalid_token", "ip", r.RemoteAddr)
		w.WriteHeader(http.StatusForbidden)
		return
	}

	// 4. Decode and validate body.
	var body bootstrapInitBody
	if !bindJSON(w, r, locale, &body) {
		return
	}
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))

	if err := validateEmail(body.Email); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":   "invalid_email",
			"message": i18n.T(locale, i18n.MsgInvalidEmail),
		})
		return
	}
	if err := auth.ValidatePasswordComplexity(body.Password); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":   "weak_password",
			"message": i18n.T(locale, i18n.MsgWeakPassword),
		})
		return
	}

	// 5–8. Atomic TX: advisory lock + count check + insert.
	userID, err := h.createRootUser(r, body)
	if err != nil {
		if err == errAlreadyBootstrapped {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":   "bootstrap_already_done",
				"message": i18n.T(locale, i18n.MsgBootstrapAlreadyDone),
			})
			return
		}
		slog.Error("bootstrap.create_root_user_failed", "err", err)
		// Generic message — do NOT leak raw err.Error() (may include DSN, schema, driver info).
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "internal_error",
		})
		return
	}

	// 9. Issue refresh token (new family).
	familyID, err := uuid.NewV7()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "uuid error"})
		return
	}
	rawRefresh, _, err := auth.IssueRefresh(r.Context(), h.sessions, userID, familyID, h.refreshTTL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "session error"})
		return
	}

	// 10. Issue access JWT.
	accessToken, err := auth.IssueAccess(h.jwtKs, userID.String(), permissions.RoleRoot, h.accessTTL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "token error"})
		return
	}

	// 11. Flip state.
	SetBootstrapRequired(false)
	clearBootstrapToken()

	// 12. Audit log.
	slog.Info("auth.bootstrap_completed", "user_id", userID, "ip", r.RemoteAddr)

	// 13. Return tokens.
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  accessToken,
		"refresh_token": rawRefresh,
		"user_id":       userID.String(),
		"role":          string(permissions.RoleRoot),
	})
}
