# GoClaw Gateway

PostgreSQL multi-tenant AI agent gateway with WebSocket RPC + HTTP API.

## Language

Always respond in the same language as the user's prompt. If the user writes in Vietnamese, respond in Vietnamese. If in English, respond in English. Match the user's language naturally.

## Tech Stack

**Backend:** Go 1.26, Cobra CLI, gorilla/websocket, pgx/v5 (database/sql, no ORM), golang-migrate, go-rod/rod, telego (Telegram)
**Web UI:** React 19, Vite 6, TypeScript, Tailwind CSS 4, Radix UI, Zustand, React Router 7. Located in `ui/web/`. **Use `pnpm` (not npm).**
**Desktop UI:** React 19, Vite 6, TypeScript, Tailwind CSS 4, Zustand, Framer Motion. Located in `ui/desktop/frontend/`. **Use `pnpm`.**
**Desktop App:** Wails v2 (`//go:build sqliteonly`). Located in `ui/desktop/`. Embeds gateway + React frontend in single binary.
**Database:** PostgreSQL 18 with pgvector (standard). SQLite via `modernc.org/sqlite` (desktop/lite). Raw SQL with `$1, $2` (PG) or `?` (SQLite) positional params. Nullable columns: `*string`, `*time.Time`, etc.

## Project Structure

```
cmd/                          CLI commands, gateway startup, onboard wizard, migrations
internal/
├── agent/                    Agent loop (think→act→observe), router, resolver, input guard
├── bootstrap/                System prompt files (SOUL.md, IDENTITY.md) + seeding + per-user seed
├── bus/                      Event bus system
├── cache/                    Caching layer
├── channels/                 Channel manager: Telegram, Feishu/Lark, Zalo, Discord, WhatsApp
│   └── whatsapp/             Native WhatsApp via whatsmeow (v3)
├── config/                   Config loading (JSON5) + env var overlay
├── consolidation/            Memory consolidation workers (episodic, semantic, dreaming) (v3)
├── crypto/                   AES-256-GCM encryption for API keys
├── cron/                     Cron scheduling (at/every/cron expr)
├── edition/                  Edition system (Lite, Standard) with feature gating
├── eventbus/                 Domain event bus with worker pool, dedup, retry (v3)
├── gateway/                  WS + HTTP server, client, method router
│   └── methods/              RPC handlers (chat, agents, sessions, config, skills, cron, pairing)
├── hooks/                    Hook system for extensibility
├── http/                     HTTP API (/v1/chat/completions, /v1/agents, /v1/skills, etc.)
├── i18n/                     Message catalog: T(locale, key, args...) + per-locale catalogs (en/vi/zh)
├── knowledgegraph/           Knowledge graph storage and traversal
├── mcp/                      Model Context Protocol bridge/server
├── media/                    Media handling utilities
├── memory/                   Memory system (pgvector)
├── oauth/                    OAuth authentication
├── orchestration/            Orchestration primitives: BatchQueue[T] generic, ChildResult, media conversion (v3)
├── permissions/              RBAC (admin/operator/viewer)
├── pipeline/                 8-stage agent pipeline (context→history→prompt→think→act→observe→memory→summarize)
├── providers/                LLM providers: Anthropic (native HTTP+SSE), OpenAI-compat (HTTP+SSE), DashScope (Alibaba Qwen), Claude CLI (stdio+MCP bridge), ACP (Anthropic Console Proxy), Codex (OpenAI)
├── providerresolve/          Provider adapter + model registry with forward-compat resolver
├── sandbox/                  Docker-based code execution sandbox
├── scheduler/                Lane-based concurrency (main/subagent/cron)
├── sessions/                 Session management
├── skills/                   SKILL.md loader + BM25 search
├── store/                    Store interfaces + implementations (PostgreSQL, SQLite)
│   ├── base/                 Shared store abstractions: Dialect interface, helpers (NilStr, BuildMapUpdate, BuildScopeClause)
│   ├── pg/                   PostgreSQL implementations (database/sql + pgx/v5)
│   └── sqlitestore/          SQLite implementations (modernc.org/sqlite)
├── tasks/                    Task management
├── tokencount/               tiktoken BPE token counting
├── tools/                    Tool registry, filesystem, exec, web, memory, subagent, MCP bridge, delegate
├── tracing/                  LLM call tracing + optional OTel export (build-tag gated)
├── tts/                      Text-to-Speech (OpenAI, ElevenLabs, Edge, MiniMax)
├── updater/                  Desktop auto-update checker (Lite edition)
├── upgrade/                  Database schema version tracking
├── vault/                    Knowledge Vault with wikilinks, hybrid search, FS sync
├── workspace/                WorkspaceContext resolver for 6 scenarios
pkg/protocol/                 Wire types (frames, methods, errors, events)
pkg/browser/                  Browser automation (Rod + CDP)
migrations/                   PostgreSQL migration files
ui/web/                       React SPA (pnpm, Vite, Tailwind, Radix UI)
ui/desktop/                   Wails v2 desktop app (React frontend + embedded gateway)
```

