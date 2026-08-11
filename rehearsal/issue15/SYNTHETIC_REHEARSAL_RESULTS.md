# Issue #15 synthetic rehearsal results

Date: 2026-08-11

Data classification: fabricated content only; no fleet data or credentials

Status: local proof passed; fleet rehearsal and production gate remain pending

## Inputs

- Combined manifest: `rehearsal/issue15/namespace-manifest.csv`
- Candidate allowlist: `rehearsal/issue15/post-migration-allowlist.txt`
- Runtime control-plane policy: `CONTROL_PLANE_AGENTS=managerd`
- Rehearsal entrypoint: `scripts/rehearse-namespace-migration.sh`
- Baseline: v0.4.1 commit `77fb37f` verified as an ancestor of the rehearsal
  branch before the run

The manifest contains 132 authority rows: 44 JK and 88 UCLA. The generated
allowlist contains 92 identities, excluding every `retire` row.

## Results

| Proof | Result |
| --- | --- |
| JK dry-run | `ready`; 44 manifest rows; 387 rewrites across all eight surfaces |
| JK apply | `applied` |
| JK inverse apply | `applied`; restored dry-run returned `ready` |
| UCLA dry-run | `ready`; 88 manifest rows; 765 rewrites across all eight surfaces |
| UCLA apply | `applied` |
| UCLA inverse apply | `applied`; restored dry-run returned `ready` |
| Exact invertibility | Passed: the fail-closed logical comparator matched schema and canonically ordered rows across all nine tables for both authorities |
| Empty strict store | `passed`; empty before registration |
| Representative registration | Both authority prefixes and both calendar-guard deployments registered with distinct fabricated secrets |
| Strict-mode negative probe | Unlisted unprefixed identity rejected |
| Control-plane credential binding | Wrong and missing credentials were rejected before registration state changed |
| Control plane | Configured `managerd` registered unprefixed and received `personal`, `ucla`, and `shared` scopes |
| Candidate allowlist regeneration | Exact match |
| Repository validation | `go vet ./...` and `go test ./...` passed |

## Remaining gates

- Review the manifest ownership classifications and every compatibility-ID
  retirement called out in `rehearsal/issue15/README.md`.
- Joel or a designated fleet-side operator must execute the real snapshot and
  real-data rehearsal from the runbook and publish sanitized results.
- A passed rehearsal is not production authorization. Production execution
  remains blocked until Joel explicitly approves it on issue #15.

Generated synthetic databases and JSON evidence were written under
`/tmp/pinakes-issue15-rerun-20260811` for this run and are intentionally not
part of the repository deliverables. The comparison reports contain table
names and equality status only, never row content or digests.
