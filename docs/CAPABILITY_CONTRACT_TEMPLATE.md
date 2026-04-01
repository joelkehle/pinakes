---
summary: Template for documenting agent capability delivery semantics. Copy to your agent repo and fill in per capability.
read_when:
  - adding a new capability to an agent
  - documenting an existing capability for the first time
  - reviewing whether a capability contract is complete
status: working draft
---

# Capability Contract Template

Copy this template to your agent repo (e.g. `docs/capabilities/<capability-name>.md`) and fill it in for each capability your agent advertises at registration.

The bus routes opaque messages. This contract tells producers, consumers, and operators what's inside them and how they behave.

---

## `<capability-name>`

**Agent:** `<agent_id>`
**Owner:** `<repo>`
**Mutation class:** `observe` | `recommend` | `mutate`

### Purpose

One paragraph. What does this capability do? What problem does it solve?

### Request

Message type sent to this agent to invoke the capability.

```json
{
  "type": "<message-type>",
  "body": {
  }
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| | | | |

### Reply

Message sent back on completion.

```json
{
  "type": "<reply-type>",
  "body": {
  }
}
```

| Field | Type | Description |
|-------|------|-------------|
| | | |

### Error

Message sent on failure.

```json
{
  "type": "error",
  "body": {
    "code": "<error-code>",
    "message": "<human-readable>"
  }
}
```

Known error codes:

| Code | Meaning | Retryable? |
|------|---------|------------|
| | | |

### Delivery semantics

| Property | Value | Notes |
|----------|-------|-------|
| Timeout | e.g. 120s | How long the sender should wait before treating as failed. |
| Idempotent | yes / no | Can the same request be sent twice safely? |
| Side effects | none / read-only / writes to X | What external systems are touched? |
| Retry safety | safe / unsafe / safe-with-dedup | Can the sender retry on timeout or error? |
| Ordering | unordered / ordered-per-conversation | Does message order matter? |

### Progress events

Does this capability emit progress events via `POST /v1/events`?

- [ ] Yes — emits `progress` events with: `<describe shape>`
- [ ] No

### Dependencies

External systems required for this capability to function:

-
-

### Example flow

Brief description of a typical request-reply cycle, including any progress events.

---

*Template: Claude + Codex (architecture review, 2026-03-31)*
