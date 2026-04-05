---
summary: Cross-repo rollout record for the passport registration wave and the settled v0.2.0 release boundary.
read_when:
  - reality-checking the passport rollout after pinakes v0.2.0
  - checking the settled health_url convention across the active repos
  - deciding whether pinakes-side passport work is closed
status: working draft
---

# Phase 5 Passport Rollout

Last updated: 2026-03-31

## Purpose

Phase 4 completed runtime citizenship for the active manager-scope repos. Phase 5 was the coordinated passport wave:

- add shared passport field support to the `pinakes` client surface
- migrate active repos to register full passport fields
- add capability docs for the main active capabilities

This doc now records the settled rollout state after `pinakes v0.2.0` shipped and the shared bus runtime was deployed on that line.

## Current State

- `ucla-tdg/ucla-tdg-email-triage`
  - deploy-managed daemons migrated to `RegisterAgentWithPassport(...)`
  - `pinakes v0.2.0` is now the clean shared dependency boundary
  - remaining capability-doc cleanup is repo-local follow-through, not a pinakes release blocker
- `ucla-tdg/ucla-tdg-ip-agents`
  - deploy-managed services migrated to passport registration
  - `pinakes v0.2.0` now provides the published shared release boundary
  - any remaining local/vendored client cleanup is downstream follow-through, not blocked on pinakes release state
- `jk/jk-email-agents`
  - active shared-bus participant
  - not in active manager adoption scope now
  - explicitly deferred for this wave unless scope changes
- `manager`
  - promotion controller path implemented
  - should treat deploy manifest `health_url` as authoritative for host-side verification
  - may use bus `meta.health_url` only as migration fallback / inspection hint
- `pinakes`
  - passport-capable client and registry support shipped in `v0.2.0`
  - operational `meta.health_url` semantics are now pinned explicitly in the contract docs

## Phase 5 Scope

Phase 5 includes:

- shared client/passport field support
- shared release/dependency cleanup so downstream repos can consume that support without local forks
- capability docs in the active repos

Phase 5 does not include:

- more runtime health/drain rollout
- manager redesign
- rollback automation expansion
- `jk/jk-email-agents` manager adoption
- broad metrics rewrites beyond what passport visibility needs

## Rollout Order Used

1. `pinakes` docs + release boundary for passport support
2. downstream repos switch to the released `pinakes` client surface for deploy-managed services
3. capability docs in the active repos
4. optional `jk/jk-email-agents` review only if its scope changes

## Repo-By-Repo State

### `ucla-tdg/ucla-tdg-email-triage`

Needed:

- keep passport registration on the shared upstream client surface
- pin `github.com/joelkehle/pinakes` to `v0.2.0+`
- add capability docs:
  - `triage-summarizer.md`
  - `triage-project-mapper.md`
  - `triage-action-extractor.md`
  - `triage-archive-learner.md`

Notes:

- runtime citizenship is already complete
- passport registration migration is already done for deploy-managed daemons
- deploy manifest remains the host-side verification authority

### `ucla-tdg/ucla-tdg-ip-agents`

Needed:

- drop the vendored/local passport client workaround
- pin `github.com/joelkehle/pinakes` to `v0.2.0+`
- add capability docs:
  - `submission-portal.md`
  - `disclosure-processor.md`
  - extractor ingress docs
  - pipeline/report docs

Notes:

- runtime citizenship is already complete
- passport registration migration is already done for deploy-managed services
- deploy manifest remains the host-reachable authority for promotion; bus `meta.health_url` is not a substitute

### `jk/jk-email-agents`

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

- `pinakes v0.2.0` is the released passport-capable baseline
- downstream repos should consume `github.com/joelkehle/pinakes/pkg/busclient` from that tag or later
- the shared bus runtime is already deployed on that release line
- any remaining vendored/local passport client cleanup is downstream follow-through, not blocked on pinakes release or runtime state
- capability docs should use the shared template:
  - `docs/CAPABILITY_CONTRACT_TEMPLATE.md`
- migrations should stay repo-local:
  - one repo context at a time
  - no cross-repo implementation in one agent context

## Closeout State

Passport support is considered cleanly closed on the pinakes side when these are true:

- `pinakes v0.2.0` is the passport-capable baseline
- active downstream repos have migrated deploy-managed services to passport registration
- manager can rely on manifest `health_url` for host-side verification while the bus stores runtime-network `meta.health_url`
- `jk/jk-email-agents` remains explicitly deferred unless its scope changes

Capability docs and any remaining vendored-client cleanup are still valuable, but they are downstream follow-through rather than unfinished pinakes passport protocol work.

## Out Of Scope

- new runtime helper waves
- broad cross-repo refactors
- full `jk/jk-email-agents` adoption
- speculative schema-registry work
