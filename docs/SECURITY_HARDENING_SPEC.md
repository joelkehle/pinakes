---
summary: "Implementation spec for closing the 2026-06-09 fleet-audit findings against the bus: secret persistence, re-registration proof-of-possession, auth on inject/observe/conversation-create."
read_when:
  - Implementing or reviewing bus auth/security changes.
  - Investigating agent 401s after a bus restart.
status: approved-for-implementation (Fable spec 2026-06-10; Codex executes; Fable reviews diff)
---

# Pinakes Security Hardening Spec (v1)

Source findings: fleet audit 2026-06-09 (`http://beelink:8091/codex-output/fleet-audit-20260609T224608Z/`).
Investigated against code 2026-06-10; line refs below verified then.

## Goals

1. Bus restart must not invalidate agent auth (persist secrets).
2. A network peer must not be able to hijack an allowlisted agent identity
   by re-registering it (proof-of-possession).
3. No unauthenticated write or firehose-read endpoints (`/v1/inject`,
   `/v1/observe`, `POST /v1/conversations`).

Non-goals (v1): TLS/mTLS, per-agent ACLs/scopes, secret encryption at rest,
consumer-repo migrations beyond the two bus compose files, image publish/deploy
(separate gated step — see Deployment Checklist).

## Current facts (verified)

- Secrets live only in `Server.agentSecrets map[string]string`
  (`pkg/httpapi/server.go:31`), populated by `setAgentSecret` (`server.go:397`),
  read by `verifySignature` (`server.go:294-325`). Lost on restart; all signed
  endpoints 401 until each agent's 60s heartbeat re-registers it.
- `handleRegisterAgent` (`server.go:343-404`) checks only: non-empty secret,
  agent id in allowlist. A re-register for an existing id silently overwrites
  metadata (`pkg/bus/store.go:508-510`) and the secret. This is the hijack.
- Open endpoints today: `/v1/agents/register`, `GET /v1/agents`,
  `POST+GET /v1/conversations`, `GET /v1/conversations/{id}/messages`,
  `GET /v1/observe` (full SSE firehose incl. message bodies, `server.go:659-729`),
  `POST /v1/inject` (`server.go:731-761`, `HUMAN_ALLOWLIST` defaults allow-all).
- HMAC-required endpoints: `/v1/messages`, `/v1/inbox`, `/v1/acks`, `/v1/events`.
- Storage backends (`cmd/pinakes/main.go:39-73`): SQLite (`pkg/bus/sqlite.go`,
  schema lines 32-92 — no secret column), JSON (`pkg/bus/persistent.go:11-25` —
  no secrets field), pure memory. Both prod deployments mount persistent volumes
  already (`ucla-tdg-ip-agents/deploy/docker-compose.yml`,
  `jk-email-agents/docker-compose.yml`).

## Design decisions (do not relitigate in implementation)

- **Secrets are stored raw, not hashed.** HMAC-SHA256 verification requires the
  raw shared secret. Protection = file permissions on the store + Tailscale-only
  exposure. Encryption-at-rest is a later phase.
- **Fail closed.** `/v1/inject` and `/v1/observe` with no token configured
  return 403, not open. This is a deliberate breaking change; compose files are
  updated in the same change set.
- **Idempotent re-registration stays cheap.** Agents heartbeat-re-register every
  60s; re-register with the *same* secret must keep working with no extra
  ceremony.
- **State doctrine:** the agent secret is identity state owned by the bus's
  existing agents store (extension of an existing store, not a new store).
  Update `~/Projects/shared/agent-scripts/docs/STATE_ARCHITECTURE.md` in the
  same commit that adds the persistence (one line under the pinakes/transport
  entry noting the bus owns agent registration secrets).

## Phase 1 — Secret persistence

- SQLite backend: add `secret TEXT NOT NULL DEFAULT ''` to the `agents` table.
  Schema uses `CREATE TABLE IF NOT EXISTS`, so existing DBs need a guarded
  migration (check `pragma table_info`, `ALTER TABLE` if column missing).
- JSON backend: add `AgentSecrets map[string]string` to `persistentState`
  (`persistent.go:11-25`).
- Server startup: load persisted secrets into `agentSecrets`;
  `setAgentSecret` writes through to the store.
- Memory backend: unchanged (secrets die with process — fine for tests).
- Hygiene: never log secrets; verify `GET /v1/agents` and any debug/status
  output cannot leak the secret field; SQLite file already lives in a Docker
  volume — no perms change needed, but confirm the JSON state file is 0600.

Tests (in `pkg/httpapi/contract_test.go` + `pkg/bus/sqlite_test.go` /
`persistent_test.go`):
- Register agent → restart server against same store → signed `/v1/messages`
  succeeds WITHOUT re-registration (both SQLite and JSON backends).
- Migration test: open a pre-existing SQLite DB without the column → migrated.
- `GET /v1/agents` response contains no secret material.

## Phase 2 — Re-registration proof-of-possession

