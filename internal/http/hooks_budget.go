package http

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// HooksBudgetHandler serves per-user hook token budget information.
//
//	GET /v1/hooks/budget
//
// Reads the caller's own budget row — never leaks cross-user data.
// UserID is extracted from the JWT context (set by requireAuth enrichContext),
// never from query params or request body.
type HooksBudgetHandler struct {
	budget store.UserHookBudgetStore
}

// NewHooksBudgetHandler constructs a HooksBudgetHandler.
func NewHooksBudgetHandler(budget store.UserHookBudgetStore) *HooksBudgetHandler {
	return &HooksBudgetHandler{budget: budget}
}

// RegisterRoutes mounts the hooks budget endpoint on mux.
func (h *HooksBudgetHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v1/hooks/budget", requireAuth(permissions.RoleViewer, h.handleGet))
}

// hooksBudgetResp is the JSON response shape for GET /v1/hooks/budget.
type hooksBudgetResp struct {
	UserID          string `json:"user_id"`
	MonthStart      string `json:"month_start"`      // YYYY-MM-DD
	BudgetTotal     int    `json:"budget_total"`
	Remaining       int    `json:"remaining"`
	WarnThresholdPct int   `json:"warn_threshold_pct"`
}

func (h *HooksBudgetHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	// UserID comes exclusively from the JWT/session context — never from request input.
	rawID := store.UserIDFromContext(r.Context())
	userID, err := uuid.Parse(rawID)
	if err != nil || userID == uuid.Nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing or invalid user identity"})
		return
	}

	b, err := h.budget.Get(r.Context(), userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error": "no budget row yet — row is seeded on first prompt hook call this month",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	writeJSON(w, http.StatusOK, hooksBudgetResp{
		UserID:           b.UserID.String(),
		MonthStart:       b.MonthStart.Format("2006-01-02"),
		BudgetTotal:      b.BudgetTotal,
		Remaining:        b.Remaining,
		WarnThresholdPct: 20,
	})
}