## Key Patterns

- **Store layer:** Interface-based (`store.SessionStore`, `store.AgentStore`, etc.) with shared Dialect pattern in `store/base/`. PostgreSQL (`pg/`) and SQLite (`sqlitestore/`) implementations use `database/sql` + `pgx/v5/stdlib` + sqlx, raw SQL, `BuildMapUpdate()` and `BuildScopeClause()` helpers
- **Agent types:** `open` (per-user context, 7 files) vs `predefined` (shared context + USER.md per-user)
- **Context files:** `agent_context_files` (agent-level) + `user_context_files` (per-user), routed via `ContextFileInterceptor`
- **Providers:** Anthropic (native HTTP+SSE), OpenAI-compat (HTTP+SSE), DashScope (Alibaba Qwen), Claude CLI (stdio+MCP bridge), ACP (Anthropic Console Proxy), Codex (OpenAI). All use `RetryDo()` for retries. Loads from `llm_providers` table with encrypted API keys. ProviderAdapter enables pluggable implementations with ModelRegistry forward-compat resolver. Shared SSEScanner in `providers/sse_reader.go` for streaming providers
- **Pipeline:** 8-stage loop (context→history→prompt→think→act→observe→memory→summarize) with pluggable callbacks, always-on execution path
- **DomainEventBus:** Typed events with worker pool, dedup, retry. Used by consolidation pipeline and memory workers
- **3-tier memory:** Working (conversation) → Episodic (session summaries) → Semantic (KG). Progressive loading L0/L1/L2 with auto-inject for L0
- **Knowledge Vault:** Document registry + [[wikilinks]] + hybrid search, query layer above existing stores, FS sync, unified search
- **Context propagation:** `store.WithAgentType(ctx)`, `store.WithUserID(ctx)`, `store.WithAgentID(ctx)`, `store.WithLocale(ctx)`, `store.WithTenantID(ctx)`
- **Request middleware:** Composable chain (cache, service tier, request guards), zero-alloc fast path for hot operations
- **Self-evolution:** Metrics → suggestions → auto-adapt. 3 progressive stages: metrics collection, suggestion analysis, guardrail-protected apply/rollback
- **Orchestration:** Delegate tool for inter-agent task delegation with agent_links, 3 delegation modes (auto/explicit/manual), token-aware work distribution. BatchQueue[T] generic for result aggregation
- **WebSocket protocol:** Frame types `req`/`res`/`event`. First request must be `connect`
- **Config:** JSON5 at `GOCLAW_CONFIG` env. Secrets in `.env.local` or env vars, never in config.json
- **Security:** Rate limiting, input guard (detection-only), CORS, shell deny patterns, SSRF protection, path traversal prevention, AES-256-GCM encryption. All security logs: `slog.Warn("security.*")`
- **Telegram formatting:** LLM output → `SanitizeAssistantContent()` → `markdownToTelegramHTML()` → `chunkHTML()` → `sendHTML()`. Tables rendered as ASCII in `<pre>` tags
- **i18n:** Web UI uses `i18next` with namespace-split locale files in `ui/web/src/i18n/locales/{lang}/`. Backend uses `internal/i18n` message catalog with `i18n.T(locale, key, args...)`. Locale propagated via `store.WithLocale(ctx)` — WS `connect` param `locale`, HTTP `Accept-Language` header. Supported: en (default), vi, zh. New user-facing strings: add key to `internal/i18n/keys.go`, add translations to all 3 catalog files. New UI strings: add key to all 3 locale dirs. Bootstrap templates (SOUL.md, etc.) stay English-only (LLM consumption).

## Running

