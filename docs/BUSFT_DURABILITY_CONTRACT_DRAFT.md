---
summary: "Decision draft for durable at-least-once direct-message delivery, durable duplicate suppression, and transient observer events."
read_when:
  - Planning or reviewing Pinakes issue #5.
  - Changing SQLite delivery, inbox cursor, idempotency, push retry, restart, restore, or observer-event behavior.
status: implementation
---

# BUSFT Direct-Message Durability Contract

- Issue: [#5](https://github.com/joelkehle/pinakes/issues/5)
- Status: Accepted; implementation in review
- Prepared: 2026-07-25
- Maintainer: Joel Kehle
- Executor: Unassigned

## Decision

Pinakes should provide durable, at-least-once delivery for direct messages and
durable duplicate suppression for accepted sends. Observer/SSE events should
remain transient.

This preserves Pinakes as transport infrastructure. It does not make Pinakes
the system of record for consumer workflow or business facts.

## Terms

- **Direct message:** a `request`, `response`, or `inform` addressed to a
  non-empty `to` identity.
- **Accepted send:** `POST /v1/messages` has committed the message, conversation
  linkage, delivery record, and idempotency record and may return success.
- **Transport receipt:** proof that a recipient received a delivery attempt.
  For pull delivery, this is the recipient's next poll cursor. For push
  delivery, this is a successful callback response.
- **Application acknowledgment:** a request recipient's later
  `accepted`/`rejected` acknowledgment. This is distinct from transport receipt.
- **Observer event:** an event exposed through `/v1/observe`. It is a live
  projection, not a durable message queue or audit log.

## Required guarantees

### 1. Atomic acceptance

An accepted send must atomically persist:

- the message record;
- its conversation and ordered conversation link;
- one pending delivery record for the target;
- the durable idempotency key; and
- required counter advances.

The HTTP success response must be sent only after commit.

If the process fails before commit, a retry may create the message. If it fails
after commit but before the caller receives the response, the retry must return
the original message with `duplicate: true`.

### 2. At-least-once direct delivery

Every accepted direct message remains deliverable until one of these terminal
transport conditions is durably recorded:

- a pull recipient advances its cursor past that delivery;
- a push recipient returns a successful callback response;
- a request recipient sends an application acknowledgment, which also proves
  receipt;
- a request recipient posts `progress`, `final`, or `error`, which likewise
  proves receipt; or
- the message expires under the documented TTL/deadline policy.

A restart, crash, restore, or callback-worker restart must not silently discard
an unreceived delivery. Redelivery must use the same `message_id`,
`request_id`, and message content.

At-least-once delivery permits duplicate delivery attempts. Consumers must
deduplicate by `message_id` and use `request_id` for business-operation
idempotency.

### 3. Durable duplicate suppression

The idempotency key remains:

```text
(from, to, request_id)
```

Within `IdempotencyWindow`, exactly one accepted message record may exist for
that key. The mapping from key to original `message_id` must survive restart
and restore.

Message retention must not shorten the promised idempotency window. Either:

- retain the original message for at least `IdempotencyWindow`; or
- retain a durable duplicate receipt sufficient to return the original
  `message_id` and `duplicate: true`.

The idempotency expiry clock starts at first acceptance and is not extended by
retries.

### 4. Durable pull cursors

Each target has a monotonically increasing delivery sequence. A pull response
returns a cursor covering the deliveries in that response.

Each response is bounded by configured event-count and byte limits. Rows beyond
the returned batch remain durable and pending; the returned cursor covers only
the batch actually returned.

When the recipient later polls with cursor `C`, Pinakes must durably record that
all delivery sequences below `C` were received before returning the next
response. A crash before that cursor advancement may cause replay; a crash
after the durable advancement must not resurrect the acknowledged prefix.

Cursor values must remain meaningful after restart and restore.

### 5. Durable push attempts

The in-memory push queue becomes a scheduling projection of pending durable
delivery rows. Queue saturation or process exit may delay work but must not
delete it.

A push callback:

- uses the same `message_id` on every attempt;
- includes `request_id` so the recipient can deduplicate;
- durably records a 2xx transport receipt;
- retries non-2xx and network failures with bounded backoff; and
- stops at the message's documented transport deadline, recording an explicit
  terminal transport failure and metric.

A 2xx callback proves receipt only, not successful business execution.
During shutdown, Pinakes drains accepted callback work through durable receipt
persistence. If the shutdown deadline expires, it cancels in-flight callbacks
and returns their durable delivery rows to pending before SQLite closes.

### 6. Request lifecycle after restart

| Persisted state | Restart behavior |
| --- | --- |
| queued for an inactive target | remain queued until registration or the earlier of message TTL and registration-grace deadline |
| delivered, no application acknowledgment | eligible for same-ID redelivery |
| `accepted` / executing | do not redeliver the work request; retain its execution deadline |
| `rejected`, completed, or error | retain terminal state under normal retention |
| response/inform completed but not transport-received | redeliver the direct message until receipt or deadline |

All state and deadline transitions must be transactional with the affected
delivery record.

## Observer-event contract

Observer events remain transient:

- they are held only in bounded process memory;
- they are excluded from SQLite, replication, backup, and restore;
- their cursor is valid only within one process epoch;
- restart may create a gap and must not block reconnection behind a stale
  cursor; and
- an observer must query durable message state or the owning domain system to
  reconstruct facts.

Implementation must expose an observe epoch or an equivalent stale-cursor
signal so clients can reconnect to the live head after restart. Observe events
must not be represented as an immutable audit log.

## Storage shape

Exact schema names remain an implementation choice, but the durable model needs
the equivalent of:

```text
deliveries(
  target_agent_id,
  delivery_seq,
  message_id,
  status,
  next_attempt_at,
  attempt_count,
  received_at,
  expires_at
)

idempotency(
  from_agent,
  to_agent,
  request_id,
  message_id,
  accepted_at,
  expires_at
)
```

Foreign keys or equivalent integrity checks must prevent orphaned delivery and
idempotency rows. Litestream replication is useful only after these semantics
exist in the authoritative SQLite transaction log.

## Required black-box proofs

Close/reopen and crash-boundary tests must cover:

1. request committed before response; retry returns the same message;
2. request not committed; retry creates one message;
3. unpolled request survives restart;
4. poll response followed by crash before cursor advancement replays the same
   message;
5. cursor advancement survives restart;
6. duplicate retry survives restart and message-retention pruning;
7. accepted/executing request does not restart execution delivery;
8. unreceived response and inform survive restart;
9. push callback retries survive restart and retain the same IDs;
10. successful push transport receipt survives restart;
11. an initial targeted push injection records its receipt without a second
    callback;
12. progress, final, and error events without a separate ack record receipt and
    do not replay;
13. queued work stops at its registration-grace deadline;
14. a large durable inbox is returned in bounded cursor-preserving batches;
15. restore from a replicated rehearsal database preserves pending deliveries,
    cursors, lifecycle state, and duplicate suppression; and
16. observer events disappear on restart without preventing a fresh observer
    connection.

No test may require a consumer to perform a real external write.

## Explicit non-goals

- exactly-once business execution;
- durable observer replay;
- workflow orchestration or business-state ownership;
- WP4 identity/signing changes;
- bus consolidation; or
- production replication and recovery drills before this local contract is
  implemented and proven.

## Decision checklist

- [x] Durable delivery for all three direct-message types.
- [x] `(from, to, request_id)` as the durable duplicate key.
- [x] Transport receipt semantics for pull, push, and application ack.
- [x] Transient observer events with restart-visible epoch handling.
- [x] Preserve the existing 24-hour idempotency window and 10-minute default
      message transport deadline.
