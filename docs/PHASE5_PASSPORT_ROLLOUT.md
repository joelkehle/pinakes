---
summary: Cross-repo rollout plan for passport registration fields and capability documentation after Phase 4 runtime adoption.
read_when:
  - planning the next shared pinakes ecosystem wave after runtime citizenship
  - coordinating passport rollout across active repos
  - deciding what remains for email-triage, tdg-ip-agents, and email-agents
status: working draft
---

# Phase 5 Passport Rollout

Last updated: 2026-03-31

## Purpose

Phase 4 completed runtime citizenship for the active manager-scope repos. Phase 5 is the next coordinated wave:

- add shared passport field support to the `pinakes` client surface
- migrate active repos to register full passport fields
- add capability docs for the main active capabilities

This is a cross-repo dependency wave, not an isolated repo task.

## Current State

- `email-triage`
  - deploy-managed daemons migrated to `RegisterAgentWithPassport(...)`
  - waiting on shared release/dependency cleanup and capability docs
- `tdg-ip-agents`
  - deploy-managed services migrated to passport registration
  - currently carries local/vendored client workaround because the shared release boundary is not published cleanly yet
- `email-agents`
  - active shared-bus participant
  - not in active manager adoption scope now
  - explicitly deferred for this wave unless scope changes
- `manager`
  - promotion controller path implemented
  - should treat deploy manifest `health_url` as authoritative for host-side verification
  - may use bus `meta.health_url` only as migration fallback / inspection hint
- `pinakes`
  - passport-capable client and registry support exist in-tree
  - operational `meta.health_url` semantics and release/dependency consumption still need to be pinned explicitly

## Phase 5 Scope

Phase 5 includes:

- shared client/passport field support
- shared release/dependency cleanup so downstream repos can consume that support without local forks
- capability docs in the active repos

Phase 5 does not include:

- more runtime health/drain rollout
- manager redesign
- rollback automation expansion
- `email-agents` manager adoption
- broad metrics rewrites beyond what passport visibility needs

## Rollout Order

1. `pinakes` docs + release boundary for passport support
2. downstream repos switch to the released `pinakes` client and remove local workarounds
3. capability docs in the active repos
4. optional `email-agents` review only if its scope changes

## Repo-By-Repo Work

### `email-triage`

Needed:

- keep passport registration on the shared upstream client surface
- pin `github.com/joelkehle/pinakes` to the passport-capable release tag once published
- add capability docs:
  - `triage-summarizer.md`
  - `triage-project-mapper.md`
  - `triage-action-extractor.md`
  - `triage-archive-learner.md`

Notes:

- runtime citizenship is already complete
- passport registration migration is already done for deploy-managed daemons
- deploy manifest exists and is ready once the shared release line is published

### `tdg-ip-agents`

Needed:

- drop the vendored/local passport client workaround
- pin `github.com/joelkehle/pinakes` to the passport-capable release tag once published
- add capability docs:
  - `submission-portal.md`
  - `disclosure-processor.md`
  - extractor ingress docs
  - pipeline/report docs

Notes:

- runtime citizenship is already complete
- passport registration migration is already done for deploy-managed services
- deploy manifest remains the host-reachable authority for promotion; bus `meta.health_url` is not a substitute

### `email-agents`

Status:

- adopt later

Reason:

- on the shared bus now
- not in active manager promotion/verification scope now

Trigger to re-enter active scope:

- if it is brought under shared manager promotion/verification
- or if fleet-level conformance requires its current health/metrics/registration surfaces to match the active repos

## Health URL Convention

Chosen convention:

- `meta.health_url` is the agent's self-declared health endpoint from the runtime-network point of view.
- It may be compose-network-only. Host reachability is not required.
- Manager and other host-run verification should rely on repo manifest `services.<name>.health_url` as the authoritative probe address.
- If both manifest and bus health URLs exist, they may differ by address but must describe the same service and same `/health` response contract.
- Bus `meta.health_url` remains useful for registry display, in-network callers, and the migration window where some manifests or tools are not yet fully wired.

## Dependencies And Blockers

- shared client support exists in-tree, but downstream cleanup is blocked on a released tag
- recommended clean boundary: `pinakes` `v0.2.0`
- downstream repos should consume `github.com/joelkehle/pinakes/pkg/busclient` from that tag or later
- vendored/local passport client workarounds should remain only until:
  - `pinakes` `v0.2.0` (or later) is tagged
  - the shared bus runtime is upgraded to that release line
  - downstream repos update `go.mod` to the released tag and switch imports back to upstream `pkg/busclient`
- capability docs should use the shared template:
  - `docs/CAPABILITY_CONTRACT_TEMPLATE.md`
- migrations should stay repo-local:
  - one repo context at a time
  - no cross-repo implementation in one agent context

## Exit Criteria

Phase 5 is complete when:

- `email-triage` registers full passport fields for its active bus agents
- `tdg-ip-agents` registers full passport fields for its active bus agents
- downstream repos consume a released `pinakes` passport client (`v0.2.0`+) without vendored/local client forks
- main active capabilities in both repos have first-pass capability docs
- manager can observe richer passport info during promotion
- `email-agents` remains explicitly deferred or is intentionally re-scoped

## Recommended Next Shared Step

One shared step remains before Phase 5 is cleanly unblocked for downstream consumption:

- publish the first passport-capable `pinakes` release (`v0.2.0` recommended) and deploy the shared bus on that line

After that, repo-local work can proceed directly to capability docs and cleanup of vendored client code.

## Out Of Scope

- new runtime helper waves
- broad cross-repo refactors
- full `email-agents` adoption
- speculative schema-registry work
