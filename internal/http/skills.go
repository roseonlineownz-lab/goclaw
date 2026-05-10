package http

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/skills"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

const maxSkillUploadSize = 20 << 20 // 20 MB

var (
	aggregateInstallDeps = skills.AggregateMissingDeps
	installManagedDeps   = skills.InstallDeps
)

// SkillsHandler handles skill management HTTP endpoints.
type SkillsHandler struct {
	skills        store.SkillManageStore
	skillVersions store.SkillVersionsStore // optional; nil if not wired
	baseDir       string                   // filesystem base for skill content (skills-store/) — master scope
	dataDir       string                   // parent data dir for scoped skill paths
	bundledDir    string                   // original bundled skills dir (fallback for broken managed copies)
	msgBus        *bus.MessageBus
	db            *sql.DB  // for export/import direct queries
	uploadLocks   sync.Map // per-slug mutex; bounded by validated slug set, entries are tiny (*sync.Mutex)
}

// SetSkillVersionsStore injects the skill versions store into the handler.
func (h *SkillsHandler) SetSkillVersionsStore(sv store.SkillVersionsStore) {
	h.skillVersions = sv
}

// NewSkillsHandler creates a handler for skill management endpoints.
func NewSkillsHandler(skills store.SkillManageStore, baseDir, dataDir, bundledDir string, msgBus *bus.MessageBus) *SkillsHandler {
	return &SkillsHandler{skills: skills, baseDir: baseDir, dataDir: dataDir, bundledDir: bundledDir, msgBus: msgBus}
}

// tenantSkillsDir returns the skills-store directory. v4 single-tenant: always returns base.
func (h *SkillsHandler) tenantSkillsDir(_ *http.Request) string {
	return filepath.Join(h.dataDir, "skills-store")
}

func (h *SkillsHandler) skillUploadLock(scopeKey string) *sync.Mutex {
	actual, _ := h.uploadLocks.LoadOrStore(scopeKey, &sync.Mutex{})
	return actual.(*sync.Mutex)
}

// emitCacheInvalidate broadcasts a skill-related cache invalidation event.
func (h *SkillsHandler) emitCacheInvalidate(kind, key string) {
	if h.msgBus == nil {
		return
	}
	h.msgBus.Broadcast(bus.Event{
		Name:    protocol.EventCacheInvalidate,
		Payload: bus.CacheInvalidatePayload{Kind: kind, Key: key},
	})
}

// RegisterRoutes registers all skill management routes on the given mux.
func (h *SkillsHandler) RegisterRoutes(mux *http.ServeMux) {
	// Skill reads (viewer+)
	mux.HandleFunc("GET /v1/skills", h.authMiddleware(h.handleList))
	mux.HandleFunc("GET /v1/skills/{id}", h.authMiddleware(h.handleGet))
	mux.HandleFunc("GET /v1/agents/{agentID}/skills", h.authMiddleware(h.handleListAgentSkills))
	mux.HandleFunc("GET /v1/skills/{id}/versions", h.authMiddleware(h.handleListVersions))
	mux.HandleFunc("GET /v1/skills/{id}/version-records", h.authMiddleware(h.handleListVersionsDB))
	mux.HandleFunc("GET /v1/skills/{id}/files/{path...}", h.authMiddleware(h.handleReadFile))
	mux.HandleFunc("GET /v1/skills/{id}/files", h.authMiddleware(h.handleListFiles))
	// Version archive (admin+)
	mux.HandleFunc("POST /v1/skills/{id}/versions/{vid}/archive", h.adminMiddleware(h.handleArchiveVersion))
	// Skill writes (admin+)
	mux.HandleFunc("POST /v1/skills/upload", h.adminMiddleware(h.handleUpload))
	mux.HandleFunc("PUT /v1/skills/{id}", h.adminMiddleware(h.handleUpdate))
	mux.HandleFunc("DELETE /v1/skills/{id}", h.adminMiddleware(h.handleDelete))
	// Skill grants (admin+)
	mux.HandleFunc("POST /v1/skills/{id}/grants/agent", h.adminMiddleware(h.handleGrantAgent))
	mux.HandleFunc("DELETE /v1/skills/{id}/grants/agent/{agentID}", h.adminMiddleware(h.handleRevokeAgent))
	mux.HandleFunc("POST /v1/skills/{id}/grants/user", h.adminMiddleware(h.handleGrantUser))
	mux.HandleFunc("DELETE /v1/skills/{id}/grants/user/{userID}", h.adminMiddleware(h.handleRevokeUser))
	// System-level operations: admin + master tenant only.
	// These execute shell commands (pip/npm install) and affect the entire server.
	mux.HandleFunc("POST /v1/skills/rescan-deps", h.adminMiddleware(h.handleRescanDeps))
	mux.HandleFunc("POST /v1/skills/install-deps", h.adminMiddleware(h.handleInstallDeps))
	mux.HandleFunc("POST /v1/skills/install-dep", h.adminMiddleware(h.handleInstallDep))
	mux.HandleFunc("GET /v1/skills/runtimes", h.adminMiddleware(h.handleRuntimes))
	mux.HandleFunc("POST /v1/skills/{id}/toggle", h.adminMiddleware(h.handleToggle))
	// Sidecar actions (member+)
	mux.HandleFunc("POST /v1/skills/{id}/pin", h.authMiddleware(h.handlePin))
	mux.HandleFunc("POST /v1/skills/{id}/view", h.authMiddleware(h.handleMarkView))
	// Export / Import (admin+)
	mux.HandleFunc("GET /v1/skills/export/preview", h.adminMiddleware(h.handleSkillsExportPreview))
	mux.HandleFunc("GET /v1/skills/export", h.adminMiddleware(h.handleSkillsExport))
	mux.HandleFunc("POST /v1/skills/import", h.adminMiddleware(h.handleSkillsImport))
}

