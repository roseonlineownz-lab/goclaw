package consolidation

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/bgalert"
	"github.com/nextlevelbuilder/goclaw/internal/eventbus"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/providerresolve"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// episodicWorker handles session.completed events → creates episodic summaries.
type episodicWorker struct {
	store         store.EpisodicStore
	sessions      store.SessionCoreStore      // for reading session messages during summarization
	systemConfigs store.SystemConfigStore     // per-tenant provider config
	registry      *providers.Registry         // provider resolution
	eventBus      eventbus.DomainEventBus
	alertDeps     bgalert.AlertDeps
}

// resolveProvider delegates to shared background provider resolution.
func (w *episodicWorker) resolveProvider(ctx context.Context) (providers.Provider, string) {
	return providerresolve.ResolveBackgroundProvider(ctx, w.registry, w.systemConfigs)
}

// Handle processes a session.completed event into an episodic summary.
func (w *episodicWorker) Handle(ctx context.Context, event eventbus.DomainEvent) error {
	slog.Debug("episodic: received session.completed",
		"agent", event.AgentID, "user", event.UserID,
		"source", event.SourceID, "payload_type", fmt.Sprintf("%T", event.Payload))

	payload, ok := event.Payload.(*eventbus.SessionCompletedPayload)
	if !ok {
		return fmt.Errorf("episodic: unexpected payload type %T", event.Payload)
	}

	// Parse agent ID up front so we fail fast on bad input instead of
	// leaking into a PG error or panicking via uuid.MustParse later on.
	agentUUID, err := uuid.Parse(event.AgentID)
	if err != nil {
		return fmt.Errorf("episodic: invalid agent_id %q: %w", event.AgentID, err)
	}
	// UserID parsed at entry to fail fast — v4 schema treats user_id as UUID and
	// downstream store calls (ExistsBySourceID, Create) would otherwise leak
	// non-UUID strings into PG with confusing type errors.
	if _, err := uuid.Parse(event.UserID); err != nil {
		return fmt.Errorf("episodic: invalid user_id %q: %w", event.UserID, err)
	}

	// Build source_id for idempotency
	sourceID := fmt.Sprintf("%s:%d", payload.SessionKey, payload.CompactionCount)
	exists, err := w.store.ExistsBySourceID(ctx, agentUUID.String(), event.UserID, sourceID)
	if err != nil {
		return fmt.Errorf("episodic: check source_id: %w", err)
	}
	if exists {
		slog.Debug("episodic: skipping duplicate", "source_id", sourceID)
		return nil
	}

	// Use compaction summary if available, else call LLM
	summary := payload.Summary
	if summary == "" {
		provider, model := w.resolveProvider(ctx)
		if provider != nil {
			summary, err = w.summarizeSession(ctx, provider, model, payload)
			if err != nil {
				bgalert.ReportProviderError(ctx, w.alertDeps, "episodic", err)
				return fmt.Errorf("episodic: summarize: %w", err)
			}
		}
	}
	if summary == "" {
		provider, _ := w.resolveProvider(ctx)
		slog.Warn("episodic: no summary available, skipping", "session", payload.SessionKey,
			"compaction_summary_empty", payload.Summary == "", "provider_nil", provider == nil)
		return nil
	}
	slog.Debug("episodic: creating summary", "session", payload.SessionKey, "summary_len", len(summary))

	// Create episodic summary
	l0 := generateL0Abstract(summary)
	entities := extractEntityNames(summary)
	expiresAt := time.Now().UTC().Add(90 * 24 * time.Hour)

	ep := &store.EpisodicSummary{
		AgentID:    agentUUID,
		UserID:     event.UserID,
		SessionKey: payload.SessionKey,
		Summary:    summary,
		KeyTopics:  entities,
		TurnCount:  payload.MessageCount,
		TokenCount: payload.TokensUsed,
		L0Abstract: l0,
		SourceID:   sourceID,
		SourceType: "session",
		ExpiresAt:  &expiresAt,
	}
	// Propagate 5D scope from the session payload so the episodic row is tagged
	// with the correct scope dimensions, enabling cross-tenant isolation in reads.
	if payload.TeamID != "" {
		if tid, err := uuid.Parse(payload.TeamID); err == nil {
			ep.TeamID = &tid
		}
	}
	if payload.ContactID != "" {
		if cid, err := uuid.Parse(payload.ContactID); err == nil {
			ep.ContactID = &cid
		}
	}
	if payload.ProjectID != "" {
		if pid, err := uuid.Parse(payload.ProjectID); err == nil {
			ep.ProjectID = &pid
		}
	}
	if err := w.store.Create(ctx, ep); err != nil {
		return fmt.Errorf("episodic: create: %w", err)
	}

	// Publish episodic.created event for downstream semantic extraction.
	// Thread the intersected 5D scope so KG workers can tag entities correctly.
	createdPayload := &eventbus.EpisodicCreatedPayload{
		EpisodicID:  ep.ID.String(),
		SessionKey:  payload.SessionKey,
		Summary:     summary,
		KeyEntities: entities,
	}
	if ep.TeamID != nil {
		createdPayload.TeamID = ep.TeamID.String()
	}
	if ep.ContactID != nil {
		createdPayload.ContactID = ep.ContactID.String()
	}
	if ep.ProjectID != nil {
		createdPayload.ProjectID = ep.ProjectID.String()
	}
	w.eventBus.Publish(eventbus.DomainEvent{
		Type:     eventbus.EventEpisodicCreated,
		SourceID: ep.ID.String(),
		AgentID:  event.AgentID,
		UserID:   event.UserID,
		Payload:  createdPayload,
	})

	slog.Info("episodic: created summary", "session", payload.SessionKey, "l0_len", len(l0))
	return nil
}

// summarizeSession reads actual session messages and calls LLM to summarize.
func (w *episodicWorker) summarizeSession(ctx context.Context, provider providers.Provider, model string, payload *eventbus.SessionCompletedPayload) (string, error) {
	// Try reading session messages for a real summary.
	if w.sessions != nil {
		messages := w.sessions.GetHistory(ctx, payload.SessionKey)
		if len(messages) > 0 {
			return w.summarizeFromMessages(ctx, provider, model, messages)
		}
		// Messages may have been compacted away — try existing session summary.
		if summary := w.sessions.GetSummary(ctx, payload.SessionKey); summary != "" {
			return summary, nil
		}
	}
	return "", fmt.Errorf("no messages or summary available for session %s", payload.SessionKey)
}

// summarizeFromMessages builds a conversation excerpt and calls LLM.
func (w *episodicWorker) summarizeFromMessages(ctx context.Context, provider providers.Provider, model string, messages []providers.Message) (string, error) {
	var sb strings.Builder
	for _, m := range messages {
		if m.Role == "system" {
			continue
		}
		content := m.Content
		// Rune-safe truncation to avoid corrupting UTF-8 (CJK, emoji).
		if runes := []rune(content); len(runes) > 500 {
			content = string(runes[:500]) + "..."
		}
		sb.WriteString(m.Role)
		sb.WriteString(": ")
		sb.WriteString(content)
		sb.WriteByte('\n')
		if sb.Len() > 8000 {
			sb.WriteString("...(truncated)\n")
			break
		}
	}

	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, err := provider.Chat(sctx, providers.ChatRequest{
		Messages: []providers.Message{
			{Role: "system", Content: summarizationPrompt},
			{Role: "user", Content: sb.String()},
		},
		Model:   model,
		Options: map[string]any{"max_tokens": 1024, "temperature": 0.3},
	})
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}