```bash
go build -o goclaw . && ./goclaw onboard && source .env.local && ./goclaw
./goclaw migrate up                 # DB migrations
# Integration tests (requires pgvector pg18 on port 5433)
docker run -d --name pgtest -p 5433:5432 -e POSTGRES_PASSWORD=test -e POSTGRES_DB=goclaw_test pgvector/pgvector:pg18
TEST_DATABASE_URL="postgres://postgres:test@localhost:5433/goclaw_test?sslmode=disable" \
  go test -v -tags integration ./tests/integration/

cd ui/web && pnpm install && pnpm dev   # Web dashboard (dev)

# Desktop (Wails + SQLite)
cd ui/desktop && wails dev -tags sqliteonly  # Dev mode with hot reload (direct)
make desktop-dev                             # Same as above via Makefile
make desktop-build VERSION=0.1.0             # Build .app (macOS) or .exe (Windows)
make desktop-dmg VERSION=0.1.0               # Create .dmg installer (macOS only)
```

## CI/CD & Releases

### Workflows

| Workflow | Trigger | Purpose |
|----------|---------|---------|
| `ci.yaml` | push main, PR→main/dev | Go build+test+vet, Web build |
| `release.yaml` | push main | semantic-release → binaries + Docker (4 variants + web) + Discord |
| `release-beta.yaml` | tag `v*-beta*` / `v*-rc*` | Beta binaries + Docker + GitHub prerelease |
| `release-desktop.yaml` | tag `lite-v*` | Desktop app (macOS+Windows), auto prerelease for `-beta`/`-rc` tags |

### Creating Releases

**Standard release** — merge `dev` → `main`. `go-semantic-release` auto-creates version from conventional commits.

**Beta release** (from dev):
```bash
git tag v2.67.0-beta.1 && git push origin v2.67.0-beta.1   # standard beta
git tag lite-v1.2.0-beta.1 && git push origin lite-v1.2.0-beta.1  # lite beta
```

**Desktop release:**
```bash
git tag lite-v1.1.0 && git push origin lite-v1.1.0   # stable
git tag lite-v1.1.0-beta.1 && git push origin lite-v1.1.0-beta.1  # beta (prerelease)
```

### Docker Images

Published to GHCR (`ghcr.io/nextlevelbuilder/goclaw`) and Docker Hub (`digitop/goclaw`).

| Variant | Tag | Contents |
|---------|-----|----------|
| latest | `:latest`, `:vX.Y.Z` | Backend + web UI + Python |
| base | `:base`, `:vX.Y.Z-base` | Backend only, no UI/runtimes |
| full | `:full`, `:vX.Y.Z-full` | All runtimes + skills pre-installed |
| otel | `:otel`, `:vX.Y.Z-otel` | Latest + OpenTelemetry tracing |
| web | `-web:latest` | Standalone web UI (Nginx) |
| beta | `:beta`, `:vX.Y.Z-beta.N` | Beta builds from dev |

### Tag Pattern Safety

- `release.yaml`: branch-triggered (push main) → `go-semantic-release` creates clean `vX.Y.Z` tags
- `release-beta.yaml`: tag-triggered (`v*-beta*`, `v*-rc*`) — never matches clean semver
- `release-desktop.yaml`: tag-triggered (`lite-v*`) — `lite-` prefix prevents overlap
- No workflow triggers overlap — each tag pattern is distinct

## Desktop Edition (Lite)

- **Build tag:** `//go:build sqliteonly` — desktop binary includes only SQLite, no PostgreSQL
- **Edition system:** `internal/edition/edition.go` — `Lite` preset auto-selected for SQLite backend. Check `edition.Current()` for feature limits
- **Entry point:** `ui/desktop/main.go` + `ui/desktop/app.go` — Wails bindings, embedded gateway
- **Secrets:** OS keyring (`go-keyring`) with file fallback at `~/.goclaw/secrets/`
- **Data dir:** `~/.goclaw/data/` (SQLite DB, configs)
- **Workspace:** `~/.goclaw/workspace/` (agent files, team workspace)
- **Port:** 18790 (localhost only), configurable via `GOCLAW_PORT`
- **WS params:** All WS method params use **camelCase** (`teamId`, `taskId`, `sessionKey`) — match Go struct `json:"..."` tags
- **Version:** `cmd.Version` set via `-ldflags` at build time. Frontend calls `wails.getVersion()`
- **Auto-update:** `internal/updater/updater.go` checks GitHub Releases for `lite-v*` tags. Frontend `UpdateBanner` shows notification
- **Releases:** Tag `lite-v*` triggers `.github/workflows/release-desktop.yaml` → builds macOS (arm64+amd64) + Windows → GitHub Release
- **Install scripts:** `scripts/install-lite.sh` (macOS), `scripts/install-lite.ps1` (Windows PowerShell)
- **Lite limits:** 5 agents, 1 team, 5 members, 50 sessions. No channels, heartbeat, file storage UI, skill self-manage, KG, RBAC, multi-tenant
- **Tool gating:** `TeamActionPolicy` in `internal/tools/team_action_policy.go` — lite blocks comment/review/approve/reject/attach/ask_user. `skill_manage`/`publish_skill` not registered in lite
- **File serving:** 2-layer path isolation in `internal/http/files.go` — workspace boundary (all editions) + tenant scope (standard only with RBAC)

