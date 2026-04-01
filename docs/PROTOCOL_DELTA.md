---
summary: Concrete checklist of pinakes code changes needed to support the passport-capable registration contract.
read_when:
  - implementing passport fields in pinakes
  - reviewing a PR that adds registration extensions
  - planning the pinakes release that enables citizenship
status: working draft
---

# Protocol Delta: Current Contract to Passport-Capable Contract

Last updated: 2026-03-31

## Scope

This is the implementation checklist for extending the pinakes bus to support the agent citizenship passport. Each item is a discrete, testable change. No item changes existing behavior — all additions are backwards-compatible.

## Prerequisites

- Hot-reloadable allowlist (Fix 1 from `BUS_STABILITY_SPEC.md`) — shipped in v0.1.4.
- Compose v2 migration and bus stack separation — in progress per stability spec.

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
- Updated on each heartbeat (re-registration). Version changes are normal (deploy in progress).
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

Currently accepts any string in `status`. Pin to allowed values:

- `processed` — handled successfully.
- `rejected` — refused (wrong type, validation failure).
- `error` — processing failed.

**Behavior (matches wire contract migration policy):**
- Unknown `status` values: accept with warning log. This is a migration stance — existing agents may use other strings. The wire contract (`BUS_HTTP_CONTRACT.md`) documents `processed`/`rejected`/`error` as canonical, with unknown values tolerated during migration.
- `reason` field: SHOULD be present when `status` is `rejected` or `error`. Log warning if missing but don't reject. Same migration stance as status.

**Tests:**
- [ ] Ack with `processed`: accepted.
- [ ] Ack with `rejected` + reason: accepted.
- [ ] Ack with `error` + reason: accepted.
- [ ] Ack with unknown status: accepted with warning log.
- [ ] Ack with `error` but no reason: accepted with warning log.

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

**Priority:** Low. Nice-to-have for ops page. Not blocking.

**Tests:**
- [ ] System status includes class/mutation breakdowns when agents provide them.
- [ ] System status works unchanged when no agents provide new fields.

### 5. Contract test updates

**File:** `pkg/httpapi/contract_test.go`

Add tests that pin the new registration shape alongside existing contract tests.

- [x] Passport-complete registration round-trip.
- [x] Legacy registration (no new fields) still works.
- [x] Agent list shape with mixed legacy + compliant agents.
- [ ] Ack status values pinned.

### 6. Documentation updates

- [ ] `BUS_HTTP_CONTRACT.md` — planned extensions section (done in draft).
- [ ] `AGENT_CITIZENSHIP.md` — canonical JSON payload (done in draft).

## Release plan

Ship as a single pinakes release (recommend `v0.2.0` — new capabilities, no breaking changes).

Order within the release:

1. Agent struct + registration handler changes (items 1-2).
2. Ack enumeration (item 3).
3. Contract tests (item 5).
4. System status enhancement (item 4, can defer to next release).
5. Doc updates (item 6).

After release:

- downstream Go repos pin `github.com/joelkehle/pinakes` at `v0.2.0` or later
- local/vendored copies of the passport client surface can be removed
- the shared bus runtime should be upgraded to the same release line so registry reads (`GET /v1/agents`) expose the passport fields operationally
- capability docs can proceed repo-by-repo; no big-bang migration required

---

*Draft: Claude + Codex (architecture review, 2026-03-31)*