func (h *SkillsHandler) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return requireAuth("", next)
}

// adminMiddleware requires admin role — used for system-level operations
// (rescan deps, install packages, toggle skills) that affect the entire server.
func (h *SkillsHandler) adminMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return requireAuth(permissions.RoleAdmin, next)
}

// requireMasterTenant rejects requests from non-master tenants.
// System skill management (install packages, rescan deps) is a server-wide operation
// that should only be accessible to the master tenant or cross-tenant admins.
func (h *SkillsHandler) requireMasterTenant(w http.ResponseWriter, r *http.Request) bool {
	ctx := r.Context()
	if store.IsMasterScope(ctx) {
		return true
	}
	locale := store.LocaleFromContext(ctx)
	writeJSON(w, http.StatusForbidden, map[string]string{
		"error": i18n.T(locale, i18n.MsgPermissionDenied, "system skill management"),
	})
	return false
}

func (h *SkillsHandler) handleList(w http.ResponseWriter, r *http.Request) {
	skillList := h.skills.ListSkills(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"skills": skillList})
}

func (h *SkillsHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	locale := store.LocaleFromContext(r.Context())
	id := r.PathValue("id")
	skill, ok := h.skills.GetSkill(r.Context(), id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": i18n.T(locale, i18n.MsgNotFound, "skill", id)})
		return
	}
	// Best-effort sidecar: record this view.
	if skill.ID != "" {
		if uid, err := uuid.Parse(skill.ID); err == nil {
			if err := h.skills.MarkSkillViewed(r.Context(), uid); err != nil {
				slog.Warn("skill view mark failed", "id", skill.ID, "error", err)
			}
		}
	}
	writeJSON(w, http.StatusOK, skill)
}

