# Pinakes Bus Deploy

This Compose file runs one independently operated pinakes bus. Agent stacks
should not define their own `bus` service after they are migrated to it.

The beelink read-only inventory confirmed that UCLA and JK operate separate bus
runtimes:

| Domain | Current Compose project | Port | Network | State volume | DB path | Memory |
| --- | --- | --- | --- | --- | --- | --- |
| UCLA | `deploy` | `8080` | `tta-agentnet` | `deploy_bus-data` | `/data/pinakes.db` | `2g` |
| JK | `jk-email-agents` | `8081` | `jk-email-agents_default` | `jk-email-agents_jk-bus-data` | `/data/pinakes.sqlite` | `768m` |

Both currently run `ghcr.io/joelkehle/pinakes:v0.3.0`, use
`GOMEMLIMIT=1536MiB`, and set `stop_grace_period: 15s`. Re-run the inventory
before cutover because production may have changed after this file was written.

The checked-in `.env.ucla` and `.env.jk` files are non-secret deployment
templates containing verified topology and runtime values only. Never add
tokens, passwords, agent secrets, or other credentials to them.

## Phase 1: Read-Only Host Inventory

Use Docker Compose v2:

```bash
docker compose version
```

Record the running bus containers, images, ports, mounts, networks, and Compose
project labels:

```bash
docker ps --format 'table {{.Names}}\t{{.Image}}\t{{.Ports}}\t{{.Status}}'
docker inspect <bus-container>
docker inspect --format '{{json .Mounts}}' <bus-container>
docker inspect --format '{{json .NetworkSettings.Networks}}' <bus-container>
docker inspect --format '{{json .Config.Labels}}' <bus-container>
docker volume ls
docker network ls
```

For each bus domain, confirm:

- the bus container and Compose project name
- the published host port
- the exact state volume name and mount path
- every attached network and the DNS name agents use
- the manager config directory and allowlist file
- the currently deployed pinakes image tag
- `DB_PATH`, `GOMEMLIMIT`, Docker memory limit, and stop timeout

UCLA and JK must retain separate environment files, Compose project names,
networks, and state volumes. Containers with the `bus` service alias coexist
safely because the two networks are isolated.

The UCLA network `tta-agentnet` also carries Langfuse services. Langfuse does
not require direct bus connectivity. The standalone UCLA bus must still attach
to the existing external network, preserve the `bus` DNS alias, and reuse the
existing external volume. This work must not recreate, split, or delete
`tta-agentnet`.

## Phase 2: Validate Without Changing Runtime State

The domain files are checked in:

```bash
cat .env.ucla
cat .env.jk
```

The token values are required at deploy time but are intentionally not in Git.
Export them from the current live bus values or a secret manager before running
Compose validation:

```bash
export PINAKES_INJECT_TOKEN=...
export PINAKES_OBSERVE_TOKEN=...
```

Validate interpolation before touching the old bus:

```bash
docker compose --env-file .env.ucla -p pinakes-ucla config --quiet
docker compose --env-file .env.jk -p pinakes-jk config --quiet
```

The allowlist source of truth must be mounted as a read-only directory, not as
an individual file:

```text
/home/joelkehle/Projects/shared/manager/ops/config:/etc/pinakes:ro
```

## Consumer Recovery Behavior

Recovery behavior is mixed:

- UCLA IP agents and most JK agents retry registration and heartbeat
  in-process.
- Some UCLA email-triage daemons rely on Docker restart when their initial
  registration or poll fails.

Keep the UCLA `:8080` handoff short. After each cutover, verify registry
reconvergence rather than assuming every process recovered: compare
`GET /v1/agents` with the pre-cutover registry, confirm expected email-triage
daemons returned, and inspect restart state for any missing daemon.

## Phase 3: Maintenance-Window Cutover

Do not run `docker compose down` on the old agent project. It can remove the
project network and enlarge the blast radius. Stop only the old bus:

```bash
docker compose -f <old-compose-file> -p <old-project> stop bus
```

After the bus has stopped cleanly, take a consistent backup of its state
volume:

```bash
docker run --rm \
  -v <exact-volume-name>:/source:ro \
  -v <host-backup-directory>:/backup \
  alpine tar -C /source -czf /backup/pinakes-<domain>-pre-cutover.tgz .
```

Start the new bus-only project and verify health and persisted registrations.
Run commands for one domain at a time:

```bash
docker compose --env-file .env.ucla -p pinakes-ucla up -d
docker compose --env-file .env.ucla -p pinakes-ucla ps
curl -fsS http://localhost:8080/v1/health
curl -fsS http://localhost:8080/v1/agents
```

For JK, use `.env.jk`, project `pinakes-jk`, and port `8081`. Complete and
accept one bus domain before starting another.

## Phase 4: Consumer Stack Changes

In each downstream agent stack:

- remove the `bus` service
- remove agent `depends_on: bus` entries
- attach agents only to that bus domain's confirmed external network

```yaml
networks:
  agentnet:
    external: true
    name: ${STACK_NETWORK_NAME:?set STACK_NETWORK_NAME from the host inventory}
```

Run `docker compose config`, then use `docker compose up -d` for that consumer.
Do not use `down` during migration. Migrate and verify one consumer at a time.

## Phase 5: Verification

```bash
docker inspect --format '{{.State.StartedAt}} restart={{.RestartCount}}' \
  <new-bus-container>
docker compose -f <consumer-compose-file> up -d --build <one-agent-service>
docker inspect --format '{{.State.StartedAt}} restart={{.RestartCount}}' \
  <new-bus-container>
```

The bus start time and restart count must not change. Agents in other consumer
stacks must remain registered and continue processing messages. Registry agent
counts are dynamic, so validate expected identities and reconvergence rather
than requiring an exact historical count.

## Rollback

Because the old container, volume, and network were preserved, rollback is:

```bash
docker compose --env-file .env.ucla -p pinakes-ucla stop bus
docker compose -f <old-compose-file> -p <old-project> start bus
```

Confirm health and agent recovery before ending the maintenance window. Do not
delete the old bus service definition or old container until the new domain has
passed verification and its rollback window has closed.
