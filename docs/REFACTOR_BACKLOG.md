---
summary: "Prioritized refactor backlog from the 2026-06-10 architecture review: items deferred after retention/byte-budget/body-cap, sqlite-default, push worker pool, and graceful shutdown shipped. Each item has a trigger condition for picking it up."
read_when:
  - planning the next pinakes maintenance or refactor session
  - a trigger condition below fires (long-lived conversation growth, sqlite working set, push-mode rollout, etc.)
  - deciding whether to retire the JSON persistent backend
status: backlog (review 2026-06-10)
---

# Refactor Backlog

Items deferred from the 2026-06-10 architecture review. None block current operation; each lists the trigger that promotes it to active work. Ordered by expected pick-up order, not severity.

| # | What | Trigger | Size |
|---|------|---------|------|
| 1 | Split `pkg/bus/store.go` into store/sweep/push/observe files | Next time the file is touched | S |
| 2 | Conversation message-id index compaction | Conversation >100k messages | M |
| 3 | Query-on-demand SQLite reads | Retention lengthened or working set >~500MB | L |
| 4 | Retire JSON `PersistentStore` backend | One clean month on sqlite, both buses | S |
| 5 | Push callback jitter + per-target circuit breaker | Push-mode agents in production | M |
| 6 | Deploy-side: GOMEMLIMIT, stop_grace_period, tag bump | Next deploy of ucla-tdg-ip-agents stack | S |
| 7 | Secret hygiene: encrypt secrets at rest | Security review or compliance ask | M (needs design) |
| 8 | Extract testable `run()`/config package from `cmd/pinakes` | Shutdown + backend-selection logic settles | S |

## 1. Split `pkg/bus/store.go`

- **What**: `pkg/bus/store.go` is ~1570 LOC; repo rule is <~500 LOC per file. Split into store core, sweep/retention, push delivery, and observe files within `pkg/bus`. Pure mechanical move, no behavior change.
- **Why**: File-size rule; easier review of retention and push changes.
- **Trigger**: Opportunistic — do it the next time store.go needs nontrivial edits.

## 2. Conversation message-id index compaction

- **What**: Long-lived conversations keep pruned-message ID strings in `conversationMessages` until the conversation itself is pruned (~25 bytes per message). Add an `inboxBase`-style base offset so pruned prefix entries can be dropped while preserving cursor semantics.
- **Why**: Unbounded slow growth for conversations that outlive their messages.
- **Trigger**: Any conversation accumulating >100k messages.

## 3. Query-on-demand SQLite reads

- **What**: `SQLiteStore` loads all rows into the embedded in-memory `Store` at startup and serves reads from memory. Move hot reads to SQL queries so the working set need not fit in RAM.
- **Why**: Fine today because retention bounds the working set (24h max age); breaks down if retention is relaxed.
- **Trigger**: Retention windows lengthened, or working set outgrows ~500MB.

## 4. Retire the JSON `PersistentStore` backend

- **What**: Remove `pkg/bus/persistent.go` (full-state JSON rewrite on every mutation), kept only as legacy `STORE_BACKEND=persistent` after sqlite became the default.
- **Why**: Write amplification, no longer the default, two durable backends to maintain.
- **Trigger**: One clean month on sqlite in both deployed buses. Removal is contract-adjacent (env var disappears): update `docs/BUS_HTTP_CONTRACT.md` if it documents backend selection, and README.

## 5. Push callback robustness

- **What**: Add jitter to push retry backoff and a per-target circuit breaker so a dead callback URL doesn't consume worker-pool slots for 3x10s on every message.
- **Why**: A single dead push-mode agent can starve delivery throughput for healthy targets.
- **Trigger**: Push-mode agents in production (current fleet is mostly pull).

## 6. Deploy-side hardening (not this repo)

- **What**: In `ucla-tdg-ip-agents/deploy/docker-compose.yml`: set `GOMEMLIMIT` ~1536MiB on the bus container, set `stop_grace_period: >=15s` so graceful shutdown completes, bump `PINAKES_TAG`.
- **Why**: Memory headroom and clean shutdown for the new drain logic.
- **Trigger**: Next deploy of that stack. Cross-reference: `docs/BUS_STABILITY_SPEC.md` Fix 2 (separate bus compose stack) remains open.

## 7. Secret hygiene

- **What**: `Server.agentSecrets` is held in plaintext in process memory and in both durable stores. Hashing at rest doesn't work — HMAC verification requires the raw secret — so the likely shape is encrypt-at-rest with a key from env/file. Needs a design pass; touching the storage format is a breaking change for existing state files.
- **Why**: A leaked state file or DB leaks every agent secret.
- **Trigger**: Security review finding or compliance requirement; no operational pressure today.

## 8. Testable `cmd/pinakes`

- **What**: `cmd/pinakes` has no tests. Extract `run()` and config/backend-selection parsing into a testable package.
- **Why**: Backend selection (`--db` > `DB_PATH` > `STORE_BACKEND`) and shutdown ordering are currently only exercised manually.
- **Trigger**: Once the shutdown + backend-selection logic settles (post sqlite-default soak).
