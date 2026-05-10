package http

import (
	"net/http"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// UsersHandler handles user CRUD operations:
//
//	GET    /v1/users
//	POST   /v1/users
//	GET    /v1/users/{id}
//	PATCH  /v1/users/{id}
//	DELETE /v1/users/{id}
//
// Note: PATCH /v1/users/me lives in AuthHandler — do not overlap.
type UsersHandler struct {
	users store.UsersStore
}

// NewUsersHandler constructs a UsersHandler.
func NewUsersHandler(users store.UsersStore) *UsersHandler {
	return &UsersHandler{users: users}
}

// RegisterRoutes mounts user CRUD endpoints on mux.
func (h *UsersHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v1/users", requireAuth(permissions.RoleViewer, h.routeCollection))
	mux.HandleFunc("/v1/users/{id}", requireAuth(permissions.RoleViewer, h.routeItem))
}

// routeCollection dispatches GET /v1/users and POST /v1/users.
func (h *UsersHandler) routeCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleList(w, r)
	case http.MethodPost:
		requireAuth(permissions.RoleAdmin, h.handleCreate)(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// routeItem dispatches GET/PATCH/DELETE /v1/users/{id}.
func (h *UsersHandler) routeItem(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleGet(w, r)
	case http.MethodPatch:
		h.handlePatch(w, r)
	case http.MethodDelete:
		requireAuth(permissions.RoleAdmin, h.handleDelete)(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// UserResp is the public shape for a user record — never exposes password_hash.
type UserResp struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name,omitempty"`
	Role        string `json:"role"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"` // RFC3339
	UpdatedAt   string `json:"updated_at"`
}

type listUsersResp struct {
	Users []UserResp `json:"users"`
}

func userToResp(u store.User) UserResp {
	r := UserResp{
		ID:        u.ID.String(),
		Email:     u.Email,
		Role:      u.Role,
		Status:    u.Status,
		CreatedAt: u.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: u.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if u.DisplayName != nil {
		r.DisplayName = *u.DisplayName
	}
	return r
}

type createUserBody struct {
	Email       string  `json:"email"`
	Password    string  `json:"password"`
	Role        string  `json:"role"`
	DisplayName *string `json:"display_name"`
}

type patchUserBody struct {
	DisplayName *string `json:"display_name"`
	Role        *string `json:"role"`
	Status      *string `json:"status"`
}
