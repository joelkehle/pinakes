---
summary: "Outline for rehearsing the retire-and-re-register consolidation of the JK and UCLA Pinakes authorities on Keystone."
read_when:
  - Preparing or reviewing the rehearsal authorized on Pinakes issue #15.
  - Taking authority snapshots or running pinakes-migrate on rehearsal copies.
  - Rehearsing the empty-store strict-mode unified bus boot.
status: draft
---

# Bus Consolidation Rehearsal Runbook

This runbook is written for cold execution by Joel or a designated fleet-side
operator. Resolve and record the host-specific values in section 14 before
running fleet steps. Commands fail closed around existing destinations and
live-path equality; do not weaken those checks for convenience.

## 1. Purpose and authorization boundary

### 1.1 Objective

- Prove the JK-to-`personal.*` and UCLA-to-`ucla.*` namespace rewrites on
  independent database copies.
- Prove a unified strict-mode bus booted from an empty store.
- Prove representative cross-domain re-registration against the isolated
  rehearsal bus.
- Produce a recommendation for the production execution gate.

### 1.2 Consolidation model

- Consolidation is **retire and re-register**, not a two-history import.
- No database rows move from one authority into the other.
- Rewritten databases are rehearsal evidence and searchable archives under the
  final namespaced identities; they prove that the rewrite tool is correct and
  exactly invertible, but they never become the unified bus's boot database.
- The unified bus starts from an empty strict-mode store. This is the
  2026-08-08 plan of record, not a rehearsal comparison or execution-gate
  choice.

### 1.3 Authorized in this phase

- Online-consistent snapshots on Keystone using SQLite `VACUUM INTO`.
- Dry-run and apply against disposable copies of those snapshots only.
- Local synthetic-fixture rehearsals using schema-true databases containing
  fabricated content only.
- An isolated strict-mode boot from an empty store.
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
- Ludi owns this runbook, both manifests, the post-migration allowlist proposal,
  rehearsal scripts, and the local schema-true synthetic-fixture rehearsal.
- Joel or a designated fleet-side operator owns the real Keystone snapshots,
  the real-data rehearsal executed step-for-step from this runbook, and any
  separately authorized production execution.
- Ludi requires no fleet access for this work and does not receive real bus
  databases. The databases contain private message content and remain on
  Joel-controlled fleet machines throughout snapshotting, rehearsal,
  retention, and execution.
- Rehearsal reviewer/witness: `[TBD]`.
- Issue #15 authorization link and timestamp: `[TBD]`.

### 2.2 Access and tool prerequisites

- The fleet-side operator confirms access to Keystone and both authority
  runtimes by read-only inventory; do not rely only on historical Beelink port
  information.
- Record versions for `sqlite3`, `pinakes-migrate`, Pinakes, Go (if invoking via
  `go run`), Docker, and Docker Compose.
- Require a Pinakes revision containing v0.4.1 commit `77fb37f` or a verified
  descendant. Record the ancestry check; a rehearsal timestamp after the
  deployment is not a substitute for testing the corrected send-response path.
- The fleet-side operator records the exact live DB paths without printing
  secrets or message content.
- The fleet-side operator confirms sufficient disk space and permissions for
  two pristine snapshots, disposable working copies, an empty-store rehearsal,
  logs, and reports.
- Confirm that Ludi's local rehearsal uses only generated schema-true fixtures
  with fabricated content and rehearsal-only credentials.
- Confirm the manager-repository paths for the reviewed manifests and staged
  post-migration allowlist.

### 2.3 Required artifacts

- Reviewed combined JK/UCLA manifest at
  `rehearsal/issue15/namespace-manifest.csv`, using the exact schema documented
  in `docs/NAMESPACE_MIGRATION_TOOL.md`. The combined file is required so every
  split pair is present during parsing; each run still selects one authority.
- Candidate post-migration allowlist at
  `rehearsal/issue15/post-migration-allowlist.txt`. It is rehearsal input, not
  authorization to replace the manager-owned production file.
