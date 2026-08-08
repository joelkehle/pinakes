---
summary: "Outline for rehearsing the retire-and-re-register consolidation of the JK and UCLA Pinakes authorities on Keystone."
read_when:
  - Preparing or reviewing the rehearsal authorized on Pinakes issue #15.
  - Taking authority snapshots or running pinakes-migrate on rehearsal copies.
  - Comparing seeded and fresh strict-mode unified bus boots.
status: outline
---

# Bus Consolidation Rehearsal Runbook

> **Outline only.** Commands, host paths, container names, ports, credentials,
> manifests, allowlist contents, and evidence locations remain to be confirmed
> before this runbook is executable.

## 1. Purpose and authorization boundary

### 1.1 Objective

- Prove the JK-to-`personal.*` and UCLA-to-`ucla.*` namespace rewrites on
  independent database copies.
- Compare a unified strict-mode bus booted from a rewritten survivor copy with
  one booted from an empty store.
- Prove representative cross-domain re-registration against the isolated
  rehearsal bus.
- Produce a recommendation for the production execution gate.

### 1.2 Consolidation model

- Consolidation is **retire and re-register**, not a two-history import.
- No database rows move from one authority into the other.
- Rewritten databases are rehearsal evidence and searchable archives under the
  final namespaced identities.
- The production survivor and seeded-versus-fresh choice remain Joel's decision
  at the execution gate.

### 1.3 Authorized in this phase

- Online-consistent snapshots on Keystone using SQLite `VACUUM INTO`.
- Dry-run and apply against disposable copies of those snapshots only.
- Isolated strict-mode boots from a rewritten survivor copy and an empty store.
- Representative registration tests using rehearsal-only credentials and
  endpoints.
- Evidence collection and a recommendation on issue #15.

### 1.4 Not authorized in this phase

- Running `pinakes-migrate` against a live database path.
- Changing a production allowlist or production agent identity.
- Redirecting production agents, retiring an authority, or activating strict
  mode in production.
- Copying bus databases, message content, or secrets off Keystone or into Git.
- Executing the production consolidation without Joel's explicit approval on
  issue #15.

## 2. Roles, prerequisites, and stop conditions

### 2.1 Roles and approvals

- Decision and production execution owner: Joel Kehle.
- Rehearsal operator: `[TBD]`.
- Rehearsal reviewer/witness: `[TBD]`.
- Issue #15 authorization link and timestamp: `[TBD]`.

### 2.2 Access and tool prerequisites

- Confirm Joel-granted Keystone access.
- Confirm the current Keystone host and both authority runtimes by read-only
  inventory; do not rely only on historical Beelink port information.
- Record versions for `sqlite3`, `pinakes-migrate`, Pinakes, Go (if invoking via
  `go run`), Docker, and Docker Compose.
- Record the exact live DB paths without printing secrets or message content.
- Confirm sufficient disk space and permissions for two pristine snapshots,
  disposable working copies, empty-store comparison, logs, and reports.
- Confirm the manager-repository paths for the reviewed manifests and staged
  post-migration allowlist.

### 2.3 Required artifacts

- Reviewed JK and UCLA manifest data using the exact schema documented in
  `docs/NAMESPACE_MIGRATION_TOOL.md`.
- A staged post-migration allowlist from the manager repository.
- Named consumer and removal date for every alias proposed to survive.
- Separate rehearsal identities and credentials for the personal and UCLA
  `jk-calendar-guard-agent` deployments.
- A reviewed isolation plan for rehearsal ports, network, volumes, and
  credentials.

### 2.4 Immediate stop conditions

- Any resolved DB path is the live path when a migration command is about to
  run.
- The snapshot destination already exists or is outside the approved Keystone
  snapshot directory.
- The source authority, manifest authority, filename, or recorded checksum does
  not agree.
- A rehearsal endpoint is reachable by production agents or shares production
  credentials unexpectedly.
- A report returns `refused`, an unexpected collision, mixed-scope
  participants, invalid JSON, or an unclassified identity.
- Any step would require a production mutation not covered by the rehearsal
  authorization.

