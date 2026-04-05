---
summary: Short briefing and spoken walkthrough for explaining the shared bus, manager, and agent lifecycle model.
read_when:
  - explaining the post-v0.2.0 architecture to new contributors
  - justifying the added platform structure to operators or stakeholders
  - onboarding teams that need to update the bus or an agent safely
status: working draft
---

# Architecture Briefing

Last updated: 2026-03-31

## One-Paragraph Version

`pinakes` is now the shared runtime platform for agents, `manager` is the shared ops control plane, and each agent repo still owns its own domain logic. The extra structure did not centralize agent behavior. It standardized the operational contract around identity, health, drain, promotion, and inspection so multiple stacks can share one bus without stepping on each other.

## The Core Model

Three owners:

- `pinakes` — transport, auth, registration, registry, passport fields, bus health.
- `manager` — allowlist policy, promotion workflow, host-side verification, bystander checks, dashboards, runbooks.
- Agent repos — actual agent behavior, `/health`, `/metrics`, graceful drain, capability docs, repo-local deploy manifests.

One cross-cut:

- observability

The practical split is:

- `pinakes` answers: "can these agents talk, and what is on the bus right now?"
- `manager` answers: "what should be promoted, how do we verify it, and did anything else break?"
- Agent repos answer: "what does this agent actually do?"

## Why The Complexity Is Worth It

Before:

- health was inconsistent
- deploy verification was inconsistent or missing
- other agents and humans could not reliably tell what was running
- every repo carried its own hidden operational assumptions

Now:

- one citizenship contract
- one passport surface
- one promotion model
- one shared agent registry
- one consistent health/drain pattern
- one clean answer to "what changed, and is it safe?"

The complexity was not created from nothing. It was taken out of tribal knowledge and repo-local scripts and turned into explicit shared contracts.

## If You Need To Update The Bus

Use this path:

1. Change `pinakes`.
2. Update the relevant docs:
   - `docs/BUS_HTTP_CONTRACT.md`
   - `docs/AGENT_CITIZENSHIP.md`
   - any rollout/release docs if needed
3. Release a new `pinakes` tag.
4. Deploy the bus image.
5. Verify bus health and registry behavior.
6. Let agents re-register on heartbeat after the bus comes back.

Operational point:

- bus changes are shared-infrastructure changes
- they should be rarer, more documented, and more carefully verified than agent changes

## If You Need To Update An Agent

Use this path:

1. Change the agent in its own repo.
2. Keep the citizenship contract intact:
   - `/health`
   - `/metrics`
   - graceful drain
   - passport registration
3. Update the repo's capability docs if the contract changed.
4. Use `manager` to promote the target service:
   - dry-run
   - secret hydration
   - deploy target
   - verify target
   - verify bystanders

Operational point:

- agent changes are repo-local implementation changes
- they should not require bus changes unless the shared protocol itself is changing

## Lifecycle: Bus

Normal lifecycle:

1. Bus starts.
2. Agents register.
3. Agents heartbeat.
4. Bus exposes registry and routing.
5. If the bus restarts, agents re-register on heartbeat.

Monitoring:

- bus health endpoint
- bus metrics
- `GET /v1/agents`

When updating the bus:

- release first
- deploy second
- verify bus health third
- confirm agents reappear in the registry

## Lifecycle: Agent

Normal lifecycle:

1. Agent starts.
2. Agent registers with passport fields.
3. Agent exposes `/health` and `/metrics`.
4. Agent does work.
5. On `SIGTERM`, agent marks `draining`.
6. Agent stops taking new work.
7. Agent finishes in-flight work.
8. Agent exits cleanly.

Promotion lifecycle:

1. `manager` snapshots bus state.
2. `manager` hydrates secrets.
3. `manager` deploys only the target services.
4. `manager` verifies target health, version, and bus re-registration.
5. `manager` verifies bystanders stayed healthy.

This is the main architectural payoff: one agent can be promoted without treating the rest of the bus as collateral damage.

## Health URL Rule

There are two different health addresses for different purposes:

- Bus `meta.health_url`
  - self-declared
  - runtime-network point of view
  - may be compose-network-only
  - useful for registry display and in-network callers

- Manifest `health_url`
  - host-side probe address
  - authoritative for `manager` verification

This split prevents host-side ops from depending on container-network assumptions.

## How Manager Fits

`manager` is not the bus and not the agent framework.

It is the operator-facing control plane that consumes the citizenship contract and makes rollouts safe.

It owns:

- allowlist policy
- promotion planning
- secret hydration
- target verification
- bystander verification
- fleet/runbook integration

It does not own:

- message protocol semantics
- capability schemas
- agent business logic

## What To Tell Skeptics

Use this framing:

"We did not make the agents more coupled. We made the platform contracts more explicit. `pinakes` defines what a good citizen looks like. `manager` verifies and promotes those citizens safely. Each repo still owns its own logic."

That is the justification for the extra structure.

## Five-Minute Walkthrough

Here is a short talk track you can use almost verbatim:

"The architectural change is that the bus is no longer just a message pipe. `pinakes` is now the shared runtime platform, `manager` is the shared ops control plane, and each agent repo still owns its own domain behavior.

The reason we did this is scale. Once multiple applications share one bus, message routing alone is not enough. We need to know who is on the bus, what version they are, whether they are healthy, whether they can drain safely during deploy, and whether promoting one agent disrupts the others.

So we introduced a citizenship contract. Every long-lived agent now has a passport when it registers, a standard `/health`, a standard `/metrics`, and graceful drain behavior on shutdown. That gives humans and tooling a common surface to inspect.

We kept the boundaries tight. `pinakes` owns transport, auth, registration, and the registry. `manager` owns promotion, verification, allowlist policy, and ops checks. Agent repos still own the real business logic and the capability contracts for what messages mean.

That means updating the bus and updating an agent are now different workflows. If you update the bus, you change `pinakes`, release it, deploy it, and verify that agents re-register cleanly. If you update an agent, you change only that repo, keep the citizenship contract intact, update its capability docs if needed, and then use `manager` to promote just that target and verify both the target and the bystanders.

The extra complexity is there to remove hidden complexity. Before, each repo had its own assumptions about health, deploys, and observability. Now those operational assumptions are shared, explicit, and testable. So the system is a little more structured, but it is much less ambiguous.

The short version is: `pinakes` is the shared runtime platform, `manager` is the shared control plane, and each repo still owns its own agents. That is what lets us grow the ecosystem without turning every deploy into a guess." 

## Related Docs

- `ECOSYSTEM_ARCHITECTURE.md` — full ownership and boundary model
- `AGENT_CITIZENSHIP.md` — passport and runtime obligations
- `BUS_HTTP_CONTRACT.md` — wire-level protocol
- `PHASE5_PASSPORT_ROLLOUT.md` — shipped passport rollout baseline