- Named consumer and removal date for every alias proposed to survive.
- Separate rehearsal identities and credentials for the personal and UCLA
  `jk-calendar-guard-agent` deployments.
- A reviewed `CONTROL_PLANE_AGENTS=managerd` policy for the unified rehearsal
  bus and matching `control_plane=true` rows in both authority manifests.
- A fleet-only, untracked
  `CONTROL_PLANE_AGENT_SECRET_HASHES=managerd=<sha256>` value provisioned by
  Joel. Do not print, copy into reports, or commit this secret-derived value.
- Reviewed isolation plans for Ludi's local synthetic rehearsal and the
  fleet-side real-data rehearsal, including ports, network, volumes/paths, and
  credentials.

### 2.4 Immediate stop conditions

- Any resolved DB path is the live path when a migration command is about to
  run.
- The snapshot destination already exists or is outside the approved Keystone
  snapshot directory.
- The source authority, manifest authority, filename, or recorded checksum does
  not agree.
- A local or fleet-side rehearsal endpoint is reachable by production agents
  or shares production credentials unexpectedly.
- Any real database, private message content, or production credential would
  leave Joel-controlled fleet machines or become accessible to Ludi.
- A report returns `refused`, an unexpected collision, mixed-scope
  participants, invalid JSON, or an unclassified identity.
- Any step would require a production mutation not covered by the rehearsal
  authorization.

## 3. Paths, naming, and evidence ledger

### 3.1 Fleet-side Keystone directory layout

Proposed layout under the approved absolute snapshot root (confirm the root
before use):

```text
~/bus-snapshots/<UTC-date>/
  pristine/
  working/
  empty/
  reports/
  logs/
  checksums/
```

Create a new restrictive directory for this run; do not reuse a previous
rehearsal root:

```bash
SNAPSHOT_BASE=/confirmed/absolute/path/to/bus-snapshots
case "$SNAPSHOT_BASE" in /*) ;; *) exit 1 ;; esac
REHEARSAL_UTC=$(date -u +%Y%m%dT%H%M%SZ)
REHEARSAL_ROOT="$SNAPSHOT_BASE/$REHEARSAL_UTC"
test ! -e "$REHEARSAL_ROOT"
install -d -m 700 \
  "$REHEARSAL_ROOT/pristine" \
  "$REHEARSAL_ROOT/working" \
  "$REHEARSAL_ROOT/empty" \
  "$REHEARSAL_ROOT/reports" \
  "$REHEARSAL_ROOT/logs" \
  "$REHEARSAL_ROOT/checksums"
```

- Pristine snapshots are never passed to `pinakes-migrate --apply`.
- Each apply or rollback test starts from a new working copy of a verified
  pristine snapshot.
- Real database files and content-bearing output never leave Joel-controlled
  fleet machines. Ludi's local files are independently generated synthetic
  fixtures containing fabricated content only.

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

The fleet-side operator performs this section against the real authorities.
Ludi performs the structurally equivalent checks against generated synthetic
fixtures without receiving or querying fleet data.

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

This section is executed only by Joel or a designated fleet-side operator on
Keystone. Ludi does not execute `VACUUM INTO`, access the live DB paths, or
receive the resulting snapshots.

### 5.1 Preflight

- Create the approved UTC-dated Keystone directory with restrictive
  permissions.
- Confirm each `VACUUM INTO` destination does not already exist.
- Confirm available disk space and source database readability.
- Record source health and inventory timestamp immediately before snapshot.

The fleet-side operator resolves these values from the live container/volume
inventory and records them in the evidence ledger:

```bash
JK_LIVE_DB=/confirmed/absolute/path/to/jk.db
UCLA_LIVE_DB=/confirmed/absolute/path/to/ucla.db
JK_SNAPSHOT="$REHEARSAL_ROOT/pristine/pinakes-jk-$REHEARSAL_UTC-pristine.db"
UCLA_SNAPSHOT="$REHEARSAL_ROOT/pristine/pinakes-ucla-$REHEARSAL_UTC-pristine.db"

test -f "$JK_LIVE_DB"
test -f "$UCLA_LIVE_DB"
test "$JK_LIVE_DB" != "$UCLA_LIVE_DB"
test ! -e "$JK_SNAPSHOT"
test ! -e "$UCLA_SNAPSHOT"
case "$JK_SNAPSHOT$UCLA_SNAPSHOT" in *"'"*) exit 1 ;; esac
```

