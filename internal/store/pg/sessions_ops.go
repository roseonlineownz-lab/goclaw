package pg

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

func (s *PGSessionStore) TruncateHistory(ctx context.Context, key string, keepLast int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if data, ok := s.cache[sessionCacheKey(ctx, key)]; ok {
		if keepLast <= 0 {
			data.Messages = []providers.Message{}
		} else if len(data.Messages) > keepLast {
			data.Messages = data.Messages[len(data.Messages)-keepLast:]
		}
		data.Updated = time.Now()
	}
}

func (s *PGSessionStore) SetHistory(ctx context.Context, key string, msgs []providers.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if data, ok := s.cache[sessionCacheKey(ctx, key)]; ok {
		data.Messages = msgs
		data.Updated = time.Now()
	}
}

func (s *PGSessionStore) Reset(ctx context.Context, key string) {
	s.mu.Lock()
	if data, ok := s.cache[sessionCacheKey(ctx, key)]; ok {
		data.Messages = []providers.Message{}
		data.Summary = ""
		data.Updated = time.Now()
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	// Session not in cache (e.g. after server restart). Clear directly in DB
	// so the next GetOrCreate loads a clean session instead of stale history.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE agent_sessions SET messages = '[]', summary = '', updated_at = $1
		 WHERE session_key = $2`,
		time.Now(), key,
	); err != nil {
		slog.Warn("sessions.reset_db_fallback_failed", "key", key, "error", err)
	}
}

func (s *PGSessionStore) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	delete(s.cache, sessionCacheKey(ctx, key))
	s.mu.Unlock()

	// Clean up associated media files before deleting from DB.
	if s.OnDelete != nil {
		s.OnDelete(key)
	}

	_, err := s.db.ExecContext(ctx, "DELETE FROM agent_sessions WHERE session_key = $1", key)
	return err
}

// UpdateProject sets the project_id FK on an existing session row.
// Pass nil to clear the binding. Updates the in-memory cache when present.
// Permission verification is the caller's responsibility.
func (s *PGSessionStore) UpdateProject(ctx context.Context, sessionKey string, projectID *uuid.UUID) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE agent_sessions SET project_id = $1 WHERE session_key = $2`,
		projectID, sessionKey,
	)
	if err != nil {
		return fmt.Errorf("session update project: %w", err)
	}

	// Sync the in-memory cache entry if present.
	s.mu.Lock()
	if data, ok := s.cache[sessionCacheKey(ctx, sessionKey)]; ok {
		data.ProjectID = projectID
	}
	s.mu.Unlock()
	return nil
}
