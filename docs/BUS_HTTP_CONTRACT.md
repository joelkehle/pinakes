---
summary: Frozen HTTP contract and runtime config surface for the current bus implementation in pinakes.
read_when:
  - extracting the bus into pinakes
  - validating contract compatibility during repo split
  - checking runtime env/config behavior for the current bus
---

# Bus HTTP Contract

Last updated: 2026-06-10

Purpose: preserve the extracted bus surface in `pinakes`.

This doc describes the extracted bus contract as implemented by:

- [main.go](/home/joelkehle/Projects/shared/pinakes/cmd/pinakes/main.go)
- [server.go](/home/joelkehle/Projects/shared/pinakes/pkg/httpapi/server.go)
- [store.go](/home/joelkehle/Projects/shared/pinakes/pkg/bus/store.go)
- [contract_test.go](/home/joelkehle/Projects/shared/pinakes/pkg/httpapi/contract_test.go)

## Freeze Checklist

- [x] bus routes inventoried from `pkg/httpapi/server.go`
- [x] auth/signature rules inventoried from handlers + store behavior
- [x] env/flag surface inventoried from `cmd/pinakes/main.go`, `pkg/httpapi/server.go`, `pkg/bus/store.go`
- [x] current store-selection precedence documented
- [x] current runtime defaults documented
- [x] contract tests pin allowlist-gated behavior
- [x] contract tests pin health/system-status payload shapes

## Endpoints

### Agent lifecycle

- `POST /v1/agents/register`
  - source: `handleRegisterAgent`
  - body: `agent_id`, `allowed_scopes`, `shared_grants`, `capabilities`, `version`, `description`, `agent_class`, `mutation_class`, `build`, `meta`, `mode`, `callback_url`, `ttl`, `secret`
  - response: `ok`, `agent_id`, `expires_at`
- `GET /v1/agents`
  - source: `handleListAgents`
  - optional query: `capability`
  - response: `agents` (agent objects include passport fields when present)

### Conversations

- `POST /v1/conversations`
  - source: `handleConversations`
  - body: `conversation_id`, `title`, `participants`, `meta`
  - auth: `X-Agent-ID` + `X-Bus-Signature` over raw JSON body using the creator agent secret, or `Authorization: Bearer <token>` from `INJECT_TOKENS`
  - response: `ok`, `conversation_id`
- `GET /v1/conversations`
  - source: `handleConversations`
  - optional query: `participant`, `status`
  - auth: observe-equivalent auth: `Authorization: Bearer <token>` from `OBSERVE_TOKENS`, or `X-Agent-ID` + `X-Bus-Signature` over the exact raw query string
  - response: `conversations`
- `GET /v1/conversations/{conversation_id}/messages`
  - source: `handleConversationMessages`
  - query: `cursor`, `limit`
  - auth: observe-equivalent auth: `Authorization: Bearer <token>` from `OBSERVE_TOKENS`, or `X-Agent-ID` + `X-Bus-Signature` over the exact raw query string
  - response: `conversation_id`, `messages`, `cursor`

### Messaging

- `POST /v1/messages`
  - source: `handleMessages`
  - body: `to`, `from`, `conversation_id`, `request_id`, `type`, `body`, `meta`, `attachments`, `ttl`, `in_reply_to`
  - auth: `X-Bus-Signature` over raw JSON body using sender secret
  - response: `ok`, `message_id`, `duplicate`
- `GET /v1/inbox`
  - source: `handleInbox`
  - query: `agent_id`, `cursor`, `wait`
  - auth: `X-Bus-Signature` over raw query string using target agent secret
  - response: `events`, `cursor`
- `POST /v1/acks`
  - source: `handleAcks`
  - body: `agent_id`, `message_id`, `status`, `reason`
  - auth: `X-Bus-Signature` over raw JSON body using target agent secret
  - response: `ok`
  - current `status` values:
    - `accepted` — target agent accepted the request and moved it into execution
    - `rejected` — target agent refused the request
  - the current bus implementation rejects other `status` values with validation error
  - `reason` is accepted as optional opaque text; the current bus does not validate or surface it
  - sender expectations: senders SHOULD NOT blindly retry on `rejected`. Silent drops (no ack at all) violate the agent citizenship contract.
