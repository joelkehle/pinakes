# Standalone UCLA Pinakes Bus

## Overview

This is the standalone bus stack for the shared UCLA pinakes bus. The bus formerly lived in `ucla-tdg-ip-agents/deploy`; it now reuses the live external volume `deploy_bus-data` and live external network `tta-agentnet`. The service name `bus` preserves the `http://bus:8080` network alias. Consumers also reach it through host port `:8080`.

## Prerequisites

- Docker Compose v2. Docker Compose v2.27.0 is confirmed on the host.
- External Docker network `tta-agentnet` and volume `deploy_bus-data` must already exist. They do; the old IP-agents stack created them.
- Copy `.env.example` to `.env` and fill `PINAKES_INJECT_TOKEN` and `PINAKES_OBSERVE_TOKEN`. Reuse the values currently in `ucla-tdg-ip-agents/deploy/.env`.

## First-Time Cutover Sequence

1. In `/home/joelkehle/Projects/shared/pinakes/deploy`, run `docker compose config` to validate.
2. Remove the bus from the IP-agents stack: in `ucla-tdg-ip-agents/deploy`, run `docker compose up -d --remove-orphans` after the `bus` service has been deleted from that compose file and the `depends_on: bus` blocks removed. That edit is tracked separately; this stack does not perform it. This frees host port `:8080` and the container name.
3. Bring up this stack with `docker compose up -d`.
4. Verify with `curl -fsS http://localhost:8080/v1/health` and expect 200. Then run `curl -fsS http://localhost:8080/v1/agents` and expect the registered agents.
5. Keep the `:8080` republish window short. During the swap, the affected `ucla-tdg-email-triage` agents, including `next-email`, `inbox-triage`, and `contact-resolver`, plus `jk-calendar-guard`'s UCLA endpoint exit on first register/poll error and recover only through Docker `restart: unless-stopped`. `ucla-tdg-gmail-ingest` tolerates a brief blip in its steady-state poll loop. All four worker agents plus the operator re-attempt on a 60s heartbeat and poll forever on errors, so agents reconverge automatically.

## Normal Upgrade Procedure

Bump `PINAKES_TAG` in `.env`, or set it inline, then run `docker compose pull bus && docker compose up -d bus`. The external volume preserves bus state and secrets across the restart.

## Rollback

Re-add the `bus` service block and its `depends_on: bus` references to `ucla-tdg-ip-agents/deploy/docker-compose.yml` from git history with `git show <old-commit>:deploy/docker-compose.yml`. Run `docker compose down` in this stack, then `docker compose up -d` in the IP-agents stack. The external volume `deploy_bus-data` is shared, so no state is lost either direction.
