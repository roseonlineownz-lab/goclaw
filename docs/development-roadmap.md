# Development Roadmap

GoClaw v4 RC1 implementation phases and progress tracking.

---

## v4 RC1 Foundation — Phase A (COMPLETE)

**Status:** 100% DONE | **Completed:** 2026-05-04

### Scope

Single-tenant greenfield rebuild with slug-based identity, user-kind discriminator, and metadata JSONB standardization.

### Key Deliverables

- `users.kind` (enum: human/channel) + `users.user_key` (slug identity)
- `users.channel_type` for channel extensibility
- Metadata JSONB on 13 entity tables (agents, teams, shares, links, memory, skills, channels, MCP, cron, providers, configs, sessions)
- PostgreSQL 1418 LOC greenfield schema + SQLite parity
- Tenant code purge complete (zero functional residue)
- Build + test gates: PG, SQLite, vet, tenant-purge checks GREEN

### Dependencies Unblocked

Plans #2–11 now proceed.

---

## v4 RC1 Projects & Permissions — Phase B.1 (COMPLETE)

**Status:** 100% DONE | **Completed:** 2026-05-05

### Scope

Project identity system, workspace schema extension, permission 4-layer RBAC, and channel_contacts.default_project_id binding.

### Key Deliverables

- `projects` table with `workspace_id` FK, `kind` enum (personal/team/shared)
- `agent_config_permissions` JSONB permissions RBAC (admin/operator/viewer)
- `channel_contacts.default_project_id` binding for group × project defaults
- `resolveSessionProject()` helper for workspace builder agent request routing
- SQLite parity + 60+ integration tests
- Build + test gates GREEN

---

## v4 RC1 Channel Chat Support — Phase B.2 (COMPLETE)

**Status:** 100% DONE | **Completed:** 2026-05-06

### Scope

Channel workspace path resolution, identity merge atomicity, dispatch routing, sub-agent isolation, and pairing/merge separation.

### Key Deliverables

- **12-scenario channel path matrix** (agent-type × identity × context)
  - Human personal/team + channel DM/group + predefined + sub-agent
  - Production wire-in **landed 2026-05-07** (commit `780eab16`) — both
    `loop_context.go:256` and `loop_pipeline_callbacks.go:101` route through
    the new resolver; legacy 6-scenario `Resolve()` reduced to project-only
    branch. Single workspace root invariant: `ProjectWorkspacePath` now takes
    `baseDir` (env `GOCLAW_WORKSPACE_ROOT` removed).
- **Composite-key outbound dispatch** (channel_type, chat_id) with merged-contact canonical lookup
  - DM routes to canonical merged contact; group stays in original chat
  - Privacy: FS/memory scoped to merged user, addressability unchanged
- **6-table atomic merge TX**
  - channel_contacts + agent_sessions + user_context_files + memory_documents + agent_config_permissions + traces
  - Ordered locks, audit trail, post-commit best-effort FS relocation
- **Sub-agent dispatch isolation** (ProjectID snapshot, no UserID/GroupID leak)
- **Pairing vs Merge separation** (strict table ownership, no cross-mutations)
- ADR: `docs/adr/2026-05-pairing-vs-merge.md`
- **60+ integration tests** (PG + SQLite parity)

### Test Coverage

- `contact_identity_schema_test.go` (5 tests)
- `resolver_path_matrix_test.go` (12 table-driven scenarios)
- `contact_pairing_merge_separation_test.go` (5 isolation + divergence tests)
- `channel_outbound_merged_lookup_test.go` (16 composite-key tests)
- `team_tool_dispatch_sub_agent_isolation_test.go` (6 tests)
- `merge_aggregate_atomic_tx_chaos_test.go` (5 concurrent cases)
- `v4_channel_chat_e2e_test.go` (5 flows + SQLite parity)

---

## v4 RC1 Sharing Model — Phase B.3 (COMPLETE)

**Status:** 100% DONE | **Completed:** 2026-05-05

### Scope

Granular agent sharing with agent_shares table, share-link generation, permission enforcement, and privacy hard rule.

### Key Deliverables

- `agent_shares` table (granular per-agent shares vs team-wide roles)
- `AgentShareRequest` + role enum (viewer/collaborator/admin)
- Share-link generation with UUID + secret token
- Privacy hard rule: no cross-tenant access via shares
- HTTP endpoints: POST /agents/{id}/shares, GET /agents/{id}/shares, DELETE shares/{id}
- 25+ tests

---

## v4 RC1 Permission 4-Layer — Phase B.4 (COMPLETE)

**Status:** 100% DONE | **Completed:** 2026-05-05

### Scope

RBAC enforcement across agent/team scope with admin/operator/viewer roles, agent_config_permissions table, and merge-aware permission migration.

### Key Deliverables

