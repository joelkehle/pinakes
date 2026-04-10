---
summary: Agent-side runtime contract for the pinakes bus ecosystem. Defines the passport (registration obligations), health/lifecycle behavior, and rings of optional participation.
read_when:
  - building a new agent that connects to the bus
  - adding health, lifecycle, or observability to an existing agent
  - designing deploy/promotion tooling that verifies agent behavior
  - reviewing or extending the bus protocol
status: working draft
---

# Agent Citizenship Contract

Last updated: 2026-03-31

## Purpose

The bus HTTP contract (`BUS_HTTP_CONTRACT.md`) defines what the bus exposes. This document defines what **agents** owe the ecosystem in return.

Agent authors are free to build agents however they want, in any language. But agents must present a standard passport to join the bus, and must behave predictably so they can be operated safely alongside other agents.

Bus-side passport registration and listing support shipped in `pinakes v0.2.0`. Treat `v0.2.0+` as the clean passport-capable baseline. Earlier bus versions may tolerate extra fields, but they are compatibility-only and do not define the steady-state integration surface.

Core distinction:

- **Bus registration** means "reachable participant" — the agent exists and can exchange messages.
- **Health** means "safe to give work" — the agent is ready and functioning correctly.

These are separate concerns. An agent can be registered but not healthy (starting up, draining, degraded). Tooling and humans must be able to distinguish the two.

## The Passport

The passport is what an agent presents at registration (`POST /v1/agents/register`). Two tiers: required fields gate participation, optional fields improve inspection.

### Canonical registration payload

One JSON shape. Required fields are top-level. Optional build metadata nests under `build`. Optional inspection metadata nests under `meta`. No dotted notation at top level.

```json
{
  "agent_id": "triage-intake",
  "secret": "hmac-secret-here",
  "version": "v0.3.1",
  "description": "Classifies and routes incoming invention disclosures",
  "capabilities": ["email-triage", "intake-processing"],
  "agent_class": "worker",
  "mutation_class": "observe",
  "mode": "pull",
  "callback_url": null,
  "build": {
    "commit": "a1b2c3d",
    "dirty": false
  },
  "meta": {
    "owner": "ucla-tdg-email-triage",
    "repo": "github.com/joelkehle/ucla-tdg-email-triage",
    "health_url": "http://triage-intake:8101/health",
    "dependencies": ["anthropic-api", "gmail-api"]
  }
}
```

### Required fields (top-level)

| Field | Type | Purpose |
|-------|------|---------|
| `agent_id` | string | Unique, stable identifier. Must be on the allowlist. |
| `secret` | string | HMAC signing key for message auth. |
| `version` | string | Semver tag (e.g. `v0.3.1`) or `<commit>[-dirty]` for dev builds. |
| `description` | string | Human-readable one-liner: what this agent does. |
| `capabilities` | []string | Message types this agent handles. Flat strings (e.g. `prior-art-search`, `email-triage`). |
| `agent_class` | string | `worker` or `orchestrator`. |
| `mutation_class` | string | `observe`, `recommend`, or `mutate`. |
| `mode` | string | Transport: `pull` (polls inbox) or `push` (receives callbacks). |
| `callback_url` | string | Required if `mode` is `push`. Null otherwise. |

### Optional fields

| Field | Type | Purpose |
|-------|------|---------|
| `build.commit` | string | Git commit hash of the running binary. |
| `build.dirty` | bool | Whether the build included uncommitted changes. |
| `meta.owner` | string | Which repo or team owns this agent. |
| `meta.repo` | string | Source repository URL. |
| `meta.health_url` | string | Agent-declared health endpoint URL. May be runtime-network-only. Not the authority for host-side promotion checks. |
| `meta.dependencies` | []string | External systems the agent requires. |

### Field semantics

**`agent_class`** describes the agent's structural role:

- `worker` — receives tasks, produces results. Most agents are workers.
- `orchestrator` — coordinates workflows across multiple agents. The operator is an orchestrator.

This is separate from `mode`, which is transport. A worker can use push or pull. An orchestrator can use push or pull. Don't conflate role with delivery mechanism.

**`mutation_class`** declares the agent's side-effect boundary:

- `observe` — reads, classifies, summarizes, enriches. No external mutation. Safe to run speculatively.
- `recommend` — proposes actions or artifacts for human or downstream approval. Does not act autonomously.
- `mutate` — can change external state or trigger irreversible side effects (send emails, modify records, call external APIs with write semantics).

This gives you:

- **Safer ops page.** Humans see at a glance which agents can change real systems.
- **Safer promotion rules.** `mutate` agents may require stricter verification before promotion.
- **Future policy hooks.** Governance rules can key off mutation class without overbuilding now.

