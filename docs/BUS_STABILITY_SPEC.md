# Bus Stability Improvements

## Handoff

- Ball with: Codex (in `shared/pinakes`)
- Blocking question: none (Codex review findings addressed below)
- Next action: implement fix 1 (hot-reloadable allowlist)

## Problem

Three compose stacks share one pinakes bus. Adding a new agent to the allowlist used to require restarting the bus, which forced all agents to re-register because agent HMAC secrets were memory-only. The allowlist restart path was removed by hot reload, and the secret-loss root cause is fixed by the 2026-06 security-hardening Phase 1 persistence change. The remaining stability risk is Compose v1 `ContainerConfig` cascading instability during stack work.

## Three fixes, in priority order

### Fix 1: Hot-reloadable allowlist

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

**Deploy change:** Bind-mount from manager ops config:

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

### Fix 2: Separate bus compose stack

**Goal:** Decoupling the bus lifecycle from any agent stack so agent deploys never risk bouncing the bus.

**Verified production state (audit generated 2026-06-23T00:49:45Z):** UCLA and
JK are separate operational domains, not one shared runtime.

| Domain | Current Compose project | Host port | Network | State volume |
| --- | --- | --- | --- | --- |
| UCLA | `deploy` | `8080` | `tta-agentnet` | `deploy_bus-data` |
| JK | `jk-email-agents` | `8081` | `jk-email-agents_default` | `jk-email-agents_jk-bus-data` |

Both bus containers were running `ghcr.io/joelkehle/pinakes:v0.3.0`. Re-run the
read-only inventory before cutover because production may have changed after
the audit. The standalone compose file must also carry over the current live
runtime settings: `INJECT_TOKENS`, `OBSERVE_TOKENS`, `GOMEMLIMIT`, Docker
memory limit, and `stop_grace_period`.

`tta-agentnet` also carries the `langfuse-stack` services. Langfuse does not
require direct bus connectivity. Fix 2 must nevertheless reuse this existing
network, preserve the `bus` network alias, and must not recreate or split the
network as an incidental part of the bus move.

The audit found legacy networks (`agent-bus-v2_agentnet`,
`techtransfer-agency_agentnet`, `tta-agentnet-repair-17615`,
`tta-agentnet-smoke`) and similarly named legacy volumes
(`agent-bus-v2_bus-data`, `techtransfer-agency_bus-data`). They are out of scope
and must not be referenced or deleted without a separate cleanup plan and
explicit Joel approval.

**Prerequisite:** Fix 3 (Compose v2) should land first or simultaneously. This is compose-stack surgery — do it on v2, not v1.

**Implementation:**

1. Run a read-only host inventory:
   - confirm `docker compose version` is v2
   - list running bus containers, images, ports, Compose project labels,
     mounts, and attached networks
   - identify each consumer stack and the DNS name it uses for its bus
   - identify the exact existing state volume for each bus
   - audit cron, systemd, and scripts for `docker-compose` v1 usage

2. Create `~/Projects/shared/pinakes/deploy/docker-compose.yml` — a reusable
bus-only stack definition. Host-specific values must be required rather than
silently defaulted:

```yaml
services:
  bus:
    image: ghcr.io/joelkehle/pinakes:${PINAKES_TAG:?set an approved released tag}
    ports:
      - "${BUS_PORT:?set from host inventory}:8080"
    volumes:
      - bus-data:/data
      - ${MANAGER_CONFIG_DIR:?set manager config directory}:/etc/pinakes:ro
    environment:
      ALLOWLIST_FILE: /etc/pinakes/allowlist.txt
      DB_PATH: /data/bus.db
      GOMEMLIMIT: ${GOMEMLIMIT:?set to match current live bus container}
      INJECT_TOKENS: ${INJECT_TOKENS:?set from current live bus container; do not commit real tokens}
      OBSERVE_TOKENS: ${OBSERVE_TOKENS:?set from current live bus container; do not commit real tokens}
      STATE_FILE: /data/state.json
    mem_limit: ${BUS_MEMORY_LIMIT:?set to match current live bus container}
    stop_grace_period: 15s
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:8080/v1/health"]
      interval: 5s
      timeout: 3s
      retries: 5
    networks:
      agentnet:
        aliases:
          - bus
    restart: unless-stopped

volumes:
  bus-data:
    name: ${BUS_DATA_VOLUME:?set exact existing volume name}
    external: true

networks:
  agentnet:
    name: ${STACK_NETWORK_NAME:?set exact existing network name}
    external: true
```

