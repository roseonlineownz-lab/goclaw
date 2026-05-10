package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// EpisodicSummary represents a Tier 2 episodic memory entry.
// Created from session summaries via the consolidation pipeline.
type EpisodicSummary struct {
	ID        uuid.UUID  `json:"id" db:"id"`
	AgentID   uuid.UUID  `json:"agent_id" db:"agent_id"`
	UserID    string     `json:"user_id" db:"user_id"`         // string: chat-based IDs
	TeamID    *uuid.UUID `json:"team_id,omitempty" db:"team_id"`
	ContactID *uuid.UUID `json:"contact_id,omitempty" db:"contact_id"`
	ProjectID *uuid.UUID `json:"project_id,omitempty" db:"project_id"`
	SessionKey string    `json:"session_key" db:"session_key"`
	Summary    string     `json:"summary" db:"summary"`
	KeyTopics  []string   `json:"key_topics" db:"key_topics"`
	L0Abstract string     `json:"l0_abstract" db:"l0_abstract"` // ~50 tokens, pre-computed
	SourceType string     `json:"source_type" db:"source_type"` // "session", "v2_daily", "manual"
	SourceID   string     `json:"source_id" db:"source_id"`     // dedup key
	TurnCount  int        `json:"turn_count" db:"turn_count"`
	TokenCount int        `json:"token_count" db:"token_count"`
	CreatedAt  time.Time  `json:"created_at" db:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty" db:"expires_at"`

	// Dreaming weighted scoring signals. Populated by
	// EpisodicStore.RecordRecall; consumed by consolidation.ComputeRecallScore.
	RecallCount    int        `json:"recall_count" db:"recall_count"`
	RecallScore    float64    `json:"recall_score" db:"recall_score"`         // running average of memory_search hit scores
	LastRecalledAt *time.Time `json:"last_recalled_at,omitempty" db:"last_recalled_at"`
}

// EpisodicSearchResult is a search hit with L0 summary.
type EpisodicSearchResult struct {
	EpisodicID string    `json:"episodic_id" db:"episodic_id"`
	L0Abstract string    `json:"l0_abstract" db:"l0_abstract"`
	Score      float64   `json:"score" db:"score"`
	CreatedAt  time.Time `json:"created_at" db:"created_at"`
	SessionKey string    `json:"session_key" db:"session_key"`
}

// EpisodicScope holds the optional 5D scope dimensions used to filter reads.
// A nil field means the dimension is not active and the clause is omitted.
// All non-nil dimensions must match for a row to be returned (AND-intersect).
type EpisodicScope struct {
	TeamID    *uuid.UUID
	ContactID *uuid.UUID
	ProjectID *uuid.UUID
}

// EpisodicSearchOptions configures episodic search behavior.
type EpisodicSearchOptions struct {
	MaxResults   int
	MinScore     float64
	VectorWeight float64
	TextWeight   float64
	// Scope restricts results to the exact 5D scope bucket.
	// When non-nil the corresponding SQL clause is AND-appended.
	// A nil Scope means no additional scope filter (agent+user only).
	Scope *EpisodicScope
}

// EpisodicStore manages Tier 2 episodic memory.
type EpisodicStore interface {
	// CRUD
	Create(ctx context.Context, ep *EpisodicSummary) error
	Get(ctx context.Context, id string) (*EpisodicSummary, error)
	Delete(ctx context.Context, id string) error
	List(ctx context.Context, agentID, userID string, limit, offset int) ([]EpisodicSummary, error)

	// Search (hybrid FTS + vector, returns L0 by default)
	Search(ctx context.Context, query string, agentID, userID string, opts EpisodicSearchOptions) ([]EpisodicSearchResult, error)

	// Lifecycle
	ExistsBySourceID(ctx context.Context, agentID, userID, sourceID string) (bool, error)
	PruneExpired(ctx context.Context) (int, error)

	// UpdateSessionProject re-tags episodic_summaries rows for a single
	// session_key from oldProjectID to newProjectID. Used by the
	// /project switch path so that session-scoped episodic memory follows
	// the new project binding. Pass nil for either side to match NULL
	// (i.e. "previously unbound" or "now unbound"). Memory tables that
	// are project-scoped without a session_key (memory_documents,
	// memory_chunks, kg_entities, vault_documents) are intentionally
	// NOT mutated — those rows remain semantically tagged to the old
	// project work.
	UpdateSessionProject(ctx context.Context, sessionKey string, oldProjectID, newProjectID *uuid.UUID) error

	// Promotion lifecycle (used by consolidation pipeline)
	// ListUnpromoted returns summaries not yet promoted to long-term memory, oldest first.
	ListUnpromoted(ctx context.Context, agentID, userID string, limit int) ([]EpisodicSummary, error)
	// ListUnpromotedScored returns unpromoted summaries ordered by recall_score DESC
	// (fallback: created_at ASC). Used by the dreaming worker to prioritise entries
	// with stronger recall signals — see internal/consolidation/scoring.go.
	ListUnpromotedScored(ctx context.Context, agentID, userID string, limit int) ([]EpisodicSummary, error)
	// MarkPromoted sets promoted_at=now() for the given IDs.
	MarkPromoted(ctx context.Context, ids []string) error
	// CountUnpromoted returns the count of unpromoted summaries for an agent/user.
	CountUnpromoted(ctx context.Context, agentID, userID string) (int, error)
	// RecordRecall updates the per-episode recall signal after a memory_search hit.
	// Implementations must increment recall_count, fold `score` into the running
	// average stored in recall_score, and set last_recalled_at=NOW().
	RecordRecall(ctx context.Context, id string, score float64) error

	// Embedding
	SetEmbeddingProvider(provider EmbeddingProvider)
	Close() error
}
