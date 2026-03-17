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

- [docs/BUS_HTTP_CONTRACT.md](/home/joelkehle/Projects/pinakes/docs/BUS_HTTP_CONTRACT.md)
