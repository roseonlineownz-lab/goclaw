# System Architecture

Overview of GoClaw v4 core architectural patterns and subsystems.

---

## Channel Path Resolution

GoClaw v4 channels support 12 workspace scenarios based on agent type (open/predefined), identity (user/channel), and context (team/project). The resolver (`internal/workspace/resolver.go` + `resolver_channel.go`) maps inbound messages to workspace paths and determines visibility scope.

### 12-Scenario Matrix (v4 Phase B)

| Agent Type | Identity | Scope | Workspace Path | Contact Isolation |
|-----------|----------|-------|-----------------|------------------|
| **open** | human | personal | `users/{user_key}/agents/{agent_key}` | per-user context files |
| **open** | human | team | `teams/{team_key}/agents/{agent_key}` | team scope |
| **open** | channel DM | personal | `users/{merged_user_key}/agents/{agent_key}` | merged user's context |
| **open** | channel group | team | `teams/{team_key}/agents/{agent_key}` | team scope (group chat identity) |
| **predefined** | human | personal | `users/{user_key}` (shared agent) | per-user overrides + agent defaults |
| **predefined** | human | team | `teams/{team_key}` (shared agent) | team scope |
| **predefined** | channel DM | personal | `users/{merged_user_key}` (shared agent) | merged user's context |
| **predefined** | channel group | team | `teams/{team_key}` (shared agent) | team identity, group is member |
| **open** | sub-agent | personal | inherited from parent ProjectID | no new isolation |
| **open** | sub-agent | team | inherited from parent ProjectID | no new isolation |
| **predefined** | sub-agent | personal | inherited from parent ProjectID | no new isolation |
| **predefined** | sub-agent | team | inherited from parent ProjectID | no new isolation |

### Resolver Behavior

**Current (production, rc1):** `Resolver.Resolve()` uses 6-scenario tree (human/channel × personal/team/project). Handles session project override for advanced routing.

**Implemented (deferred to rc2):** `Resolver.ResolveChannel()` uses full 12-scenario matrix. Includes group-chat logic, sub-agent snapshot isolation, and privacy zone separation. Cutover requires `loop_context.go` refactoring. [Implementation: `plans/260504-2230-channel-chat-support/plan.md`]

---

## Channel Dispatch Lookup

### Outbound Routing (Merged Contact Canonical Lookup)

When an agent sends a message to a channel, the dispatcher must route to the correct chat. **Composite-key lookup** uses channel type + chat ID:

```
For DM (pairwise):
  • Lookup contact by (channel_type, sender_chat_id) → channel_contacts row
  • If merged_id present → fetch canonical user's primary DM contact
  • Send to canonical contact's chat_id (single DM)
  
For Group:
  • Lookup by (channel_type, group_chat_id) → channel_contacts row
  • merged_id is IGNORED for group outbound
  • Send to original group_chat_id (privacy: FS/memory scoped to merged user, but group addressability unchanged)
```

**Methods:** `store.ContactStore.GetContactByChannelAndChatID()` (direct lookup) + `GetCanonicalDMContact()` (follows merged_id for DM routing). [Implementation: `internal/store/contact_store.go`, `internal/channels/dispatch.go` PeerKind branch]

**Privacy Model:** Merged users share a single identity for workspace/context access, but group membership remains on the original contact. Replies to group messages route to the original group chat, preserving group conversation coherence.

---

## Sub-Agent Isolation

Sub-agents dispatched via `team_tool_dispatch.go` are isolated from parent identity context to prevent token/scope leakage:

### Isolation Pattern

- **Parent context removed:** UserID, GroupID, ContactID NOT propagated to sub-agent
- **ProjectID snapshot:** Parent's resolved ProjectID passed as `RunRequest.ProjectOverride` (source 0 — parent value, immutable by sub-agent)
- **TeamID explicit:** Propagated if dispatch is team-scoped; sub-agent resolver independently applies its own 12-scenario logic

### Race Safety

Sub-agent snapshot captures parent's ProjectID at dispatch time. If parent's group later changes its default_project_id, the sub-agent's already-started run uses the parent-provided snapshot. No bidirectional coupling. [Implementation: `internal/tools/team_tool_dispatch.go`; verified in integration tests for concurrent merge/dispatch scenarios]

