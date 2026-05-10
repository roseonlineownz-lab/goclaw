package http

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// handleListVersionsDB lists skill_versions rows (not filesystem dirs).
// Supports ?archived=true to include archived versions (default: active only).
func (h *SkillsHandler) handleListVersionsDB(w http.ResponseWriter, r *http.Request) {
	locale := store.LocaleFromContext(r.Context())
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidID, "skill")})
		return
	}
	if h.skillVersions == nil {
		// Wiring failure — surface explicitly so misconfig is visible in metrics
		// instead of an empty list that the FE would interpret as "no versions".
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "skill versions store not available",
		})
		return
	}
	includeArchived := r.URL.Query().Get("archived") == "true"
	versions, err := h.skillVersions.ListBySkillIDFiltered(r.Context(), id, includeArchived)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if versions == nil {
		versions = []store.SkillVersion{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": versions})
}

// handleArchiveVersion archives a skill version — sets archived_at, archive_path,
// and clears the content field. The tarball itself is not written here (deferred).
func (h *SkillsHandler) handleArchiveVersion(w http.ResponseWriter, r *http.Request) {
	locale := store.LocaleFromContext(r.Context())
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidID, "skill")})
		return
	}
	vidStr := r.PathValue("vid")
	vid, err := uuid.Parse(vidStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidID, "version")})
		return
	}
	if h.skillVersions == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "skill versions store not available"})
		return
	}
	// Build deterministic archive path: archives/skills/{skillID}/{versionID}/{unix}.tar.gz
	archivePath := fmt.Sprintf("archives/skills/%s/%s/%d.tar.gz",
		id.String(), vid.String(), time.Now().Unix())
	if err := h.skillVersions.Archive(r.Context(), vid, id, archivePath); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": i18n.T(locale, i18n.MsgVersionAlreadyArchived),
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "archive_path": archivePath})
}
