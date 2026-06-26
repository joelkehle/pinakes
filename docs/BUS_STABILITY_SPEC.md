# Bus Stability Improvements

## Handoff

- Ball with: Joel / orchestrator for the live cutover
- Blocking question: none
- Status: Fixes 1, 2, and 3 are implemented in `shared/pinakes`
- Next action: consumer-repo edits plus live cutover per `deploy/README.md`

## Problem

Three compose stacks share one pinakes bus. Adding a new agent to the allowlist used to require restarting the bus, which forced all agents to re-register because agent HMAC secrets were memory-only. The allowlist restart path was removed by hot reload, and the secret-loss root cause is fixed by the 2026-06 security-hardening Phase 1 persistence change. The remaining stability risk is Compose v1 `ContainerConfig` cascading instability during stack work.

## Three fixes, in priority order

### Fix 1: Hot-reloadable allowlist (IMPLEMENTED)

**Goal:** Add agents to the allowlist without restarting the bus.

**Current state:** `AGENT_ALLOWLIST` env var parsed once at startup in `pkg/httpapi/server.go:NewServer()`. Stored in `agentAllowset map[string]struct{}` protected by RWMutex.

**Approach:** Add file-based allowlist that the bus watches for changes.

**Implementation:**

1. New env var: `ALLOWLIST_FILE` (optional). Path to a text file with one agent ID per line. If set, takes precedence over `AGENT_ALLOWLIST` env var.

2. **Startup behavior:**
   - If `ALLOWLIST_FILE` is set and the file is readable: load it, populate `agentAllowset`.
   - If `ALLOWLIST_FILE` is set but the file is unreadable or missing: **fail startup with a clear error.** Do NOT fall back to env var. A configured but broken file path is a misconfiguration, not a graceful degradation case.
   - If `ALLOWLIST_FILE` is not set: use `AGENT_ALLOWLIST` env var (existing behavior, no breaking change).

3. **Runtime reload behavior:**
   - Watch the **parent directory** of `ALLOWLIST_FILE` (not the file itself) using `fsnotify`. On Write, Create, Rename, or Chmod events matching the target filename, re-read the file and swap the allowset under write lock.
   - Watching the parent directory handles Docker bind-mount atomic replace and editor save-rename patterns correctly.
   - If reload fails (file unreadable, parse error): **keep last-good allowset.** Log the error at WARN level. Do not fail open, do not crash.
   - On successful reload: log the delta (added/removed agent IDs) at INFO level.

4. **Removal semantics:** Removing an agent ID from the file means **block future registration only.** Already-registered agents continue working until their TTL expires and they attempt to re-register. No immediate eviction, no mid-session disruption.

5. The RWMutex infrastructure already exists (`server.go:25`). `isAgentAllowed()` already uses RLock. The write path just needs to acquire a write lock, rebuild the map, and release.

**File format:**
```
# One agent ID per line. Comments and blank lines ignored.
tta-operator
tta-patent-extractor
gmail-ingest
triage-intake
# ...
```

**Allowlist file location:** Source of truth lives in `~/Projects/shared/manager/ops/config/allowlist.txt`, git-tracked alongside other ops config. Bind-mounted into the bus container. NOT in a Docker volume.

**Changes:**
- `pkg/httpapi/server.go` — add `loadAllowlistFile()`, `watchAllowlistFile()` methods
- `cmd/pinakes/main.go` — read `ALLOWLIST_FILE` env var, start watcher goroutine if set
- `go.mod` — add `github.com/fsnotify/fsnotify` dependency
- `docs/BUS_HTTP_CONTRACT.md` — document `ALLOWLIST_FILE` behavior, startup semantics, reload semantics, removal semantics

**Deploy change:** Bind-mount the manager ops config directory:

```yaml
bus:
  volumes:
    - bus-data:/data
    - /home/joelkehle/Projects/shared/manager/ops/config:/etc/pinakes:ro
  environment:
    - ALLOWLIST_FILE=/etc/pinakes/allowlist.txt
```

