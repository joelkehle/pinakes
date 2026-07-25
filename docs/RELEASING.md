---
summary: Human release checklist for Pinakes tags. v0.4.0 is the approved namespace compatibility and fail-closed rollout; earlier release lines are retained as history.
read_when:
  - preparing a new pinakes tag
  - reviewing the v0.4.0 namespace rollout or an earlier release line
  - coordinating downstream repo upgrades after a pinakes release
status: working draft
---

# Releasing

`pinakes` releases are tag-driven. Pushing a `v*` tag triggers the image build in `.github/workflows/release.yml`.

Do not tag or release without explicit Joel approval.

## v0.4.0 (approved release boundary)

`v0.4.0` is approved for release. This statement records approval and intended
contents; it does not claim that the tag, image, or any deployment is complete.

The release contains:

- namespace compatibility support for the existing JK and UCLA authorities;
- explicit rollout configuration for legacy identity and resource handling;
- fail-closed startup validation when a non-empty namespace rollout setting is
  invalid; unset settings retain the documented compatibility defaults;
- contract and operator documentation for the compatibility boundary.

This release does not consolidate the two authorities, implement the held WP4
identity design, change public exposure, or settle WP2 recovery semantics.

## v0.3.0 (history)

`v0.3.0` was deployed to both buses (UCLA `:8080`, JK `:8081`) on 2026-06-10.

What shipped on this line:

- Memory retention: time-based pruning of messages, conversations, and agents (`*_RETENTION_SECONDS`, `MESSAGE_MAX_AGE_SECONDS`).
- Per-agent and observe byte budgets (`MAX_INBOX_BYTES_PER_AGENT`, `MAX_OBSERVE_BYTES`) on top of the existing event-count caps.
- Request body caps.
- SQLite is now the default store backend (`STORE_BACKEND` defaults to `sqlite`; one-time JSON-state import runs on first boot).
- Graceful shutdown: SIGTERM/SIGINT drains in-flight requests within the grace window, then closes the store cleanly. Deploy-side `stop_grace_period` and `GOMEMLIMIT` were bumped in the consumer stacks to match (see `docs/REFACTOR_BACKLOG.md` item 6).

## v0.2.0 Baseline (history)

`v0.2.0` is the first passport-capable release line.
It is already released and was the first deployed passport baseline.

It is the clean dependency boundary for downstream repos to consume:

- richer agent registration payload support
- richer `GET /v1/agents` passport fields
- shared Go `RegisterAgentWithPassport(...)` client surface

## Pre-Tag Checklist

1. Confirm the repo gate passes via the validation entrypoint:
   - `gofmt -l .` returns no files
   - `go build ./...`
   - `go test ./...`
   - `agent-check`
   - run all checks on the exact commit intended for the tag
2. Confirm docs are in place and consistent:
   - `docs/BUS_HTTP_CONTRACT.md` — protocol contract; must reflect any protocol change in the release
   - `docs/JK-SPEC-BUSFT-001.md` and `docs/BUSFT-ISSUES.md` — rollout boundary and held decisions
   - `docs/REFACTOR_BACKLOG.md` — deferred items reviewed; anything shipped this line marked DONE
   - `docs/AGENT_CITIZENSHIP.md`
   - `docs/PROTOCOL_DELTA.md`
   - `docs/PHASE5_PASSPORT_ROLLOUT.md`
3. Confirm `README.md` reflects the release boundary and required rollout
   configuration.
4. Review `git status --short`, `git diff`, and the candidate commit; ensure the
   worktree has no unintended uncommitted changes.
5. Get explicit Joel approval to tag.

## Tag And Publish

After the candidate commit passes the local gate:

```bash
git tag -a v0.4.0 -m "Release v0.4.0"
git push origin v0.4.0
```

This triggers the GHCR image build for:

- `ghcr.io/joelkehle/pinakes:v0.4.0`

## Post-Tag Verification

Release verification uses the published artifact, not GitHub Actions:

1. Confirm the remote tag resolves to the locally validated release commit:

   ```bash
   git ls-remote --tags origin refs/tags/v0.4.0
   git ls-remote --tags origin 'refs/tags/v0.4.0^{}'
   git rev-list -n 1 v0.4.0
   ```

2. Confirm the tagged GHCR manifest is published:

   ```bash
   docker manifest inspect ghcr.io/joelkehle/pinakes:v0.4.0
   ```

3. Pull and inspect the tagged image. Confirm its recorded source revision, when
   present, matches the validated release commit:

   ```bash
   docker pull ghcr.io/joelkehle/pinakes:v0.4.0
   docker image inspect ghcr.io/joelkehle/pinakes:v0.4.0
   ```

4. Record tag, image digest, source revision, and local gate result in the
   release handoff before any deployment consumes the image.

Deployment is a separate step with its own acceptance and rollback checks.
Publishing or verifying `v0.4.0` does not by itself mean either authority was
upgraded.

## Downstream Follow-On

After a release is published and each authority is separately accepted on that
line:

1. `ucla-tdg/ucla-tdg-email-triage`
   - pin `github.com/joelkehle/pinakes` to the accepted compatible release
   - keep using `RegisterAgentWithPassport(...)`
2. `ucla-tdg/ucla-tdg-ip-agents`
   - pin `github.com/joelkehle/pinakes` to the accepted compatible release
   - remove the local/vendored passport client workaround
3. Continue Phase 5 with capability docs in both repos.

Older buses may tolerate extra registration fields without reliably persisting
or returning them; that compatibility behavior is not the clean passport
steady state.
