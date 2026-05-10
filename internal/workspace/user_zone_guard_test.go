package workspace

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

type stubResolver map[string]uuid.UUID

func (s stubResolver) LookupUserIDByKey(key string) (uuid.UUID, bool) {
	id, ok := s[key]
	return id, ok
}

func TestExtractUserKeyFromPath(t *testing.T) {
	cases := []struct {
		name string
		path string
		root string
		want string
		ok   bool
	}{
		{"under root users zone", "/ws/users/alice/notes.md", "/ws", "alice", true},
		{"deep nested file", "/ws/users/alice/projects/x/y.md", "/ws", "alice", true},
		{"no user zone", "/ws/shared/team-doc.md", "/ws", "", false},
		{"outside root pass-through with users/", "/other/users/bob/x.md", "/ws", "bob", true},
		{"users/ as substring of folder name not matched", "/ws/myusers/bob/x.md", "/ws", "", false},
		{"empty workspace any path", "/abs/users/charlie/file", "", "charlie", true},
		{"missing trailing path", "/ws/users/alice", "/ws", "alice", true},
		{"empty key after users/ rejected", "/ws/users//file", "/ws", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := extractUserKeyFromPath(tc.path, tc.root)
			if got != tc.want || ok != tc.ok {
				t.Errorf("got (%q,%v), want (%q,%v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestEnforceUserZoneAccess(t *testing.T) {
	alice := uuid.New()
	bob := uuid.New()
	res := stubResolver{"alice": alice, "bob": bob}

	t.Run("self path allowed", func(t *testing.T) {
		err := EnforceUserZoneAccess("/ws/users/alice/notes.md", "/ws", alice, res)
		if err != nil {
			t.Errorf("self access must be allowed: %v", err)
		}
	})

	t.Run("cross-user rejected", func(t *testing.T) {
		err := EnforceUserZoneAccess("/ws/users/bob/secret.md", "/ws", alice, res)
		if !errors.Is(err, ErrUserZoneViolation) {
			t.Errorf("cross-user must reject, got %v", err)
		}
	})

	t.Run("non-user-zone path passes through", func(t *testing.T) {
		err := EnforceUserZoneAccess("/ws/shared/doc.md", "/ws", alice, res)
		if err != nil {
			t.Errorf("non-zone path must pass: %v", err)
		}
	})

	t.Run("unknown key rejected", func(t *testing.T) {
		err := EnforceUserZoneAccess("/ws/users/ghost/x.md", "/ws", alice, res)
		if !errors.Is(err, ErrUserZoneViolation) {
			t.Errorf("unknown key must reject (deny-by-default), got %v", err)
		}
	})

	t.Run("nil resolver rejects user-zone paths", func(t *testing.T) {
		err := EnforceUserZoneAccess("/ws/users/alice/file", "/ws", alice, nil)
		if !errors.Is(err, ErrUserZoneViolation) {
			t.Errorf("nil resolver in user zone must reject, got %v", err)
		}
		// But pass through for non-user-zone:
		err2 := EnforceUserZoneAccess("/ws/shared/file", "/ws", alice, nil)
		if err2 != nil {
			t.Errorf("nil resolver outside user zone must pass: %v", err2)
		}
	})
}