### 5.2 Create one snapshot per authority

- Run the following independently for JK and UCLA:

```bash
sqlite3 "$JK_LIVE_DB" "VACUUM INTO '$JK_SNAPSHOT'"
sqlite3 "$UCLA_LIVE_DB" "VACUUM INTO '$UCLA_SNAPSHOT'"
```

- Never use a plain file copy of a running SQLite database.
- Capture exit status and sanitized stderr without exposing database content.

### 5.3 Validate and seal pristine snapshots

- Run SQLite integrity checks against each snapshot.
- Confirm expected schema and baseline counts.
- Record byte size and SHA-256 checksum.
- Mark the pristine files read-only after validation.
- Create separately named working copies for all subsequent rehearsal steps.

```bash
test "$(sqlite3 "$JK_SNAPSHOT" 'PRAGMA quick_check')" = ok
test "$(sqlite3 "$UCLA_SNAPSHOT" 'PRAGMA quick_check')" = ok
sha256sum "$JK_SNAPSHOT" "$UCLA_SNAPSHOT" \
  >"$REHEARSAL_ROOT/checksums/pristine.sha256"
chmod 400 "$JK_SNAPSHOT" "$UCLA_SNAPSHOT"
```

## 6. Manifest and staged allowlist review

### 6.1 Manifest validation

- Review the combined issue #15 manifest and its evidence fields without
  copying production configuration or private database content into it.
- Confirm exact CSV header, authority values, closed disposition vocabulary,
  unique source rows, unique targets, and full legacy-ID preservation.
- Confirm `managerd` is unchanged as the one privileged control-plane identity.
- Confirm both `managerd` rows carry `control_plane=true`, every other row
  carries `control_plane=false`, and each rehearsal run sets
  `CONTROL_PLANE_AGENTS=managerd` so the manifest and runtime policy agree.
- Confirm `jk-calendar-guard-agent` has one `split` row per authority and will
  use separate deployments and credentials.
- Confirm retired identities retain namespaced historical homes but receive no
  future consumer, credential, configuration, or allowlist entry.

### 6.2 Post-migration allowlist review

- Review the candidate allowlist in this branch. The approved production
  version will later replace the manager-owned source of truth only after the
  execution gate.
- Confirm final `personal.*` and `ucla.*` entries match active consumers.
- Confirm alias exceptions include a consumer and removal date.
- Confirm no production allowlist file is changed during rehearsal.
- Stage or mount a rehearsal-only copy for unified boot tests.

Regenerate the candidate and require no diff before rehearsal:

```bash
go run ./cmd/pinakes-manifest-allowlist \
  --manifest rehearsal/issue15/namespace-manifest.csv \
  >"$REHEARSAL_ROOT/working/generated-allowlist.txt"
diff -u rehearsal/issue15/post-migration-allowlist.txt \
  "$REHEARSAL_ROOT/working/generated-allowlist.txt"
```

## 7. JK namespace rewrite rehearsal

Ludi first runs this flow locally against a generated JK synthetic fixture.
The fleet-side operator later repeats it against a disposable working copy of
the real JK snapshot and reports sanitized results on issue #15.

### 7.1 Reset JK working copy

- Preserve any prior failed copy and choose a new working-copy path.
- Create the new copy from the checksummed pristine JK snapshot.
- Verify its checksum and prove its path differs from the live DB and pristine
  snapshot paths.

```bash
JK_WORKING="$REHEARSAL_ROOT/working/pinakes-jk-$REHEARSAL_UTC-rewrite.db"
test ! -e "$JK_WORKING"
test "$JK_WORKING" != "$JK_LIVE_DB"
test "$JK_WORKING" != "$JK_SNAPSHOT"
install -m 600 "$JK_SNAPSHOT" "$JK_WORKING"
```