func (h *SkillsHandler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	locale := store.LocaleFromContext(r.Context())
	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidID, "skill")})
		return
	}

	// Ownership check (admins bypass)
	auth := resolveAuth(r)
	if !permissions.HasMinRole(auth.Role, permissions.RoleAdmin) {
		userID := store.UserIDFromContext(r.Context())
		if ownerID, found := h.skills.GetSkillOwnerID(r.Context(), id); found && ownerID != userID {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the skill owner can perform this action"})
			return
		}
	}

	var updates map[string]any
	if !bindJSON(w, r, locale, &updates) {
		return
	}
	// Reject deprecated is_system field.
	if _, hasIsSystem := updates["is_system"]; hasIsSystem {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgIsSystemDeprecated)})
		return
	}
	// Validate source enum if provided. `builtin` and `agent-created` are
	// server-only sources (seed and agent context, respectively) — reject if
	// supplied via user payload regardless of role.
	if src, ok := updates["source"]; ok {
		if !isValidSkillSource(src) || !isUserAssignableSkillSource(src) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidSkillSource)})
			return
		}
	}
	// Prevent changing sensitive fields (use /toggle endpoint for enabled)
	delete(updates, "id")
	delete(updates, "owner_id")
	delete(updates, "file_path")
	delete(updates, "enabled")

	if err := h.skills.UpdateSkill(r.Context(), id, updates); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	h.skills.BumpVersion()
	h.emitCacheInvalidate(bus.CacheKindSkills, idStr)
	emitAudit(h.msgBus, r, "skill.updated", "skill", idStr)
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (h *SkillsHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	locale := store.LocaleFromContext(r.Context())
	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidID, "skill")})
		return
	}

	// Ownership check (admins bypass)
	auth := resolveAuth(r)
	if !permissions.HasMinRole(auth.Role, permissions.RoleAdmin) {
		userID := store.UserIDFromContext(r.Context())
		if ownerID, found := h.skills.GetSkillOwnerID(r.Context(), id); found && ownerID != userID {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the skill owner can perform this action"})
			return
		}
	}

	if err := h.skills.DeleteSkill(r.Context(), id); err != nil {
		if err.Error() == "cannot delete system skill" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "cannot delete system skill"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	h.skills.BumpVersion()
	h.emitCacheInvalidate(bus.CacheKindSkills, idStr)
	emitAudit(h.msgBus, r, "skill.deleted", "skill", idStr)
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// handleInstallDeps installs missing dependencies for all system skills, then re-checks status.
func (h *SkillsHandler) handleInstallDeps(w http.ResponseWriter, r *http.Request) {
	if !h.requireMasterTenant(w, r) {
		return
	}
	masterCtx := r.Context()

	dirs := h.skills.ListSystemSkillDirs(masterCtx)
	if len(dirs) == 0 {
		writeJSON(w, http.StatusOK, map[string]string{"message": "no system skills"})
		return
	}

	manifest, missing := aggregateInstallDeps(dirs)
	if len(missing) == 0 {
		writeJSON(w, http.StatusOK, map[string]string{"message": "all deps satisfied"})
		return
	}

	if h.msgBus != nil {
		h.msgBus.Broadcast(bus.Event{
			Name:    protocol.EventSkillDepsInstalling,
			Payload: map[string]any{"count": len(missing)},
		})
	}

	result, err := installManagedDeps(masterCtx, manifest, missing)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Re-check all system skills, persist missing deps, and update status.
	allSkills := h.skills.ListAllSkills(masterCtx)
	statusChanged := false
	for _, sk := range allSkills {
		if sk.Source != "builtin" {
			continue
		}
		if _, exists := dirs[sk.Slug]; !exists {
			continue
		}
		m := h.scanWithFallback(sk)
		if m == nil || m.IsEmpty() {
			continue
		}
		ok, miss := skills.CheckSkillDeps(m)
		id, err := uuid.Parse(sk.ID)
		if err != nil {
			continue
		}

		// Persist actual missing deps to DB so reload reflects reality.
		_ = h.skills.StoreMissingDeps(masterCtx, id, miss)

		// Update status in both directions.
		switch {
		case ok && sk.Status == "archived":
			_ = h.skills.UpdateSkill(masterCtx, id, map[string]any{"status": "active"})
			statusChanged = true
		case !ok && sk.Status != "archived":
			_ = h.skills.UpdateSkill(masterCtx, id, map[string]any{"status": "archived"})
			statusChanged = true
		}

		status := "active"
		if !ok {
			status = "archived"
		}
		if h.msgBus != nil {
			h.msgBus.Broadcast(bus.Event{
				Name: protocol.EventSkillDepsChecked,
				Payload: map[string]any{
					"slug":    sk.Slug,
					"status":  status,
					"missing": miss,
				},
			})
		}
	}
	if statusChanged {
		h.skills.BumpVersion()
		h.emitCacheInvalidate(bus.CacheKindSkills, "")
	}

	if h.msgBus != nil {
		h.msgBus.Broadcast(bus.Event{
			Name:    protocol.EventSkillDepsInstalled,
			Payload: result,
		})
	}

	writeJSON(w, http.StatusOK, result)
}

// handleInstallDep installs a single dependency and re-checks all skill statuses.
// Body: {"dep": "pip:openpyxl"}
func (h *SkillsHandler) handleInstallDep(w http.ResponseWriter, r *http.Request) {
	if !h.requireMasterTenant(w, r) {
		return
	}
	locale := extractLocale(r)
	var body struct {
		Dep string `json:"dep"`
	}
	if !bindJSON(w, r, locale, &body) {
		return
	}
	if body.Dep == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "dep required"})
		return
	}

	if h.msgBus != nil {
		h.msgBus.Broadcast(bus.Event{
			Name:    protocol.EventSkillDepItemInstalling,
			Payload: map[string]any{"dep": body.Dep},
		})
	}

	ok, errMsg := skills.InstallSingleDep(r.Context(), body.Dep)

	if h.msgBus != nil {
		payload := map[string]any{"dep": body.Dep, "ok": ok}
		if errMsg != "" {
			payload["error"] = errMsg
		}
		h.msgBus.Broadcast(bus.Event{
			Name:    protocol.EventSkillDepItemInstalled,
			Payload: payload,
		})
	}

	if ok {
		h.rescanAndUpdate()
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "error": errMsg})
}