## [IMPORTANT] When Uncertain, Scout — Don't Guess

**Default rule for any non-trivial design / brainstorm / planning work:** if you are unsure about how something works in the codebase (existing scope, schema, control flow, integration), **spawn an Explore scout agent**. Do not infer, guess, or extrapolate from partial reads.

Triggers — when to scout:
- Designing schema changes that touch tables you haven't read end-to-end (columns, FKs, constraints)
- Proposing API/RPC contracts when the existing handler shape is not memorized
- Asserting behavior of a subsystem (channels, memory, hooks, MCP, cron, etc.) without re-reading its current code
- User asks "how does X work today?" and you have not opened the relevant files in this session
- Planning ripple effects across multiple subsystems — scout each independently before integrating
- Re-scoping or revisiting an earlier decision (treat as fresh question)

Anti-patterns:
- ❌ "I think the table has column X" — grep it
- ❌ "It probably routes through Y handler" — read the file
- ❌ "Likely the same as v3 pattern" — verify v4 didn't refactor it

Scout cost is low; hallucinated facts in design docs cost rebuild cycles. Bias toward over-scouting in brainstorm/planning phases.

## Plan Verification Rules

Apply before finalizing any multi-phase plan. Trust-but-verify between scout → planner → final plan.

### Verification discipline (what to verify)

1. **Verify factual claims against code** — re-grep/re-count every number, path, endpoint. Don't copy from scout summaries.
2. **Trace semantics, not just cite lines** — when plan references existing/upstream code, identify WHEN each field mutates and under WHAT conditions. Line-range citation without control-flow trace = how ports silently invert behavior. Check: every call, or specific branches only?
3. **No fabricated identifiers / API families** — every symbol in plan must cite `file:line`. RED FLAGS: plausible-sounding wrappers (`Keyring`, `Validator`, `Manager`), centralized packages (`internal/security`, `internal/auth`) that may be scattered, OTel-style (`StartSpan/EndSpan`) when codebase is emit-based. When unsure, `go doc <pkg>` lists actual exported surface. Apply especially when plan says "reuse existing X".
4. **Struct scope audit before adding state** — verify lifetime (per-request/session/agent/process) before adding a field to an existing struct. "Plausibly per-X" is a red flag — grep construction + ownership. Shared-instance state leaks across isolation boundaries.
5. **Gate-premise test math** — before asserting "feature X triggers independently of Y", list all early-returns from function entry to X. Math-verify any fixture claiming "X without Y".
6. **Port = config-shape match** — "faithful port" divergences in config field name/type are silent breaking changes for users copying upstream config. Match upstream shape, or explicitly flag each divergence with rationale in the phase file.
7. **Verify external API endpoints via `docs-seeker`** — before writing endpoint into plan. Sibling APIs often use different roots.

### Scope & coverage (where to look)

8. **Grep delete scope deep** — `grep -rn '<symbol>' .` whole repo. Stubs often have refs in catalogs/routing/switch cases. Enumerate ALL sites in todo.
9. **Signature-change callers enumeration** — grep + list all callers explicitly. "Update all callers" insufficient.
10. **Alias/shim coverage** — enumerate ALL exported symbols via `go doc <pkg>`. Add compile-time signature guards.
11. **Scout desktop and web separately** — `ui/desktop/frontend/` ≠ `ui/web/`. Different structure, i18n namespaces, test framework presence.

### Phasing & ordering (when)

12. **Re-scout on scope change** — if phase promotes from deferred → active, re-scout. Don't reuse brainstorm summary.
13. **Cross-phase gates explicit** — "Phase N-1 merged + tests green" in phase Context. Execution order alone ≠ enforcement.
14. **Zero-coverage characterization test = blocker step** — write byte/request-body fixture test BEFORE migration. Not "recommended".
15. **i18n keys ordering** — add key + 3 catalogs as explicit todo step BEFORE handler code. Missing key = runtime crash.

### Conventions & finalization

16. **Context key style convention** — check existing `context.go` pattern before introducing new key types. Mixed = code smell.
17. **Verify pass MANDATORY after rewrite** — spawn fresh Explore/grep to audit planner output. Don't trust self-validation.

