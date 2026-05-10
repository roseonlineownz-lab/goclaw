package eventbus

import (
	"log/slog"

	"github.com/google/uuid"
)

// validateUserID is a publish-time observer that logs a warning when a
// DomainEvent carries a non-UUID UserID. Mirrors validateAgentID — does NOT
// block the publish, observability only. Catches drift before the event reaches
// a consumer that parses UserID as a UUID and queries the DB with it.
//
// Log field name is `non_uuid_user_id` — distinct from the standard `user_id`
// field used elsewhere — so the warning never collides with valid UUID-typed
// user_id fields parsed by observability tooling.
func validateUserID(event DomainEvent) {
	if event.UserID == "" {
		return // legitimate anonymous, system, or owner-less event
	}
	if _, err := uuid.Parse(event.UserID); err != nil {
		slog.Warn("eventbus.non_uuid_user_id",
			"event_type", event.Type,
			"non_uuid_user_id", event.UserID,
			"source_id", event.SourceID,
		)
	}
}