To add an agent: edit `allowlist.txt` in `~/Projects/shared/manager`, commit, bus picks it up automatically. No restart.

**Tests:**
- Unit test: startup with valid file — allowset populated correctly
- Unit test: startup with configured but missing file — startup fails
- Unit test: startup with no `ALLOWLIST_FILE` — falls back to env var
- Unit test: runtime reload adds new agent — `isAgentAllowed` returns true
- Unit test: runtime reload removes agent — `isAgentAllowed` returns false for NEW registrations, but does NOT evict already-registered agents
- Unit test: runtime reload with malformed file — keeps last-good set, logs error
- Unit test: file with comments and blank lines — handled gracefully
- Unit test: parent-dir watch fires on atomic rename (simulating editor save pattern)

**Verify:** Add an agent ID to the allowlist file while bus is running. Agent should be able to register without bus restart.

### Fix 2: Separate bus compose stack (IMPLEMENTED)

**Status:** Implemented (2026-06-10) in `deploy/docker-compose.yml`, `deploy/.env.example`, and `deploy/README.md`.

**Verified corrections vs the original planning example:**

- External network is `tta-agentnet`, configured as `external: true` with `name: ${STACK_NETWORK_NAME:-tta-agentnet}`. It is not `ucla-tdg-agentnet`.
- Bus state reuses the live Docker volume `deploy_bus-data`, declared `external: true`. Do not create a new volume.
- The allowlist mount is the manager config directory, `/home/joelkehle/Projects/shared/manager/ops/config:/etc/pinakes:ro`, not a file mount. Directory mounting preserves hot reload on atomic renames.
- The full live env set is carried: `PORT`, `GOMEMLIMIT=1536MiB`, `DB_PATH`, `STATE_FILE`, `ALLOWLIST_FILE`, `INJECT_TOKENS`, and `OBSERVE_TOKENS`.
- Runtime guards are carried: `mem_limit: 2g`, `stop_grace_period: 15s`, and the healthcheck keeps `wget --spider -q`.
- Consumer-side edits are tracked separately: remove the `bus` service and `depends_on: bus` blocks from `ucla-tdg-ip-agents`, and declare `agentnet` external in consumer stacks. Those edits are not part of this pinakes-repo change/commit.

**Goal:** Decoupling the bus lifecycle from any agent stack so agent deploys never risk bouncing the bus.

**Current state:** Bus is defined in `ucla-tdg/ucla-tdg-ip-agents/deploy/docker-compose.yml` alongside 7 IP agents. Three stacks (`ucla-tdg/ucla-tdg-ip-agents`, `jk/jk-email-agents`, `ucla-tdg/ucla-tdg-email-triage`) all depend on this bus container.

**Prerequisite:** Fix 3 (Compose v2) should land first or simultaneously. This is compose-stack surgery — do it on v2, not v1.

**Implementation:**

1. Create `~/Projects/shared/pinakes/deploy/docker-compose.yml` — bus-only stack. Done.

**Volume migration:** The existing bus-data volume is named `deploy_bus-data` (compose prefixes project name). The new stack must reference it as external with the same name to reuse existing state. Do NOT create a new volume — that would fork bus state.

2. Remove `bus` service from `ucla-tdg/ucla-tdg-ip-agents/deploy/docker-compose.yml`. Remove `depends_on: bus` from all agents. Drop `network_mode` - use external network only:

3. Update all three consumer stacks to declare the network as external:

```yaml
networks:
  agentnet:
    external: true
    name: ${STACK_NETWORK_NAME:-tta-agentnet}
```

4. Update `deploy/.env.example` in all three repos to remove bus config (it lives in `shared/pinakes/deploy` now).

**Agent retry behavior:** Before implementing, confirm that all three agent repos retry registration on bus unavailability. Check:
- `ucla-tdg/ucla-tdg-ip-agents` - operator bridge heartbeat loop (`internal/operator/bridge.go`)
- `jk/jk-email-agents` - agent main loops
- `ucla-tdg/ucla-tdg-email-triage` - triage-intake and adapter main loops