- `POST /v1/events`
  - source: `handleEvents`
  - body: `message_id`, `type`, `body`, `meta`
  - headers: `X-Agent-ID`, `X-Bus-Signature`
  - auth: signature over raw JSON body using actor agent secret
  - allowed event types: `progress`, `final`, `error`
  - response: `ok`

### Observation / manual injection

- `GET /v1/observe`
  - source: `handleObserve`
  - query: optional `cursor`, `conversation_id`, `agent_id`
  - auth: `Authorization: Bearer <token>` from `OBSERVE_TOKENS`, or `?token=<token>` from `OBSERVE_TOKENS` as an SSE fallback, or `X-Agent-ID` + `X-Bus-Signature` over the exact raw query string
  - header fallback for cursor: `Last-Event-ID`
  - response: SSE stream
- `POST /v1/inject`
  - source: `handleInject`
  - body: `identity`, `conversation_id`, `to`, `body`
  - auth: `Authorization: Bearer <token>` from `INJECT_TOKENS`, then `HUMAN_ALLOWLIST` if configured
  - response: `ok`, `message_id`

### Health / status

- `GET /health`
  - source: `handleTopLevelHealth`
  - response shape:
    - matches `GET /v1/health`
- `GET /metrics`
  - source: `handleMetrics`
  - response shape:
    - Prometheus text exposition
    - low-cardinality runtime gauges/counters only
- `GET /v1/health`
  - source: `handleHealth`
  - response shape:
    - `ok`
    - `status`
    - `agents`
    - `observe`
    - `push.successes`
    - `push.failures`
- `GET /v1/system/status`
  - source: `handleSystemStatus`
  - response shape:
    - `ok`
    - `system.agents_active`
    - `system.agents_expired`
    - `system.conversations`
    - `system.messages`
    - `system.observe_events`
    - `system.push_successes`
    - `system.push_failures`

## Auth Rules

- Agent registration requires a non-empty `secret`.
- Agent IDs are also queue names and are moving to `personal.`, `ucla.`, or `shared.` prefixes. During the default `BUS_NAMESPACE_MODE=compat` migration window, legacy unprefixed IDs are accepted and assigned `BUS_LEGACY_SCOPE`. In `BUS_NAMESPACE_MODE=strict`, unprefixed IDs are rejected.
- Agent registration tolerates `allowed_scopes` (`personal`, `ucla`, `shared`) and `shared_grants` (`shared`) claims for compatibility and validation, but callers cannot define their own policy. Effective scopes are assigned server-side from the namespace prefix on `agent_id`.
- Effective `shared.*` access is assigned server-side through `SHARED_GRANT_AGENTS`; registration-body `shared_grants` claims do not grant access.
- Publishing to `personal.*` or `ucla.*` requires that scope in the sender identity's `allowed_scopes`.
- Publishing to or subscribing as `shared.*` requires an explicit `shared_grants: ["shared"]`; `allowed_scopes: ["shared"]` alone is not sufficient.
- Scope denials are logged with action, identity, resource, and reason.
- Message `from` and `to`, inbox `agent_id`, and conversation participants follow the same namespace migration behavior: legacy unprefixed IDs are accepted in compat mode and rejected in strict mode.
- Agent registration is gated by `ALLOWLIST_FILE` if set, otherwise `AGENT_ALLOWLIST`.
- Removing an agent from the allowlist blocks future registration only; it does not evict already-registered agents mid-session.
- Agent registration secrets are persisted in the active durable agent store so a bus restart does not force re-registration before signed endpoints work.
- Re-registration with the same secret is idempotent and may update passport metadata.
- Re-registration with a different secret requires `X-Bus-Signature` over the raw registration body using the current stored secret. Missing or invalid proof returns `409 Conflict` with `re-registration requires proof of current secret`.
- Legacy rows with no stored secret may establish one secret after the allowlist check.
- Message send auth uses the `from` agent secret.
- Inbox poll auth uses the exact raw query string.
- Ack auth uses the `agent_id` secret.
- Event auth uses `X-Agent-ID` + that agent's secret.
- Conversation creation requires agent HMAC over the raw body or a valid inject token.
- Conversation listing and conversation message history require observe-equivalent auth because conversation metadata and message bodies can contain private operational data.
- Observe requires an observe token (header preferred, `?token=` fallback for SSE clients) or agent HMAC over the exact raw query string.
- Human inject requires a valid inject token and is then gated by `HUMAN_ALLOWLIST` if set.
- `INJECT_TOKENS` or `OBSERVE_TOKENS` unset means token auth fails closed for those token paths.

