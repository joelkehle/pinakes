# pinakes

Reusable HTTP agent bus for agentic application composition.

Current contents:

- `pkg/bus` - core bus runtime and storage backends
- `pkg/httpapi` - HTTP transport and handlers
- `pkg/busclient` - Go client SDK
- `cmd/pinakes` - reference standalone server

Endpoints:

- `GET /health`
- `GET /metrics`
- `POST /v1/agents/register`
- `GET /v1/agents`
- `POST /v1/conversations`
- `GET /v1/conversations`
- `GET /v1/conversations/{id}/messages`
- `POST /v1/messages`
- `GET /v1/inbox`
- `POST /v1/acks`
- `POST /v1/events`
- `GET /v1/observe`
- `POST /v1/inject`
- `GET /v1/health`
- `GET /v1/system/status`

Run locally:

```bash
go run ./cmd/pinakes
```

Runtime config:

- `PORT`
- `DB_PATH`
- `STORE_BACKEND`
- `STATE_FILE`
- `AGENT_ALLOWLIST`
- `HUMAN_ALLOWLIST`

Contract doc:

- [docs/BUS_HTTP_CONTRACT.md](docs/BUS_HTTP_CONTRACT.md)

## For consumers

Consume the client SDK via `github.com/joelkehle/pinakes/pkg/busclient`.

- Pin released tags in `go.mod`; do not depend on floating branches or pseudo-versions unless you are testing an unreleased change.
- Treat `pkg/busclient` as the supported integration surface for Go consumers.
- Breaking protocol or client changes require a major version bump.

## License

This repository is licensed under the PolyForm Noncommercial License 1.0.0.
