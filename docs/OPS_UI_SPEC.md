---
summary: Concrete site map, wireframe-level page spec, and backend API contracts for the shared fleet/ops UI.
read_when:
  - implementing ops.techtransfer.agency or a /fleet UI
  - designing backend handlers for fleet visibility
  - deciding what data each page should read from pinakes, manager, or repo docs
status: working draft
---

# Ops UI Spec

Last updated: 2026-03-31

## Proposed App

Preferred host:

- `https://ops.techtransfer.agency/`

Fallback:

- `https://techtransfer.agency/fleet`

The app is internal. It is a human-facing observation and audit surface for the shared bus, agents, capabilities, and promotions.

## Global Navigation

Navigation groups:

- `Fleet`
- `Projects`
- `Docs`

Primary routes:

- `/`
- `/agents`
- `/agents/:agent_id`
- `/capabilities`
- `/capabilities/:name`
- `/promotions`
- `/promotions/:id`
- `/bus`
- `/projects`
- `/projects/:project_id`
- `/topology`
- `/docs`
- `/docs/:slug`

## Global Layout

### App Shell

Left nav:

- Overview
- Agents
- Capabilities
- Promotions
- Bus
- Projects
- Topology
- Docs

Top bar:

- bus health pill
- unhealthy count
- draining count
- active promotion count
- global search

Detail page utility rail:

- owner
- repo
- runbook link
- raw JSON link

## Data Source Rules

Use the right source for the right question:

- registered state → `pinakes`
- expected state → `manager`
- host-side probe address → manifest `health_url`
- runtime-network health URL → bus `meta.health_url`
- capability meaning → repo capability docs
- promotion results → `manager`

Do not use bus `meta.health_url` as host-side truth.

## API Surface

These routes are UI/backend routes, not raw bus endpoints.

### Search

Route:

- `GET /api/search?q=<term>`

Response:

```json
{
  "agents": [
    {
      "agent_id": "triage-summarizer",
      "project_id": "ucla-tdg-email-triage"
    }
  ],
  "capabilities": [
    {
      "name": "triage-summarizer",
      "producer_agents": ["triage-summarizer"]
    }
  ],
  "projects": [
    {
      "project_id": "ucla-tdg-email-triage",
      "name": "Email Triage"
    }
  ]
}
```

## Pages

### 1. Fleet Overview

Route:

- `/`

Purpose:

- instant answer to whether the ecosystem is okay

Components:

- hero status strip
- fleet state cards
- project summary table
- recent activity panel
- mutation-risk panel

Hero status strip fields:

- bus health
- registered agents / expected agents
- unhealthy count
- draining count
- promotions in last 24h

Fleet state cards:

- healthy
- draining
- missing
- unexpected

API:

- `GET /api/fleet/overview`

Response:

```json
{
  "bus": {
    "ok": true,
    "version": "v0.2.0",
    "agents_registered": 16,
    "agents_expected": 16,
    "push_successes": 12034,
    "push_failures": 14
  },
  "fleet": {
    "healthy": 14,
    "draining": 1,
    "unhealthy": 1,
    "missing": 0,
    "unexpected": 0
  },
  "mutation_classes": {
    "observe": 9,
    "recommend": 5,
    "mutate": 2
  },
  "projects": [
    {
      "project_id": "ucla-tdg-email-triage",
      "name": "Email Triage",
      "registered": 6,
      "expected": 6,
      "unhealthy": 0,
      "last_promotion_at": "2026-03-31T17:40:00Z"
    }
  ],
  "recent_activity": [
    {
      "type": "promotion",
      "id": "prom_123",
      "project_id": "tdg-ip-agents",
      "summary": "tta-disclosure-processor promoted successfully",
      "at": "2026-03-31T17:40:00Z"
    }
  ]
}
```

Data sources:

- `pinakes GET /v1/health`
- `pinakes GET /v1/system/status`
- `pinakes GET /v1/agents`
- manager project inventory
- manager promotion history