type depResult struct {
	Slug    string   `json:"slug"`
	Status  string   `json:"status"`
	Missing []string `json:"missing,omitempty"`
}

// rescanAndUpdate re-checks system skills and updates their status + missing deps in DB.
// Only system skills have filesystem dependencies that need rescanning.
func (h *SkillsHandler) rescanAndUpdate() (updated int, results []depResult) {
	masterCtx := context.Background()
	allSkills := h.skills.ListAllSystemSkills(context.Background())

	for _, sk := range allSkills {
		manifest := h.scanWithFallback(sk)

		id, err := uuid.Parse(sk.ID)
		if err != nil {
			continue
		}

		if manifest == nil || manifest.IsEmpty() {
			// No deps needed — if archived, recover to active and clear stale deps.
			if sk.Status == "archived" {
				_ = h.skills.StoreMissingDeps(masterCtx, id, nil)
				_ = h.skills.UpdateSkill(masterCtx, id, map[string]any{"status": "active"})
				results = append(results, depResult{Slug: sk.Slug, Status: "active"})
				updated++
				slog.Debug("rescan: recovered archived skill (no deps)", "slug", sk.Slug)
			} else {
				results = append(results, depResult{Slug: sk.Slug, Status: "ok"})
			}
			continue
		}

		ok, missing := skills.CheckSkillDeps(manifest)
		_ = h.skills.StoreMissingDeps(masterCtx, id, missing)

		switch {
		case ok && sk.Status == "archived":
			_ = h.skills.UpdateSkill(masterCtx, id, map[string]any{"status": "active"})
			results = append(results, depResult{Slug: sk.Slug, Status: "active"})
			updated++
		case !ok && sk.Status == "active":
			_ = h.skills.UpdateSkill(masterCtx, id, map[string]any{"status": "archived"})
			results = append(results, depResult{Slug: sk.Slug, Status: "archived", Missing: missing})
			updated++
		case !ok:
			results = append(results, depResult{Slug: sk.Slug, Status: sk.Status, Missing: missing})
		default:
			results = append(results, depResult{Slug: sk.Slug, Status: "ok"})
		}

		slog.Debug("rescan: checked skill", "slug", sk.Slug, "ok", ok, "missing", len(missing))
	}

	if updated > 0 {
		h.skills.BumpVersion()
	}
	return updated, results
}

// scanWithFallback scans skill deps from the managed dir, falling back to the
// bundled dir if the managed copy's scripts/ directory is missing or empty.
// If a fallback scan succeeds, re-copies the bundled scripts to the managed dir.
func (h *SkillsHandler) scanWithFallback(sk store.SkillInfo) *skills.SkillManifest {
	manifest := skills.ScanSkillDeps(sk.BaseDir)
	if manifest != nil && !manifest.IsEmpty() {
		return manifest
	}

	// Fallback: try bundled dir for builtin skills whose managed copy is broken.
	if sk.Source != "builtin" || h.bundledDir == "" {
		return manifest
	}

	managedScripts := filepath.Join(sk.BaseDir, "scripts")
	if _, err := os.Stat(managedScripts); err == nil {
		// scripts/ exists in managed dir but scanner found nothing — not a copy issue.
		return manifest
	}

	bundledSkillDir := filepath.Join(h.bundledDir, sk.Slug)
	bundledManifest := skills.ScanSkillDeps(bundledSkillDir)
	if bundledManifest == nil || bundledManifest.IsEmpty() {
		return manifest
	}

	slog.Warn("rescan: managed scripts/ missing, using bundled fallback",
		"slug", sk.Slug, "managed", sk.BaseDir, "bundled", bundledSkillDir)

	// Re-copy bundled scripts to managed dir so future scans work without fallback.
	bundledScripts := filepath.Join(bundledSkillDir, "scripts")
	if err := skills.CopyDir(bundledScripts, managedScripts); err != nil {
		slog.Error("rescan: failed to re-copy bundled scripts", "slug", sk.Slug, "error", err)
	}

	return bundledManifest
}

