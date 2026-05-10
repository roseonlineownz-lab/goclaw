package http

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// UserSearchResult is a unified result from contacts + tenant_users.
// ID is the user_id string (human-facing identifier). UUID is the tenant_user
// primary key (only populated when Source == "tenant_user"); callers that need
// to reference a tenant_user by foreign key (e.g. contact merge) must use UUID.
type UserSearchResult struct {
	ID                 string  `json:"id"`
	UUID               string  `json:"uuid,omitempty"`
	DisplayName        *string `json:"display_name,omitempty"`
	Username           *string `json:"username,omitempty"`
	Source             string  `json:"source"` // "contact" or "tenant_user"
	ChannelType        *string `json:"channel_type,omitempty"`
	PeerKind           *string `json:"peer_kind,omitempty"`
	MergedUserID *string `json:"merged_user_id,omitempty"`
	Role               *string `json:"role,omitempty"`
}

// handleSearchUsers returns unified results from channel_contacts + tenant_users.
// GET /v1/users/search?q=&limit=30&peer_kind=
// Empty q → return most recent. With q → ILIKE search across both tables.
func (h *ChannelInstancesHandler) handleSearchUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	peerKind := r.URL.Query().Get("peer_kind")
	source := r.URL.Query().Get("source") // "contact", "tenant_user", or "" (both)
	limit := 30
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}

	ctx := r.Context()
	var results []UserSearchResult

	// Search channel_contacts (skip if source=tenant_user; tenant_users is gone in v4).
	if h.contactStore != nil && source != "tenant_user" {
		opts := store.ContactListOpts{
			Search:   q,
			PeerKind: peerKind,
			Limit:    limit,
		}
		contacts, err := h.contactStore.ListContacts(ctx, opts)
		if err != nil {
			slog.Warn("user_search.contacts", "error", err)
		}
		for _, c := range contacts {
			r := UserSearchResult{
				ID:          c.SenderID,
				DisplayName: c.DisplayName,
				Username:    c.Username,
				Source:      "contact",
				ChannelType: &c.ChannelType,
				PeerKind:    c.PeerKind,
			}
			results = append(results, r)
		}
	}

	if results == nil {
		results = []UserSearchResult{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}