### 7.2 Dry-run

- Run the default dry-run with `--authority jk --acknowledge-copy` against the
  JK working copy.
- Save the deterministic JSON report.
- Require `ready`; review mapping counts, rewrite counts, already-applied
  mappings, and collision/refusal sections.

```bash
export CONTROL_PLANE_AGENTS=managerd
go run ./cmd/pinakes-migrate \
  --db "$JK_WORKING" \
  --manifest rehearsal/issue15/namespace-manifest.csv \
  --authority jk \
  --acknowledge-copy \
  >"$REHEARSAL_ROOT/reports/jk-dry-run.json"
```

### 7.3 Apply and verification

- Run the same reviewed inputs with `--apply` against the JK working copy.
- Require `applied` or a separately explained expected `no-op`.
- Re-run dry-run to prove the applied state is recognized.
- Compare counts and representative rows across all eight rewrite surfaces.
- Confirm no out-of-surface field changed.

```bash
go run ./cmd/pinakes-migrate \
  --db "$JK_WORKING" \
  --manifest rehearsal/issue15/namespace-manifest.csv \
  --authority jk \
  --acknowledge-copy \
  --apply \
  >"$REHEARSAL_ROOT/reports/jk-apply.json"
```

### 7.4 Invertibility spot-check

- Create a new inverse-test copy from the rewritten JK result.
- Generate/use the reviewed exact inverse manifest.
- Apply the inverse to that disposable copy.
- Compare selected records and counts with the pristine baseline; record the
  scope and result of the spot-check.

Generate the exact inverse once, then apply it only to a new copy of the
rewritten working database:

```bash
INVERSE_MANIFEST="$REHEARSAL_ROOT/working/inverse-manifest.csv"
test ! -e "$INVERSE_MANIFEST"
go run ./cmd/pinakes-manifest-invert \
  --manifest rehearsal/issue15/namespace-manifest.csv \
  --output "$INVERSE_MANIFEST"

JK_INVERSE="$REHEARSAL_ROOT/working/pinakes-jk-$REHEARSAL_UTC-inverse.db"
test ! -e "$JK_INVERSE"
install -m 600 "$JK_WORKING" "$JK_INVERSE"
go run ./cmd/pinakes-migrate \
  --db "$JK_INVERSE" \
  --manifest "$INVERSE_MANIFEST" \
  --authority jk \
  --acknowledge-copy \
  --apply \
  >"$REHEARSAL_ROOT/reports/jk-inverse-apply.json"
```

## 8. UCLA namespace rewrite rehearsal

Repeat the reset, dry-run, apply, post-apply comparison, and inverse spot-check
from section 7 using the UCLA pristine snapshot and `--authority ucla`.

Ludi first runs the flow against a generated UCLA synthetic fixture. The
fleet-side operator later repeats it against a disposable working copy of the
real UCLA snapshot and reports sanitized results on issue #15.

Explicitly confirm that neither authority relies on the other authority's
manifest rows for completeness and that no foreign-authority mutation occurs.

Use unique `UCLA_WORKING` and `UCLA_INVERSE` paths under `working/`; substitute
only the authority, pristine path, working paths, and report-name prefix in the
commands from section 7. Do not reuse either JK database path.

```bash
UCLA_WORKING="$REHEARSAL_ROOT/working/pinakes-ucla-$REHEARSAL_UTC-rewrite.db"
UCLA_INVERSE="$REHEARSAL_ROOT/working/pinakes-ucla-$REHEARSAL_UTC-inverse.db"
test ! -e "$UCLA_WORKING"
test ! -e "$UCLA_INVERSE"
test "$UCLA_WORKING" != "$UCLA_LIVE_DB"
test "$UCLA_WORKING" != "$UCLA_SNAPSHOT"
install -m 600 "$UCLA_SNAPSHOT" "$UCLA_WORKING"

go run ./cmd/pinakes-migrate \
  --db "$UCLA_WORKING" \
  --manifest rehearsal/issue15/namespace-manifest.csv \
  --authority ucla \
  --acknowledge-copy \
  >"$REHEARSAL_ROOT/reports/ucla-dry-run.json"
go run ./cmd/pinakes-migrate \
  --db "$UCLA_WORKING" \
  --manifest rehearsal/issue15/namespace-manifest.csv \
  --authority ucla \
  --acknowledge-copy \
  --apply \
  >"$REHEARSAL_ROOT/reports/ucla-apply.json"

install -m 600 "$UCLA_WORKING" "$UCLA_INVERSE"
go run ./cmd/pinakes-migrate \
  --db "$UCLA_INVERSE" \
  --manifest "$INVERSE_MANIFEST" \
  --authority ucla \
  --acknowledge-copy \
  --apply \
  >"$REHEARSAL_ROOT/reports/ucla-inverse-apply.json"
```

