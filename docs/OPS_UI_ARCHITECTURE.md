---
summary: Audience split, app boundaries, and page families for the shared pinakes operational UI and adjacent product apps.
read_when:
  - deciding where fleet visibility should live
  - separating product ops from platform ops
  - planning a GUI for pinakes, manager, and agent capability discovery
status: working draft
---

# Ops UI Architecture

Last updated: 2026-03-31

## Thesis

`techtransfer.agency/ops` is now overloaded if it serves both:

- product-specific operations for IP Agency
- shared fleet visibility for every agent on `pinakes`

Those are different audiences and different questions. The UI should split by purpose, not force one page to do everything.

## The Three Apps

The recommended split is now:

- `https://techtransfer.agency/` — IP Agency product
- `https://assist.techtransfer.agency/` — email product
- `https://ops.techtransfer.agency/` — shared fleet/platform ops

Docs and developer reference should live inside the ops app under `/docs`, not as a fourth separate app.

### 1. IP Agency Product App

Suggested home:

- `https://techtransfer.agency/`

Audience:

- end users
- IP Agency operators working on product outcomes

Questions it answers:

- how do I submit a disclosure?
- is the IP Agency workflow healthy?
- where are disclosures getting stuck?
- how is the IP Agency pipeline performing?

This app should stay product-specific. It should not become the place to inspect every agent on the shared bus.

### 2. Email Product App

Suggested home:

- `https://assist.techtransfer.agency/`

Audience:

- end users
- operators of the email-facing product

Questions it answers:

- what work did the email product do?
- what needs human attention?
- what outcomes matter to the email workflow?

This app should not expose internal runtime terminology like `email-triage` as the primary product framing.

### 3. Fleet / Platform Ops App

Suggested home:

- `https://ops.techtransfer.agency/`

Fallback if one host is required:

- `https://techtransfer.agency/fleet`

Audience:

- Joel
- operators
- maintainers of `pinakes`, `manager`, and agent repos

Questions it answers:

- what agents are on the bus right now?
- what versions are running?
- which agents are ready, draining, unhealthy, missing, or unexpected?
- what capabilities are advertised?
- what happened during the last promotion?
- did promoting one target disrupt bystanders?

This is the natural home for:

- the fleet console
- the agent registry
- capability discovery
- promotion history
- bus runtime state
- topology view

Suggested docs home inside the app:

- `https://ops.techtransfer.agency/docs`

## Why The Split Is Worth It

Without the split:

- product operators get buried in platform detail
- platform operators lose a clear fleet-wide view
- the IP Agency stack looks like the owner of the whole ecosystem
- shared-bus concepts stay hidden behind product-specific screens

With the split:

- product pages stay outcome-oriented
- fleet pages stay platform-oriented
- docs stay reference-oriented
- each audience gets the right level of abstraction

## Recommended Information Architecture

### IP Agency Product Surface

Keep:

- product home
- submit flow
- product status
- product-specific workflow metrics

Do not put here:

- full bus registry
- cross-project agent health
- platform-wide promotion history

### Email Product Surface

Keep:

- product home
- workflow/outcome views for the email product
- product-specific status and intervention screens

Do not put here:

- shared fleet registry
- bus internals
- cross-project promotion history

### Fleet Surface

Primary navigation groups:

- `Fleet`
- `Projects`
- `Docs`

Primary pages:

- overview
- agents
- capabilities
- promotions
- bus
- topology
- projects

### Docs Surface Within Ops App

Primary pages:

- architecture
- citizenship
- capability index
- runbooks
- release notes

## Source-Of-Truth Rules

Each page should use the right source for the right question.

- "What is registered right now?" → `pinakes`
- "What should exist?" → `manager`
- "How should the host probe this service?" → repo manifest `health_url`
- "What does this capability mean?" → repo capability docs
- "What happened during deploy?" → `manager`

Important operational rule:

- bus `meta.health_url` is runtime-network, self-declared, and may be compose-network-only
- manifest `health_url` is authoritative for host-side verification

The UI must not blur those two concepts.

## Suggested First Build Order

### First Wave

- fleet overview
- agents registry
- agent detail
- capabilities index
- promotion detail
- project detail

This exposes the new value already created by:

- passport registration
- runtime health/drain
- manager promotion verification
- capability docs

### Second Wave

- bus runtime page
- promotions list
- topology page
- docs surface integration

### Later

- richer event explorer
- advanced search
- diff views between expected vs registered fleet state

## Candidate App Types

The UI does not have to be a single monolith. Useful app/page families include:

- Fleet Console
- Product Ops pages
- Agent Catalog
- Capability Explorer
- Promotion Console or promotion history viewer
- Bus/Event explorer
- Fleet status board
- Topology map

They should share data sources, but not necessarily all live on the same initial page.

## Recommended Naming

If you want the split to be obvious:

- `techtransfer.agency/` → IP Agency product
- `assist.techtransfer.agency/` → email product
- `ops.techtransfer.agency/` → platform/fleet
- `ops.techtransfer.agency/docs` → docs/reference

If you keep one host:

- `/` → IP Agency product
- `/assist` → email product
- `/fleet` → platform
- `/docs` → reference

The important part is not the hostname. It is the conceptual split.

## Repo Layout

Recommended app repos:

- `~/Projects/ucla-tdg/ucla-tdg-ip-agency`
- `~/Projects/ucla-tdg/ucla-tdg-assistant-bus`
- `~/Projects/ucla-tdg/ucla-tdg-bus-ops`

Runtime and platform repos stay separate:

- `~/Projects/ucla-tdg/ucla-tdg-ip-agents`
- `~/Projects/ucla-tdg/ucla-tdg-email-triage`
- `~/Projects/shared/pinakes`
- `~/Projects/shared/manager`
- `~/Projects/jk/jk-email-agents`

This keeps product/frontend concerns out of the runtime repos while preserving the current Go service boundaries.

## Frontend Stack Recommendation

Recommended stack for all three web apps:

- Next.js
- TypeScript
- React
- App Router
- server-side route handlers / BFF layer

Keep the runtime/platform backbone in Go:

- `ucla-tdg/ucla-tdg-ip-agents`
- `ucla-tdg/ucla-tdg-email-triage`
- `shared/pinakes`
- `shared/manager`

This standardizes the frontend stack without forcing the runtime repos into a web-monolith shape.

## Current IP Agency State

Today the IP Agency UI still lives inside `~/Projects/ucla-tdg/ucla-tdg-ip-agents`:

- static frontend assets in `web/`
- Go `net/http` server in `cmd/operator` and `internal/operator`
- Docker Compose deployment in `deploy/docker-compose.yml`

Current stack:

- Go backend/runtime
- Go-served static HTML/CSS/vanilla JS frontend
- `pinakes` bus
- Docker Compose deployment

Migration direction:

- leave the current operator-served UI in place until the dedicated `techtransfer.agency` app is ready
- then cut traffic from the embedded product UI to the dedicated app repo

## Outcome

The post-passport ecosystem now has enough structure that a GUI is useful, not ornamental. The CLI remains the control surface. The UI becomes the observation, comprehension, and audit surface for humans.

## Related Docs

- `ARCHITECTURE_BRIEFING.md`
- `ECOSYSTEM_ARCHITECTURE.md`
- `AGENT_CITIZENSHIP.md`
- `PHASE5_PASSPORT_ROLLOUT.md`
