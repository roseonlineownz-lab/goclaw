package agent

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// ResolverFunc is called when an agent isn't found in the cache.
// Used to lazy-create agents from DB. Context carries tenant scope.
type ResolverFunc func(ctx context.Context, agentKey string) (Agent, error)

const defaultRouterTTL = 10 * time.Minute

// agentEntry wraps a cached Agent with a timestamp for TTL-based expiration.
type agentEntry struct {
	agent    Agent
	cachedAt time.Time
}

// AgentActivityStatus tracks the current phase of a running agent for status queries.
type AgentActivityStatus struct {
	RunID     string
	Phase     string // "thinking", "tool_exec", "compacting"
	Tool      string // current tool name (when Phase == "tool_exec")
	Iteration int
	StartedAt time.Time
}

// TraceCollector is a minimal interface for marking a trace as aborted.
// Defined here (not in internal/tracing) to avoid import cycles.
// *tracing.Collector implements this interface implicitly.
type TraceCollector interface {
	FinishTrace(ctx context.Context, traceID uuid.UUID, status, errMsg, outputPreview string)
}

// Router manages multiple agent Loop instances.
// Each agent has a unique ID and its own provider/model/tools config.
// Cached Loops expire after TTL (safety net for multi-instance).
type Router struct {
	agents          map[string]*agentEntry
	mu              sync.RWMutex
	activeRuns      sync.Map     // runID → *ActiveRun
	sessionRuns     sync.Map     // sessionKey → runID (secondary index for O(1) IsSessionBusy)
	agentActivity   sync.Map     // sessionKey → *AgentActivityStatus
	resolver        ResolverFunc // optional: lazy creation from DB
	ttl             time.Duration
	traceCollector  TraceCollector // optional: for force-marking aborted traces
}

func NewRouter() *Router {
	return &Router{
		agents: make(map[string]*agentEntry),
		ttl:    defaultRouterTTL,
	}
}

// SetTraceCollector wires the trace collector so forceMarkTraceAborted can update DB.
func (r *Router) SetTraceCollector(c TraceCollector) {
	r.traceCollector = c
}

// SetResolver sets a resolver function for lazy agent creation.
func (r *Router) SetResolver(fn ResolverFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resolver = fn
}

// Register adds an agent to the router.
func (r *Router) Register(ag Agent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[ag.ID()] = &agentEntry{agent: ag, cachedAt: time.Now()}
}

// Get returns an agent by ID. Lazy-creates from DB via resolver if needed.
// Cached entries expire after TTL as a safety net for multi-instance deployments.
//
// Canonicalization: after a successful resolver call, the cache entry is stored
// under the agent's canonical key (`ag.ID()` returns agent_key) regardless of
// whether the caller passed a UUID or an agent_key. Callers passing the UUID
// form incur a fresh resolver call on every invocation because the raw-UUID
// input never lands in the map.
func (r *Router) Get(ctx context.Context, agentID string) (Agent, error) {
	r.mu.RLock()
	entry, ok := r.agents[agentID]
	resolver := r.resolver
	r.mu.RUnlock()

	if ok && (r.ttl == 0 || time.Since(entry.cachedAt) < r.ttl) {
		return entry.agent, nil
	}

	// TTL expired → remove stale entry so resolver re-creates
	if ok {
		r.mu.Lock()
		delete(r.agents, agentID)
		r.mu.Unlock()
	}

	// Try resolver (create from DB)
	if resolver != nil {
		ag, err := resolver(ctx, agentID)
		if err != nil {
			return nil, err
		}
		// Canonicalize: always cache under the agent's own ID (agent_key).
		canonicalKey := ag.ID()
		r.mu.Lock()
		if existing, ok := r.agents[canonicalKey]; ok {
			if r.ttl == 0 || time.Since(existing.cachedAt) < r.ttl {
				r.mu.Unlock()
				return existing.agent, nil
			}
			delete(r.agents, canonicalKey)
		}
		r.agents[canonicalKey] = &agentEntry{agent: ag, cachedAt: time.Now()}
		r.mu.Unlock()
		return ag, nil
	}

	return nil, fmt.Errorf("agent not found: %s", agentID)
}

// Remove removes an agent from the router by agent_key.
func (r *Router) Remove(agentID string) {
	if agentID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.agents, agentID)
}

// List returns all registered agent IDs.
func (r *Router) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.agents))
	for id := range r.agents {
		ids = append(ids, id)
	}
	return ids
}

// AgentInfo is lightweight metadata about an agent.
type AgentInfo struct {
	ID        string `json:"id"`
	Model     string `json:"model"`
	IsRunning bool   `json:"isRunning"`
}

