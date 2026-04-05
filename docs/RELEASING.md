---
summary: Human release checklist for pinakes tags plus the settled v0.2.0 passport baseline note.
read_when:
  - preparing a new pinakes tag
  - reviewing the v0.2.0 passport baseline
  - coordinating downstream repo upgrades after a pinakes release
status: working draft
---

# Releasing

`pinakes` releases are tag-driven. Pushing a `v*` tag triggers the image build in `.github/workflows/release.yml`.

Do not tag or release without explicit Joel approval.

## v0.2.0 Baseline

`v0.2.0` is the first passport-capable release line.
It is already released, and the shared bus runtime is deployed on that line.

It is the clean dependency boundary for downstream repos to consume:

- richer agent registration payload support
- richer `GET /v1/agents` passport fields
- shared Go `RegisterAgentWithPassport(...)` client surface

## Pre-Tag Checklist

1. Confirm repo gate passes:
   - `go test ./...`
2. Confirm docs are in place and consistent:
   - `docs/AGENT_CITIZENSHIP.md`
   - `docs/BUS_HTTP_CONTRACT.md`
   - `docs/PROTOCOL_DELTA.md`
   - `docs/PHASE5_PASSPORT_ROLLOUT.md`
3. Confirm `README.md` reflects the release boundary:
   - downstreams start clean passport adoption at `v0.2.0+`
4. Review the worktree and ensure the intended release contents are present.
5. Get explicit Joel approval to tag.

## Tag And Publish

Historical command sequence for the `v0.2.0` baseline:

```bash
git tag v0.2.0
git push origin v0.2.0
```

This triggers the GHCR image build for:

- `ghcr.io/joelkehle/pinakes:v0.2.0`

## Post-Tag Verification

1. Confirm the GitHub release workflow succeeds.
2. Confirm the image is published on the expected tag line.
3. Confirm the shared bus runtime is deployed on the same release line before downstreams rely on the richer passport fields operationally.
   - This is already true for `v0.2.0`.

Migration nuance:

- older bus releases tolerate extra registration fields but do not persist/echo them reliably
- that is compatibility behavior, not the clean steady state
- the clean steady state begins once the shared runtime is on `v0.2.0+`

## Downstream Follow-On

With `v0.2.0` published and the shared bus runtime deployed on that line:

1. `ucla-tdg/ucla-tdg-email-triage`
   - pin `github.com/joelkehle/pinakes` to `v0.2.0+`
   - keep using `RegisterAgentWithPassport(...)`
2. `ucla-tdg/ucla-tdg-ip-agents`
   - pin `github.com/joelkehle/pinakes` to `v0.2.0+`
   - remove the local/vendored passport client workaround
3. Continue Phase 5 with capability docs in both repos.

## Out Of Scope

- rollback automation
- downstream capability-doc authoring details
- `jk/jk-email-agents` manager adoption