## 3. Paths, naming, and evidence ledger

### 3.1 Keystone-only directory layout

Proposed root (confirm before use):

```text
~/bus-snapshots/<UTC-date>/
  pristine/
  working/
  empty/
  reports/
  logs/
  checksums/
```

- Pristine snapshots are never passed to `pinakes-migrate --apply`.
- Each apply or rollback test starts from a new working copy of a verified
  pristine snapshot.
- Database files and content-bearing output never leave Keystone.

### 3.2 File naming convention

```text
pinakes-<jk|ucla>-<UTC-timestamp>-pristine.db
pinakes-<jk|ucla>-<UTC-timestamp>-rewrite-working.db
pinakes-<jk|ucla>-<UTC-timestamp>-inverse-working.db
pinakes-empty-<UTC-timestamp>-working.db
```

Record the live source path, pristine snapshot path, working-copy path,
authority, timestamp, byte size, and SHA-256 checksum in the evidence ledger.

### 3.3 Evidence-handling rules

- Store deterministic metadata-only migration JSON reports under `reports/`.
- Do not record message bodies, secrets, callback URLs, private metadata,
  attachments, or manifest evidence text in issue comments.
- Reference each `--acknowledge-copy` run by its exact working-copy path and
  checksum in operator notes.
- Post only reviewed, sanitized results to issue #15.

## 4. Read-only pre-rehearsal inventory

### 4.1 Runtime inventory

- Identify the JK and UCLA bus containers/processes, images, DB paths, volumes,
  namespace settings, allowlist path, ports, networks, and health endpoints.
- Confirm both live authorities remain healthy before snapshotting.
- Record registry metadata and expected active identities without relying on
  total counts alone.

### 4.2 Database baseline

- Record SQLite integrity result and schema/version metadata.
- Record counts for every migration rewrite surface:
  `agents`, message senders/targets, conversation participant occurrences,
  deliveries, delivery cursors, and idempotency senders/targets.
- Record representative non-content identifiers needed for later comparison.
- Inventory namespaced, unprefixed, persisted-only, split, retired, and alias
  identities for manifest-completeness review.

### 4.3 Registry and alias baseline

- Capture a sanitized before-registry inventory for each authority.
- Reconcile current registrations with the manager allowlist and manifests.
- For every retained alias, record its named consumer and removal date.
- Resolve every other alias to `retire` before cutting the final manifest.

## 5. Online-consistent snapshot procedure

### 5.1 Preflight

- Create the approved UTC-dated Keystone directory with restrictive
  permissions.
- Confirm each `VACUUM INTO` destination does not already exist.
- Confirm available disk space and source database readability.
- Record source health and inventory timestamp immediately before snapshot.

### 5.2 Create one snapshot per authority

- Run `sqlite3 <confirmed-live-db> "VACUUM INTO '<new-pristine-path>'"` for JK.
- Run the same procedure independently for UCLA.
- Never use a plain file copy of a running SQLite database.
- Capture exit status and sanitized stderr without exposing database content.

### 5.3 Validate and seal pristine snapshots

- Run SQLite integrity checks against each snapshot.
- Confirm expected schema and baseline counts.
- Record byte size and SHA-256 checksum.
- Mark the pristine files read-only after validation.
- Create separately named working copies for all subsequent rehearsal steps.

## 6. Manifest and staged allowlist review

### 6.1 Manifest validation

- Reference, but do not duplicate, the private manager-owned artifact paths.
- Confirm exact CSV header, authority values, closed disposition vocabulary,
  unique source rows, unique targets, and full legacy-ID preservation.
- Confirm `managerd` is unchanged as the one privileged control-plane identity.
- Confirm `jk-calendar-guard-agent` has one `split` row per authority and will
  use separate deployments and credentials.
- Confirm retired identities retain namespaced historical homes but receive no
  future consumer, credential, configuration, or allowlist entry.

### 6.2 Post-migration allowlist review

- Build the candidate allowlist in the manager repository.
- Confirm final `personal.*` and `ucla.*` entries match active consumers.
- Confirm alias exceptions include a consumer and removal date.
- Confirm no production allowlist file is changed during rehearsal.
- Stage or mount a rehearsal-only copy for unified boot tests.