All should already retry via heartbeat (60s interval), but verify before removing `depends_on`.

**Deploy order:** Follow `deploy/README.md`. The live cutover must free host port `:8080` from the old IP-agents stack before bringing up this standalone stack, then verify health and registry.

**Verify:**
- Stop and restart `ucla-tdg/ucla-tdg-ip-agents` agents without touching the bus
- Bus stays up, all agents from other stacks stay registered
- New agent deploys in any stack don't affect the bus

### Fix 3: Compose v2 migration (DONE)

**Status:** Done. `docker compose version` confirms Docker Compose v2.27.0 installed on the host.

**Goal:** Eliminate the `ContainerConfig` KeyError bug.

**Previous state:** The host used `docker-compose` v1 (Python, 1.29.2). The compose files already used v2 syntax. The bug was in the Python client, not the file format.

**Prerequisite for Fix 2.** Do this first or at the same time as Fix 2.

**Implementation:**

1. Install Docker Compose v2 plugin if not already present:
```bash
docker compose version

# If not present:
sudo apt-get update && sudo apt-get install docker-compose-plugin
```

2. Update all documentation and scripts that reference `docker-compose` to use `docker compose` (space, not hyphen).

3. Files to update:
   - `ucla-tdg/ucla-tdg-ip-agents/deploy/.env.example`
   - `ucla-tdg/ucla-tdg-ip-agents/AGENTS.md`
   - `ucla-tdg/ucla-tdg-ip-agents/README.md`
   - `jk/jk-email-agents/README.md`
   - `ucla-tdg/ucla-tdg-email-triage/README.md`
   - `ucla-tdg/ucla-tdg-email-triage/docker-compose.yml`

4. Do NOT move or alias the old `docker-compose` binary until all host automation (cron, systemd, scripts) has been audited. Just stop using it in docs and manual operations.

**Verify:**
- `docker compose version` shows v2
- `docker compose up -d` in all three stacks works without `ContainerConfig` errors
- Full cycle: `docker compose down && docker compose up -d` with no cascading failures

## Revised implementation order

1. **Hot-reloadable allowlist (Fix 1)** — implemented in pinakes.
2. **Compose v2 migration (Fix 3)** — done on the host; Docker Compose v2.27.0 confirmed.
3. **Separate bus stack (Fix 2)** — implemented in pinakes; consumer repo edits and live cutover remain.

## What NOT to do

- Don't add an admin HTTP API for allowlist management. File-based is simpler, auditable, and doesn't need auth.
- Don't move to Kubernetes. Docker Compose on beelink is the right scale.
- Don't add service mesh or discovery. The bus IS the discovery layer.
- Don't fail open on misconfiguration. Configured file path must be valid at startup.
- Don't evict live agents on allowlist removal. Block future registration only.
- Don't move the old `docker-compose` binary until host automation is audited.

## Codex review findings — resolved

| Finding | Resolution |
|---------|-----------|
| Fail-open on missing file | Fixed: configured but missing file fails startup. Runtime reload keeps last-good. |
| Removal semantics undefined | Defined: block future registration only, no immediate eviction. |
| File watch brittleness | Fixed: watch parent directory, reopen target on events. |
| Volume fork risk | Fixed: new stack references existing volume as external with explicit name. |
| Consumer retry assumption | Added: must verify retry behavior in all three repos before removing `depends_on`. |
| Rollout order wrong | Fixed: Compose v2 before bus-stack separation. |
| `docker-compose` binary move | Fixed: don't move until automation audited. |
| Contract update needed | Added: `docs/BUS_HTTP_CONTRACT.md` update required. |

---

*Analysis: Claude (tdg-ip-agents context, 2026-03-30)*
*Reviewed: Codex (pinakes repo, 2026-03-30)*
*Implementation: Codex (pinakes repo)*
