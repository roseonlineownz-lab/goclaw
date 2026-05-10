# Project Changelog

Significant changes, features, and fixes in reverse chronological order.

---

## 2026-05-06 — v4 RC1 Phase B: Channel Chat Support

**All 11 phases complete.** Channel identity merge atomicity, workspace path resolution, and sub-agent isolation landed.

- **12-scenario channel path matrix** — foundation for routing across agent-type × identity × context combinations. Production wire-in deferred to rc2; implementation fully tested.
- **Composite-key outbound dispatch** — merged contact canonical lookup for DM routing; group messages route to original chat, preserving group conversation coherence.
- **6-table atomic merge TX** — consolidates channel identities across contacts, sessions, context files, memory, permissions, traces with ordered locks and audit trail. Post-commit FS relocation best-effort.
- **Sub-agent isolation** — ProjectID snapshot prevents parent context leakage; UserID/GroupID not propagated. Race-safe against parent group changes.
- **Pairing vs merge separation** — orthogonal operations with strict table ownership (PairingStore, ContactStore, PermissionStore). No cross-mutations; devices can diverge from identities by design.
- **Group × project binding** — `channel_contacts.default_project_id` wired via `resolveSessionProject()` helper (Plan #4 P05).
- **60+ integration tests** — PG + SQLite parity verified across all 11 phases. Chaos tests for merge concurrency (5 cases) and sub-agent snapshot isolation.
- **ADR:** `docs/adr/2026-05-pairing-vs-merge.md` — formalizes separation invariants.

### Test Summary

- Contact identity schema (5 tests)
- 12-scenario path matrix (12 table-driven scenarios)
- Pairing/merge isolation (5 tests)
- Outbound merged lookup (16 composite-key tests)
- Sub-agent dispatch isolation (6 tests)
- Atomic merge TX chaos (5 concurrent cases)
- End-to-end flows (5 flows across merge, group-team, sub-agent, pairing, group-project)

### Dependencies Verified

- Plan #4 P05 (channel_contacts.default_project_id migration + resolveSessionProject) — verified wired
- Plan #7 P06 (MigrateConfigPermissionsForMerge helper + traces.contact_id) — verified imports
- Plan #1 (users.kind enum + user_key) — tested in P01
- Plan #5 (memory_documents.contact_id 5D scope + project_id) — pre-check in P11 flow

### RC1 Status

All Phase B phases complete. Foundation (Phase A) locked. Ready for rc1 tag + docs sync.

---

## v4 rc1 Phase B Foundation — 2026-05-05

### Schema & Identity

**Greenfield foundation for v4 rebuild.** Single-tenant, slug-based identity + metadata standard.

- **Slug identity:** `users.user_key` (auto from email local-part), `agent_teams.team_key` (auto from team name), both UNIQUE + immutable post-create, used as workspace folder identifiers. Generation in `internal/identity/slug.go` (deterministic, no DB import).
- **User kind discriminator:** `users.kind` (enum: 'human', 'channel'), `users.channel_type VARCHAR(20) NULL` for future channel-type extensibility. Shape constraint enforced: human must have NULL channel_type; channel must have non-NULL. Mutations atomic via `UserStore.SetKind()`.
- **Metadata JSONB standard:** `metadata JSONB NOT NULL DEFAULT '{}'` (PG) / `metadata TEXT NOT NULL DEFAULT '{}'` (SQLite) on 13 entity tables: agents, agent_teams, agent_shares, agent_links, memory_documents, skills, skill_versions, channel_instances, mcp_servers, cron_jobs, llm_providers, system_configs, user_sessions. Extensibility point for future feature-specific data without schema migrations.

### Tenant Purge Complete

- **Last live tenant code removed:** `buildSkillEmbeddingTenantCond` helper deleted. Grep gate `make check-tenant-purge` confirms zero functional tenant residue.
- **SQLite parity:** SchemaVersion 1→2, migration map covers all new columns for legacy desktop DBs. Fresh DB applies via `schema.sql` directly.
- **v3 residue cleanup:** 4 SQLite integration test files deleted (multi-tenant features not ported to v4).

### Schema Changes (Dual DB)

**PostgreSQL:** `migrations/000001_initial.up.sql` greenfield file (1418 LOC) — no ALTER files, all tables created fresh.
**SQLite:** `internal/store/sqlitestore/schema.sql` (1387 LOC) + incremental migration map, parity verified by `parity_test.go`.

### Build & Test Gates

- `go build ./...` ✓ (PG build)
- `go build -tags sqliteonly ./...` ✓ (Desktop/SQLite build)
- `go vet ./...` ✓
- `make check-tenant-purge` ✓ (grep-zero gate)
- `make test-foundation` ✓ (~45 integration tests, all GREEN)

### Unblocks

Plans #2-11 in v4 rc1 now proceed (foundation locked).

---

## v3.11.3 — 2026-04-26

### Fixes

- **`goclaw providers verify`** — empty body now triggers ping mode (provider registered/reachable check) and returns `{valid:true}` for registered providers. New `--model <alias>` flag for chat-verify against a specific model. CLI response parser switched from stale `{success, models}` to `{valid, error}`. Onboard auto-verify path fixed identically (was silently printing "FAILED" on every successful provider creation). (#1034)
- **`goclaw providers delete`** — succeeds when referenced by `agent_heartbeats`. FK changed to `ON DELETE SET NULL`; `DeleteProvider` (PG + SQLite) now wraps in a transaction that also disables affected heartbeats so the next scheduler tick cannot fire stale config. `slog.Warn("heartbeat.provider_cleared")` emitted with the disabled count. (#1034)
- **`goclaw doctor`** — provider rows with empty `display_name` now render the canonical `name` instead of a blank line. Query switched from `COALESCE(display_name, name)` to `COALESCE(NULLIF(display_name, ''), name)`. (#1034)

### Migrations

- **PG:** `000057_heartbeat_provider_fk_set_null` — defensive orphan cleanup, drop existing FK by lookup, re-add with `ON DELETE SET NULL`. Brief `ACCESS EXCLUSIVE` lock on `agent_heartbeats` during ALTER (sub-second on small tables; heartbeat workers may pause briefly).
- **SQLite:** schema v25 → v26 — full table rebuild for `agent_heartbeats` with new FK clause; explicit 25-column INSERT/SELECT preserves existing rows. `idx_heartbeats_due` recreated.

### Upgrade notes

- **Docker users:** MUST pull the new image (`ghcr.io/nextlevelbuilder/goclaw:v3.11.3`) AND run `goclaw upgrade` (or `goclaw migrate up`). Stale images on v3.11.2 will fail boot with `schema version mismatch: required 57, current 56` after the migration runs.
- **Bare-metal users:** rebuild and run `./goclaw upgrade`.

### OpenAPI

- `/v1/providers/{id}/verify` — `requestBody.required: false`; `model` documented as optional with ping-mode semantics.

---

## 2026-04-24

### Tools: Config-driven shell deny-groups + read_audio routing fixes

**Features**

- **`shellDenyGroups` runtime config:** `config.tools.shellDenyGroups` (map[string]bool) allows operators to toggle shell deny-groups (e.g. `package_install`, `env_dump`) from the /config Web UI without restarting. Merged with per-agent overrides with per-key agent precedence; multi-tenant invariant preserved. Subscribed to `bus.TopicConfigChanged` for live reload.

**Fixes**

- **Credentialed CLI wording scope:** "operation requires admin approval" error wording now scoped to `[CREDENTIALED EXEC]` marker only — was over-applied to generic shell failures, causing unjustified LLM pre-refusals.
- **read_audio transcription routing:** Fixed silent fallback on missing API credentials for transcription/gemini/openai paths — now hard-errors with clear message. Fixed openai_compat providers (e.g. DashScope) not reaching `/v1/audio/transcriptions` endpoint; moved transcription model check above provider type switch.

**Tests**

- 6 unit tests for shell deny-groups merge/defensive-copy semantics.
- 3 pub/sub dispatch tests for config reload lifecycle.
- 3 regression tests for read_audio fail-fast paths.

---

## 2026-04-22

### Providers: Native image generation for Codex + OpenAI-compat

**Features**

- **Codex native track:** `CodexProvider` now attaches the `image_generation` tool object to `POST /codex/responses` when the agent permits it. Streams `response.image_generation_call.partial_image` intermediate frames + `response.output_item.done` (type `image_generation_call`) final images; non-stream path walks `response.output[]`. Deduped per `item_id`, partial frames emitted as `ImageContent{Partial:true}` for UI progressive render.
- **OpenAI-compat track:** `tools[]` serializer passes `{type:"image_generation"}` entries through natively; response parser reads `choices[0].message.images[]` / `choices[0].delta.images[]` (data URLs) into `ChatResponse.Images`.
- **Media persistence:** `internal/agent/media.go` `persistAssistantImages()` writes final images to `{workspace}/media/{sha256}.{ext}`, returns `MediaRef` entries, clears inline base64. Idempotent on hash. Wired via `pipeline.Deps.PersistAssistantImages` callback from `FinalizeStage`. Partial frames skipped.
- **Capabilities + gate:** `ProviderCapabilities.ImageGeneration` flag, set true on Codex provider. Tri-level gate in agent loop: provider capability AND `AgentConfig.AllowImageGeneration` (read from `other_config.allow_image_generation`, default true) AND request not opted-out via `x-goclaw-no-image-gen` header.
- **Web UI:** Composer "Images" toggle chip (visible only when provider supports image gen, per-agent persistence in localStorage). Streaming placeholder skeleton in `ActiveRunZone` while partials arrive. `MediaGallery` assigns `generated-{timestamp}.png` filename for assistant-generated PNGs.

**Wire format**

Implementation is evidence-backed against the native ChatGPT Responses API event shape, not the compat shim shape. Research notes in `plans/reports/`.

**i18n**

- 1 UI key (`imageGenDownloadName`) in `ui/web/src/i18n/locales/{en,vi,zh}/chat.json` — download filename for generated images.

**Tests**

- Unit tests across providers (Codex native + OpenAI-compat), agent media persistence, store config. Full test sweep: 2618 pass.

**Internal docs**

- `plans/260422-1349-goclaw-chatgpt-image-gen/` — plan + phase files.
- `plans/reports/researcher-260422-1414-codex-native-image-events.md` — native event schema.

## 2026-04-20

### Pipeline: accurate context token tracking + dynamic compaction

**Features**

- **Session token display from metadata:** `sessions.metadata` now carries `last_prompt_tokens` and `last_message_count` on finalize. List query reads from metadata; fallback to octet/rune heuristic when absent. Fixes stale token display across session re-opens.
- **Tool-schema token accounting:** `TokenCounter.CountToolSchemas(model, tools)` new method counts tool definitions serialized as JSON. Tool-schema tokens included in `OverheadTokens` at ContextStage.
- **Dynamic compaction max_tokens:** Compaction `max_tokens` now derived from `in/25` with clamp `[1024, 8192]`. Applied to both summarization flow (`loop_compact.go`) and history sanitization (`loop_history_sanitize.go`). Replaces static 4096 limit — adapts budget to context size.

**Code**

- `internal/store/pg/sessions_list.go` — read/write `last_prompt_tokens` and `last_message_count` in metadata.
- `internal/store/sqlitestore/sessions*.go` — parity SQLite store updates.
- `internal/tokencount/token_counter.go` — `CountToolSchemas` interface method + `tiktoken_counter.go` impl.
- `internal/pipeline/context_stage.go` — include tool overhead in `OverheadTokens`.
- `internal/agent/loop_compact.go` — `dynamicSummaryMax` function; apply to compaction call.
- `internal/agent/loop_history_sanitize.go` — apply dynamic max to sanitization.

**Tests**

- `internal/tokencount/count_tool_schemas_test.go` — tool schema token counting.
- `internal/agent/loop_compact_dynamic_max_test.go` — dynamic max_tokens clamping.
- `internal/pipeline/context_stage_tool_overhead_test.go` — tool overhead integration.
- `internal/store/sqlitestore/sessions_display_tokens_integration_test.go` — metadata round-trip.

---

### TTS: timeout tenant-config + Gemini text-only 400 fix

**Features & Fixes**

- **Tenant-config timeout:** HTTP `/v1/tts/synthesize` and `/v1/tts/test-connection` now read `tts.timeout_ms` from system_configs (default 120s, was hardcoded 15s/10s). Gemini client default bumped 30s→120s for end-to-end alignment.
- **Gemini text-only error recovery:** Gemini preview models occasionally emit 400 "text generation" responses. Fixed by: (1) prepending inline prefix `"Speak naturally: "` to single-voice synthesis (multi-speaker untouched), (2) 1-retry with stronger prefix `"Read the following text aloud without translating, commenting, or modifying: "`, (3) new sentinel `gemini.ErrTextOnlyResponse` preserved through fallback chain via `errors.Join`.
- **Error UX:** HTTP returns 422 with localized `MsgTtsGeminiTextOnly` message. Agent TTS tool branches on sentinel to emit locale-translated ForLLM response.
- **Model default:** Gemini default model bumped `gemini-2.5-flash-preview-tts` → `gemini-3.1-flash-tts-preview` for higher stability.
- **UI bounds:** TTS timeout input now has `max=300000` (5 min).

**i18n**

- New key `MsgTtsGeminiTextOnly` in EN/VI/ZH catalogs for HTTP 422 + agent-tool ForLLM mapping.

**Code**

- `internal/audio/tts.go` — read tenant timeout in synthesize handlers.
- `internal/audio/gemini/` — inline prefix logic, retry budget, text-only sentinel.
- `internal/tools/tts.go` — agent-tool i18n branching on sentinel.
- `internal/http/methods/tts.go` — HTTP 422 error mapping.

---

### Tools: `send_file` — explicit workspace file delivery

**Features**

- **`send_file` tool** (`internal/tools/send_file.go`): dedicated tool for sending existing workspace files as chat attachments. Takes `path` (required) and `caption` (optional). Replaces implicit `message(MEDIA:path)` convention for re-delivering already-created files. Marks `DeliveredMedia` on success to prevent duplicate delivery.
- **`DeliveredMedia` mark on `message(MEDIA:)` sends** (`internal/tools/message.go`): patched to call `IsDelivered` / mark after successful MEDIA upload — closes the cross-tool duplicate-delivery gap where a file sent via `message(MEDIA:)` was not tracked and could be re-sent by `send_file`.
- Registered as builtin tool in `cmd/gateway_tools_wiring.go` and seeded in `cmd/gateway_builtin_tools.go`.

---

## 2026-04-22

### Codex OAuth pool routing strategy cleanup

**Changes**

- Removed `primary_first` from the public Codex OAuth routing strategy surface. The API, OpenAPI schema, and web UI now expose only `round_robin` and `priority_order`.
- Legacy `primary_first` and `manual` routing values now normalize to `priority_order` on read in the backend store layer.
- Activity endpoints now default empty/no-pool responses to `priority_order` instead of `primary_first`.
- Agent overrides that explicitly persist `extra_provider_names: []` continue to behave as single-account-only routing after the migration.

**Docs**

- Updated `docs/02-providers.md` and `docs/18-http-api.md` to describe the two-strategy model and the compatibility migration.

## 2026-04-19

### TTS: Gemini provider + ProviderCapabilities schema engine

**Features**

- **Gemini TTS provider** (`internal/audio/gemini/`): supports `gemini-2.5-flash-preview-tts` and `gemini-2.5-pro-preview-tts`. 30 prebuilt voices, 70+ languages, multi-speaker mode (up to 2 simultaneous speakers with distinct voices), audio-tag styling, WAV output via PCM-to-WAV conversion.
- **`ProviderCapabilities` schema** (`internal/audio/capabilities.go`): dynamic per-provider param descriptor. Each provider exposes `Capabilities()` returning `[]ParamSchema` (type, range, default, dependsOn conditions, hidden flag) + `CustomFeatures` flags. UI reads `GET /v1/tts/capabilities` and renders param editors without hard-coded field lists.
- **Dual-read TTS storage**: tenant config read from both legacy flat keys (`tts.provider`, `tts.voice_id`, …) and new params blob (`tts.<provider>.params` JSON). Blob wins on conflict. Allows gradual migration; no data loss on downgrade.
- **`VoiceListProvider` interface** refactor: dynamic voice fetching (ElevenLabs, MiniMax) now via `ListVoices(ctx, ListVoicesOptions)` instead of per-provider ad-hoc methods. Unified `audio.Voice` type.
- **`POST /v1/tts/test-connection`**: ephemeral provider creation from request credentials + short synthesis smoke test. Returns `{ success, latency_ms }`. No provider registration; no config mutation. Operator role required.
- **`GET /v1/tts/capabilities`**: returns `ProviderCapabilities` JSON for all registered providers.

**i18n**

- Backend sentinel error keys (`MsgTtsGeminiInvalidVoice`, `MsgTtsGeminiInvalidModel`, `MsgTtsGeminiSpeakerLimit`, `MsgTtsParamOutOfRange`, `MsgTtsParamDependsOn`, `MsgTtsMiniMaxVoicesFailed`) in all 3 catalogs (EN/VI/ZH).
- HTTP 422 responses for Gemini sentinel errors now use `i18n.T(locale, key, args...)` — locale from `Accept-Language` header.
- ~80 param `label`/`help` keys across web + desktop locale files (EN/VI/ZH); parity enforced by `ui/web/src/__tests__/i18n-tts-key-parity.test.ts`.

**Security**

- SSRF guard on `api_base` override for test-connection (`validateProviderURL()`) — blocks `127.0.0.1` / `localhost` / RFC1918 ranges.

**Docs**

- `docs/tts-provider-capabilities.md` — schema reference + per-provider param tables + storage format + "Adding a new provider" checklist.
- `docs/codebase-summary.md` — TTS subsystem section documenting manager, providers, storage, endpoints.
