# ADR: Pairing, BindUser, and Merge Are Three Orthogonal Operations

**Date:** 2026-05
**Status:** Accepted
**Deciders:** v4 channel-chat-support design review

---

## Context

Three distinct admin actions operate on channel-related data and were historically
conflated or performed together:

1. **Pairing** — an admin approves a device's channel access request. Writes to
   `pairing_requests` → `paired_devices`. The device is now allowed to communicate
   via the channel. `channel_contacts.merged_id` is NOT touched.

2. **BindUser** (`PairingStore.BindUser`) — an optional subsequent step that links a
   paired device to an authenticated `users` row. Writes `paired_devices.user_id`.
   This allows HTTP/WS requests from that sender to carry user scope. Independent from
   both Pairing and Merge; admin-triggered or channel-triggered on first authenticated
   message.

3. **Merge** (`ContactStore.MergeUserAggregate`) — an admin-only atomic operation that
   consolidates multiple channel identities into one `users` row. Writes
   `channel_contacts.merged_id` and ripples `user_id` across `agent_sessions`,
   `user_context_files`, and `memory_documents`. Does NOT touch `paired_devices`.

In v3 these were sometimes performed in the same code path, creating implicit coupling
that made replay-safety and privacy reasoning hard.

## Decision

**Enforce strict table ownership — no cross-mutations:**

- `PairingStore` methods: read/write `paired_devices` and `pairing_requests` only.
- `ContactStore.MergeUserAggregate`: read/write `channel_contacts` and ripple tables
  only (`agent_sessions`, `user_context_files`, `memory_documents`).
- `PairingStore.BindUser`: read/write `paired_devices.user_id` only.

A caller that needs both (e.g., pairing a device AND associating it with a merged
identity) MUST call the two methods independently and intentionally.

The `contact_pairing_merge_separation_test.go` integration tests lock these invariants.
The notable by-design divergence: if an admin binds a device to userA and later merges
the corresponding contact into userB, the final state is `paired_devices.user_id = A`
AND `channel_contacts.merged_id = B`. This is correct — a device binding is per-device
physical identity; a contact merge is per-identity logical unification. They describe
different things and SHOULD diverge independently.

## Rationale

1. **Privacy boundary.** Pairing alone must not grant access to a merged user's
   history. Keeping the operations separate means the merge decision (which is
   irreversible and has data-ripple consequences) stays exclusively in admin hands via
   `MergeUserAggregate`.

2. **Replay-safety.** Each operation is idempotent and scoped to its own tables.
   A retry of Pairing never accidentally re-sets `merged_id`; a retry of Merge never
   clobbers `paired_devices.user_id`.

3. **Auditability.** `merge_audit` JSONB records who triggered the merge and when.
   BindUser has its own idempotency guard (`ErrPairingBoundToDifferentUser`). Mixing
   them would lose this per-operation audit trail.

## Consequences

- Callers that previously assumed pairing implies identity linkage must now call
  `BindUser` and/or `MergeUserAggregate` explicitly.
- Admins will see divergent device-binding vs merged-identity states — this is
  expected and documented (see Test 5 in the separation test file).
- Future code reviewers: any function that writes to BOTH `paired_devices` AND
  `channel_contacts.merged_id` is a violation of this ADR and must be rejected.

## Alternatives Considered

- **Auto-merge on pairing** — rejected. Implicit merge without admin consent violates
  the privacy boundary and makes audit trails unreliable.
- **Single unified "link" operation** — rejected. Pairing is a channel-scope,
  revocable access gate with short TTL (30 days). Merge is permanent identity
  consolidation with data ripple. Their lifecycles and ownership are incompatible.