Rules for `POST /v1/agents/register` for agent id A with offered secret S:

| Bus state for A | Condition | Result |
|---|---|---|
| unknown | A in allowlist | accept, store S (current behavior) |
| known, stored secret == S | — | accept (idempotent heartbeat path) |
| known, stored secret != S | request carries valid `X-Bus-Signature` over body using the CURRENT stored secret | accept, rotate to S |
| known, stored secret != S | no/invalid signature | reject `409 Conflict` (body: re-registration requires proof of current secret) |
| known, stored secret == "" (legacy pre-persistence row) | A in allowlist | accept, store S (one-time migration grace) |

Operator recovery for a genuinely lost secret: documented runbook step =
delete the agent row from the store (SQLite `DELETE FROM agents WHERE id=?` /
JSON edit) and let the agent re-register. Add this to the contract doc; no API
for it in v1.

Tests:
- Hijack regression: register A, attempt re-register with different secret and
  no signature → 409, original secret still verifies.
- Rotation: re-register with new secret signed by old secret → new secret
  verifies, old one rejected.
- Idempotent path unaffected.
- Update `TestContractPassportReregistrationUpdatesFields` (contract_test.go:361)
  to sign its re-registration.

## Phase 3 — Close open endpoints

- `POST /v1/inject`: require header `Authorization: Bearer <token>` where token
  ∈ `INJECT_TOKENS` (comma-separated env). Keep `HUMAN_ALLOWLIST` check, but it
  is no longer the only gate. No tokens configured → 403 always.
- `GET /v1/observe`: accept EITHER (a) `Authorization: Bearer <token>` with
  token ∈ `OBSERVE_TOKENS`, or (b) agent HMAC: `X-Agent-ID` +
  `X-Bus-Signature` over the raw query string (same pattern as `/v1/inbox`,
  `server.go:549`). SSE consumers that can't set headers may pass
  `?token=<token>` as a fallback — support it, document it as the weaker option.
- `POST /v1/conversations`: require agent HMAC (creator agent id + signature
  over body) OR a valid inject token (human-created conversations). Trace how
  the Go client SDK in this repo creates conversations and update it to sign.
  `GET /v1/agents`, `GET /v1/conversations*`, `/v1/health`, `/metrics`,
  `/v1/system/status` stay open in v1 (read-only, pending fleet-wide policy
  decision in `manager/docs/decisions/endpoint-auth-policy-2026-06.md`).
- Grep `~/Projects` for consumers of `/v1/observe` and `/v1/inject`
  (expected: dev-dashboard, ops pages, email-operator, operator UIs). Do NOT
  modify those repos; list every consumer + required change in the final report.

Compose wiring (apply in the same change set — these repos are local):
- `~/Projects/ucla-tdg/ucla-tdg-ip-agents/deploy/docker-compose.yml` and
  `~/Projects/jk/jk-email-agents/docker-compose.yml`: pass
  `INJECT_TOKENS: ${PINAKES_INJECT_TOKEN:?}` and
  `OBSERVE_TOKENS: ${PINAKES_OBSERVE_TOKEN:?}` on the bus service.
- Generate values with `openssl rand -hex 32` and append to each repo's
  gitignored `.env` (verify gitignored first). Note them in the report as
  "set, not displayed".

Tests: inject/observe/conversation-create each: no auth → 403; valid token →
200; (observe) valid agent HMAC → 200; no tokens configured → 403.

## Docs to update (same change set)

- `docs/BUS_HTTP_CONTRACT.md`: auth rules section (135-144) — re-registration
  proof-of-possession + rotation; endpoint section — inject/observe/conversation
  auth; new section on secret persistence + lost-secret runbook; runtime config
  (INJECT_TOKENS / OBSERVE_TOKENS).
- `docs/BUS_STABILITY_SPEC.md`: restart-forces-re-register root cause is fixed
  by Phase 1 — update.
- `~/Projects/shared/agent-scripts/docs/STATE_ARCHITECTURE.md`: one-line
  ownership note (same commit as Phase 1).

## Validation gate

`go test ./...` green after every phase. One conventional commit per phase via
`committer` (`feat(security): ...` / `fix(security): ...`), local only — DO NOT
PUSH. Keep each file <~500 LOC; split if a handler file grows past that.

## Deployment checklist (NOT executed in this task — gated on Joel)

1. Build + tag new image, push to ghcr, bump `PINAKES_TAG` in both compose files.
2. Restart ucla-tdg bus, then jk bus; agents re-register via heartbeat ≤60s.
3. Verify: signed message round-trip per bus; unauthenticated inject/observe → 403.
4. Update consumers of observe/inject per the report, then re-verify ops pages.

## Report

Write an HTML proof pack to
`~/Projects/shared/dev-dashboard/codex-output/pinakes/security-hardening-<YYYYMMDDThhmmssZ>/index.html`
(per AGENTS.MD): what changed per phase, test results, consumer inventory for
observe/inject, deployment checklist status.