## 9. Isolated unified strict-mode boot rehearsal

### 9.1 Isolation and common configuration

- Use rehearsal-only ports, container/project names, network, volume/path,
  tokens, and credentials.
- Prevent production agents from discovering or registering with the rehearsal
  bus.
- Set `BUS_NAMESPACE_MODE=strict` and use only the staged post-migration
  allowlist.
- Set `CONTROL_PLANE_AGENTS=managerd`; confirm the empty/default configuration
  grants no strict-mode exception and no name is hardcoded in the bus.
- Joel provisions `CONTROL_PLANE_AGENT_SECRET_HASHES` in the fleet-side
  deploying environment. Missing, extra, malformed, or mismatched entries must
  stop startup; operators must not bypass this fail-closed condition.
- Record the exact Pinakes build/tag and rendered non-secret configuration.

### 9.2 Empty-store plan of record

- Start the isolated unified bus from a new empty database in strict mode.
- Do not start it from either rewritten archive. The namespace rewrites exist
  only as correctness/invertibility evidence and searchable historical
  archives.
- This empty-store plan was ratified on 2026-08-08 based on ruling 4 of issue
  #16, heartbeat-driven agent re-registration, and consistency with the
  same-day decision that the IP Agency bus resets fresh at its UCLA cutover.

### 9.3 Empty-store boot verification

- Verify health, empty baseline behavior, strict-mode rejection of unprefixed
  identities, and readiness for namespaced re-registration.
- Verify that no state from either authority or rewritten archive is present.
- Record startup time, health results, empty baseline counts, registry
  observations after representative re-registration, and operational risks.

The local synthetic proof uses the real SQLite store and HTTP handler with the
candidate allowlist and distinct fabricated credentials. It derives only
fabricated control-plane hashes internally; neither raw credentials nor hashes
appear in its report:

```bash
CONTROL_PLANE_AGENTS=managerd \
  go run ./cmd/pinakes-empty-strict-rehearsal \
  --db "$REHEARSAL_ROOT/empty/empty-strict-rehearsal.db" \
  --allowlist rehearsal/issue15/post-migration-allowlist.txt \
  --agents personal.jk-gmail-ingest,ucla.ucla-tdg-gmail-ingest,personal.jk-calendar-guard-agent,ucla.jk-calendar-guard-agent \
  >"$REHEARSAL_ROOT/reports/empty-strict-rehearsal.json"
```

Require `status=passed`, `empty_before_registration=true`, distinct synthetic
secrets, all three control-plane scopes, rejection of wrong and missing
control-plane credentials, and rejection of the unlisted unprefixed probe. For
fleet execution, also start the approved Pinakes build on an isolated
non-production port/network from a new empty database and repeat the same
registrations against its HTTP endpoint before recording success.

### 9.4 Decision evidence and execution-gate record

- Record that empty-store boot is the approved topology and that neither
  rewritten archive is a boot candidate.
- Confirm that both old authority snapshots and both rewritten archives will be
  retained on fleet machines for the approved retention period.
- Report any evidence that would block the empty-store plan, but do not reopen
  the ratified decision as an operator preference.

## 10. Representative cross-domain re-registration

Run against the isolated empty-store bus using rehearsal-only agents or
adapters and separate credentials. Ludi performs the local synthetic version;
the fleet-side operator performs the real-data rehearsal version.

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

