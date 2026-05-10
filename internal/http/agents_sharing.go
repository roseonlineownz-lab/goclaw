package http

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

func (h *AgentsHandler) handleListShares(w http.ResponseWriter, r *http.Request) {
	userID := store.UserIDFromContext(r.Context())
	locale := store.LocaleFromContext(r.Context())
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidID, "agent")})
		return
	}

	// Only the agent's UUID owner (or platform owner) can list shares.
	ag, err := h.agents.GetByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": i18n.T(locale, i18n.MsgNotFound, "agent", id.String())})
		return
	}
	if !sharesOwner(ag, userID) && !h.isOwnerUser(userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(locale, i18n.MsgOwnerOnly, "view shares")})
		return
	}

	shares, err := h.agents.ListShares(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"shares": shares})
}

func (h *AgentsHandler) handleShare(w http.ResponseWriter, r *http.Request) {
	userID := store.UserIDFromContext(r.Context())
	locale := store.LocaleFromContext(r.Context())
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidID, "agent")})
		return
	}

	// CreatedBy must be a real user UUID — share rows have a NOT NULL FK to
	// users(id). Reject channel-style identities (e.g. "telegram:123") at
	// the handler so we never insert uuid.Nil and trip a 500 from the DB.
	createdBy, perr := uuid.Parse(userID)
	if perr != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(locale, i18n.MsgOwnerOnly, "share agent")})
		return
	}

	// Only the agent's UUID owner (or platform owner) may share. Compare
	// against owner_user_id (UUID), not legacy owner_id (VARCHAR).
	ag, err := h.agents.GetByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": i18n.T(locale, i18n.MsgNotFound, "agent", id.String())})
		return
	}
	isOwner := ag.OwnerUserID != nil && *ag.OwnerUserID == createdBy
	if !isOwner && !h.isOwnerUser(userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(locale, i18n.MsgOwnerOnly, "share agent")})
		return
	}

	var req struct {
		UserID string `json:"user_id,omitempty"`
		TeamID string `json:"team_id,omitempty"`
		Role   string `json:"role"`
	}
	if !bindJSON(w, r, locale, &req) {
		return
	}
	// Target mutex: exactly one of user_id, team_id must be set.
	hasUser := req.UserID != ""
	hasTeam := req.TeamID != ""
	if hasUser == hasTeam {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidShareTarget)})
		return
	}
	if !store.ValidShareRole(req.Role) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidShareRole)})
		return
	}
	in := store.AgentShareInput{AgentID: id, Role: req.Role, CreatedBy: createdBy}
	if hasUser {
		uid, perr := uuid.Parse(req.UserID)
		if perr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidID, "user_id")})
			return
		}
		in.SharedWithUserID = &uid
	} else {
		tid, perr := uuid.Parse(req.TeamID)
		if perr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidID, "team_id")})
			return
		}
		in.SharedWithTeamID = &tid
	}

	if err := h.agents.CreateShare(r.Context(), in); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	emitAudit(h.msgBus, r, "agent.shared", "agent", id.String())
	writeJSON(w, http.StatusCreated, map[string]string{"ok": "true"})
}

func (h *AgentsHandler) handleRevokeShare(w http.ResponseWriter, r *http.Request) {
	userID := store.UserIDFromContext(r.Context())
	locale := store.LocaleFromContext(r.Context())
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidID, "agent")})
		return
	}

	// Only the agent's UUID owner (or platform owner) can revoke shares.
	ag, err := h.agents.GetByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": i18n.T(locale, i18n.MsgNotFound, "agent", id.String())})
		return
	}
	if !sharesOwner(ag, userID) && !h.isOwnerUser(userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(locale, i18n.MsgOwnerOnly, "revoke shares")})
		return
	}

	// Path supports either a UUID (user) or a "team:<uuid>" prefix to disambiguate
	// team revocation. We accept both; mismatched prefix → 400.
	targetID := r.PathValue("userID")
	if after, ok := strings.CutPrefix(targetID, "team:"); ok {
		tid, perr := uuid.Parse(after)
		if perr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidID, "team_id")})
			return
		}
		if err := h.agents.RevokeShareByTeam(r.Context(), id, tid); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	} else {
		uid, perr := uuid.Parse(targetID)
		if perr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidID, "user_id")})
			return
		}
		if err := h.agents.RevokeShareByUser(r.Context(), id, uid); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}

	emitAudit(h.msgBus, r, "agent.share_revoked", "agent", id.String())
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (h *AgentsHandler) handleRegenerate(w http.ResponseWriter, r *http.Request) {
	userID := store.UserIDFromContext(r.Context())
	locale := store.LocaleFromContext(r.Context())
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidID, "agent")})
		return
	}

	// Only owner can regenerate
	ag, err := h.agents.GetByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": i18n.T(locale, i18n.MsgNotFound, "agent", id.String())})
		return
	}
	if userID != "" && ag.OwnerID != userID && !h.isOwnerUser(userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(locale, i18n.MsgOwnerOnly, "regenerate agent")})
		return
	}
	if h.summoner == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": i18n.T(locale, i18n.MsgSummoningUnavailable)})
		return
	}

	var req struct {
		Prompt string `json:"prompt"`
	}
	if !bindJSON(w, r, locale, &req) {
		return
	}
	if req.Prompt == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgRequired, "prompt")})
		return
	}

	// Set status to summoning
	if err := h.agents.Update(r.Context(), id, map[string]any{"status": store.AgentStatusSummoning}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	go h.summoner.RegenerateAgent(id, ag.Provider, ag.Model, req.Prompt)

	emitAudit(h.msgBus, r, "agent.regenerated", "agent", id.String())
	writeJSON(w, http.StatusAccepted, map[string]string{"ok": "true", "status": store.AgentStatusSummoning})
}