The bus does not enforce mutation_class semantics — it's a declaration, not a sandbox. Enforcement is through review, policy, and ops tooling in manager.

**`capabilities`** use flat, stable strings. Existing agents already use names like `prior-art-search`, `patent-screen`, `email-triage`. Don't rename them. If taxonomy becomes useful later, add optional metadata — don't change routing keys.

**`version`** is a first-class bus field, not buried in meta. The bus reports it in `GET /v1/agents` so tooling and ops pages can show agent versions without querying each agent's health endpoint individually.

**`meta.health_url`** is a runtime-network hint, not a universal probe target:

- It MAY be a compose-network/internal DNS address such as `http://triage-intake:8101/health`.
- It is NOT required to be host-reachable from the machine running promotion tooling.
- It SHOULD point at the same agent process and same `/health` contract the repo manifest exposes through any host-side address.
- The bus stores and echoes it as agent-declared metadata. The bus does not verify reachability.

Authoritative split:

- **Repo deploy manifest `services.<name>.health_url`** is authoritative for host-executed verification, promotion checks, and any tooling running outside the agent network.
- **Bus `meta.health_url`** is authoritative only as the agent's self-declared runtime-network health address for inspection, registry display, and in-network callers.
- If both exist, they may differ by hostname/port, but they MUST represent the same service instance and return matching `agent_id`, `version`, and readiness semantics.

Operational consequence: manager should rely on manifest health addresses in steady state. Bus `meta.health_url` is a migration fallback and inspection hint, not the long-term source of truth for host reachability.

### Protocol delta

See `BUS_HTTP_CONTRACT.md` "Passport Extensions" for the exact wire-level changes. The bus-side passport slice shipped in `v0.2.0`. The bus still accepts legacy registration for backward compatibility with pre-`v0.2.0` callers, but compliant agents MUST provide the full passport.

## Ring 1: Citizenship (MUST)

The cost of admission. Every agent on the bus MUST satisfy these.

### 1.1 Registration with full passport

See passport section above. Register with all required fields on every heartbeat.

### 1.2 Heartbeat

Re-register before TTL expires (default 60s). On bus restart, re-register on next heartbeat cycle. Existing behavior — documenting as a citizenship obligation.

### 1.3 Health endpoint

Every agent MUST expose `GET /health` returning JSON:

```json
{
  "ok": true,
  "agent_id": "triage-intake",
  "status": "ready",
  "version": "v0.3.1",
  "build": {
    "commit": "a1b2c3d",
    "time": "2026-03-31T14:00:00Z",
    "dirty": false
  },
  "uptime_seconds": 3612
}
```

**Required health fields:**

| Field | Type | Meaning |
|-------|------|---------|
| `ok` | bool | `true` if the agent can accept and process work. `false` if degraded or draining. |
| `agent_id` | string | Must match registration. |
| `status` | enum | `starting`, `ready`, `draining`, `unhealthy` |
| `version` | string | Must match registration version. |

**Recommended health fields:**

| Field | Type | Meaning |
|-------|------|---------|
| `build.commit` | string | Git commit hash of the running binary. |
| `build.time` | string | ISO 8601 build timestamp. |
| `build.dirty` | bool | Whether the build included uncommitted changes. |
| `uptime_seconds` | int | Seconds since process start. |

**Status semantics:**

- `starting` — process is up but not ready for work. Lifecycle tooling waits.
- `ready` — fully operational. Accept work.
- `draining` — shutting down gracefully. Finishing in-flight work, rejecting new messages. Lifecycle tooling waits for exit, does not force-kill.
- `unhealthy` — running but unable to process work (lost dependency, internal error). Still responds to health checks so tooling can observe the state.

### 1.4 Graceful shutdown

On SIGTERM, an agent MUST:

1. Set health status to `draining`.
2. Stop polling inbox / accepting new messages.
3. Finish in-flight work (recommend 30s max drain timeout).
4. Exit cleanly.

An agent MUST NOT:
- Drop messages silently on shutdown.
- Accept new work after SIGTERM.
- Hang indefinitely. If in-flight work can't complete within drain timeout, log the abandoned work and exit.

### 1.5 Metrics endpoint

Every agent MUST expose `GET /metrics` in Prometheus text exposition format. Minimum:

```
agent_up{agent_id="triage-intake",version="v0.3.1"} 1
agent_messages_processed_total{agent_id="triage-intake"} 42
agent_messages_errors_total{agent_id="triage-intake"} 3
agent_build_info{agent_id="triage-intake",commit="a1b2c3d",version="v0.3.1",dirty="false"} 1
```

Labels MUST be low-cardinality. No message bodies, disclosure text, or user data in labels.

### 1.6 No silent message loss