---

## Pairing vs Merge Boundary

Three orthogonal operations on channel data, each with strict table ownership:

### Pairing (DMPolicyPairing)

- **Scope:** Device-to-channel access approval
- **Writes:** `paired_devices`, `pairing_requests`
- **Duration:** Short-lived (30-day expiry)
- **Ownership:** PairingStore only — does NOT touch merged_id

### Merge (MergeUserAggregate)

- **Scope:** Atomic identity consolidation across 6 tables
- **Writes:** `channel_contacts.merged_id` + ripple to `agent_sessions`, `user_context_files`, `memory_documents`, `agent_config_permissions`, `traces`
- **Duration:** Permanent (admin-only, audit trail required)
- **Ownership:** ContactStore + PermissionStore only — does NOT touch paired_devices

### BindUser (optional)

- **Scope:** Link a paired device to an authenticated user row
- **Writes:** `paired_devices.user_id`
- **Ownership:** PairingStore only

### Separation Invariant

An admin can pair a device to userA, later merge the contact into userB, and the final state is: `paired_devices.user_id = A` AND `channel_contacts.merged_id = B`. This is correct. Pairing describes physical device identity; merge describes logical identity unification. They MUST diverge independently.

[Details: `docs/adr/2026-05-pairing-vs-merge.md`]

---

## Channel Identity Schema

### Core Tables

**channel_contacts** — Per-contact identity mapping (one row per channel user/group):
- `channel_type` — telegram, discord, whatsapp, etc.
- `chat_id` — platform-native ID (user ID, group ID, or conversation ID)
- `merged_id` — FK to `users.id` (NULL if not merged; SET to canonical user on merge)
- `default_project_id` — FK to `projects.id` (NULL if no default; used for group × project binding)

**paired_devices** — Per-device channel access (one row per device pairing):
- `device_id` — hardware identifier
- `user_id` — FK to `users.id` (NULL if not yet bound; BindUser sets this)
- Device lifecycle managed by PairingStore; independent of merge

### Merge Transaction (Atomic, 6 tables)

When `MergeUserAggregate` executes:

1. Acquire `SELECT FOR UPDATE` on source contact row (serialization)
2. Insert/Update `channel_contacts.merged_id = target_user_id`
3. Ripple `user_id` to `agent_sessions`, `user_context_files`, `memory_documents` (WHERE contact_id IN (source_contact_ids))
4. Migrate permission records via `permissions.MigrateConfigPermissionsForMerge()` helper (Plan #7 P06)
5. Update `traces.contact_id`, `spans.contact_id` to new canonical user (if present)
6. Write audit trail to `channel_contacts.merge_audit` JSONB
7. Post-commit: async FS relocation (best-effort, non-blocking)

[Implementation: `internal/store/pg/merge_aggregate.go`]

---

## Context Files & Memory Scoping

**Context files** (`user_context_files`) — Accessible only by the file owner's merged user identity. Group memberships do not grant cross-user context access.

**Memory documents** — 5D scope: `(tenant_id, contact_id/user_id, session_id, scope, type)`. Merge ripples user_id to all rows with source contact_id, consolidating episodic/semantic memory under merged identity.

---

## Key Patterns Summary

| Pattern | Purpose | Reference |
|---------|---------|-----------|
| **Composite-key dispatch** | Route DM/group outbound to correct chat | `internal/channels/dispatch.go` |
| **Merged contact canonical lookup** | Follow merged_id for DM canonical routing | `store.ContactStore.GetCanonicalDMContact()` |
| **12-scenario path matrix** | Resolve workspace paths for all agent × identity × context combinations | `internal/workspace/resolver_channel.go` |
| **ProjectID snapshot on sub-agent dispatch** | Prevent parent project changes affecting running sub-agents | `internal/tools/team_tool_dispatch.go` |
| **Pairing/merge separation** | Strict table ownership, no cross-mutations | `docs/adr/2026-05-pairing-vs-merge.md` |
| **6-table atomic merge TX** | Consolidate identity with audit trail, ordered locks for concurrency | `internal/store/pg/merge_aggregate.go` |

---