// handleResummon re-runs SummonAgent from scratch using the original description.
// Used when initial summoning failed (e.g. wrong model) and user wants to retry.
func (h *AgentsHandler) handleResummon(w http.ResponseWriter, r *http.Request) {
	userID := store.UserIDFromContext(r.Context())
	locale := store.LocaleFromContext(r.Context())
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidID, "agent")})
		return
	}

	ag, err := h.agents.GetByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": i18n.T(locale, i18n.MsgNotFound, "agent", id.String())})
		return
	}
	if userID != "" && ag.OwnerID != userID && !h.isOwnerUser(userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(locale, i18n.MsgOwnerOnly, "resummon agent")})
		return
	}
	if h.summoner == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": i18n.T(locale, i18n.MsgSummoningUnavailable)})
		return
	}

	description := ag.AgentDescription
	if description == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgNoDescription)})
		return
	}

	if err := h.agents.Update(r.Context(), id, map[string]any{"status": store.AgentStatusSummoning}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	go h.summoner.SummonAgent(id, ag.Provider, ag.Model, description)

	emitAudit(h.msgBus, r, "agent.resummoned", "agent", id.String())
	writeJSON(w, http.StatusAccepted, map[string]string{"ok": "true", "status": store.AgentStatusSummoning})
}

// handleCancelSummon force-transitions a stuck 'summoning' agent to 'summon_failed'.
// Used when user wants to abort a hanging summon (UI Cancel button after 60s).
func (h *AgentsHandler) handleCancelSummon(w http.ResponseWriter, r *http.Request) {
	userID := store.UserIDFromContext(r.Context())
	locale := store.LocaleFromContext(r.Context())
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidID, "agent")})
		return
	}

	ag, err := h.agents.GetByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": i18n.T(locale, i18n.MsgNotFound, "agent", id.String())})
		return
	}
	if userID != "" && ag.OwnerID != userID && !h.isOwnerUser(userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(locale, i18n.MsgOwnerOnly, "cancel summon")})
		return
	}
	if ag.Status != store.AgentStatusSummoning {
		writeJSON(w, http.StatusConflict, map[string]string{"error": i18n.T(locale, i18n.MsgCannotCancel)})
		return
	}

	if err := h.agents.Update(r.Context(), id, map[string]any{"status": store.AgentStatusSummonFailed}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if h.msgBus != nil {
		bus.Broadcast(h.msgBus, protocol.EventAgentSummoning, map[string]any{
			"agent_id": id.String(),
			"type":     "failed",
			"error":    i18n.T(locale, i18n.MsgSummonCancelled),
		})
	}

	emitAudit(h.msgBus, r, "agent.summon_cancelled", "agent", id.String())
	writeJSON(w, http.StatusAccepted, map[string]string{"ok": "true", "status": store.AgentStatusSummonFailed})
}

// sharesOwner reports whether userID is the UUID owner of ag. Compares against
// owner_user_id (UUID) — never against the legacy owner_id VARCHAR column,
// which holds channel-style identities and is not a share-decision input.
func sharesOwner(ag *store.AgentData, userID string) bool {
	if ag == nil || ag.OwnerUserID == nil || userID == "" {
		return false
	}
	uid, err := uuid.Parse(userID)
	if err != nil {
		return false
	}
	return *ag.OwnerUserID == uid
}

// writeJSON moved to response_helpers.go
