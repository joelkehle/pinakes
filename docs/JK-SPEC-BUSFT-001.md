# JK-SPEC-BUSFT-001 — Fault-Tolerant Pinakes Bus

- ID: JK-SPEC-BUSFT-001
- Version: v0.1
- Status: Draft for review (implementation candidate: Ludi Zhou)
- Author: Claude (drafted), Joel Kehle (owner)
- Date: 2026-07-08
- Related: JK-SPEC-FAULTTOL-001 (layers 2, 8), JK-SPEC-INTERNPM-001 (worker model), JK-SPEC-KEYMASTER-001 (future bus consumer)

## 1. Motivation

The Pinakes bus is the coordination fabric for Joel's agent ecosystem — IP Agency workers, intern PM, wwi, and (future) the keymaster all depend on it. Today it runs solely on the Beelink, which has been dark for 8+ days: every bus-dependent workflow has been down with it. The bus is small, stateful-but-lightly, and must-be-up — the exact profile FAULTTOL assigns to keystone.

Decision already made (2026-07-08 discussion): ONE bus serving both personal and UCLA agent networks, not two. Separation is enforced by identity + scope policy, not by physical duplication. However, a future institutional extraction (UCLA owning its agent infrastructure) must remain cheap — so scope separation must be structural (namespacing), not cosmetic.

## 2. Constraints and principles

- B1. One bus, two scopes. Every topic, queue, and identity carries a scope (`personal` | `ucla` | `shared`). Cross-scope traffic is explicit, never default.
- B2. Extraction seam. "Split the bus" must be an extraction, not a rewrite: scope-prefixed namespaces, per-scope config blocks, no personal/ucla data interleaved in a single stream or table.
- B3. Must-be-up tier. The bus moves to keystone (FAULTTOL layers 1–2). The Beelink becomes a bus client, never again the bus host.
- B4. n=1 honesty (inherited FAULTTOL C6). No clustered brokers, no Raft, no Kafka. The bus is a small Go service; its fault tolerance comes from placement (keystone), state replication (Litestream), and reprovisionability (config-as-code) — not from distributed-systems machinery.
- B5. Degrade, don't die. Bus outage must not corrupt work: clients retry with backoff; messages that matter are persisted; at-least-once delivery with idempotent consumers is the contract.
- B6. Intern-implementable. The work is packaged so a competent intern with agent assistance can build it without holding personal-scope credentials or personal-scope data access at any point.

## 3. Requirements

Transport & persistence
- BF-1 The bus service runs on keystone as a compose service (FAULTTOL FT-6.x conventions: restart policy, healthcheck, config in git).
- BF-2 Bus state (subscriptions, undelivered/persistent messages, identity registry) lives in SQLite, replicated continuously via Litestream to the jk-litestream B2 bucket (FAULTTOL FT-7.1/7.2).
- BF-3 Recovery drill: destroy the bus container, restore from Litestream replica, clients reconnect and resume — executed and documented before v1.0 (FAULTTOL FT-11.1 discipline).

Scoping & extraction seam
- BF-4 All topics/queues are namespace-prefixed: `personal.*`, `ucla.*`, `shared.*`. The service rejects unprefixed names.
- BF-5 Identities are registered with allowed scopes; publish/subscribe outside an identity's scopes is denied and logged. `shared.*` requires explicit grant, not scope membership.
- BF-6 Extraction test (design-time proof of B2): a documented procedure showing how `ucla.*` traffic, identities, and persisted state would be exported to a second bus instance with zero changes to client code beyond an endpoint URL.

Access & exposure
- BF-7 Tailnet-only by default. Public exposure, if any client requires it, goes through the keystone front door pattern (FAULTTOL FT-5.x) with authentication — never a bare public port.
- BF-8 Authn: per-identity bearer tokens issued by Joel (later: by the keymaster, KEYMASTER OQ1/OQ2). Tokens live in Infisical; no token appears in any repo.
- BF-9 Audit log: every publish/subscribe denial and every cross-scope (`shared.*`) message is logged with identity and timestamp.

