---
summary: Platform boundaries, ownership map, and architectural principles for the pinakes agent ecosystem.
read_when:
  - onboarding to the agent ecosystem
  - deciding where new functionality belongs
  - reviewing boundary violations between repos
  - planning cross-repo changes
status: working draft
---

# Ecosystem Architecture

Last updated: 2026-03-31

## Thesis

Pinakes is the shared border, not the shared government. The bus admits agents, identifies them, routes work, and exposes enough truth for humans and tooling to operate safely. Everything domain-specific stays outside.

The ecosystem standardizes **surfaces**, not implementations.

## Three Owners, One Cross-Cut

### Pinakes — bus protocol + runtime citizenship

Owns: transport, auth, registration, agent passport, basic discovery, bus-level health, observe stream.

Does not own: what agents do with messages, how they're deployed, what their capabilities mean, or how the fleet is operated.

Key docs:

- `BUS_HTTP_CONTRACT.md` — wire-level API, auth rules, config surface.
- `AGENT_CITIZENSHIP.md` — passport fields, health contract, lifecycle obligations.
- This doc — boundaries and ownership.

### Manager — ops policy + promotion/verification

Owns: allowlist governance, deploy/promotion tooling, rollout verification, fleet compliance, dashboards, alerts, runbooks, secret policy.

Key locations:

- `ops/config/allowlist.txt` — agent allowlist (bind-mounted to bus).
- `ops/config/projects.json` — monitored projects and health probes.
- `bin/` — fleet tooling (compliance, release, future promotion controller).
- `docs/runbooks/` — operational runbooks linked from alerts.

Manager consumes the citizenship contract. It checks that agents satisfy Ring 1 during promotion, aggregates health and metrics for dashboards, and enforces fleet policy. It does not define the protocol.

### Agent repos — domain logic + capability contracts

Own: agent implementation, durable state, artifacts, domain-specific message schemas, workflow logic, capability documentation.

Each agent repo documents its capabilities:

- Request/reply shapes
- Timeout and idempotency expectations
- Side-effect model and retry safety
- Failure modes

These are delivery semantics contracts. The bus doesn't interpret them — it routes opaque messages. The contracts tell producers and consumers what's inside.

Current agent repos: `ucla-tdg/ucla-tdg-ip-agents`, `jk/jk-email-agents`, `ucla-tdg/ucla-tdg-email-triage`.

### Observability — shared contract, implemented everywhere

The cross-cutting concern. Not owned by one repo.

- **Pinakes** defines the health and metrics endpoint shape (citizenship contract).
- **Agent repos** implement `/health` and `/metrics` on each agent.
- **Manager** aggregates metrics (Prometheus), visualizes them (Grafana), and alerts on them.

The telemetry contract in `AGENTS.md` and `manager/docs/decisions/telemetry-strategy-rollout-plan-2026-03.md` governs the policy side.

## The Passport

The key protocol concept. An agent presents a passport at registration. The passport carries what the bus and tooling need at runtime to route, verify, and display. Everything else is external metadata.

**In the passport:**

- agent_id, version, description, capabilities
- agent_class (worker / orchestrator)
- mutation_class (observe / recommend / mutate)
- transport fields (mode, callback_url)
- optional: owner, repo, health_url, build metadata

**Not in the passport (external docs/config):**

- Deploy manifests, compose config
- Detailed capability schemas
- Runbook URLs, alert thresholds, SLO targets
- Secret names and rotation policy
- Dashboard links
- Architecture rationale

The test: "Does the bus or the promotion controller need this at runtime without consulting external files?" If yes, passport. If no, docs.

## Mutation Class

A first-class governance primitive on the passport.

| Class | Meaning | Example |
|-------|---------|---------|
| `observe` | Reads, classifies, summarizes, enriches. No external mutation. | patent-screen, market-analysis |
| `recommend` | Proposes actions/artifacts for human or downstream approval. | prior-art-search, disclosure-processor |
| `mutate` | Can change external state or trigger irreversible side effects. | email senders, record updaters |

Declared by the agent, not enforced by the bus. Consumed by:

- Ops page (humans see which agents can change real systems)
- Promotion tooling (stricter verification for mutators)
- Future policy hooks (without overbuilding now)

## What Pinakes Is Not

| Not this | Why | Where it belongs |
|----------|-----|-----------------|
| Workflow engine | Bus routes messages, doesn't orchestrate. | Orchestrator agents in agent repos. |
| Schema registry | Bus carries opaque messages. | Capability docs in agent repos. |
| Deployment system | Promotion is ops, not protocol. | Manager. |
| Durable store | Bus state is operational (routing, cursors). Not system-of-record. | Agent repos, external storage. |
| Secret manager | Bus uses secrets for auth. Doesn't manage rotation or policy. | Manager. |
| Monitoring stack | Bus exposes health/metrics. Doesn't aggregate or alert. | Manager ops (Prometheus, Grafana). |

The moment the bus absorbs any of these, agent authors lose the freedom to build as they see fit. The bus is the border checkpoint, not the government.

## How Agents Join the Ecosystem

1. **Build the agent.** Any language, any framework. Implement the citizenship contract: passport fields, `/health`, `/metrics`, graceful shutdown, no silent message drops.
2. **Document capabilities.** In your repo. Request/reply shapes, timeouts, idempotency, side effects, failure modes.
3. **Get on the allowlist.** Add agent ID to `shared/manager/ops/config/allowlist.txt`. Commit. Bus picks it up via hot-reload.
4. **Deploy.** Add agent to your stack's docker-compose. Connect to the `tta-agentnet` network. Agent registers with bus on first heartbeat.
5. **Get monitored.** Add to `shared/manager/ops/config/projects.json`. Set up dashboard inclusion and alerts with runbook links.

Steps 1-4 get you on the bus. Step 5 makes you production-ready.

## Current State

| Stack | Agents | Deploy mode | Citizenship status |
|-------|--------|-------------|-------------------|
| `ucla-tdg/ucla-tdg-ip-agents` | operator, disclosure-processor, patent-extractor, prior-art-extractor, market-extractor, patent-screen, prior-art-search, market-analysis | Image tag (GHCR) | Partial — no agent-level /health or /metrics yet. |
| `ucla-tdg/ucla-tdg-email-triage` | triage-intake, triage-summarizer, triage-project-mapper, triage-action-extractor, triage-archive-watcher, triage-archive-learner | Local build | Partial — has buildinfo + /health; deploy-managed daemons now register passport fields, while graceful drain and broader citizenship rollout remain repo-local. |
| `jk/jk-email-agents` | gmail-ingest, contacts-agent | Mixed | Partial. |

Migration path: adopt citizenship incrementally. Add passport fields to registration, add /health and /metrics, add graceful shutdown. No big-bang rewrite.

---

*Draft: Claude + Codex (architecture review, 2026-03-31)*
