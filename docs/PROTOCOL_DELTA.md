---
summary: Historical record of the pinakes changes that shipped the passport-capable registration contract in v0.2.0.
read_when:
  - auditing what shipped in pinakes v0.2.0
  - checking which passport items were part of the v0.2.0 baseline
  - reviewing follow-up work against the shipped passport slice
status: working draft
---

# Protocol Delta: v0.2.0 Passport Baseline

Last updated: 2026-03-31

## Scope

This doc records the pinakes changes that shipped the passport-capable bus baseline in `v0.2.0`. It is no longer a forward plan. It is the historical delta for the additive registration and listing work that landed without breaking older callers.

## Prerequisites

- Hot-reloadable allowlist (Fix 1 from `BUS_STABILITY_SPEC.md`) — shipped in v0.1.4.
- Compose v2 migration and bus stack separation — separate infra work, not part of the `v0.2.0` passport slice.

## Changes

### 1. Registration: accept new fields

**File:** `pkg/httpapi/server.go` (handleRegisterAgent)
**File:** `pkg/bus/store.go` (Agent struct)

Add to Agent struct:

```go
type Agent struct {
    // existing fields
    ID           string   `json:"agent_id"`
    Capabilities []string `json:"capabilities"`
    Description  string   `json:"description"`
    Mode         string   `json:"mode"`
    CallbackURL  string   `json:"callback_url"`
    TTL          int      `json:"ttl"`
    Secret       string   `json:"-"`

    // new passport fields
    Version       string      `json:"version,omitempty"`
    AgentClass    string      `json:"agent_class,omitempty"`
    MutationClass string      `json:"mutation_class,omitempty"`
    Build         *BuildInfo  `json:"build,omitempty"`
    Meta          *AgentMeta  `json:"meta,omitempty"`

    // existing internal fields
    ExpiresAt    time.Time `json:"expires_at"`
    RegisteredAt time.Time `json:"registered_at"`
}

type BuildInfo struct {
    Commit string `json:"commit,omitempty"`
    Dirty  bool   `json:"dirty,omitempty"`
}

type AgentMeta struct {
    Owner        string   `json:"owner,omitempty"`
    Repo         string   `json:"repo,omitempty"`
    HealthURL    string   `json:"health_url,omitempty"`
    Dependencies []string `json:"dependencies,omitempty"`
}
```

**Behavior:**
- All new fields are parsed from registration JSON if present.
- Missing new fields: registration succeeds; legacy callers remain compatible.
- Updated on each heartbeat (re-registration). Version changes during deploys are normal.
- `secret` field remains excluded from JSON serialization (existing behavior).

**Validation:**
- `agent_class`: if provided, must be `worker` or `orchestrator`. Reject otherwise.
- `mutation_class`: if provided, must be `observe`, `recommend`, or `mutate`. Reject otherwise.
- `version`: free-form string, no format enforcement. Presence is what matters.

**Tests:**
- [x] Registration with all new fields: accepted, fields stored and echoed.
- [x] Registration with no new fields: accepted, backwards compatible.
- [x] Registration with invalid `agent_class`: rejected with 400.
- [x] Registration with invalid `mutation_class`: rejected with 400.
- [x] Re-registration (heartbeat) updates version and other passport fields.

### 2. Agent listing: surface new fields

**File:** `pkg/httpapi/server.go` (handleListAgents)

`GET /v1/agents` response already returns agent objects. New fields are included automatically via struct serialization (`omitempty` means they're absent for legacy agents, present for passport-compliant agents).

**Tests:**
- [x] `GET /v1/agents` includes `version`, `agent_class`, `mutation_class` for compliant agents.
- [x] `GET /v1/agents` omits new fields for legacy agents (no null pollution).
- [x] `GET /v1/agents?capability=X` filter continues to work unchanged.

### 3. Ack status enumeration

**File:** `pkg/httpapi/server.go` (handleAcks)

Not part of the shipped passport baseline.

Current bus behavior remains:

- `accepted` — request accepted and moved to execution
- `rejected` — request refused
- other status values are rejected

The ack contract should be tracked separately from passport support. It is not part of the `v0.2.0` passport release boundary.

### 4. System status: version summary

**File:** `pkg/httpapi/server.go` (handleSystemStatus)

Optional enhancement. Add agent version breakdown to system status:

```json
{
  "system": {
    "agents_active": 12,
    "agents_by_class": { "worker": 10, "orchestrator": 2 },
    "agents_by_mutation": { "observe": 7, "recommend": 3, "mutate": 2 }
  }
}
```

**Status:** Deferred. Nice-to-have for ops page. Not part of `v0.2.0`.

**Tests:**
- [ ] System status includes class/mutation breakdowns when agents provide them.
- [ ] System status works unchanged when no agents provide new fields.

### 5. Contract test updates

**File:** `pkg/httpapi/contract_test.go`

Add tests that pin the new registration shape alongside existing contract tests.

- [x] Passport-complete registration round-trip.
- [x] Legacy registration (no new fields) still works.
- [x] Agent list shape with mixed legacy + compliant agents.
- [x] Passport registration validation pinned for `agent_class` / `mutation_class`.
- [ ] Ack status values pinned separately.

### 6. Documentation updates

- [x] `BUS_HTTP_CONTRACT.md` — passport extensions documented.
- [x] `AGENT_CITIZENSHIP.md` — canonical JSON payload documented.
- [x] `README.md` — `v0.2.0` release boundary called out for downstream consumers.

## Release outcome

Shipped as `pinakes v0.2.0` — new capabilities, no breaking changes.

Order within the shipped release:

1. Agent struct + registration handler changes (items 1-2).
2. Ack enumeration (item 3).
3. Contract tests (item 5).
4. System status enhancement (item 4, can defer to next release).
5. Doc updates (item 6).

Post-release reality:

- downstream Go repos pin `github.com/joelkehle/pinakes` at `v0.2.0` or later
- the shared bus runtime is deployed on the same release line, so registry reads (`GET /v1/agents`) expose the passport fields operationally
- local/vendored copies of the passport client surface are no longer blocked on an unpublished upstream release
- capability docs can proceed repo-by-repo; no big-bang migration required
