READ `~/Projects/agent-scripts/AGENTS.MD` BEFORE ANYTHING (skip if missing).

# Repo Purpose

`pinakes` is the shared agent message bus. It owns the HTTP bus runtime, the busclient Go package consumed by all agent repos, and the bus protocol contract.

# Start Here

- Read `README.md` for build, test, and release.
- Read `docs/BUS_HTTP_CONTRACT.md` for the full protocol spec.
- Read `docs/BUS_STABILITY_SPEC.md` for the hot-reload allowlist and infra improvement plan.

# Repo-Specific Rules

- This is shared infrastructure. Changes here affect `tdg-ip-agents`, `email-agents`, and `email-triage`.
- Bus protocol changes must update `docs/BUS_HTTP_CONTRACT.md`.
- `busclient` is the Go package consumed by all agent repos. Breaking changes to the client interface require version bumps and coordinated updates across consumers.
- Agent allowlist source of truth: `~/Projects/manager/ops/config/allowlist.txt`. Bus hot-reloads from this file via `ALLOWLIST_FILE` env var.
- Do not put the allowlist in a Docker volume. It lives in manager so it's git-tracked.
- Mount the config directory, not the file: `manager/ops/config:/etc/pinakes:ro`.

# Testing Strategy

- Default gate: `go test ./...`
- Protocol changes should include contract tests in `pkg/httpapi/contract_test.go`.

# Git & Deploy

- Releases are tag-driven. Push a `v*` tag to trigger the image build on ghcr.io.
- Do not tag or release without explicit Joel approval.
- Bus container currently runs in `~/Projects/tdg-ip-agents/deploy/docker-compose.yml` (planned move to `~/Projects/pinakes/deploy/`).