**Volume migration:** Do not assume the volume is named `deploy_bus-data`.
Inspect the running bus and use its exact existing volume name. The new stack
must reference that volume as external. Do NOT create a new volume — that would
fork bus state. Stop the old bus cleanly and back up the volume before starting
the new project.

**Token migration:** `INJECT_TOKENS` and `OBSERVE_TOKENS` are required runtime
secrets. They must be supplied from the current live bus configuration at
config-check and deploy time, but must not be committed to `.env.ucla`,
`.env.jk`, git history, PR comments, or logs. If these variables are omitted,
`/v1/inject`, human-created conversations, and token-based `/v1/observe` fail
closed even though health checks can still pass.

3. Do not run `docker compose down` on the old agent project during cutover.
Stop only its bus service, preserving its containers, volume, and network for
fast rollback:

```bash
docker compose -f <old-compose-file> -p <old-project> stop bus
```

4. Start the independent bus project with its confirmed domain-specific env
file and project name:
   - UCLA: `.env.ucla`, project `pinakes-ucla`, port `8080`,
     network `tta-agentnet`, volume `deploy_bus-data`
   - JK: `.env.jk`, project `pinakes-jk`, port `8081`,
     network `jk-email-agents_default`, volume
     `jk-email-agents_jk-bus-data`

Migrate and accept one domain completely before starting the other.

5. After the new bus is healthy, remove the `bus` service from each consumer
Compose file. Remove `depends_on: bus` from all agents. Drop `network_mode` and
use only the confirmed external network:

Each consumer stack must declare the confirmed network as external:

```yaml
networks:
  agentnet:
    external: true
    name: ${STACK_NETWORK_NAME:?set from host inventory}
```

6. Update `deploy/.env.example` in all three repos to remove bus config (it
lives in `shared/pinakes/deploy` now).

**Agent retry behavior:** Recovery behavior is mixed:

- UCLA IP agents and most JK agents retry registration and heartbeat
  in-process.
- Some UCLA email-triage daemons rely on Docker restart if their initial
  registration or poll fails.

Keep the UCLA `:8080` handoff as short as possible. After cutover, verify
registry reconvergence by expected agent identity, not only by total count,
because live counts change over time. Confirm the email-triage daemons have
returned and inspect Docker restart state for any missing daemon.

**Deploy order:** Inventory first. Then migrate one bus domain completely:
stop only the old bus, back up its volume, start and verify the independent bus,
then migrate its consumer stacks one at a time. Finish acceptance and preserve
rollback before starting another bus domain.

**Verify:**
- Rebuild one agent service without using `docker compose down`
- Confirm the bus `StartedAt` and restart count do not change
- Bus stays up, all agents from other stacks stay registered
- New agent deploys in any stack don't affect the bus

### Fix 3: Compose v2 migration

**Goal:** Eliminate the `ContainerConfig` KeyError bug.

**Verified current state:** The active `deploy` and `jk-email-agents` projects
were created with Compose v2 (`2.27.0`). Compose v1 (`1.29.2`) labels remain on
inactive-looking legacy resources, which are out of scope for Fix 2. Confirm
the operator command resolves to Compose v2 immediately before cutover.

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
- `docker compose config` and targeted `docker compose up -d` work in all three stacks without `ContainerConfig` errors

## Revised implementation order

1. **Hot-reloadable allowlist (Fix 1)** — pinakes code change. Highest impact. Ship as a new pinakes release.
2. **Compose v2 migration (Fix 3)** — infra prerequisite for Fix 2. Install plugin, update docs.
3. **Separate bus stack (Fix 2)** — compose surgery. Do on v2.

## What NOT to do

- Don't add an admin HTTP API for allowlist management. File-based is simpler, auditable, and doesn't need auth.
- Don't move to Kubernetes. Docker Compose on beelink is the right scale.
- Don't add service mesh or discovery. The bus IS the discovery layer.
- Don't fail open on misconfiguration. Configured file path must be valid at startup.
- Don't evict live agents on allowlist removal. Block future registration only.
- Don't move the old `docker-compose` binary until host automation is audited.
- Don't run `docker compose down` on the old agent project during bus cutover.
- Don't assume host ports, volume names, network names, or the number of buses
  from repository documentation; inventory the running host first.

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