// ListInfo returns metadata for all agents.
func (r *Router) ListInfo() []AgentInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	infos := make([]AgentInfo, 0, len(r.agents))
	for _, entry := range r.agents {
		infos = append(infos, AgentInfo{
			ID:        entry.agent.ID(),
			Model:     entry.agent.Model(),
			IsRunning: entry.agent.IsRunning(),
		})
	}
	return infos
}

// IsRunning checks if a specific agent is currently running (cached in router).
func (r *Router) IsRunning(ctx context.Context, agentID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if entry, ok := r.agents[agentID]; ok {
		return entry.agent.IsRunning()
	}
	return false
}

// GetCached returns a cached agent without invoking the resolver. Used by
// cache-aware helpers that want to avoid a DB roundtrip on the hot path.
// Returns (nil, false) on miss or when the entry has exceeded TTL.
// Does NOT trigger resolver fallback — call Router.Get() for that path.
func (r *Router) GetCached(ctx context.Context, agentID string) (Agent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.agents[agentID]
	if !ok {
		return nil, false
	}
	if r.ttl > 0 && time.Since(entry.cachedAt) >= r.ttl {
		return nil, false
	}
	return entry.agent, true
}

// --- Active Run Tracking (matching TS chat-abort.ts) ---

// ActiveRun tracks a running agent invocation so it can be aborted via chat.abort
// and supports mid-run message injection via InjectCh.
type ActiveRun struct {
	RunID      string
	SessionKey string
	AgentID    string
	Cancel     context.CancelFunc
	StartedAt  time.Time
	InjectCh   chan InjectedMessage // buffered channel for mid-run user message injection
	Done       chan struct{}        // closed when goroutine actually exits (via UnregisterRun)
	State      atomic.Int32         // 0=running, 1=aborting, 2=done
	TraceID    uuid.UUID            // set after trace creation via SetRunTraceID
}

// AbortResult describes the outcome of a single AbortRun call.
type AbortResult struct {
	RunID           string
	Stopped         bool // graceful stop within abortGraceTimeout
	Forced          bool // timeout exceeded; trace force-marked cancelled
	AlreadyAborting bool // 2nd click while phase 2 still in flight
	NotFound        bool // run never existed or already finished
	Unauthorized    bool // sessionKey mismatch
}

// abortGraceTimeout is the maximum time AbortRun waits for a goroutine to exit
// before force-marking the trace as cancelled.
const abortGraceTimeout = 3 * time.Second

// safeClose closes ch without panicking if ch is already closed.
// recover-based: cheaper than a sync.Once per ActiveRun because it avoids an
// extra heap allocation per run on the common (single-close) path.
func safeClose(ch chan struct{}) {
	defer func() { _ = recover() }()
	close(ch)
}

// RegisterRun records an active run so it can be aborted later.
// ctx is used to capture the tenant ID for forceMarkTraceAborted.
// Returns a receive-only channel for mid-run message injection.
func (r *Router) RegisterRun(ctx context.Context, runID, sessionKey, agentID string, cancel context.CancelFunc) <-chan InjectedMessage {
	injectCh := make(chan InjectedMessage, injectBufferSize)
	r.activeRuns.Store(runID, &ActiveRun{
		RunID:      runID,
		SessionKey: sessionKey,
		AgentID:    agentID,
		Cancel:     cancel,
		StartedAt:  time.Now(),
		InjectCh:   injectCh,
		Done:       make(chan struct{}),
	})
	r.sessionRuns.Store(sessionKey, runID)
	return injectCh
}

// SetRunTraceID associates a trace UUID with an active run.
// Called from loop_run.go after CreateTrace succeeds so forceMarkTraceAborted
// can update the correct trace record on a 3s timeout.
func (r *Router) SetRunTraceID(runID string, traceID uuid.UUID) {
	if val, ok := r.activeRuns.Load(runID); ok {
		val.(*ActiveRun).TraceID = traceID
	}
}

// UnregisterRun removes a completed/cancelled run from tracking.
// Closes Done BEFORE deleting from maps so any concurrent AbortRun waiting
// on Done sees the signal before the entry disappears.
func (r *Router) UnregisterRun(runID string) {
	if val, ok := r.activeRuns.Load(runID); ok {
		run := val.(*ActiveRun)
		run.State.Store(2) // mark done
		safeClose(run.Done)
		r.sessionRuns.Delete(run.SessionKey)
	}
	r.activeRuns.Delete(runID)
}