**Pattern to avoid:** user asks → planner writes → report "done".
**Safer pattern:** user asks → scout → planner writes → audit-verify → report.

**Red-team practice:** After planner completes, run `code-reviewer`/`brainstormer` in audit mode: "spot-check 15+ claims vs live codebase". Past catches: fabricated `crypto.Keyring`/`tracing.StartSpan` (agent-hooks plan); inverted TS-port semantics + wrong struct scope + misread early-return gate (context-pruning plan). See `plans/*/reports/audit-*.md` for concrete examples.

## [IMPORTANT] Verified Decisions Are Sticky — Audit Does Not Auto-Reverse

When a solution has been verified by reading actual source, running tests, or empirical experiment, lock it into the report/plan with a source note (e.g., `verified by reading {file:line}` or `verified by test {name}`).

### Protocol when audit/red-team raises a counter-argument

1. **Check verification trail:** is the decision marked as verified? (Note in report/plan, or code-read earlier in conversation.)
2. **Audit must not auto-reverse:** a counter-argument alone is insufficient. Only revise when:
   - Audit finds a **new** issue the verification missed (state the issue + why the prior check missed it)
   - Or context changed since verification (codebase moved, business decision changed)
3. **Clean stale notes:** after verifying, prune outdated risk rows / unresolved questions from reports so future agents do not misread them as open conflicts.
4. **Surface contradictions on verified decisions to user as:** "audit says X, but Y is verified by {source} — does the audit bring new data to justify a reverse?" Do not silently flip the decision and re-ask the user.

**Why:** decision drift wastes cycles (AI reverses → user re-confirms → AI agrees). Audit value is highest when finding new issues, not re-litigating settled ones. Verification source (`code:line`) is the source of truth, not audit reasoning alone.

### Examples
- ❌ Brainstorm verified pattern X via grep. Audit raises "maybe pattern Y" → present as "conflict, please confirm". Should be: "already verified, audit brought no new data."
- ✅ Audit discovers FE consumes `error.code` as string slug (not scouted earlier) → genuine new anomaly → surface to user.

## [IMPORTANT] Guard User Decisions Against Audit/YAGNI Drift

When applying audit feedback (code-reviewer, brainstorm audit, red-team, delta audit, etc.) or YAGNI principles to revise designs/reports/plans:

**NEVER silently reverse decisions the user has already confirmed.**

### Mandatory protocol before any cut/change

1. **Trace before cutting:** Before applying an audit recommendation that removes/changes a field, column, threshold, or architectural choice — trace back the conversation to check if the user explicitly chose that value/design.

2. **Categorize each change:**
   - ✅ **Safe to apply:** cuts of items you (Claude) proposed but user never explicitly confirmed (e.g., research-driven additions, defensive extras).
   - ⚠️ **Must confirm first:** anything touching a user's explicit answer — thresholds, scope, library choice, schema shape, phase content, feature inclusion/exclusion.
   - 🚫 **Never auto-reverse:** business decisions (pricing, timing, team size, scope boundaries, compliance stance, locked Q-* answers).

3. **Surface reversals before executing:** If audit recommends reversing a user decision, present the conflict to user with: (a) user's original decision verbatim, (b) audit's reasoning, (c) trade-off, (d) explicit ask "giữ nguyên / đổi theo audit / hybrid?". Do NOT apply.

4. **Document drift in reports:** When cutting something user proposed/confirmed, annotate with reason + user-confirmation trail (e.g., "CUT per audit FX — user did not explicitly choose this field, em added from research"). Preserves traceability.

5. **Auditor bias awareness:** Audit/red-team agents lean heavily toward YAGNI/minimalism + maximum-paranoia security. This is valuable input but not authoritative over product/business decisions. Audit findings are **input to user**, not orders to Claude.

### Red flags to catch automatically
- Changing a numeric threshold user picked (TTL, memory cost, retry count, rate limit, confidence)
- Removing a column/field user explicitly mentioned
- Swapping library/framework user endorsed
- Moving scope across phases user agreed on
- Cutting a feature user confirmed "cần" or "có"
- Reverting a locked decision (Q-1, Q-A, X-locks, etc.)

### Carve-out: correctness fixes always apply
Findings that fix **security holes, race conditions, data integrity bugs, or wire-format incompatibilities** apply regardless of user-decision overlap — but document the overlap in the change note. The carve-out is narrow: it does not extend to "could be more secure" or "could be tighter".

**Rule of thumb:** If unsure whether a cut reverses user intent, ask. Cost of 1 clarifying question ≪ cost of silent regression that surfaces at demo.