If an agent receives a message and cannot process it (error, unsupported type, internal failure), it MUST ack with an error status via `POST /v1/acks`. Silent drops are a citizenship violation — the sender and bus have no way to know the message was lost.

## Ring 2: Discoverability (SHOULD)

Strongly recommended. Unlocks ecosystem-wide visibility without requiring pinakes to absorb domain knowledge.

### 2.1 Dependency declaration

Registration meta SHOULD include a `dependencies` key:

```json
{
  "meta": {
    "dependencies": ["anthropic-api", "gmail-api"]
  }
}
```

Useful for incident triage ("Gmail is down — which agents are affected?") and onboarding.

### 2.2 Health detail for orchestrators

Orchestrators SHOULD include workflow-level status in health:

```json
{
  "ok": true,
  "workflows": {
    "active": 3,
    "completed": 47,
    "failed": 1
  }
}
```

### 2.3 Capability contracts

For each capability an agent advertises, the **owning repo** (not pinakes) SHOULD document:

- Request shape
- Reply shape
- Timeout expectation
- Idempotency expectation
- Side-effect model
- Retry safety
- Failure modes

This is the delivery semantics contract. Without it, the bus ecosystem is inspectable but not reliably composable. The bus routes opaque messages — the capability contract tells you what's inside them and how they behave.

Location: in the agent's repo, e.g. `docs/capabilities/prior-art-search.md` or inline in the agent's README.

## Ring 3: Ecosystem Coordination (MAY — future)

Documented so the architecture stays open to them. Not needed at current scale.

### 3.1 Capability-based discovery

Agents MAY query `GET /v1/agents?capability=<type>` to discover peers dynamically. Decouples producers from consumers. Valuable when agent count grows or third-party agents appear.

### 3.2 Backpressure signaling

Agents MAY report load in health:

```json
{
  "load": {
    "queue_depth": 15,
    "processing": 3,
    "capacity": 5
  }
}
```

Cooperative convention. Bus does not enforce.

### 3.3 Schema versioning

Agents MAY include `schema_version` in message bodies for format migration. Needed when multiple versions of an agent run simultaneously.

## Compliance Verification

Lifecycle tooling in manager verifies citizenship during agent promotion.

### Promotion checklist

1. **Pre-deploy:** Query health of all agents. Record version and status.
2. **Deploy:** Restart target agent(s).
3. **Post-deploy — target:**
   - Health endpoint responds within timeout (recommend 20s).
   - `status` is `ready`.
   - `version` matches expected.
   - `build.commit` matches expected (local-build mode).
   - Agent re-registered in `GET /v1/agents`.
4. **Post-deploy — bystanders:**
   - Health of non-target agents unchanged.
   - No unexpected deregistrations in `GET /v1/system/status`.
5. **Rollback:** If target fails post-deploy, tooling SHOULD roll back and alert.

### Observability minimum (production-ready, not just bus-compliant)

- Health and metrics endpoints present and correct.
- Agent in `manager/ops/config/projects.json`.
- Dashboard or stack-level metric inclusion.
- Alerts: agent_up == 0, error rate spike, status != ready.
- Runbook linked from alert annotations.

## Out of Scope for Pinakes

These belong elsewhere. Pinakes is the shared border, not the shared government.

| Concern | Where it belongs | Why not pinakes |
|---------|-----------------|-----------------|
| Deploy manifests | Agent repo `deploy/` | Deployment is stack-specific. |
| Promotion/rollout logic | `manager` | Ops policy, not protocol. |
| Detailed capability schemas | Agent repo docs | Domain knowledge. Bus routes opaque messages. |
| Secret policy and rotation | `manager` | Fleet security policy. |
| Durable artifacts and state | Agent repo / external storage | Bus carries messages, not system-of-record data. |
| Workflow orchestration logic | Agent repo (orchestrator agent) | Domain logic, not bus logic. |
| Runbooks | `manager/docs/runbooks/` | Operational, not protocol. |
| Alert thresholds and SLOs | `manager` ops stack | Policy, not contract. |
| Dashboard definitions | `manager/ops/` | Operational, not protocol. |

The moment the bus becomes where all meaning lives, agent freedom is gone.

## Relationship to Other Docs

| Doc | Scope |
|-----|-------|
| `BUS_HTTP_CONTRACT.md` | What the bus exposes — endpoints, auth, config. |
| `BUS_STABILITY_SPEC.md` | Operational improvements — hot-reload, stack separation, compose v2. |
| `ECOSYSTEM_ARCHITECTURE.md` | Platform boundaries, ownership map, what pinakes is not. |
| **This doc** | What agents owe the ecosystem — passport, health, lifecycle. |

---

*Draft: Claude + Codex (architecture review, 2026-03-31)*
