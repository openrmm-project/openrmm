# 01 — Architecture

## Components

| Component | Where | Role |
|-----------|-------|------|
| **Control plane** | `cmd/server` | HTTP/REST API, agent WebSocket, remote relay, AI engine, MCP server, check/task scheduler |
| **Agent** | `cmd/agent` | Runs on each endpoint: enroll, heartbeat, inventory, run jobs |
| **PostgreSQL** | external | Durable state: agents, jobs, inventory, audit, chat, sessions, migration history |
| **AI provider** | external | Any OpenAI-compatible `/chat/completions` endpoint |
| **Remote client** | technician side | MeshCentral web/native, or the native browser viewer |
| **Console** | `openrmm` | npm-built React bundle, served by nginx with runtime `config.js` |

## Package layout

```
cmd/
  server/        # control-plane entrypoint (wiring + graceful shutdown)
  agent/         # endpoint agent entrypoint
internal/
  config/        # env-based configuration
  settings/      # merges env config with editable DB-backed runtime settings (secrets encrypted)
  models/        # domain types + status/shell constants
  protocol/      # agent <-> server WebSocket message types
  store/         # PostgreSQL persistence (pgx) + immutable embedded migrations
  auth/          # token generation, hashing, constant-time compare, TOTP 2FA
  hub/           # registry of live agent connections (push channel per agent)
  jobs/          # job lifecycle: propose/approve/reject/dispatch/result + audit
  patches/       # approved patch scan/install/reboot dispatch and result handling
  scheduler/     # dispatches due checks/tasks, evaluates results, raises/resolves alerts
  ai/            # OpenAI-compatible client, permission-filtered tool catalog, tool loop
  mcpserver/     # exposes the same permission/scope-filtered tools over Streamable HTTP
  remote/        # RemoteProvider abstraction (native + terminal + meshcentral) + session relay
  api/           # chi router, auth middleware, REST handlers, WebSocket handlers
```

## Data flow: AI proposes a script

```
1. POST /api/chat/sessions/{id}/messages  { "content": "install 7zip on host Z" }
2. ai.Engine.Respond:
     - persists the user message
     - loads history + system prompt
     - calls the LLM with the permission-filtered tool catalog
3. LLM calls list_devices -> finds host Z's agent_id
4. LLM calls a proposal tool, e.g. propose_script(agent_id, shell, script, description)
     - jobs.Propose creates a job with status = awaiting_approval  (NOT executed)
     - audit: job.propose
5. LLM produces a final message: "I've prepared a script, awaiting your approval."
6. UI shows the proposed job. A human clicks Approve.
7. POST /api/jobs/{id}/approve
     - jobs.Approve -> status = pending -> dispatched over the agent's WebSocket
     - audit: job.approve
8. Agent runs the script, returns job_result.
9. jobs.OnResult -> status = completed/failed + stdout/stderr/exit_code
     - audit: job.result
```

## Data flow: agent connectivity

- The agent dials `ws://server/ws/agent` and sends a `register` frame
  (enroll token on first run, then agent id + token).
- The server keeps a per-agent outbound channel in `hub`. Jobs and remote-start
  requests are **pushed** to online agents.
- If an agent is offline, approved jobs stay `pending` and are delivered when it
  reconnects (`DispatchPending`).
- Heartbeats (20s) keep `last_seen`/status fresh; the read deadline (90s) plus
  WebSocket pings detect dead links.

## Data flow: native remote session

```
POST /api/devices/{id}/remote { "provider": "native" }
  -> remote.Manager creates a RemoteSession (separate viewer/agent tickets, expiry)
  -> pushes remote_start{session_id, agent_ticket} to the agent over its WebSocket
  -> returns the one-time viewer ticket to the authenticated caller
The agent peer derives /ws/remote?session=...&role=agent from its agents WebSocket
origin; the browser peer connects to the console origin with role=viewer
  -> relay consumes the role-specific ticket from the WebSocket handshake,
     binds the viewer to its authenticated principal, then pairs by session id
```

The **media engine** (screen capture/encode/input) lives in the agent's remote
module and the viewer client and rides on top of this relay. See
[08-remote-access](08-remote-access.md).

## Design choices

- **Go everywhere** — one toolchain; agent is a static single binary, trivial to
  deploy at scale, the core requirement for a self-hosted RMM.
- **WebSocket push, not polling** — NAT/firewall friendly, low latency for jobs
  and remote-session setup.
- **Provider abstraction for remote access** — never couple the product to one
  remote tool. Drop a provider without touching callers.
- **OpenAI-compatible AI client** — the broadest interop surface; the same code
  targets cloud and local models.

## Deployment boundaries

Production nginx has internal listeners `8080` (agents), `8081` (console), and
`8082` (MCP); all forward dynamic traffic to the single app on `server:8080`.
PostgreSQL is `postgres:5432` on an internal Docker network. Host publications
default to `127.0.0.1:8443`, `127.0.0.1:9443`, and `127.0.0.1:7443`; TLS and
source ACLs are controls of the trusted edge in front, not UFW rules on those
loopback listeners.

The application currently supports one server replica. `hub`, the relay pairing
state, and scheduler execution are in memory and are not distributed. PostgreSQL
advisory locking serializes migrations across concurrent starts, but there is no
scheduler leader election, shared WebSocket ownership, or cross-replica relay
bus; migration safety must not be mistaken for HA support.

## Startup and migrations

Before opening the HTTP listener, the server applies embedded forward migrations
in one transaction under an advisory lock. Current history is v1 `baseline` then
v2 `pending_totp_secret`; names and SHA-256 checksums are immutable and the
database history must be a contiguous prefix. See
[03-database-schema](03-database-schema.md) and the production rollback runbook.