// AbortRun cancels a single run by ID using a 2-phase verified abort.
//
// Phase 1: CAS state 0→1 (idempotent, prevents double-cancel).
// Phase 2: wait ≤abortGraceTimeout for goroutine to exit via Done channel.
// On timeout: force-mark trace as cancelled so the UI moves on.
//
// sessionKey is validated for authorization when non-empty.
func (r *Router) AbortRun(runID, sessionKey string) AbortResult {
	res := AbortResult{RunID: runID}

	val, ok := r.activeRuns.Load(runID)
	if !ok {
		res.NotFound = true
		return res
	}
	run := val.(*ActiveRun)

	// Authorization: sessionKey must match (matching TS behavior)
	if sessionKey != "" && run.SessionKey != sessionKey {
		res.Unauthorized = true
		return res
	}

	// CAS 0→1: transition Running → Aborting.
	// Any failure means abort is already in flight (state==1) or run is done (state==2).
	if !run.State.CompareAndSwap(0, 1) {
		if run.State.Load() == 2 {
			// Race: run finished between Load and CAS
			res.NotFound = true
		} else {
			res.AlreadyAborting = true
		}
		return res
	}

	run.Cancel()

	select {
	case <-run.Done:
		res.Stopped = true
	case <-time.After(abortGraceTimeout):
		// Goroutine is stuck; force trace status so the UI does not hang.
		r.forceMarkTraceAborted(runID)
		res.Forced = true
	}
	return res
}

// forceMarkTraceAborted marks a trace as cancelled in DB when the 3s grace
// period expires and the goroutine has not yet exited.
// Uses a detached background context scoped to the run's tenant so the DB
// write is not blocked by the caller's (already-cancelled) run context.
func (r *Router) forceMarkTraceAborted(runID string) {
	if r.traceCollector == nil {
		return
	}
	val, ok := r.activeRuns.Load(runID)
	if !ok {
		return
	}
	run := val.(*ActiveRun)
	if run.TraceID == uuid.Nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r.traceCollector.FinishTrace(ctx, run.TraceID, "cancelled", "force-aborted (3s grace exceeded)", "")
}

// AbortRunsForSession cancels all active runs for a session key.
// Returns rich results for each run (one timer per run is fine for typical 1–2 runs/session).
func (r *Router) AbortRunsForSession(sessionKey string) []AbortResult {
	var results []AbortResult
	r.activeRuns.Range(func(key, val any) bool {
		run := val.(*ActiveRun)
		if run.SessionKey == sessionKey {
			results = append(results, r.AbortRun(run.RunID, ""))
		}
		return true
	})
	return results
}

// InjectMessage sends a user message to the running loop for a session.
// Returns true if the message was accepted, false if no active run or channel full.
func (r *Router) InjectMessage(sessionKey string, msg InjectedMessage) bool {
	runIDVal, ok := r.sessionRuns.Load(sessionKey)
	if !ok {
		return false
	}
	runVal, ok := r.activeRuns.Load(runIDVal)
	if !ok {
		return false
	}
	run := runVal.(*ActiveRun)
	select {
	case run.InjectCh <- msg:
		return true
	default:
		return false // channel full
	}
}

// InvalidateUserWorkspace clears the cached workspace for a user across all cached agent loops.
// Used when user_agent_profiles.workspace changes (e.g. admin reassignment).
func (r *Router) InvalidateUserWorkspace(userID string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, entry := range r.agents {
		if loop, ok := entry.agent.(*Loop); ok {
			loop.InvalidateUserWorkspace(userID)
		}
	}
}

// SessionKeyForRun returns the session key associated with a run ID, or "" if not found.
func (r *Router) SessionKeyForRun(runID string) string {
	val, ok := r.activeRuns.Load(runID)
	if !ok {
		return ""
	}
	return val.(*ActiveRun).SessionKey
}

// UpdateActivity records the current phase of a running agent for status queries.
// Called from the bus subscriber on agent.activity events.
func (r *Router) UpdateActivity(sessionKey, runID, phase, tool string, iteration int) {
	r.agentActivity.Store(sessionKey, &AgentActivityStatus{
		RunID:     runID,
		Phase:     phase,
		Tool:      tool,
		Iteration: iteration,
		StartedAt: time.Now(),
	})
}

// ClearActivity removes the activity status for a session (on run completion).
func (r *Router) ClearActivity(sessionKey string) {
	r.agentActivity.Delete(sessionKey)
}

// GetActivity returns the current activity status for a session, or nil if idle.
func (r *Router) GetActivity(sessionKey string) *AgentActivityStatus {
	val, ok := r.agentActivity.Load(sessionKey)
	if !ok {
		return nil
	}
	return val.(*AgentActivityStatus)
}

// IsSessionBusy returns true if there's an active run for the given session key.
// O(1) via sessionRuns secondary index.
func (r *Router) IsSessionBusy(sessionKey string) bool {
	_, ok := r.sessionRuns.Load(sessionKey)
	return ok
}

// SessionRunID returns the active run ID for a session, if any.
func (r *Router) SessionRunID(sessionKey string) (string, bool) {
	val, ok := r.sessionRuns.Load(sessionKey)
	if !ok {
		return "", false
	}
	return val.(string), true
}
