---
summary: Frozen HTTP contract and runtime config surface for the current bus implementation in pinakes.
read_when:
  - extracting the bus into pinakes
  - validating contract compatibility during repo split
  - checking runtime env/config behavior for the current bus
---

# Bus HTTP Contract

Last updated: 2026-03-17

Purpose: preserve the extracted bus surface in `pinakes`.

This doc describes the extracted bus contract as implemented by:

- [main.go](/home/joelkehle/Projects/pinakes/cmd/pinakes/main.go)
- [server.go](/home/joelkehle/Projects/pinakes/pkg/httpapi/server.go)
- [store.go](/home/joelkehle/Projects/pinakes/pkg/bus/store.go)
- [contract_test.go](/home/joelkehle/Projects/pinakes/pkg/httpapi/contract_test.go)

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
  - body: `agent_id`, `capabilities`, `description`, `mode`, `callback_url`, `ttl`, `secret`
  - response: `ok`, `agent_id`, `expires_at`
- `GET /v1/agents`
  - source: `handleListAgents`
  - optional query: `capability`
  - response: `agents`

### Conversations

- `POST /v1/conversations`
  - source: `handleConversations`
  - body: `conversation_id`, `title`, `participants`, `meta`
  - response: `ok`, `conversation_id`
- `GET /v1/conversations`
  - source: `handleConversations`
  - optional query: `participant`, `status`
  - response: `conversations`
- `GET /v1/conversations/{conversation_id}/messages`
  - source: `handleConversationMessages`
  - query: `cursor`, `limit`
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
  - header fallback for cursor: `Last-Event-ID`
  - response: SSE stream
- `POST /v1/inject`
  - source: `handleInject`
  - body: `identity`, `conversation_id`, `to`, `body`
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
- Agent registration is gated by `ALLOWLIST_FILE` if set, otherwise `AGENT_ALLOWLIST`.
- Removing an agent from the allowlist blocks future registration only; it does not evict already-registered agents mid-session.
- Message send auth uses the `from` agent secret.
- Inbox poll auth uses the exact raw query string.
- Ack auth uses the `agent_id` secret.
- Event auth uses `X-Agent-ID` + that agent's secret.
- Human inject is gated by `HUMAN_ALLOWLIST` if set.

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
    - `memory`
    - any other value => persistent JSON-file backend
  - default when unset: `persistent`
- `STATE_FILE`
  - path for persistent JSON-file backend
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
  - empty/unset means allow all

### Store selection order

1. `--db`
2. `DB_PATH`
3. `STORE_BACKEND=memory`
4. persistent JSON-file backend using `STATE_FILE`

## Runtime Defaults

These values are currently hard-coded in [main.go](/home/joelkehle/Projects/pinakes/cmd/pinakes/main.go) and mirrored as fallback defaults in [store.go](/home/joelkehle/Projects/pinakes/pkg/bus/store.go).

- `GracePeriod = 30s`
- `ProgressMinInterval = 2s`
- `IdempotencyWindow = 24h`
- `InboxWaitMax = 60s`
- `AckTimeout = 10s`
- `DefaultMessageTTL = 600s`
- `DefaultRegistrationTTL = 60s`
- `PushMaxAttempts = 3`
- `PushBaseBackoff = 500ms`
- `MaxInboxEventsPerAgent = 10000`
- `MaxObserveEvents = 50000`

Important current behavior:

- these tunables are not externally configurable via env vars today
- extraction should preserve them unless a deliberate compatibility change is called out

## Contract Owners

- canonical protocol contract tests live in [contract_test.go](/home/joelkehle/Projects/pinakes/pkg/httpapi/contract_test.go)
- this repo is now the upstream home for those tests
- product-level integration tests should stay outside the canonical protocol suite