Migration
- BF-10 The Beelink bus instance, when the machine returns, is demoted: its state (if any survives) is migrated into the keystone bus, and Beelink-resident agents are reconfigured as clients of keystone's bus. Old instance decommissioned.
- BF-11 Client SDK (shared-pinakes Go client) gains: endpoint from env/Infisical (not hardcoded host), reconnect with exponential backoff + jitter, and idempotency guidance in its README (B5).

## 4. Implementation & delegation model (Ludi)

- L1. Scope of access: Ludi works in `ucla` scope. Implementation happens in shared-pinakes (already scoped personal+ucla) on a branch; she never needs personal-scope credentials, personal project data, or keystone root. Deploy-to-keystone is performed by Joel's agents (Claude Code/Codex) from her merged branch — she writes it, the fleet ships it.
- L2. Workflow: GitHub-only communication per INTERNPM-001 conventions — issues for work packages, PRs for review. Joel's Claude Code reviews PRs (agent-assisted) before merge; allow_push remains false for agents, humans merge.
- L3. Inference funding: Joel pays. Mechanism options, decide at kickoff:
  - (a) Ludi dispatches agent runs on Joel's infrastructure via the existing `start_agent_run` path (workbench/bus) — inference bills to Joel's subscriptions naturally, zero new credentials. Preferred once the bus/workbench access for her is set up; slightly circular for bus work itself.
    - **HARD PRECONDITION (do not enable L3(a) for a non-Joel identity until met):** `allow_push:false` is today only a *bypassable guardrail*, not technical enforcement — a dispatched run can still push via `git -C`/absolute-path git and gh's file credential helper (proven 2026-07-08; wrapper since hardened for `-C`/`-c` but the absolute-path + credential bypass remains). Letting a non-Joel identity dispatch runs therefore requires **technical push enforcement** first (the separate agent-user design: runs execute as an unprivileged user with no push credentials). Tracked: pinakes issue #11.
  - (b) Joel-funded Anthropic/OpenAI API key, minted narrow, stored in Infisical ucla scope, usage-capped. Works day one, is the fifth-token pattern the keymaster will later absorb.
  - Recommendation: (b) now, migrate to (a) when convenient.
- L4. Non-blocking rule: no work package assigned to Ludi may sit on the critical path of Joel's active work. Bus migration is inherently parallel to everything currently running (nothing bus-dependent works today anyway — the floor is zero).

## 5. Work packages

- WP1: Namespace + scope enforcement in the bus service (BF-4, BF-5) with tests.
- WP2: SQLite persistence layer + Litestream sidecar config (BF-2), recovery drill script (BF-3).
- WP3: Client SDK hardening (BF-11) — env-based endpoint, reconnect/backoff, idempotency docs.
- WP4: Identity/token registry + audit log (BF-8, BF-9).
- WP5: keystone deploy config (BF-1, BF-7) — written by Ludi as compose + docs, applied by Joel's agents.
- WP6: Extraction procedure document (BF-6) — paper deliverable, proves the seam.
- WP7: Beelink demotion runbook (BF-10) — executed when the Beelink returns.

Suggested order: WP1 → WP4 → WP2 → WP3 → WP5 → WP6; WP7 whenever the Beelink is back. WP1–WP4 are pure code with tests, ideal early packages.

## 6. Acceptance

- All BF requirements demonstrably met; BF-3 and BF-6 have executed/authored artifacts, not intentions.
- Kill-the-bus test: stop the service mid-traffic; clients back off and resume on restart with no lost persistent messages, no duplicate side effects (given idempotent consumers).
- Scope test: a `ucla` identity attempting `personal.*` publish is denied and logged.
- Joel's connector-based workflows never touch Ludi's access path (verified by review of credentials issued).

## 7. Open questions

- BQ1. Does the current bus have persistent state worth migrating, or is it fire-and-forget today? (Determines WP7 size; answerable when the Beelink returns.)
- BQ2. Delivery semantics today vs. BF-5 target — does the current SDK already retry, or is WP3 a bigger lift?
- BQ3. Inference funding choice (L3a vs L3b) and cap amount.
- BQ4. Does Ludi's current pinakes-operators SSH access to the Beelink get revoked or narrowed as part of WP7? (It predates this architecture; the bus move may make host SSH unnecessary for her.)
