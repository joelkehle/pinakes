# Elephant Check: WP2 Durable Direct Delivery

- Status: PASS TO IMPLEMENTATION
- Checked revision: `c1dad5d8fe0dc766f733c941b1b276e5fc7e498f`
- Scope: Pinakes issue #5; direct-message transport reliability
- Non-goals: workflow ownership, WP4, bus consolidation, deployment

## Objective

An accepted direct message must survive a Pinakes restart and remain
redeliverable under the same identity until transport receipt or expiry.
Duplicate-send receipts must survive for the full idempotency window.
Observer traffic remains an explicitly transient live projection.

## Whole-system boundary

Producer -> Pinakes acceptance transaction -> durable delivery record ->
pull/push recipient -> durable transport receipt. Consumer workflow and
business facts remain in the consumer's owning system.

| Layer | Durable owner | Runtime projection |
| --- | --- | --- |
| Message, conversation link | Pinakes SQLite | in-memory message index |
| Delivery sequence and receipt | Pinakes SQLite | inbox/push scheduler |
| Duplicate receipt | Pinakes SQLite | in-memory dedupe index |
| Observer event | none | bounded process memory |

## Goals and state ownership

This change improves transport reliability only. It does not add prioritization,
workflow routing, learning, or domain-state ownership. Pinakes owns transport
metadata because it alone can atomically decide whether a send was accepted and
whether transport receipt was recorded.

## Three-state truth

- Documented: accepted durability contract and HTTP contract.
- Implemented: SQLite schema, acceptance transaction, cursor advancement,
  restart restoration, push scheduling, and observer epoch.
- Proven: local close/reopen, failure-boundary, retention, push, migration, and
  protocol tests; then the repository's full local gate.

## Representative probes

1. A UCLA request is committed, Pinakes restarts, and the recipient polls the
   same `message_id`.
2. A response or inform is committed, Pinakes restarts before receipt, and the
   recipient receives it.
3. An observer reconnects after restart, sees a new epoch, and does not treat
   the old cursor as durable history.

## Emergent conditions

- **EC-1 Atomic acceptance:** success follows one transaction containing the
  message, conversation link, delivery, duplicate receipt, and counters.
- **EC-2 Durable pull receipt:** cursor advancement is committed before the
  next poll response and does not resurrect after restart.
- **EC-3 Durable duplicate suppression:** retries return the original
  `message_id` throughout the configured idempotency window, even if the
  message row is pruned.
- **EC-4 Durable push scheduling:** pending callback work survives restart;
  callback receipt and terminal delivery failure are durable.
- **EC-5 Transient observation:** observer events do not persist and each
  process exposes a distinct observer epoch.
- **EC-6 Failure visibility:** pending and terminal delivery failures are
  visible in health/status metrics without exposing message bodies.
- **EC-7 State boundary:** new rows contain transport metadata only and do not
  become a workflow or business-fact store.
- **EC-8 Compatibility:** existing SQLite databases and legacy JSON state
  migrate without losing still-pending direct deliveries or duplicate receipts.

## Proof plan

Targeted Go tests cover every emergent condition. `agent-check` is the final
local gate. A separate reviewer evaluates the pull request. This receipt makes
no deployment, migration, or production-readiness claim.
