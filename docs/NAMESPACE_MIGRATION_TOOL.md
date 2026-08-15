---
summary: "Manifest schema, safety gates, usage, and metadata-only report contract for the Pinakes namespace migration rehearsal tool."
read_when:
  - Preparing or reviewing a namespace migration rehearsal.
  - Editing cmd/pinakes-migrate or internal/nsmigrate.
status: active
---

# Namespace Migration Rehearsal Tool

`pinakes-migrate` performs the rename-only SQLite transform approved in
`BUSFT_NAMESPACE_MIGRATION_DRAFT.md`. It is for synthetic fixtures and
separately authorized stopped or transactional database copies. It must never
be pointed at a live Pinakes database.

The command defaults to dry-run. Apply requires both `--apply` and the
copies-only acknowledgment; every run requires the acknowledgment:

`--acknowledge-copy` is an operator assertion, not a general liveness check.
Apart from the competing-writer check described below, the tool performs no
process, journal/WAL, content, or filesystem liveness heuristic.

```bash
go run ./cmd/pinakes-migrate \
  --db /path/to/stopped-copy.db \
  --manifest /path/to/manifest.csv \
  --authority jk \
  --acknowledge-copy

go run ./cmd/pinakes-migrate \
  --db /path/to/stopped-copy.db \
  --manifest /path/to/manifest.csv \
  --authority jk \
  --acknowledge-copy \
  --apply
```

If the selected manifest contains a control-plane row, set the reviewed
deployment policy for every dry-run and apply:

```bash
export CONTROL_PLANE_AGENTS=managerd
```

`--authority` is binding. A run selects only manifest rows with that
`source_authority`, and a single run never touches both authorities. A JK copy
cannot use the UCLA half of a split pair to satisfy manifest completeness.

## Manifest CSV

The manifest is UTF-8 RFC 4180 CSV. Its header and order are exact:

```csv
source_authority,source_id,target_id,disposition,owner_repo,evidence,control_plane
```

Extra, missing, reordered, or duplicate columns are refused. Every field is
required and must not have surrounding whitespace. Identity fields must not
contain control characters.

- `source_authority`: exactly `jk` or `ucla`.
- `source_id`: identity before this direction of the transform.
- `target_id`: identity after this direction of the transform.
- `disposition`: exactly `migrate`, `retire`, `split`, or `unchanged`.
- `owner_repo`: owning repository recorded for review.
- `evidence`: synthetic or private-manifest evidence recorded for review.
- `control_plane`: exactly `true` or `false`.

`(source_authority, source_id)` must be unique. Within one authority, two
different sources may not map to the same target.

For `migrate`, `retire`, and `split`, the complete legacy ID is preserved:

- JK: `legacy-id` and `personal.legacy-id`;
- UCLA: `legacy-id` and `ucla.legacy-id`.

The pair is accepted in either direction so an inverted manifest is an exact
undo plan. An ordinary `unchanged` row requires identical source and target
values with an existing `personal.`, `ucla.`, or `shared.` namespace.

A control-plane exception is represented only by an unprefixed `unchanged` row
with identical source and target IDs and `control_plane=true`. Marked rows are
cross-validated against `CONTROL_PLANE_AGENTS` for the selected authority:
every marked identity must be configured. Configured identities without a row
for the selected authority do not couple that authority's manifest to future
runtime policy. A mismatch refuses before the database is opened. All other
rows use `control_plane=false`; a marker on a rename or namespaced identity is
refused.

An authority-scoped manifest may enumerate an identity from the other
authority only as `unchanged`, with `target_id` equal to `source_id`. The row
acknowledges that identity for manifest completeness but never mutates it.
Any other attempted foreign-authority mapping is refused with
`foreign_authority_mutation`. `shared.*` identities remain separately
permitted.

`retire` and `split` are mechanically identical to `migrate`. Their
dispositions remain visible in the report; operational retirement, consumer
adapter splitting, and credential provisioning are outside this tool.

## Closed rewrite surface

The tool parses and may update only:

1. `agents.agent_id`
2. `messages.from_agent`
3. non-empty `messages.to_agent`
4. exact string elements in `conversations.participants`
5. `deliveries.target_agent_id`
6. `delivery_cursors.target_agent_id`
7. `idempotency.from_agent`
8. non-empty `idempotency.to_agent`

All other fields are outside the surface. Message bodies, titles, metadata,
attachments, callback URLs, agent secrets, and `deliveries.last_error` are
never selected, searched, copied, or rewritten. Updating `agents.agent_id`
keeps every other value, including the existing secret, on that same row.

Conversation participants must be a JSON array of strings. Rewrites use exact
element equality, never text replacement. A resulting conversation may contain
one authority scope plus `shared.*`, but never both `personal.*` and `ucla.*`.
Rewritten conversation participants are serialized as compact canonical JSON;
untouched rows keep their exact original bytes.

## Preflight and refusal

Dry-run opens the database with `mode=ro`. Apply uses one `BEGIN IMMEDIATE`
read-write transaction: all validation and database inspection completes
before the first update, and any refusal rolls back without a write. The eight
manifest/database-content preflight refusal conditions are:

- an identity not named as a source or already-applied target under the
  declared authority;
- duplicate targets;
- a source and its target both being present;
- mixed personal/UCLA conversation participants;
- invalid participants JSON;
- any manifest attempt to expand the rewrite surface;
- any attempted mutation of a foreign-authority identity.
- any marked control-plane row absent from `CONTROL_PLANE_AGENTS`.

An already-applied target is recognized only through a row selected for the
declared authority. This makes a second apply a no-op without weakening
authority isolation.

### Apply lock acquisition

A `database_busy` refusal means another process held a write lock at apply
time. `--acknowledge-copy` remains the operator's responsibility;
`BEGIN IMMEDIATE` catches only a competing writer at lock acquisition time,
not an idle process with the database open.

## Report

Standard output is a deterministic, metadata-only JSON report. It contains:

- report version, mode, authority, and status;
- selected manifest-row counts by disposition;
- planned mapping counts by disposition;
- rewrite counts for each of the eight surfaces;
- total rewrites and already-applied mapping count;
- collision entries containing only manifest identities and row numbers;
- stable refusal codes and non-secret reasons.

The `conversations.participants` rewrite count is the number of participant
identity occurrences across all conversations, not the number of conversation
rows.

Statuses are `ready` (successful dry-run), `applied`, `no-op`, or `refused`.
The report never includes message content, secrets, private metadata,
attachments, callback URLs, evidence text, or free-text delivery errors.

## Synthetic fixture rehearsal

`pinakes-migrate-fixture` creates a new schema-true SQLite database through the
real Pinakes store API. It derives identities from one authority's manifest
rows and generates only fabricated registrations, secrets, conversations,
messages, deliveries, cursors, and idempotency receipts. It refuses to
overwrite an existing destination.

`pinakes-manifest-invert` writes an exact, revalidated inverse manifest to a new
file. `pinakes-manifest-allowlist` deterministically emits the non-retired
target identities. `pinakes-migrate-compare` compares schema and canonically
ordered rows for every table without emitting row content or digests; any
difference exits nonzero. The repository script combines these tools into the
local dry-run/apply/inverse rehearsal:

```bash
CONTROL_PLANE_AGENTS=managerd \
  ./scripts/rehearse-namespace-migration.sh \
  rehearsal/issue15/namespace-manifest.csv \
  /tmp/pinakes-issue15-rehearsal
```

This script is for locally generated fixtures only. Fleet-side operators use
the runbook and real snapshot working copies that never leave fleet machines.