## Secret Persistence And Recovery

- SQLite stores the raw HMAC secret in `agents.secret`.
- The JSON persistent backend stores raw HMAC secrets in `agent_secrets`.
- Secrets are never included in `GET /v1/agents` responses or status/debug output.
- Operator recovery for a genuinely lost agent secret is manual store repair: delete that agent row from SQLite (`DELETE FROM agents WHERE agent_id=?`) or remove the agent and secret entries from the JSON state file, then let the agent re-register.

## Runtime Config Surface

### Flags

- `--db`
  - path to SQLite db file
  - overrides `DB_PATH`

### Environment variables

- `PORT`
  - listen port for bus HTTP server
  - default: `8080`
- `DB_PATH`
  - if set and `--db` unset, use SQLite store at this path
- `STORE_BACKEND`
  - used only when neither `--db` nor `DB_PATH` is set
  - supported current values:
    - `sqlite` => SQLite store at `./data/bus.db`
    - `persistent` / `json` => legacy persistent JSON-file backend at `STATE_FILE`
    - `memory`
    - any other value => legacy persistent JSON-file backend (backward compatibility)
  - default when unset: `sqlite`
- `STATE_FILE`
  - path for the legacy persistent JSON-file backend, and the migration source when the SQLite backend boots for the first time (see "Migration from the JSON backend")
  - default: `./data/state.json`
- `ALLOWLIST_FILE`
  - path to newline-delimited agent allowlist file
  - if set, takes precedence over `AGENT_ALLOWLIST`
  - startup fails if the configured file cannot be read
  - runtime reload watches the parent directory and keeps the last-good allowset on reload failure
- `AGENT_ALLOWLIST`
  - comma-separated allowed `agent_id` values for registration
  - used only when `ALLOWLIST_FILE` is unset
  - empty/unset means allow all
- `HUMAN_ALLOWLIST`
  - comma-separated allowed human identities for `/v1/inject`
  - evaluated after a valid `INJECT_TOKENS` bearer token
  - empty/unset means any identity may inject if the token is valid
- `INJECT_TOKENS`
  - comma-separated bearer tokens for `/v1/inject` and human-created `POST /v1/conversations`
  - empty/unset means token-authenticated inject and human conversation creation fail closed
- `OBSERVE_TOKENS`
  - comma-separated bearer tokens for `/v1/observe`
  - `Authorization: Bearer <token>` is preferred; `?token=<token>` exists only for SSE clients that cannot set headers
  - empty/unset means token-authenticated observe fails closed; agent HMAC observe remains available
- `BUS_NAMESPACE_MODE`
  - `compat` or `strict`
  - default: `compat`
  - `compat` accepts legacy unprefixed IDs and assigns them `BUS_LEGACY_SCOPE`
  - `strict` rejects unprefixed IDs; intended for the final cutover after downstream agents and allowlists migrate
- `BUS_LEGACY_SCOPE`
  - scope assigned to unprefixed IDs while `BUS_NAMESPACE_MODE=compat`
  - supported values: `personal`, `ucla`
  - default: `ucla`
- `SHARED_GRANT_AGENTS`
  - comma-separated agent IDs that receive explicit `shared.*` access
  - empty/unset means no identity receives `shared.*` access from registration claims alone
- `MAX_BODY_BYTES`
  - maximum request body size in bytes for all POST endpoints
  - default: `2097152` (2 MiB); `0` or negative disables the cap
  - oversized bodies return `413` with error code `payload_too_large`
- `MESSAGE_RETENTION_SECONDS`
  - prune terminal (completed/rejected/error) messages this long after they reach a terminal state
  - default: `3600` (1h); `-1` disables
- `MESSAGE_MAX_AGE_SECONDS`
  - prune any message this long after creation regardless of state (backstop for stuck/legacy messages)
  - default: `86400` (24h); `-1` disables