## [IMPORTANT] Validate Audit Findings Against Real Threat Model

Before applying a code-reviewer/audit/red-team finding that flags something as "too narrow", "too loose", "not comprehensive", or "risky", trace the finding against the **actual runtime behavior and what the code protects** — not against abstract categories.

A theoretical gap is only a real gap if the code's real usage pattern produces the failure mode.

### Protocol
1. **Identify what the code actually stores/protects.** E.g. "cache stores resolved permission decisions, not schema-derived data."
2. **Walk each scenario the reviewer flagged** through that lens. Does scenario X actually produce the bad outcome (stale decision / wrong result / security hole)? Often the answer is "theoretically yes, practically no."
3. **Separate real risks from abstract ones.** Apply fixes for real risks; document non-risks with a short rationale; surface borderline cases to the user instead of auto-accepting.
4. **Look for the failure mode the reviewer missed.** Often the more realistic bug sits one step away from what the review flagged (e.g. typo in a static list that makes the fingerprint query return zero rows — more likely than any of the "DDL width" cases the reviewer listed).

## [IMPORTANT] v4 Rebuild Discipline — Workflow-First, Not KISS/YAGNI Default

**v4 is a one-shot greenfield rebuild.** Decisions lock in for years. There is no "iterate later" because the rebuild itself IS the iteration. Default KISS / YAGNI thinking causes choices that look fine for rc1 but accrue maintenance debt, performance regression, or wire-format breaks once production load arrives.

**Discipline for every non-trivial design choice in v4 plan/audit work:**

1. **Workflow-walk BEFORE trade-off analysis.** Trace the concrete sequence first — read path, write path, cache hit, cache miss, migration, edge cases. Identify which paths actually exist and where data crosses each boundary (DB↔Go, Go↔HTTP, web↔backend, …). Trade-off comparison is meaningless until you know which path the metric applies to.
2. **Verify with grep / Read existing code, not analytical reasoning alone.** "It probably works like X" is the failure mode. Open the file, confirm the query pattern, the field shape, the call graph.
3. **Don't accept subagent / red-team / brainstorm framings as immutable.** Re-question the premise. If a finding says "fabricated SDK", the right question is not "SDK or no SDK?" — it is "do we even need the path that SDK serves?".
4. **When user pushes back, re-derive from first principles — do NOT swing.** Reactive correction (KISS → over-engineer → KISS …) is a sign you skipped step 1. Going back to the workflow walk is the right response, not flipping the conclusion.
5. **Evaluate long-term: maintenance, future features, production load, multi-tenant scale.** "Enough for rc1" is the wrong bar in a once-in-a-while rebuild. Bar is "still correct + maintainable when v4 has been in production 2 years and feature set has doubled".
6. **Idea triage takes effort. Spend it.** Every architectural choice in v4 — schema shape, scope keys, encoding format, API contract — deserves explicit walk through real usage, not template comparison.

**Anti-patterns that should trigger you to stop and re-walk:**
- Reaching for KISS or YAGNI as the deciding rationale before you have walked the workflow.
- Picking option B because user disagreed with option A, without re-deriving why.
- Trade-off table with "performance" or "consistency" rows where you cannot point to a concrete operation that benefits.
- Conclusions that depend on a subagent's framing of the choice instead of the actual code paths.

**Apply this discipline retroactively:** when revisiting prior plan decisions, re-walk the workflow even if the decision is "verified" — verification was against an earlier framing that may not reflect actual usage.

This rule has higher priority than the global "YAGNI / KISS / DRY" instruction for v4 rebuild work specifically. The global rule still applies elsewhere.

## [IMPORTANT] Code Comments & Artifact Naming — No Plan References

Code comments and file names (including SQL migration files) **must not reference plan artifacts**: phase numbers, finding codes (F1, F3, F13, Y1, CU2, …), audit labels (audit A4), red-team session labels, brainstorm section numbers (§5.4), or the plan's internal taxonomy.

Rationale: plan headers change, get renumbered, or disappear between iterations; once a plan is archived those references become noise that future readers cannot resolve. The *reason* for the code (invariant, constraint, race, trade-off) must be stable and self-contained.

### Rules
- **Explain the why, not the origin.** Write "advisory lock serializes concurrent merge so only one TX wins" — not "per F10 merge-atomic fix".
- **Migration file names** use the domain slug only: `000003_user_sessions_family_id.up.sql`, not `000003_phase_06_F4_family.up.sql`.
- **Test names** describe the scenario: `TestRefreshTokenTheftDetection`, not `TestRefreshToken_F4`.
- **Commit messages** likewise — describe the change, not the finding code.
- Plan references belong in the plan's own Markdown files (`plans/…/phase-XX-*.md`) and PR descriptions, not in code.