### 2. Agents Registry

Route:

- `/agents`

Purpose:

- full registry of live bus participants

Components:

- registry table
- filter bar
- expected-only toggle

Columns:

- agent id
- project
- status
- version
- agent class
- mutation class
- capabilities count
- owner
- last seen

Filters:

- project
- status
- mutation class
- agent class
- expected only

API:

- `GET /api/agents`

Query params:

- `project`
- `status`
- `mutation_class`
- `agent_class`
- `expected_only=true|false`

Response:

```json
{
  "items": [
    {
      "agent_id": "triage-summarizer",
      "project_id": "ucla-tdg-email-triage",
      "registered": true,
      "expected": true,
      "status": "ready",
      "version": "ebcf921",
      "agent_class": "worker",
      "mutation_class": "observe",
      "capabilities": ["triage-summarizer"],
      "owner": "ucla-tdg-email-triage",
      "repo": "github.com/joelkehle/ucla-tdg-email-triage",
      "health_url": "http://triage-summarizer:8212/health",
      "last_seen_at": "2026-03-31T18:12:03Z"
    }
  ]
}
```

### 3. Agent Detail

Route:

- `/agents/:agent_id`

Purpose:

- complete view of one agent

Components:

- header
- passport card
- health card
- runtime card
- capability links
- promotion history
- raw JSON tabs

Header fields:

- agent id
- status badge
- version
- project

Passport card fields:

- description
- capabilities
- agent class
- mutation class
- build commit / dirty
- owner
- repo
- runtime-network `health_url`
- dependencies

API:

- `GET /api/agents/:agent_id`

Response:

```json
{
  "agent": {
    "agent_id": "triage-summarizer",
    "project_id": "ucla-tdg-email-triage",
    "expected": true,
    "registered": true,
    "passport": {
      "version": "ebcf921",
      "description": "Summarizes inbound email threads",
      "agent_class": "worker",
      "mutation_class": "observe",
      "capabilities": ["triage-summarizer"],
      "build": {
        "commit": "ebcf921",
        "dirty": false
      },
      "meta": {
        "owner": "ucla-tdg-email-triage",
        "repo": "github.com/joelkehle/ucla-tdg-email-triage",
        "health_url": "http://triage-summarizer:8212/health",
        "dependencies": ["triage-claude-builder", "triage-codex-builder"]
      }
    },
    "last_seen_at": "2026-03-31T18:12:03Z"
  },
  "health": {
    "reachable": true,
    "payload": {
      "ok": true,
      "agent_id": "triage-summarizer",
      "status": "ready",
      "version": "ebcf921",
      "build": {
        "commit": "ebcf921",
        "dirty": false,
        "time": "2026-03-31T16:05:00Z"
      },
      "uptime_seconds": 8492
    },
    "checked_at": "2026-03-31T18:12:10Z"
  },
  "promotions": [
    {
      "id": "prom_123",
      "at": "2026-03-31T17:40:00Z",
      "result": "success"
    }
  ]
}
```

Data sources:

- bus registry
- in-network `meta.health_url` probe when reachable
- manager promotion history

### 4. Capabilities Index

Route:

- `/capabilities`

Purpose:

- browse what the ecosystem can do

Components:

- search/filter bar
- capability cards or table

Fields:

- capability name
- project
- producer agents
- mutation class
- summary
- source doc link

API:

- `GET /api/capabilities`

Response:

```json
{
  "items": [
    {
      "name": "triage-summarizer",
      "project_id": "ucla-tdg-email-triage",
      "producer_agents": ["triage-summarizer"],
      "mutation_class": "observe",
      "summary": "Summarizes inbound email thread packs",
      "doc_path": "/docs/capabilities/triage-summarizer"
    }
  ]
}
```

Data sources:

- generated capability index from repo docs
- bus registry for live producers

