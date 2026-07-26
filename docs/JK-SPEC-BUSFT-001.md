---
summary: "Current BUSFT roadmap for separate Pinakes authorities, durability decisions, explicit namespaces, and held identity work."
read_when:
  - Reviewing the origin or requirements of a BUSFT work package.
  - Comparing current Pinakes decisions with the original 2026-07-08 proposal.
status: draft-roadmap
---

# JK-SPEC-BUSFT-001 — Fault-Tolerant Pinakes Bus

- ID: JK-SPEC-BUSFT-001
- Version: v0.1
- Status: Draft roadmap; individual work requires maintainer approval and contributor acceptance
- Author: Claude (drafted), Joel Kehle (owner)
- Date: 2026-07-08
- Related: JK-SPEC-FAULTTOL-001 (layers 2, 8), JK-SPEC-INTERNPM-001 (worker model), JK-SPEC-KEYMASTER-001 (future bus consumer)

## 1. Motivation

The Pinakes bus is the coordination fabric for Joel's agent ecosystem — IP
Agency workers, intern PM, wwi, and future consumers all depend on it. Pinakes
currently has two separate authorities, JK and UCLA, running independently on
Keystone. The relocation is complete; the immediate work is to settle
durability and explicit-namespace decisions without combining the authorities.

Consolidating those authorities is a later planning decision, not a settled
architecture or part of the current relocation. Explicit namespaces preserve a
possible extraction or consolidation seam without predetermining the final
topology.

## 2. Constraints and principles

- B1. Separate authorities now. JK and UCLA remain distinct. Consolidation
  requires a later explicit decision.
- B2. Extraction seam. Namespace boundaries must make either continued
  separation or a future consolidation/extraction possible without rewriting
  clients.
- B3. Must-be-up tier. Both authorities run on Keystone. Beelink and the
  retained snapshots remain rollback assets until their approved retention
  period ends.
- B4. n=1 honesty (inherited FAULTTOL C6). No clustered brokers, no Raft,
  no Kafka. The bus is a small Go service; its fault tolerance comes from
  placement, the selected recovery model, and reprovisionability
  (config-as-code) — not from distributed-systems machinery.
- B5. Degrade, don't die. Bus outage must not corrupt work. WP2 and WP3 must
  make recovery, retry, and delivery semantics explicit before stronger
  guarantees are claimed.
- B6. Bounded contribution. Each work package must be small enough to offer to
  a contributor without requiring unresolved architecture or access to another
  scope's credentials or data.

## 3. Requirements

These are candidate future-state requirements. They do not authorize bus
consolidation, settle persistence semantics, or lift the WP4 design hold.

Transport & persistence
- BF-1 The bus service runs on keystone as a compose service (FAULTTOL FT-6.x conventions: restart policy, healthcheck, config in git).
- BF-2 Recovery semantics and the authoritative state set must be decided before
  selecting or implementing a replication mechanism.
- BF-3 Recovery drill: restore the selected authoritative state, then verify
  clients reconnect and resume under the decided delivery semantics — executed
  and documented before v1.0 (FAULTTOL FT-11.1 discipline).

Scoping & extraction seam
- BF-4 All topics/queues are namespace-prefixed: `personal.*`, `ucla.*`, `shared.*`. The service rejects unprefixed names.
- BF-5 Identities are registered with allowed scopes; publish/subscribe outside an identity's scopes is denied and logged. `shared.*` requires explicit grant, not scope membership.
- BF-6 Extraction test (design-time proof of B2): a documented procedure showing how `ucla.*` traffic, identities, and persisted state would be exported to a second bus instance with zero changes to client code beyond an endpoint URL.

Access & exposure
- BF-7 Tailnet-only by default. Public exposure, if any client requires it, goes through the keystone front door pattern (FAULTTOL FT-5.x) with authentication — never a bare public port.
- BF-8 Authn: per-identity bearer tokens issued by Joel (later: by the keymaster, KEYMASTER OQ1/OQ2). Tokens live in Infisical; no token appears in any repo.
- BF-9 Audit log: every publish/subscribe denial and every cross-scope (`shared.*`) message is logged with identity and timestamp.

Migration
- BF-10 Keep the UCLA authority separate on Keystone. Preserve the Beelink
  authority and rollback assets through the approved retention period, then
  execute the separately approved demotion procedure.
- BF-11 Client SDK (shared-pinakes Go client) gains: endpoint from env/Infisical (not hardcoded host), reconnect with exponential backoff + jitter, and idempotency guidance in its README (B5).

## 4. Contribution and acceptance model

- L1. Open issues are invitations to discuss or propose work. They are not
  assignments.
- L2. A contributor owns a work package only after explicitly accepting its
  scope and acceptance criteria. A GitHub assignee records that acceptance; it
  does not create it.
- L3. Offers must be bounded, non-blocking, and inside a stable contract. A
  contributor should not be asked to resolve the bus topology, persistence
  semantics, or identity architecture as an implementation detail.
- L4. Access follows the accepted slice. No contributor needs another scope's
  credentials, data, or host administration access to participate.
- L5. Non-Joel agent-run push enforcement is tracked outside Pinakes in
  [manager issue #4](https://github.com/joelkehle/manager/issues/4).

## 5. Work packages

- WP1: Namespace + scope enforcement in the bus service (BF-4, BF-5) with tests.
- WP2: Decide recovery semantics and authoritative state, then implement the
  selected replication and recovery drill (BF-2, BF-3).
- WP3: Client SDK hardening (BF-11) — env-based endpoint, reconnect/backoff, idempotency docs.
- WP4: Identity/token registry + audit log (BF-8, BF-9), held pending a
  Joel/Codex discussion of the Buzz-derived design.
- WP5: Keystone deploy config and UCLA authority relocation (BF-1, BF-7);
  complete.
- WP6: Extraction procedure document (BF-6) — paper deliverable, proves the seam.
- WP7: Beelink demotion runbook (BF-10) — executed only after UCLA relocation
  acceptance and the required soak.

Current order: WP1 compatibility support and the WP5 UCLA relocation are
complete → decide WP2 recovery semantics → complete explicit namespace
planning → consider whether any consolidation rehearsal should be approved.
WP4 remains held; WP3 and WP6 follow the topology and contract decisions.

## 6. Acceptance

- All BF requirements demonstrably met; BF-3 and BF-6 have executed/authored artifacts, not intentions.
- Kill-the-bus test: stop the service mid-traffic; clients back off and resume on restart with no lost persistent messages, no duplicate side effects (given idempotent consumers).
- Scope test: a `ucla` identity attempting `personal.*` publish is denied and logged.
- Contributor access remains bounded to the accepted scope and is verified
  before work begins.

## 7. Open questions

- BQ1. Which current state is authoritative, and what recovery semantics must
  WP2 preserve?
- BQ2. Delivery semantics today vs. BF-5 target — does the current SDK already retry, or is WP3 a bigger lift?
- BQ3. After both authorities are stable on Keystone, should they remain
  separate or converge?
- BQ4. What identity, validation, signing, and audit design should replace or
  extend the current mechanisms? This remains held until Joel and Codex discuss
  the Buzz-derived proposal.