// handleRescanDeps re-checks dependencies for all skills (including archived) and updates their status.
func (h *SkillsHandler) handleRescanDeps(w http.ResponseWriter, r *http.Request) {
	if !h.requireMasterTenant(w, r) {
		return
	}
	updated, results := h.rescanAndUpdate()
	if updated > 0 {
		// rescanAndUpdate bumped the skills version already; emit a global
		// invalidate so cached agent Loops pick up the new status set.
		h.emitCacheInvalidate(bus.CacheKindSkills, "")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"updated": updated,
		"results": results,
	})
}

// handleRuntimes returns the availability and version of prerequisite runtimes (python3, node, etc.).
func (h *SkillsHandler) handleRuntimes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, skills.CheckRuntimes())
}

// handleToggle enables or disables a skill.
// Body: {"enabled": bool}
// When enabling: re-checks deps and updates status to "active" or "archived" accordingly.
func (h *SkillsHandler) handleToggle(w http.ResponseWriter, r *http.Request) {
	if !h.requireMasterTenant(w, r) {
		return
	}
	locale := store.LocaleFromContext(r.Context())
	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidID, "skill")})
		return
	}

	var body struct {
		Enabled bool `json:"enabled"`
	}
	if !bindJSON(w, r, locale, &body) {
		return
	}

	if err := h.skills.ToggleSkill(r.Context(), id, body.Enabled); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	newStatus := ""
	if body.Enabled {
		// Re-check deps for this skill so its status reflects reality after being re-enabled.
		sk, ok := h.skills.GetSkillByID(r.Context(), id)
		if ok {
			manifest := h.scanWithFallback(sk)
			if manifest != nil && !manifest.IsEmpty() {
				depOk, missing := skills.CheckSkillDeps(manifest)
				_ = h.skills.StoreMissingDeps(r.Context(), id, missing)
				if depOk {
					newStatus = "active"
				} else {
					newStatus = "archived"
				}
			} else {
				newStatus = "active"
			}
			_ = h.skills.UpdateSkill(r.Context(), id, map[string]any{"status": newStatus})
		}
	}

	h.skills.BumpVersion()
	h.emitCacheInvalidate(bus.CacheKindSkills, idStr)
	emitAudit(h.msgBus, r, "skill.toggled", "skill", idStr)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": body.Enabled, "status": newStatus})
}

// handlePin sets the pinned flag for a skill.
// Body: {"pinned": bool}
func (h *SkillsHandler) handlePin(w http.ResponseWriter, r *http.Request) {
	locale := store.LocaleFromContext(r.Context())
	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidID, "skill")})
		return
	}
	var body struct {
		Pinned bool `json:"pinned"`
	}
	if !bindJSON(w, r, locale, &body) {
		return
	}
	if err := h.skills.PinSkill(r.Context(), id, body.Pinned); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	h.emitCacheInvalidate(bus.CacheKindSkills, idStr)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pinned": body.Pinned})
}

// handleMarkView records a view event on the skill (best-effort).
func (h *SkillsHandler) handleMarkView(w http.ResponseWriter, r *http.Request) {
	locale := store.LocaleFromContext(r.Context())
	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidID, "skill")})
		return
	}
	if err := h.skills.MarkSkillViewed(r.Context(), id); err != nil {
		slog.Warn("skill view mark failed", "id", idStr, "error", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// validSkillSources is the allowed set for the source enum.
var validSkillSources = map[string]struct{}{
	"builtin":        {},
	"hub-verified":   {},
	"hub-unverified": {},
	"agent-created":  {},
	"user-uploaded":  {},
}

// isValidSkillSource returns true if src is one of the 5 allowed values.
func isValidSkillSource(src any) bool {
	s, ok := src.(string)
	if !ok {
		return false
	}
	_, valid := validSkillSources[s]
	return valid
}

// isUserAssignableSkillSource returns false for `builtin` and `agent-created`,
// which are server-controlled (seed + agent context) and must not be settable
// via HTTP payload, even by admin.
func isUserAssignableSkillSource(src any) bool {
	s, ok := src.(string)
	if !ok {
		return false
	}
	return s != "builtin" && s != "agent-created"
}

