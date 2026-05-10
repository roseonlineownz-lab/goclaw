package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// CuratorRunsHandler handles curator run lifecycle endpoints.
type CuratorRunsHandler struct {
	runs   store.CuratorRunsStore
	events store.CuratorEventsStore
}

// NewCuratorRunsHandler creates a handler for curator run endpoints.
func NewCuratorRunsHandler(runs store.CuratorRunsStore, events store.CuratorEventsStore) *CuratorRunsHandler {
	return &CuratorRunsHandler{runs: runs, events: events}
}

// RegisterRoutes registers curator run routes on the given mux.
func (h *CuratorRunsHandler) RegisterRoutes(mux *http.ServeMux) {
	// Skill-scoped run operations (member+)
	mux.HandleFunc("POST /v1/skills/{id}/curator-runs", requireAuth("", h.handleStart))
	mux.HandleFunc("GET /v1/skills/{id}/curator-runs", requireAuth("", h.handleListBySkill))
	// Run-level operations (member+)
	mux.HandleFunc("POST /v1/curator-runs/{rid}/events", requireAuth("", h.handleAppendEvent))
	mux.HandleFunc("POST /v1/curator-runs/{rid}/complete", requireAuth("", h.handleComplete))
	mux.HandleFunc("POST /v1/curator-runs/{rid}/fail", requireAuth("", h.handleFail))
	mux.HandleFunc("GET /v1/curator-runs/{rid}/events", requireAuth("", h.handleListEvents))
}

// handleStart starts a new curator run for a skill.
func (h *CuratorRunsHandler) handleStart(w http.ResponseWriter, r *http.Request) {
	locale := store.LocaleFromContext(r.Context())
	skillIDStr := r.PathValue("id")
	skillID, err := uuid.Parse(skillIDStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidID, "skill")})
		return
	}
	var body struct {
		TriggeredBy *string `json:"triggered_by"`
	}
	if r.ContentLength > 0 {
		if !bindJSON(w, r, locale, &body) {
			return
		}
	}
	run := &store.CuratorRun{
		SkillID:     &skillID,
		TriggeredBy: body.TriggeredBy,
	}
	if err := h.runs.Start(r.Context(), run); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, run)
}

// handleAppendEvent appends an event to a curator run.
func (h *CuratorRunsHandler) handleAppendEvent(w http.ResponseWriter, r *http.Request) {
	locale := store.LocaleFromContext(r.Context())
	ridStr := r.PathValue("rid")
	runID, err := uuid.Parse(ridStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidID, "run")})
		return
	}
	var body struct {
		EventType string          `json:"event_type"`
		Payload   json.RawMessage `json:"payload"`
	}
	if !bindJSON(w, r, locale, &body) {
		return
	}
	if body.EventType == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgRequired, "event_type")})
		return
	}
	evt := &store.CuratorEvent{
		RunID:     runID,
		EventType: body.EventType,
		Payload:   body.Payload,
	}
	if err := h.events.Append(r.Context(), evt); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, evt)
}

// handleComplete transitions a run to completed.
func (h *CuratorRunsHandler) handleComplete(w http.ResponseWriter, r *http.Request) {
	locale := store.LocaleFromContext(r.Context())
	ridStr := r.PathValue("rid")
	runID, err := uuid.Parse(ridStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidID, "run")})
		return
	}
	var body struct {
		Result json.RawMessage `json:"result"`
	}
	if r.ContentLength > 0 {
		if !bindJSON(w, r, locale, &body) {
			return
		}
	}
	if err := h.runs.Complete(r.Context(), runID, body.Result); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			run, getErr := h.runs.Get(r.Context(), runID)
			status := "unknown"
			if getErr == nil {
				status = run.Status
			}
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": i18n.T(locale, i18n.MsgCuratorInvalidTransition, status, "completed"),
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "completed"})
}

// handleFail transitions a run to failed.
func (h *CuratorRunsHandler) handleFail(w http.ResponseWriter, r *http.Request) {
	locale := store.LocaleFromContext(r.Context())
	ridStr := r.PathValue("rid")
	runID, err := uuid.Parse(ridStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidID, "run")})
		return
	}
	var body struct {
		Error string `json:"error"`
	}
	if !bindJSON(w, r, locale, &body) {
		return
	}
	if err := h.runs.Fail(r.Context(), runID, body.Error); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			run, getErr := h.runs.Get(r.Context(), runID)
			status := "unknown"
			if getErr == nil {
				status = run.Status
			}
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": i18n.T(locale, i18n.MsgCuratorInvalidTransition, status, "failed"),
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "failed"})
}

// handleListBySkill lists curator runs for a skill.
func (h *CuratorRunsHandler) handleListBySkill(w http.ResponseWriter, r *http.Request) {
	locale := store.LocaleFromContext(r.Context())
	skillIDStr := r.PathValue("id")
	skillID, err := uuid.Parse(skillIDStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidID, "skill")})
		return
	}
	runs, err := h.runs.ListBySkillID(r.Context(), skillID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if runs == nil {
		runs = []store.CuratorRun{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

// handleListEvents lists events for a curator run.
func (h *CuratorRunsHandler) handleListEvents(w http.ResponseWriter, r *http.Request) {
	locale := store.LocaleFromContext(r.Context())
	ridStr := r.PathValue("rid")
	runID, err := uuid.Parse(ridStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidID, "run")})
		return
	}
	events, err := h.events.ListByRunID(r.Context(), runID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if events == nil {
		events = []store.CuratorEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}
