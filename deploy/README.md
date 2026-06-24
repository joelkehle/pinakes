# Pinakes Bus Deploy

This Compose file runs one independently operated pinakes bus. Agent stacks
should not define their own `bus` service after they are migrated to it.

The read-only beelink audit generated at `2026-06-23T00:49:45Z` confirmed that
UCLA and JK operate separate bus runtimes:

| Domain | Current container | Port | Network | State volume |
| --- | --- | --- | --- | --- |
| UCLA | `a7c789bec7f3_deploy-bus-1` | `8080` | `tta-agentnet` | `deploy_bus-data` |
| JK | `jk-email-agents-bus-1` | `8081` | `jk-email-agents_default` | `jk-email-agents_jk-bus-data` |

Both currently run `ghcr.io/joelkehle/pinakes:v0.3.0`. The checked-in
`.env.ucla` and `.env.jk` files are non-secret deployment templates containing
only these verified topology values. Never add tokens, passwords, agent
secrets, or other credentials to them. Re-run the inventory before cutover in
case production has changed since the audit.

## Phase 1: Read-only host inventory

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

Also audit host automation before retiring Compose v1:

```bash
grep -R "docker-compose" /etc/systemd /etc/cron* \
  /home/joelkehle/Projects 2>/dev/null
```

For each bus domain, write down:

- the bus container and Compose project name
- the published host port
- the exact state volume name and mount path
- every attached network and the DNS name agents use
- which agent stacks consume that bus
- the manager config directory and allowlist file
- the currently deployed pinakes image tag

UCLA and JK must retain separate environment files, Compose project names,
networks, and state volumes. Containers with the `bus` service alias coexist
safely because the two networks are isolated.

The UCLA network `tta-agentnet` also carries the `langfuse-stack` services
(`langfuse-web`, `langfuse-worker`, ClickHouse, MinIO, Postgres, and Redis).
Langfuse does not require direct bus connectivity. The standalone UCLA bus must
still attach to the existing external network, preserve the `bus` DNS alias,
and reuse the existing external volume. This work must not recreate, split, or
delete `tta-agentnet`.

The audit also found legacy resources including:

- networks `agent-bus-v2_agentnet`, `techtransfer-agency_agentnet`,
  `tta-agentnet-repair-17615`, and `tta-agentnet-smoke`
- volumes `agent-bus-v2_bus-data` and `techtransfer-agency_bus-data`

They are explicitly out of scope. Do not reference, rename, or delete them
without a separate cleanup plan and Joel's approval.

## Phase 2: Prepare without changing runtime state

The confirmed domain files are already present:

```bash
cat .env.ucla
cat .env.jk
```

Both confirmed networks already exist. Inspect them before cutover, but do not
recreate them:

```bash
docker network inspect tta-agentnet
docker network inspect jk-email-agents_default
```

Validate interpolation before touching the old bus. This intentionally fails
if any host-specific value is missing:

```bash
docker compose --env-file .env.ucla -p pinakes-ucla config
docker compose --env-file .env.jk -p pinakes-jk config
```

The allowlist source of truth must be mounted as a read-only directory, not as
an individual file:

```text
/home/joelkehle/Projects/shared/manager/ops/config:/etc/pinakes:ro
```

## Consumer recovery behavior

Recovery behavior is mixed:

- UCLA IP agents and most JK agents retry registration and heartbeat
  in-process.
- Some UCLA email-triage daemons rely on Docker restart when their initial
  registration or poll fails.

The `:8080` handoff must therefore be kept as short as possible. After each
cutover, verify registry reconvergence rather than assuming every process
recovered: compare `GET /v1/agents` with the pre-cutover registry, confirm the
expected email-triage daemons returned, and inspect restart state for any
missing daemon. Do not remove consumer `depends_on` entries until the
corresponding consumer Compose change is reviewed.

## Phase 3: Maintenance-window cutover

Do not run `docker compose down` on the old agent project. It can remove the
project network and enlarge the blast radius. Stop only the old bus:

```bash
docker compose -f <old-compose-file> -p <old-project> stop bus
```

After the bus has stopped cleanly, take a consistent backup of its state
volume. For example, archive the entire volume into an existing host backup
directory:

```bash
docker run --rm \
  -v <exact-volume-name>:/source:ro \
  -v <host-backup-directory>:/backup \
  alpine tar -C /source -czf /backup/pinakes-<domain>-pre-cutover.tgz .
```

Start the new bus-only project and verify health and persisted registrations.
Run these commands for only one domain at a time:

```bash
docker compose --env-file .env.ucla -p pinakes-ucla up -d
docker compose --env-file .env.ucla -p pinakes-ucla ps
curl -fsS http://localhost:8080/v1/health
curl -fsS http://localhost:8080/v1/agents
```

For JK, use `.env.jk`, project `pinakes-jk`, and port `8081`. Perform and accept
one bus domain completely before starting another. Keep the UCLA `:8080`
handoff especially short because some email-triage daemons depend on Docker
restart rather than an in-process retry loop.

## Phase 4: Consumer stack changes

In each downstream agent stack:

- remove the `bus` service
- remove agent `depends_on: bus` entries
- attach agents only to that bus domain's confirmed external network:

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
