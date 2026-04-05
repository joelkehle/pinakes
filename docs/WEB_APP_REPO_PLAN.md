---
summary: Final app split, repo homes, and stack choices for the three web apps sharing the techtransfer.agency domain.
read_when:
  - creating the dedicated product and ops app repos
  - deciding where frontend code should live
  - explaining why runtime repos stay separate from app repos
status: working draft
---

# Web App Repo Plan

Last updated: 2026-03-31

## Final Domain Split

Use three separate apps on one domain:

- `techtransfer.agency` → IP Agency product
- `assist.techtransfer.agency` → email product
- `ops.techtransfer.agency` → shared fleet/platform ops

This is preferred over one hostname with path-based routing because it gives cleaner audience boundaries, cleaner auth/session boundaries, and a cleaner explanation of what each app is for.

## Repo Homes

Create and use these app repos:

- `~/Projects/ucla-tdg/ucla-tdg-ip-agency`
- `~/Projects/ucla-tdg/ucla-tdg-assistant-bus`
- `~/Projects/ucla-tdg/ucla-tdg-bus-ops`

Keep the runtime/platform repos separate:

- `~/Projects/ucla-tdg/ucla-tdg-ip-agents`
- `~/Projects/ucla-tdg/ucla-tdg-email-triage`
- `~/Projects/shared/pinakes`
- `~/Projects/shared/manager`
- `~/Projects/jk/jk-email-agents`

## Ownership Split

App repos own:

- user-facing frontend UX
- server-rendered pages or BFF handlers
- product navigation and design system
- app-specific auth/session/UI concerns

Runtime/platform repos own:

- agents and workflow execution
- bus runtime
- promotion and verification logic
- `/health` and `/metrics`
- capability docs and deploy manifests

The app repos consume the runtime/platform systems. They do not replace them.

## Recommended Frontend Stack

Use the same stack in all three app repos:

- Next.js
- TypeScript
- React
- App Router
- server-side route handlers / BFF layer

Recommended styling direction:

- Tailwind if speed matters most
- CSS Modules if stricter isolation/design discipline matters most

Either is acceptable. The more important decision is using one frontend stack across all three apps.

## Keep Go Where It Already Belongs

Do not turn these repos into frontend app repos:

- `ucla-tdg/ucla-tdg-ip-agents`
- `ucla-tdg/ucla-tdg-email-triage`
- `shared/pinakes`
- `shared/manager`

They should stay Go-first runtime/platform repositories.

Resulting split:

- frontend/product + ops apps → Next.js/TypeScript
- runtime/platform backbone → Go

## Current IP Agency State

Today the IP Agency site is still embedded in `~/Projects/ucla-tdg/ucla-tdg-ip-agents`.

Current locations:

- static frontend assets in `web/`
- operator web server in `cmd/operator` and `internal/operator`
- deployment in `deploy/docker-compose.yml`

Current stack:

- Go backend/runtime
- static HTML/CSS/vanilla JS
- Go `net/http` server
- Docker Compose
- `pinakes`

This is the starting point, not the target shape.

## Migration Guidance

### IP Agency

- keep the current operator-served UI alive while the dedicated app is built
- build the new product app in `~/Projects/ucla-tdg/ucla-tdg-ip-agency`
- cut traffic over when feature parity is good enough

### Email Product

- keep `ucla-tdg/ucla-tdg-email-triage` as the runtime repo
- build the user-facing app in `~/Projects/ucla-tdg/ucla-tdg-assistant-bus`
- avoid product naming that exposes internal runtime terms like `triage`

### Ops

- build the shared fleet/ops console in `~/Projects/ucla-tdg/ucla-tdg-bus-ops`
- keep it read-only first
- treat it as the observation/audit surface over `shared/pinakes`, `shared/manager`, and capability docs

## Why This Split Is Better

- product UX does not get buried in platform detail
- ops/fleet UI does not get trapped under one product
- runtime repos stay focused on execution, not marketing/app shell concerns
- one frontend stack keeps app development simpler
- one runtime stack keeps operational complexity lower

## Related Docs

- `OPS_UI_ARCHITECTURE.md`
- `OPS_UI_SPEC.md`
- `ARCHITECTURE_BRIEFING.md`