- [ ] Empty-store strict-mode boot passed in isolation.
- [ ] Neither rewritten archive was used as the boot database.
- [ ] Unprefixed registration rejection proved.
- [ ] Representative personal and UCLA registrations proved.
- [ ] Both calendar-guard deployments registered with separate credentials.
- [ ] No production endpoint, database, allowlist, or agent was mutated.

## 12. Rehearsal reset and rollback procedures

### 12.1 Reset a failed namespace rehearsal

- Stop only the isolated process using the named working copy.
- Preserve the failed working-copy path, metadata-only reports, and sanitized
  logs for diagnosis. Create a newly timestamped working-copy path from the
  validated pristine snapshot; never reuse or overwrite the failed path.
- Reconfirm checksum, authority, manifest revision, and live-path inequality
  before retrying.

### 12.2 Reset a unified boot rehearsal

- Stop the isolated rehearsal project/process.
- Preserve its configuration summary and sanitized evidence.
- Remove/archive only the explicitly resolved disposable rehearsal database and
  create a new empty-store path.
- Reapply the rehearsal-only allowlist and credentials; never substitute the
  production allowlist path.

### 12.3 Production rollback point to recommend

- Retain both old bus deployments, both snapshots, and both rewritten archives
  on fleet machines through the approved rollback period.
- If the empty unified bus cutover fails, stop or isolate it, restore the prior
  agent endpoint configuration, and restart both old buses against their
  unchanged authority stores.
- Verify both old health endpoints and registry repopulation by expected
  identity. Snapshot restoration is not part of the normal rollback because
  the old authority databases were not rewritten or imported.
- Estimate and record the time required to restart both old buses and restore
  routing based on rehearsal evidence.

## 13. Recommendation and issue #15 report template

### 13.1 Rehearsal summary

- Date/time, operator, reviewer, Pinakes revision, tool versions.
- Sanitized snapshot identifiers/checksums and manifest revisions.
- JK and UCLA dry-run/apply/inverse outcomes.
- Empty-store strict-mode boot outcome.
- Representative re-registration outcomes.
- Deviations, refusals, unresolved risks, and evidence paths on Keystone.

### 13.2 Required recommendation

- Confirm whether the evidence supports the ratified empty-store strict-mode
  plan; identify any blocker rather than proposing a rewritten archive as the
  boot database.
- Recommend cutover order for allowlist staging, unified boot, agent endpoint
  changes, re-registration verification, and old-authority retirement.
- Document the quiet-window exposure: in-flight messages and the 24-hour
  idempotency window are the only transport state expected to blink when agents
  move to the empty unified bus.
- Recommend the rollback trigger, estimated time to restart both old buses and
  restore routing, and the retention period for both snapshots and rewritten
  archives.

### 13.3 Execution gate

- State explicitly: rehearsal completion is not production authorization.
- Request Joel's review and explicit authorization on issue #15.
- Stop. Do not redirect agents, mutate live databases/configuration, activate
  production strict mode, or retire either authority.

## 14. Parameters to resolve before fleet execution

- Joel-designated fleet-side operator account and reviewer/witness.
- Current JK/UCLA container names, DB paths, images, ports, networks, and
  volumes.
- Approved snapshot root, permissions, owner, retention period, and available
  space.
- Approved Pinakes revision containing the manifest, candidate allowlist,
  generator, migration tool, and rehearsal commands, with `77fb37f` verified as
  an ancestor.
- Joel-provisioned untracked control-plane hash configuration; its presence and
  identity coverage may be attested, but its value must not enter evidence.
- Manager-repository production allowlist path/revision to be changed only
  after the execution gate.
- Fleet-side real-data rehearsal isolation topology and non-production ports.
- Empty-store unified bus path/configuration and proof that it cannot resolve to
  either rewritten archive.
- Representative personal/UCLA agents and their rehearsal launch method.
- Calendar-guard split-deployment owners, configurations, and rehearsal-only
  credential provisioning method.
- Evidence reviewer, sanitization review, and issue #15 reporting format.