### Allowed references in code
- Function/symbol names in the same codebase (e.g., "see ValidateAgentID").
- Stable external identifiers: RFC numbers (RFC 6749 §10.4), PostgreSQL SQLSTATE codes, CVE IDs, linked issue numbers when the issue is durable.

## [IMPORTANT] Deferral Discipline — Update Files When Deferring Plan/Phase Work

When a plan item, sub-phase, finding, or scope element is deferred to a later phase / version (e.g., "defer to v4.0.1", "defer Finding 5", "skip Sub-11D this session"), the deferral MUST be reflected in the relevant files BEFORE proceeding with current work. A spoken/chat-only deferral is forgotten by future sessions and re-litigated.

### Mandatory updates on every deferral

1. **Phase file (`plans/.../phase-XX-*.md`)** — annotate the deferred section in-place with:
   - `**DEFERRED to <target>** (decision YYYY-MM-DD): <one-line reason>`
   - Move related todos under a `### Deferred` subsection so they don't read as in-scope.
   - Update `## Overview > Effort` if the deferral materially changes the budget.
2. **Overview plan (`plans/.../plan.md`)** — if the deferral spans multiple phases or shifts the roadmap (e.g., "Phase 12 → EPIC-05"), update the phase-list status/owner column.
3. **ADR** (`docs/adr/YYYY-MM-<slug>.md`) — write a short ADR (~30-50 lines) when the deferral is a permanent architectural call (e.g., "localStorage tokens kept, HttpOnly cookies deferred"). Skip ADR for tactical reorder of work within the same release.
4. **Roadmap / changelog** — if the deferral changes a public-facing release scope (`docs/development-roadmap.md` / `docs/project-changelog.md`), update there too.
5. **Linked tickets / TODO comments in code** — if code references the deferred item (e.g., `// TODO: Finding 5 cookies`), keep the marker but update the date / target so it doesn't go stale.

### When the rule kicks in
- "defer to v4.0.1 / next phase / next session"
- "skip this sub-phase, do later"
- "leave for follow-up"
- "out of scope this session"
- Picking option B/C in a /cook gate that explicitly drops scope from a plan

### Anti-patterns
- ❌ Telling user "defer Finding 5 to v4.0.1" then implementing without touching the phase file → next session re-discovers Finding 5 as if untriaged.
- ❌ Cutting Sub-11C/D from this session without updating todo list / status in plan → kanban / sync-back reads as "phase complete" prematurely.
- ❌ Adding only a chat note ("decided 2026-05-03") with no file change → invisible to future LLMs.

**Rule of thumb:** if the deferral wouldn't be obvious to a future agent reading the phase file cold, it isn't recorded yet.

## [IMPORTANT] Deferrals Land in the Plan File, Not the Chat

Deferring to a later phase / sub-plan? Write it into the receiving file BEFORE closing the current phase. Chat is ephemeral; the next cook agent only reads the plan file.

Append to receiving `phase-N.md` (or `plan.md` for cross-EPIC) under `## Cross-phase carryover` — include source phase + finding code, concrete action, and acceptance test if any.

- ❌ Chat-only: "we'll fix C1 in Phase 06."
- ✅ `phase-06.md` → carryover entry with file path + verification.

## Post-Implementation Checklist

After implementing or modifying Go code, run these checks:

```bash
go fix ./...                        # Apply Go version upgrades (run before commit)
go build ./...                      # Compile check (PG build)
go build -tags sqliteonly ./...     # Compile check (Desktop/SQLite build)
go vet ./...                        # Static analysis
go test -race ./tests/integration/  # Integration tests with race detector
```

