package pg

import (
	"encoding/json"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// --- Intermediate scan structs for sqlx SELECT queries ---
// Used where the domain struct has incompatible field types (e.g. *[]byte for JSONB, map for JSON).

// agentBackfillRow is used by BackfillAgentEmbeddings to avoid
// allocating a full AgentData for the minimal 3-column query.
type agentBackfillRow struct {
	ID          uuid.UUID `db:"id"`
	DisplayName string    `db:"display_name"`
	Frontmatter string    `db:"frontmatter"`
}

// userInstanceRow is an intermediate for ListUserInstances.
// UserInstanceData.Metadata is map[string]string with db:"-", so we scan the raw JSON separately.
type userInstanceRow struct {
	UserID      string  `db:"user_id"`
	FirstSeenAt *string `db:"first_seen_at"`
	LastSeenAt  *string `db:"last_seen_at"`
	FileCount   int     `db:"file_count"`
	MetadataRaw []byte  `db:"metadata"`
}

func (r userInstanceRow) toUserInstanceData() store.UserInstanceData {
	d := store.UserInstanceData{
		UserID:      r.UserID,
		FirstSeenAt: r.FirstSeenAt,
		LastSeenAt:  r.LastSeenAt,
		FileCount:   r.FileCount,
	}
	if len(r.MetadataRaw) > 0 {
		json.Unmarshal(r.MetadataRaw, &d.Metadata) //nolint:errcheck
	}
	return d
}

// teamMemberAgentRow is used by GetTeamMemberAgents which returns an anonymous struct.
// We avoid anonymous structs in sqlx since they can't carry db tags.
type teamMemberAgentRow struct {
	ID       uuid.UUID `db:"id"`
	AgentKey string    `db:"agent_key"`
}