- `CONVERSATION_RETENTION_SECONDS`
  - prune conversations idle this long once all their messages are pruned
  - default: `86400` (24h); `-1` disables
- `AGENT_RETENTION_SECONDS`
  - prune expired agents (and their inboxes) this long after registration expiry; returning agents re-register normally
  - default: `86400` (24h); `-1` disables
- `MAX_INBOX_BYTES_PER_AGENT`
  - approximate retained payload byte budget per agent inbox; oldest events evicted first, newest always kept
  - default: `33554432` (32 MiB); `-1` disables
- `MAX_OBSERVE_BYTES`
  - approximate retained payload byte budget for the observe event ring; oldest events evicted first, newest always kept
  - default: `67108864` (64 MiB); `-1` disables

### Store selection order

1. `--db` => SQLite at that path
2. `DB_PATH` => SQLite at that path
3. `STORE_BACKEND=memory` => in-memory store
4. `STORE_BACKEND=persistent` / `json` (or any other unrecognized value) => persistent JSON-file backend using `STATE_FILE`
5. default (`STORE_BACKEND` unset or `sqlite`) => SQLite at `./data/bus.db`

### Migration from the JSON backend

- Runs once at startup when all three hold: the resolved backend is SQLite, the SQLite db file does not exist yet, and the legacy `STATE_FILE` exists.
- Imports agents (with registration metadata and secrets), conversations, messages (including terminal/lifecycle timestamps), conversation message ordering, and id counters.
- Inbox buffers, observe events, and idempotency entries are transient and dropped once at migration: undelivered inbox events are lost, and affected in-flight requests will ack-timeout to `error`.
- On success the state file is renamed to `state.json.migrated` and kept as a backup; it is never deleted.
- The import writes into `<db>.tmp` and atomically renames it to the final db path only after the import transaction commits, so the final db file is never partially written. On any import error the bus fails startup loudly instead of booting an empty store; temp artifacts are removed (on error immediately, on crash at the next boot) so the next boot retries the migration.

## Runtime Defaults

These values are currently hard-coded in [main.go](/home/joelkehle/Projects/shared/pinakes/cmd/pinakes/main.go) and mirrored as fallback defaults in [store.go](/home/joelkehle/Projects/shared/pinakes/pkg/bus/store.go).

- `GracePeriod = 30s`
- `ProgressMinInterval = 2s`
- `IdempotencyWindow = 24h`
- `InboxWaitMax = 60s`
- `AckTimeout = 10s`
- `DefaultMessageTTL = 600s`
- `DefaultRegistrationTTL = 60s`
- `PushMaxAttempts = 3`
- `PushBaseBackoff = 500ms`
- `PushQueueSize = 256` — capacity of the bounded push-callback queue drained by the worker pool; when full, new push deliveries are dropped (logged, counted in `push_failures`) instead of blocking the API path or spawning unbounded goroutines.
- `PushWorkers = 4` — fixed pool of worker goroutines draining the push queue.
- `MaxInboxEventsPerAgent = 10000`
- `MaxObserveEvents = 50000`
- `SweepMinInterval = 250ms` — minimum gap between full sweep passes. The bus skips redundant sweeps inside this window so long-poll cycles do not re-walk hundreds of thousands of retained messages on every wake. The first sweep after process start always runs; agent expiry, TTL expiry, and ack-timeout transitions land within one `SweepMinInterval` of their deadline.
- `MessageRetention = 1h`, `MessageMaxAge = 24h`, `ConversationRetention = 24h`, `AgentRetention = 24h`, `MaxInboxBytesPerAgent = 32MiB`, `MaxObserveBytes = 64MiB`, `MaxBodyBytes = 2MiB` — memory-reclamation knobs, env-overridable (see Environment variables above).

Important current behavior:

- the non-retention tunables are not externally configurable via env vars today
- extraction should preserve them unless a deliberate compatibility change is called out
- on SIGINT/SIGTERM the bus stops accepting connections, waits up to 10s for in-flight requests to drain, then exits 0; long-polling and SSE clients should expect dropped connections at shutdown and retry

## Retention And Memory Reclamation

The bus is in-memory at runtime; without eviction its memory grows without bound and the container gets OOM-killed (observed 2026-06-09/10). Reclamation works on four levers, all sweep-driven:

- **Terminal messages** are pruned `MessageRetention` after reaching a terminal state. `terminal_at` is stamped on the message (additive response field) and persisted in both durable backends. Messages from pre-retention state files (no `terminal_at`) fall back to `created_at` and drain promptly after upgrade.
- **Any message** is pruned `MessageMaxAge` after creation regardless of state — backstop for stuck or legacy-loaded messages.
- **Conversations** are pruned once idle past `ConversationRetention` with no live messages. `GET /v1/conversations/{id}/messages` only ever returns retained messages.
- **Expired agents** and their inboxes are pruned `AgentRetention` after registration expiry. A pruned agent simply re-registers (its `registered_at` resets).

Inbox and observe buffers are additionally bounded by byte budgets (`MaxInboxBytesPerAgent`, `MaxObserveBytes`), evicting oldest-first but never the newest event. Counts alone do not bound memory when individual payloads are large.

Inbox poll-time reclamation: cursor values originate from prior poll responses, so a poll at cursor `C` proves the agent received every event below `C`; the bus frees those events immediately. Clients must not rely on re-reading inbox events below their last-acknowledged cursor (this was already unreliable under the count cap).

Idempotency caveat: replaying a `request_id` after the original message has been pruned (i.e. more than `MessageRetention` after completion) creates a new message instead of returning the duplicate. The previous behavior held the duplicate for the full 24h `IdempotencyWindow`; retries on that timescale are not a supported pattern.

The SQLite backend mirrors retention into the database: rows past retention are deleted at startup (before load, so a bloated DB cannot re-inflate memory) and every 10 minutes thereafter. The JSON backend rewrites the full pruned state on mutations.

## Passport Extensions

The following additive fields shipped in `pinakes v0.2.0` to support the agent citizenship contract (`AGENT_CITIZENSHIP.md`). Existing callers still work without them.

### Registration extensions

New fields on `POST /v1/agents/register`:

| Field | Required | Type | Purpose |
|-------|----------|------|---------|
| `version` | yes* | string | Agent version (semver or commit-based). |
| `agent_class` | yes* | string | `worker` or `orchestrator`. |
| `mutation_class` | yes* | string | Legacy wire field for safety class. Human-facing tools should render it as `read`, `propose`, or `write`. |
| `build` | no | object | `{ "commit": string, "dirty": bool }` |
| `meta` | no | object | `{ "owner": string, "repo": string, "health_url": string, "dependencies": [string] }` |

*Required by citizenship contract. The `v0.2.0+` bus line still accepts legacy registration for backward compatibility with pre-passport callers. See `AGENT_CITIZENSHIP.md` for semantics.

Safety-class display mapping: `observe` -> `read`, `recommend` -> `propose`, `mutate` -> `write`.

### Response extensions

- `GET /v1/agents` response: each agent object gains `version`, `agent_class`, `mutation_class`, and `meta` fields when present.
- `GET /v1/system/status` response: no structural change, but agent counts may be enriched with version/class breakdowns in a future iteration.

### Compatibility

- `pinakes v0.2.0` is the passport-capable baseline for downstream adoption.
- Existing agents that register without new fields continue to work. No breaking change.
- New fields are additive to the registration payload and response shapes.
- The bus stores and echoes new fields but does not enforce their semantics beyond enum validation for `agent_class` and the legacy safety-class wire field.

### `meta.health_url` operational convention

- `meta.health_url` is optional agent metadata on the bus registry.
- It MAY be reachable only from the shared agent network. Host reachability is not required.
- Consumers MUST NOT assume `meta.health_url` is a host-routable probe target.
- Host-side promotion and verification tooling should use repo-local manifest `health_url` values as the authoritative probe addresses.
- Bus `meta.health_url` remains useful for registry display, in-network inspection, and temporary migration fallback where manifest coverage is incomplete.
- If a manifest `health_url` and bus `meta.health_url` both exist, the manifest address is authoritative for host-run operations; the bus address is authoritative only for the agent's runtime-network self-description.

## Contract Owners

- canonical protocol contract tests live in [contract_test.go](/home/joelkehle/Projects/shared/pinakes/pkg/httpapi/contract_test.go)
- this repo is now the upstream home for those tests
- product-level integration tests should stay outside the canonical protocol suite
