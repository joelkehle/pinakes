---
summary: "Decision draft for migrating every configured and persisted Pinakes identity/resource to an explicit personal, UCLA, or separately approved shared namespace."
read_when:
  - Planning or reviewing Pinakes issue #16.
  - Changing BUS_NAMESPACE_MODE, BUS_LEGACY_SCOPE, agent IDs, allowlists, shared grants, or persisted identity fields.
  - Preparing any strict-mode or consolidation rehearsal.
status: accepted
---

# BUSFT Namespace Migration Draft

- Issue: [#16](https://github.com/joelkehle/pinakes/issues/16)
- Status: Accepted for planning only; no migration authorized ([maintainer ruling](https://github.com/joelkehle/pinakes/issues/16#issuecomment-5095983219))
- Prepared: 2026-07-25
- Maintainer: Joel Kehle
- Executor: Unassigned

## Decision requested

Approve or revise the mapping rules and identified cross-authority exceptions
below.
Approval of this document would authorize planning only. It would not authorize
consumer edits, allowlist changes, identity changes, persisted-state rewrites,
strict-mode activation, or bus consolidation.

## Inventory boundary

The inventory covered:

- the current manager-owned allowlist, including compatibility aliases;
- registrations returned by both live authorities;
- agent IDs, message senders, message targets, and conversation participants in
  read-only queries against each authoritative SQLite database; and
- static ownership evidence in the consumer repositories.

The inventory did not read or record message bodies, credentials, agent
secrets, callback URLs, attachments, or private metadata.

The complete one-row-per-authority/ID mapping contains operational identity
metadata and is maintained in the private manager repository. This public
document records the rules, exceptions, and decisions without publishing that
inventory.

## Current findings

1. The JK and UCLA authorities remain separate. Namespace migration does not
   imply or require consolidation.
2. Most persisted identities and resources are still unprefixed. Explicitly
   namespaced current identities also exist.
3. The allowlist mixes current identities with explicitly marked compatibility
   aliases. Compatibility entries should not automatically become permanent
   strict-mode identities.
4. Some identities have registration history on both authorities. Each needs an
   authority-specific decision instead of an automatic prefix.
5. Persisted history includes registrations absent from the current allowlist.
   Treat each as a retirement candidate unless an owning consumer demonstrates
   that it remains required.
6. No current identity can safely be renamed to `shared.*` merely because its
   implementation is shared. Under the current Pinakes contract, the identity
   prefix determines its one effective scope. A `shared.*` identity does not
   thereby gain access to both `personal.*` and `ucla.*`.
7. GitHub contribution-review automation appears only because it is a Pinakes
   transport client. Its workflow and implementation remain external to
   Pinakes development.

## Proposed deterministic rule

Preserve the complete legacy ID after adding its authority scope:

| Source classification | Target |
| --- | --- |
| Already `personal.*`, `ucla.*`, or `shared.*` | unchanged |
| Current identity/resource found only on the JK authority | `personal.<complete-source-id>` |
| Current identity/resource found only on the UCLA authority | `ucla.<complete-source-id>` |
| Compatibility alias with a confirmed current consumer | use the authority rule temporarily |
| Compatibility alias with no current consumer | retire; do not create a strict-mode target |
| Identity found on both authorities | use the exception table below |

Examples:

| Source | Proposed target |
| --- | --- |
| `legacy-personal-worker` | `personal.legacy-personal-worker` |
| `legacy-ucla-worker` | `ucla.legacy-ucla-worker` |
| `ucla.already-namespaced` | `ucla.already-namespaced` |
| a personal compatibility alias | retire, or temporarily apply the `personal.` rule if still required |
| a UCLA compatibility alias | retire, or temporarily apply the `ucla.` rule if still required |

The doubled-looking names are deliberate. Preserving the complete source ID
makes the first migration reversible, prevents accidental alias collapse, and
avoids combining a namespace migration with a product rename. Cleaner names can
be proposed later as ordinary identity migrations.

## Private mapping artifact

The private mapping expands every current allowlist entry plus the
persisted-only retirement candidate. Every authority/ID pair has one of these
dispositions:

- `migrate`;
- `unchanged`;
- `retire-unless-confirmed`;
- `retire-unless-owner-confirms`; or
- `decision-confirm-or-retire`.

The mapping covers the allowlist snapshot reviewed on 2026-07-25 and fails
validation if an unclassified source appears.

## Cross-authority exception mapping

Do not map a cross-authority identity to `shared.*`. For each private mapping
exception:

1. If both registrations are intentional, give the implementation one
   `personal.*` identity and one `ucla.*` identity.
2. If one registration is stale, retire that side rather than creating its
   target identity.
3. If one process cannot safely hold two identities, split its authority
   adapters before strict mode.

## Persisted-resource rewrite contract

A future migration tool must operate on a stopped copy or transactional
rehearsal copy, never directly on the live database. It must rewrite only:

- `agents.agent_id`;
- `messages.from_agent`;
- non-empty `messages.to_agent`;
- each exact identity string inside `conversations.participants`;
- `deliveries.target_agent_id`;
- `delivery_cursors.target_agent_id`;
- `idempotency.from_agent`;
- non-empty `idempotency.to_agent`; and
- identity-keyed policy/config entries in a separately reviewed change.

It must not search or replace message bodies, titles, metadata, attachments,
callback URLs, or arbitrary JSON text.

Amendment, 2026-07-27: the `deliveries`, `delivery_cursors`, and `idempotency`
columns were added to the surface after the WP2 durable-delivery schema landed;
the original list predates those tables. They are durable, load-bearing state
under the durability contract — pending deliveries, acknowledgment progress,
and the promised idempotency window — and must be rewritten, not cleared or
left bare. All other columns in those tables, including free-text
`deliveries.last_error`, remain outside the surface.

The tool must consume an explicit manifest with:

```text
source_authority, source_id, target_id, disposition, owner_repo, evidence, control_plane
```

Amendment, 2026-08-08: ruling 2's singular control-plane exception is
configuration-driven. The bus reads unprefixed trusted identities from
`CONTROL_PLANE_AGENTS`; those identities may register in strict mode and hold
`personal`, `ucla`, and `shared` scopes. Matching manifest rows remain
unprefixed and unchanged with `control_plane=true`, and the migration tool
requires each marked row to appear in the configured list. Runtime registration
also requires a matching configured SHA-256 secret digest in both namespace
modes; only the unprefixed exception and three-scope grant are strict-only. All
other rows use `control_plane=false`. No identity name is hardcoded in either
mechanism.

The disposition vocabulary is closed: `migrate`, `retire`, `split`,
`unchanged`. Any other value is a fail-closed refusal, not a new disposition;
conditional dispositions in earlier planning material are resolved to one of
these four before a manifest is cut.

`retire` requires a non-empty `target_id` — the identity's namespaced home
under the full-legacy-ID rule — and the tool rewrites its history rows and
`agents` row exactly as for `migrate`. Renaming persisted rows gives retired
history an explicit home; it creates no operational identity. No allowlist
entry, configuration, or credential is created for the target, which is what
"do not create a strict-mode target" forbids above. Retirement's operational
meaning (no future consumer; deregistration) is outside the tool, keeping it
rename-only with exact manifest inversion; the manifest row and the report's
per-disposition counts are the durable record of retire intent.

`split` is expressed as one manifest row per authority for the same legacy ID
— e.g. `(jk, id) → personal.id` and `(ucla, id) → ucla.id`. A run must declare
the authority of the database copy it operates on and select rows by
`(source_authority, source_id)`; each split row is then mechanically a
`migrate` against that copy. A single run never touches both authorities, and
an identity present in the copy with no manifest row under the declared
authority is a fail-closed refusal even if the other authority names it.
Credential provisioning and consumer adapter splitting remain outside the
tool.

It must fail closed, uniformly across every surface column, on:

- an unprefixed value absent from the manifest;
- two source identities mapping to one target;
- a target already occupied by another identity;
- mixed-scope conversation participants;
- an invalid participants JSON value; or
- any attempted rewrite outside the named identity columns.

Secrets stay attached to their source identity row during a same-authority
rename. A cross-authority split requires separately provisioned credentials;
copying one secret into two identities is not implied or approved.

## Proposed work sequence

1. Joel decides the identified exceptions and whether compatibility aliases
   retire.
2. Create opt-in consumer proposals by owning repository. No person is assigned
   without acceptance.
3. Update each consumer to make its target identity configurable and prove
   same-authority behavior in compatibility mode.
4. Prepare, but do not apply, coordinated allowlist and shared-grant changes.
5. Build a manifest-driven, dry-run-first database rewrite with collision
   reporting and metadata-only output.
6. Rehearse JK and UCLA independently in strict mode on copies.
7. Request a separate production approval for each authority.

Consolidation, if ever approved, is a later project. This plan deliberately
finishes explicit naming without joining the authorities.

## Decision checklist

- [ ] Preserve the full legacy ID after the scope prefix.
- [ ] Retire unneeded compatibility aliases instead of canonizing them.
- [ ] Confirm or retire each side of the identified cross-authority exceptions.
- [ ] Create no `shared.*` identity in this migration without a separate access
      contract.
- [ ] Keep production migration and consolidation behind separate approvals.