### 5. Capability Detail

Route:

- `/capabilities/:name`

Purpose:

- one capability contract per page

Components:

- header
- summary
- request shape
- reply / ack shape
- delivery semantics table
- dependencies
- failure modes
- related agents
- source doc link

API:

- `GET /api/capabilities/:name`

Response:

```json
{
  "name": "triage-summarizer",
  "project_id": "ucla-tdg-email-triage",
  "producer_agents": ["triage-summarizer"],
  "summary": "Consumes SummarizerRequest and emits thread summary output.",
  "request_shape": {
    "type": "SummarizerRequest",
    "important_fields": ["conversation_id", "thread_pack", "summary_source"]
  },
  "reply_shape": {
    "type": "SummarizerResponse",
    "important_fields": ["summary", "summary_source", "artifacts"]
  },
  "delivery": {
    "timeout_expectation": "operator expectation, not hard daemon timeout",
    "idempotency": "best effort",
    "side_effect_model": "observe",
    "retry_safety": "safe if duplicate summaries are tolerated"
  },
  "dependencies": ["triage-claude-builder", "triage-codex-builder"],
  "failure_modes": [
    "builder failure",
    "invalid thread pack",
    "timeout in downstream model call"
  ],
  "source_doc": {
    "repo": "ucla-tdg-email-triage",
    "path": "docs/capabilities/triage-summarizer.md"
  }
}
```

### 6. Promotions List

Route:

- `/promotions`

Purpose:

- operational timeline of deploy activity

Components:

- promotions table
- filter bar

Columns:

- promotion id
- project
- target services
- actor
- result
- started
- completed

API:

- `GET /api/promotions`

Response:

```json
{
  "items": [
    {
      "id": "prom_123",
      "project_id": "tdg-ip-agents",
      "targets": ["tta-disclosure-processor"],
      "actor": "joel",
      "result": "success",
      "started_at": "2026-03-31T17:38:00Z",
      "completed_at": "2026-03-31T17:40:00Z"
    }
  ]
}
```

### 7. Promotion Detail

Route:

- `/promotions/:id`

Purpose:

- render manager promotion results in human form

Components:

- header summary
- plan card
- secret hydration card
- deploy card
- target verification card
- bystander verification card
- raw JSON panel

API:

- `GET /api/promotions/:id`

Response:

```json
{
  "id": "prom_123",
  "project_id": "tdg-ip-agents",
  "overall_result": "success",
  "plan": {
    "restart": ["tta-disclosure-processor"],
    "smoke": ["tta-operator"],
    "noop": ["tta-patent-screen"]
  },
  "secret_hydration": {
    "overall_result": "success",
    "services": [
      {
        "service": "tta-disclosure-processor",
        "present": ["ANTHROPIC_API_KEY"],
        "hydrated_from_running_container": [],
        "missing": []
      }
    ]
  },
  "deploy_result": {
    "overall_result": "success",
    "executed_commands": [
      {
        "argv": ["docker", "compose", "-f", "deploy/docker-compose.yml", "up", "-d", "tta-disclosure-processor"],
        "exit_status": 0
      }
    ]
  },
  "target_verification": {
    "overall_result": "success",
    "targets": [
      {
        "service": "tta-disclosure-processor",
        "health_ready": true,
        "version_match": true,
        "bus_reregistered": true
      }
    ]
  },
  "bystander_verification": {
    "overall_result": "success",
    "bystanders": [
      {
        "agent_id": "tta-operator",
        "result": "stable"
      }
    ]
  }
}
```

### 8. Bus Runtime

Route:

- `/bus`

Purpose:

- bus health and routing state

Components:

- bus health summary
- registry counters
- throughput counters
- recent registration activity

API:

- `GET /api/bus`

Response:

