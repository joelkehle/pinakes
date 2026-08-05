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

`--acknowledge-copy` is an operator assertion, not a liveness check. The tool
performs no process, lock, journal/WAL, content, or filesystem liveness
heuristic.

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

`--authority` is binding. A run selects only manifest rows with that
`source_authority`, and a single run never touches both authorities. A JK copy
cannot use the UCLA half of a split pair to satisfy manifest completeness.

## Manifest CSV

The manifest is UTF-8 RFC 4180 CSV. Its header and order are exact:

```csv
source_authority,source_id,target_id,disposition,owner_repo,evidence
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

`(source_authority, source_id)` must be unique. Within one authority, two
different sources may not map to the same target.

For `migrate`, `retire`, and `split`, the complete legacy ID is preserved:

- JK: `legacy-id` and `personal.legacy-id`;
- UCLA: `legacy-id` and `ucla.legacy-id`.

The pair is accepted in either direction so an inverted manifest is an exact
undo plan. `unchanged` requires identical source and target values with an
existing `personal.`, `ucla.`, or `shared.` namespace.

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

## Preflight and refusal

All validation and database inspection completes under SQLite `query_only`
before apply executes its first update. Any refusal rolls back without a
write. The tool fails closed on:

- an identity not named as a source or already-applied target under the
  declared authority;
- duplicate targets;
- a source and its target both being present;
- mixed personal/UCLA conversation participants;
- invalid participants JSON;
- any manifest attempt to expand the rewrite surface.

An already-applied target is recognized only through a row selected for the
declared authority. This makes a second apply a no-op without weakening
authority isolation.

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