- `agent_config_permissions` JSONB table (scoped by agent × user/team)
- Permission resolver (principal → role → allowed actions)
- Role matrix: admin (all) / operator (read+write) / viewer (read-only)
- Merge migration helper `MigrateConfigPermissionsForMerge()` (Plan #6 P09 caller)
- Policy enforcement in HTTP + WS methods
- 40+ tests (merge, inheritance, scope isolation)

---

## v4 RC1 Memory 5D Scope — Phase B.5 (COMPLETE)

**Status:** 100% DONE | **Completed:** 2026-05-05

### Scope

Multi-dimensional memory scoping (tenant × contact/user × session × scope × type) with FS-backed vector storage.

### Key Deliverables

- `memory_documents.contact_id` + `project_id` 5D scoping
- pgvector FS-backed halfvec storage (`{workspace}/memory/{contact_id}/{document_id}.halfvec`)
- Memory consolidation workers (episodic/semantic/dreaming)
- DomainEventBus integration for event-driven consolidation
- SQLite in-memory parity + 35+ tests

---

## v4 RC1 MCP Scope Hardening — Phase B.6 (COMPLETE)

**Status:** 100% DONE | **Completed:** 2026-05-06

### Scope

Permission enforcement for Model Context Protocol servers with scope validation and credential isolation.

### Key Deliverables

- MCP server permission scoping (admin/operator/viewer)
- Server list + tool invoke authorization
- Credential isolation per scope
- 15+ tests

---

## RC1 Target Release

- **Foundation (Phase A):** COMPLETE
- **Projects (Phase B.1):** COMPLETE
- **Channels (Phase B.2):** COMPLETE
- **Sharing (Phase B.3):** COMPLETE
- **Permissions (Phase B.4):** COMPLETE
- **Memory (Phase B.5):** COMPLETE
- **MCP (Phase B.6):** COMPLETE

**RC1 Cutover Status:** Foundation locked. All plans complete. Ready for rc1 tag + documentation sync.

---

## Deferred to RC2

- ~~**Channel path matrix production wire-in**~~ **LANDED 2026-05-07** (commit `780eab16`).
- ~~**Session project override**~~ **LANDED 2026-05-07** (Layer 2). `/project list`,
  `/project current`, `/project switch <slug>`, `/project clear` ship on Telegram +
  Feishu/Lark + Discord. Storage: `agent_sessions.project_id` column directly (no
  metadata indirection, no TTL). Permission: `ProjectGrantStore.ResolveProjectRole`
  (project member+ or owner). Resolver unchanged — Source 1 (`session.ProjectID`)
  already covers the new write path.
- ~~**Workspace folder completeness audit**~~ **LANDED 2026-05-07**. 8 gaps closed:
  G1 + G2 + G6 (path traversal sanitize on contact_merge / agents_create / team_attachments —
  G2 was a false positive, NormalizeAgentID already enforces); G3 + G7 (project slug + agent_key
  immutability, Go-level guard matching team_key pattern); G5 (TTS workspace fallback to
  `/tmp` removed); G8 (`IsPredefined` dead field removed); G4 (Layer 2 FS relocation —
  `workspace.SwitchSessionProject` orchestrator with per-session mutex, atomic
  UPDATE-then-rename, B2a strict-orphan on rename failure). Wired into Layer 1 admin RPC
  (`sessions.updateProject`) and Layer 2 `/project switch` on all 3 channels. Reports:
  `plans/reports/brainstorm-260507-1734-workspace-folder-completeness-audit.md`.
- ~~**Integration test fixture cleanup**~~ **LANDED 2026-05-07** (batch 1 + batch 2 +
  batch 3). PG + unit + sqliteonly suites all green; integration suite stable across
  back-to-back runs. Reports:
  `plans/reports/test-cleanup-260507-1452-batch1-summary.md`,
  `plans/reports/test-cleanup-260507-1545-batch2-summary.md`. Batch 2 also landed two
  small production-correctness fixes carried with the tests: hooks `ScopeTenant` →
  `ScopeUser` (Go const ↔ DB constraint drift) and `kg_entities.tsv` GENERATED column
  (FTS path referenced a column the v4 schema had not added). Batch 3 closed two
  cross-run flakes: `TestShellAbort_ProcessGroupKilled` orphan check used a global
  "sleep 60" pattern (collided with stragglers); `TestVaultNamespaceFix_thuyTienScenario`
  scenario B starved the KG source out of the top-N when prior-run vault docs
  accumulated — both now scoped per-run.
- **Desktop UI enhancements** (if any)

---

## Known Blockers / Cross-Plan Dependencies

1. **Plan #4 P05 migration** (`channel_contacts.default_project_id` ADD) — REQUIRED before Plan #6 P10
2. **Plan #7 P06 helper** (`MigrateConfigPermissionsForMerge`) — REQUIRED before Plan #6 P09
3. **Plan #5 memory 5D scope** (`memory_documents.contact_id`, `project_id` fields) — REQUIRED before Plan #6 P11
4. **Plan #1 foundation** (`users.kind`, `users.user_key`) — REQUIRED for all identity tests

All blockers resolved before merge.

---