## 7. JK namespace rewrite rehearsal

### 7.1 Reset JK working copy

- Delete or archive only the specifically named disposable JK working copy
  according to the approved retention rule.
- Re-create it from the checksummed pristine JK snapshot.
- Verify its checksum and prove its path differs from the live DB and pristine
  snapshot paths.

### 7.2 Dry-run

- Run the default dry-run with `--authority jk --acknowledge-copy` against the
  JK working copy.
- Save the deterministic JSON report.
- Require `ready`; review mapping counts, rewrite counts, already-applied
  mappings, and collision/refusal sections.

### 7.3 Apply and verification

- Run the same reviewed inputs with `--apply` against the JK working copy.
- Require `applied` or a separately explained expected `no-op`.
- Re-run dry-run to prove the applied state is recognized.
- Compare counts and representative rows across all eight rewrite surfaces.
- Confirm no out-of-surface field changed.

### 7.4 Invertibility spot-check

- Create a new inverse-test copy from the rewritten JK result.
- Generate/use the reviewed exact inverse manifest.
- Apply the inverse to that disposable copy.
- Compare selected records and counts with the pristine baseline; record the
  scope and result of the spot-check.

## 8. UCLA namespace rewrite rehearsal

Repeat the reset, dry-run, apply, post-apply comparison, and inverse spot-check
from section 7 using the UCLA pristine snapshot and `--authority ucla`.

Explicitly confirm that neither authority relies on the other authority's
manifest rows for completeness and that no foreign-authority mutation occurs.

## 9. Isolated unified strict-mode boot rehearsal

### 9.1 Isolation and common configuration

- Use rehearsal-only ports, container/project names, network, volume/path,
  tokens, and credentials.
- Prevent production agents from discovering or registering with the rehearsal
  bus.
- Set `BUS_NAMESPACE_MODE=strict` and use only the staged post-migration
  allowlist.
- Record the exact Pinakes build/tag and rendered non-secret configuration.

### 9.2 Seeded-survivor boot

- Select the rewritten survivor candidate for this comparison without implying
  a production survivor decision.
- Start the isolated bus from a disposable copy of that rewritten database.
- Verify health, strict-mode rejection of an unprefixed registration, registry
  visibility, and representative persisted history under final IDs.
- Record startup time, health results, registry/count observations, and any
  operational risks.

### 9.3 Fresh-store boot

- Start the same isolated strict-mode configuration with a new empty database.
- Verify health, empty baseline behavior, strict-mode rejection of unprefixed
  identities, and readiness for namespaced re-registration.
- Record the same measurements used for the seeded boot.

### 9.4 Seeded-versus-fresh comparison

- Compare operational simplicity, retained searchable transport history,
  privacy/retention exposure, startup behavior, rollback clarity, and failure
  modes.
- Do not choose the production approach during rehearsal; produce a written
  recommendation for Joel's execution-gate decision.

## 10. Representative cross-domain re-registration

Run against each boot candidate where practical, using rehearsal-only agents
or adapters and separate credentials.

### 10.1 Required identities

- One representative `personal.*` agent.
- One representative `ucla.*` agent.
- `personal.jk-calendar-guard-agent` deployment.
- `ucla.jk-calendar-guard-agent` deployment.

### 10.2 Registration verification

- Confirm all four namespaced identities are present in the rehearsal
  allowlist.
- Confirm the two calendar-guard deployments are separate processes/adapters
  with distinct credentials.
- Register and verify heartbeat/TTL refresh behavior.
- Confirm effective scopes are derived server-side from prefixes.
- Confirm an unprefixed registration is rejected in strict mode.
- Verify representative same-domain and explicitly permitted cross-domain
  transport behavior without recording message content.
- Confirm the two calendar-guard identities do not share or overwrite registry
  credentials/state.

## 11. Verification and exit checklist

### 11.1 Namespace rewrite evidence