```json
{
  "health": {
    "ok": true,
    "status": "ready",
    "version": "v0.2.0"
  },
  "system_status": {
    "system": {
      "agents_active": 16,
      "push_successes": 12034,
      "push_failures": 14
    }
  },
  "recent_registrations": [
    {
      "agent_id": "triage-summarizer",
      "at": "2026-03-31T18:10:00Z"
    }
  ]
}
```

### 9. Projects Index

Route:

- `/projects`

Purpose:

- stack-level overview

Components:

- project cards

Card fields:

- name
- expected vs registered
- unhealthy count
- last promotion

API:

- `GET /api/projects`

Response:

```json
{
  "items": [
    {
      "project_id": "ucla-tdg-email-triage",
      "name": "Email Triage",
      "expected_agents": 6,
      "registered_agents": 6,
      "unhealthy_agents": 0,
      "last_promotion_at": "2026-03-31T17:20:00Z"
    }
  ]
}
```

### 10. Project Detail

Route:

- `/projects/:project_id`

Purpose:

- per-stack ops page

Components:

- project header
- service table
- capability section
- recent promotions
- product-specific metrics panel

Service table fields:

- agent id
- status
- version
- manifest `health_url`
- bus `meta.health_url`
- expected
- registered

API:

- `GET /api/projects/:project_id`

Response:

```json
{
  "project": {
    "project_id": "ucla-tdg-email-triage",
    "name": "Email Triage"
  },
  "services": [
    {
      "agent_id": "triage-summarizer",
      "status": "ready",
      "version": "ebcf921",
      "manifest_health_url": "http://127.0.0.1:8212/health",
      "bus_health_url": "http://triage-summarizer:8212/health",
      "expected": true,
      "registered": true
    }
  ],
  "recent_promotions": [
    {
      "id": "prom_124",
      "result": "success",
      "at": "2026-03-31T17:20:00Z"
    }
  ],
  "capabilities": ["triage-summarizer", "triage-project-mapper"]
}
```

### 11. Topology

Route:

- `/topology`

Purpose:

- understand system structure visually

Components:

- graph canvas
- display toggles
- detail inspector

Toggles:

- show projects
- show capabilities
- show dependencies

API:

- `GET /api/topology`

Response:

```json
{
  "nodes": [
    { "id": "ucla-tdg-email-triage", "type": "project" },
    { "id": "triage-summarizer", "type": "agent", "mutation_class": "observe" },
    { "id": "triage-summarizer-cap", "type": "capability" }
  ],
  "edges": [
    { "from": "ucla-tdg-email-triage", "to": "triage-summarizer", "type": "contains" },
    { "from": "triage-summarizer", "to": "triage-summarizer-cap", "type": "advertises" }
  ]
}
```

### 12. Docs

Routes:

- `/docs`
- `/docs/:slug`

Purpose:

- embed architecture, citizenship, capability, and runbook references into the ops app

Components:

- docs index
- markdown/article view

API:

- `GET /api/docs/index`
- `GET /api/docs/:slug`

Response:

```json
{
  "items": [
    {
      "slug": "architecture",
      "title": "Architecture Briefing",
      "source": "pinakes/docs/ARCHITECTURE_BRIEFING.md"
    }
  ]
}
```

## Minimum Viable Build

Build these first:

1. `/`
2. `/agents`
3. `/agents/:agent_id`
4. `/capabilities`
5. `/promotions/:id`
6. `/projects/:project_id`

That gives the core value:

- fleet status
- registry inspection
- capability discovery
- deploy verification visibility
- per-project drilldown

## Notes On Style And UX

This app should feel operational, not like a generic SaaS admin panel.

Good directions:

- strong state color system for ready / draining / unhealthy / missing
- registry and promotion pages optimized for scanning
- raw JSON available, but secondary
- capability pages readable by humans first, machines second

Avoid:

- mixing product KPIs and fleet/platform KPIs on the same primary dashboard
- implying host-side health reachability from bus `meta.health_url`
- hiding drift between expected and registered state
