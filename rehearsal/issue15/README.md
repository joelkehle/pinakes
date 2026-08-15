# Issue #15 rehearsal artifacts

Status: review candidates; not production configuration.

## Files

- `namespace-manifest.csv` contains the JK and UCLA authority manifests in one
  reviewed CSV. The combined form is intentional: the migration contract
  requires both authority rows for every `split` identity to be present in the
  same parsed manifest. Each run still selects only its declared authority.
- `post-migration-allowlist.txt` is the deterministic target allowlist produced
  from every `migrate`, `split`, and `unchanged` row. `retire` targets are
  intentionally excluded.
- `SYNTHETIC_REHEARSAL_RESULTS.md` records the sanitized local rehearsal result
  and the remaining review gates. Generated databases and raw reports stay in
  the operator-selected output directory and are not committed.

## Inputs and decisions

- Live registries captured on issue #15 on 2026-08-08: 26 identities per
  authority.
- Complete manager-owned allowlist posted verbatim on issue #15 on 2026-08-08.
- Namespace and cross-authority rulings ratified on issue #16.
- `jk-gcal-ingest` is the one persisted-only identity added beyond the current
  allowlist, per the issue #16 retirement ruling.
- `managerd` is unchanged and marked `control_plane=true` once per authority;
  every rehearsal run must use `CONTROL_PLANE_AGENTS=managerd`.
- `jk-calendar-guard-agent`, `jk-fable-operator`, and
  `local-intake-canary` have paired `split` rows.
- `project-invention-specialist-smoke-client` migrates on UCLA and retires on
  JK per the accepted exception ruling.
- Every entry explicitly labeled legacy or compatibility-window in the posted
  allowlist defaults to `retire` because no named consumer plus removal date
  was supplied. This includes the currently registered
  `ucla-tdg-github-review-agent` and `ucla-tdg-google-chat`; their live presence
  is recorded, but it does not satisfy the ratified two-part alias exception.

## Review points before fleet rehearsal

- Confirm the `owner_repo` classification derived from the allowlist sections,
  especially `managerd-pm`, the cross-authority canaries, and project-agent
  identities.
- Confirm that no legacy/compatibility identity has since acquired both a named
  consumer and a removal date. If one has, change its disposition from
  `retire` to `migrate` and regenerate the allowlist.
- Reconcile any identity present in a real snapshot but absent from this
  manifest. The migration tool will fail closed with `unknown_identity` rather
  than infer a mapping.

## Deterministic checks

Generate and compare the proposed allowlist:

```bash
go run ./cmd/pinakes-manifest-allowlist \
  --manifest rehearsal/issue15/namespace-manifest.csv
```

Run the complete local synthetic rehearsal from the repository root:

```bash
CONTROL_PLANE_AGENTS=managerd \
  ./scripts/rehearse-namespace-migration.sh \
  rehearsal/issue15/namespace-manifest.csv \
  /tmp/pinakes-issue15-rehearsal
```

The output directory must not already exist. It contains fabricated SQLite
databases and metadata-only JSON reports; it contains no fleet data. The script
uses `pinakes-migrate-compare` after each inverse apply and exits nonzero unless
the pristine and restored schemas and canonically ordered rows match across
every table.