- [ ] JK dry-run report reviewed and `ready`.
- [ ] JK apply report reviewed and `applied`/expected `no-op`.
- [ ] JK inverse spot-check passed.
- [ ] UCLA dry-run report reviewed and `ready`.
- [ ] UCLA apply report reviewed and `applied`/expected `no-op`.
- [ ] UCLA inverse spot-check passed.
- [ ] Counts and representative rows reconcile for all eight rewrite surfaces.
- [ ] No unresolved collision, unclassified identity, or foreign mutation.

### 11.2 Alias and allowlist evidence

- [ ] Alias inventory reconciled.
- [ ] Each surviving alias has a consumer and removal date.
- [ ] All other aliases are marked for operational retirement.
- [ ] Candidate strict-mode allowlist reviewed in the manager repository.
- [ ] Production allowlist remained unchanged.

### 11.3 Unified boot and registration evidence

- [ ] Seeded strict-mode boot passed in isolation.
- [ ] Fresh strict-mode boot passed in isolation.
- [ ] Unprefixed registration rejection proved.
- [ ] Representative personal and UCLA registrations proved.
- [ ] Both calendar-guard deployments registered with separate credentials.
- [ ] No production endpoint, database, allowlist, or agent was mutated.

## 12. Rehearsal reset and rollback procedures

### 12.1 Reset a failed namespace rehearsal

- Stop only the isolated process using the named working copy.
- Preserve failed metadata-only reports and sanitized logs for diagnosis.
- Remove/archive only the explicitly resolved disposable working-copy path.
- Re-create the working copy from the validated pristine snapshot.
- Reconfirm checksum, authority, manifest revision, and live-path inequality
  before retrying.

### 12.2 Reset a unified boot rehearsal

- Stop the isolated rehearsal project/process.
- Preserve its configuration summary and sanitized evidence.
- Re-create the seeded candidate from the rewritten working result, or create a
  new empty-store path.
- Reapply the rehearsal-only allowlist and credentials; never substitute the
  production allowlist path.

### 12.3 Production rollback point to recommend

- Define the last reversible point before agents are redirected or an authority
  is retired.
- Define the retained live-authority snapshot(s), previous endpoints/config,
  credential handling, and health/registry checks required to restore service.
- Estimate and record the rollback time objective based on rehearsal evidence.
- Leave the final rollback point and retention period for Joel's execution-gate
  approval.

## 13. Recommendation and issue #15 report template

### 13.1 Rehearsal summary

- Date/time, operator, reviewer, Pinakes revision, tool versions.
- Sanitized snapshot identifiers/checksums and manifest revisions.
- JK and UCLA dry-run/apply/inverse outcomes.
- Seeded and fresh strict-mode boot outcomes.
- Representative re-registration outcomes.
- Deviations, refusals, unresolved risks, and evidence paths on Keystone.

### 13.2 Required recommendation

- Recommend **seeded survivor** or **fresh strict-mode store**, with reasons.
- Recommend which current authority survives as the unified process, if the
  evidence supports a preference; otherwise state what remains to decide.
- Recommend cutover order for allowlist staging, unified boot, agent endpoint
  changes, re-registration verification, and old-authority retirement.
- Recommend the last safe rollback point, rollback trigger, estimated rollback
  duration, and snapshot retention period.

### 13.3 Execution gate

- State explicitly: rehearsal completion is not production authorization.
- Request Joel's review and explicit authorization on issue #15.
- Stop. Do not redirect agents, mutate live databases/configuration, activate
  production strict mode, or retire either authority.

## 14. Parameters to resolve before promoting this outline

- Keystone host/access grant and operator account.
- Current JK/UCLA container names, DB paths, images, ports, networks, and
  volumes.
- Approved snapshot root, permissions, owner, retention period, and available
  space.
- Manager-repository manifest and candidate allowlist paths/revisions.
- Rehearsal isolation topology and non-production ports.
- Survivor candidates and exact seeded database copy used for comparison.
- Representative personal/UCLA agents and their rehearsal launch method.
- Calendar-guard split-deployment owners, configurations, and rehearsal-only
  credential provisioning method.
- Evidence reviewer, sanitization review, and issue #15 reporting format.