Go conventions to follow:
- Use `errors.Is(err, sentinel)` instead of `err == sentinel`
- Use `switch/case` instead of `if/else if` chains on the same variable
- Use `append(dst, src...)` instead of loop-based append
- Always handle errors; don't ignore return values
- **Migrations (dual-DB):** PostgreSQL and SQLite have **separate migration systems**. When adding schema changes: (1) PG: add SQL in `migrations/` + bump `RequiredSchemaVersion` in `internal/upgrade/version.go`. (2) SQLite: update `internal/store/sqlitestore/schema.sql` (full schema for fresh DBs) + add incremental patch in `schema.go` `migrations` map + bump `SchemaVersion` constant. **Always update both** — missing SQLite migrations cause desktop edition to crash on startup
- **i18n strings:** When adding user-facing error messages, add key to `internal/i18n/keys.go` and translations to `catalog_en.go`, `catalog_vi.go`, `catalog_zh.go`. For UI strings, add to all locale JSON files in `ui/web/src/i18n/locales/{en,vi,zh}/`
- **SQL safety:** When implementing or modifying SQL store code (`store/pg/*.go`), always verify: (1) All user inputs use parameterized queries (`$1, $2, ...`), never string concatenation — prevents SQL injection. (2) Queries are optimized — no N+1 queries, no unnecessary full table scans. (3) WHERE clauses, JOINs, and ORDER BY columns use existing indices — check migration files for available indexes
- **DB query reuse:** Before adding a new DB query for key entities (teams, agents, sessions, users), check if the same data is already fetched earlier in the current flow/pipeline. Prefer passing resolved data through context, event payloads, or function params rather than re-querying. Duplicate queries waste DB resources and add latency
- **Solution design:** When designing a fix or feature, identify the root cause first — don't just patch symptoms. Think through production scenarios (high concurrency, multi-tenant isolation, failure cascades, long-running sessions) to ensure the solution holds up. Prefer explicit configuration over runtime heuristics. Prefer the simplest solution that addresses the root cause directly

## Mobile UI/UX Rules

When implementing or modifying web UI components, follow these rules to ensure mobile compatibility:

- **Viewport height:** Use `h-dvh` (dynamic viewport height), never `h-screen`. `h-screen` causes content to hide behind mobile browser chrome and virtual keyboards
- **Input font-size:** All `<input>`, `<textarea>`, `<select>` must use `text-base md:text-sm` (16px on mobile). Font-size < 16px triggers iOS Safari auto-zoom on focus
- **Safe areas:** Root layout must use `viewport-fit=cover` meta tag. Apply `safe-top`, `safe-bottom`, `safe-left`, `safe-right` utility classes on edge-anchored elements (app shell, sidebar, toasts, chat input) for notched devices
- **Touch targets:** Icon buttons must have ≥44px hit area on touch devices. CSS in `index.css` uses `@media (pointer: coarse)` with `::after` pseudo-elements to expand targets
- **Tables:** Always wrap `<table>` in `<div className="overflow-x-auto">` and set `min-w-[600px]` on the table for horizontal scroll on narrow screens
- **Grid layouts:** Use mobile-first responsive grids: `grid-cols-1 sm:grid-cols-2 lg:grid-cols-N`. Never use fixed `grid-cols-N` without a mobile breakpoint
- **Dialogs:** Full-screen on mobile with slide-up animation (`max-sm:inset-0`), centered with zoom on desktop (`sm:max-w-lg`). Handled in `ui/dialog.tsx`
- **Virtual keyboard:** Chat input uses `useVirtualKeyboard()` hook + `var(--keyboard-height, 0px)` CSS var to stay above the keyboard
- **Scroll behavior:** Use `overscroll-contain` on scrollable areas to prevent background scroll. Auto-scroll: smooth for incoming messages, instant on user send
- **Landscape:** Use `landscape-compact` class on top bars to reduce padding in phone landscape orientation (`max-height: 500px`)
- **Portal dropdowns in dialogs:** Custom dropdown components using `createPortal(content, document.body)` MUST add `pointer-events-auto` class to the dropdown element. Radix Dialog sets `pointer-events: none` on `document.body` — without this class, dropdowns are unclickable. Radix-native portals (Select, Popover) handle this automatically
- **Timezone:** User timezone stored in Zustand (`useUiStore`). Charts use `formatBucketTz()` from `lib/format.ts` with native `Intl.DateTimeFormat` — no date-fns-tz dependency
- **ErrorBoundary key:** `AppLayout` uses `<ErrorBoundary key={stableErrorBoundaryKey(pathname)}>` which strips dynamic segments (`/chat/session-A` → `/chat`). NEVER use `key={location.pathname}` on ErrorBoundary/Suspense wrapping `<Outlet>` — it causes full page remount on param changes. Pages with sub-navigation (chat sessions, detail pages) must share a stable key
- **Route params as source of truth:** For pages with URL params (e.g. `/chat/:sessionKey`), derive state from `useParams()` — do NOT duplicate into `useState`. Dual state causes race conditions between `setState` and `navigate()` leading to UI flash (state bounces: B→A→B). Use optional params (`/chat/:sessionKey?`) instead of two separate routes
