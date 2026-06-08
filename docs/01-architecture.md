# 01 — Architecture

## Components

| Component | Where | Role |
|-----------|-------|------|
| **Control plane** | `cmd/server` | HTTP/REST API, agent WebSocket, remote relay, AI engine |
| **Agent** | `cmd/agent` | Runs on each endpoint: enroll, heartbeat, inventory, run jobs |
| **PostgreSQL** | external | All state: agents, jobs, inventory, audit, chat, sessions |
| **AI provider** | external | Any OpenAI-compatible `/chat/completions` endpoint |
| **Remote client** | technician side | MeshCentral web/native, RustDesk client, or native viewer |

## Package layout

```
cmd/
  server/        # control-plane entrypoint (wiring + graceful shutdown)
  agent/         # endpoint agent entrypoint
internal/
  config/        # env-based configuration
  models/        # domain types + status/shell constants
  protocol/      # agent <-> server WebSocket message types
  store/         # PostgreSQL persistence (pgx) + embedded schema
  auth/          # token generation, hashing, constant-time compare
  hub/           # registry of live agent connections (push channel per agent)
  jobs/          # job lifecycle: propose/approve/reject/dispatch/result + audit
  ai/            # OpenAI-compatible client, tool catalog, tool-calling loop
  remote/        # RemoteProvider abstraction (native + meshcentral) + session relay
  api/           # chi router, auth middleware, REST handlers, WebSocket handlers
```

## Data flow: AI proposes a script

```
1. POST /api/chat/sessions/{id}/messages  { "content": "install 7zip on host Z" }
2. ai.Engine.Respond:
     - persists the user message
     - loads history + system prompt
     - calls the LLM with the tool catalog
3. LLM calls list_devices -> finds host Z's agent_id
4. LLM calls propose_script(agent_id, shell, script, description)
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
  -> remote.Manager creates a RemoteSession (token, expiry)
  -> pushes remote_start{token, relay_url} to the agent over its WebSocket
  -> returns a viewer connect_url to the caller
Both peers connect to /ws/remote?token=...&role=agent|viewer
  -> remote.Relay pairs them by token and pipes frames (the hbbr-equivalent)
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
