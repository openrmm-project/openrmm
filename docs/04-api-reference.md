# 04 — API Reference

Base URL: `RMM_PUBLIC_URL` (default `http://localhost:8080`).

## Authentication

All `/api/*` routes require a bearer token:

```
Authorization: Bearer <token>
```

Valid tokens: a **session token** from `POST /api/auth/login`, an **API token**
(`api_tokens`), or the bootstrap `RMM_ADMIN_TOKEN`. Each carries a role
(`admin`/`tech`/`readonly`) that gates routes — see the Role column below and
[10-monitoring-policies](10-monitoring-policies.md). WebSocket endpoints
authenticate via their own handshake/token and do not use this header.

Errors are JSON: `{"error":"message"}` with an appropriate HTTP status.

## Health

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/healthz` | none | Liveness probe → `ok` |

## Enrollment

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/enroll-tokens` | Create an enroll token |

Body: `{ "description": "lab", "uses": 5, "ttl_hours": 24 }` (all optional;
`uses` defaults to 1). Returns the token object — **the token value is only shown
here**.

## Devices

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/devices` | List all agents → `{ "devices": [...] }` |
| GET | `/api/devices/{id}` | One agent |
| GET | `/api/devices/{id}/inventory` | Latest inventory snapshot |
| POST | `/api/devices/{id}/remote` | Start a remote session |

`POST /devices/{id}/remote` body: `{ "provider": "native" }` (or `meshcentral` /
`rustdesk`; omitted → `RMM_REMOTE_PROVIDER`). Returns a `RemoteSession` including
`connect_url`.

## Jobs

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/jobs?agent_id=&limit=` | List jobs (newest first) |
| POST | `/api/jobs` | Create/propose a job |
| GET | `/api/jobs/{id}` | One job (with result) |
| POST | `/api/jobs/{id}/approve` | Approve → dispatch |
| POST | `/api/jobs/{id}/reject` | Reject a proposal |

`POST /jobs` body:
```json
{ "agent_id": "...", "shell": "bash", "script": "...",
  "description": "...", "require_approval": true }
```
`require_approval` defaults to `true`. With `false`, the job is dispatched
immediately (use only for trusted automation, never for AI-authored scripts).

## Audit

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/audit?limit=` | Recent audit entries (newest first) |

## Chat (AI)

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/chat/sessions` | List conversations |
| POST | `/api/chat/sessions` | Create a conversation `{ "title": "..." }` |
| GET | `/api/chat/sessions/{id}/messages` | Full message history |
| POST | `/api/chat/sessions/{id}/messages` | Send a user message; runs the tool-calling loop |

`POST .../messages` body: `{ "content": "..." }`. Response:
```json
{ "content": "assistant reply text", "created_job_ids": ["..."] }
```
`created_job_ids` are jobs the AI proposed during this turn — surface them for
approval in the UI.

## Auth & users

| Method | Path | Role | Description |
|--------|------|------|-------------|
| POST | `/api/auth/bootstrap` | none | Create the first admin (only when no users exist) |
| POST | `/api/auth/login` | none | `{username,password}` → `{token, role, user_id}` |
| POST | `/api/auth/logout` | any | Revoke the session token |
| GET | `/api/me` | any | Current `{actor, role}` |
| GET | `/api/users` | admin | List users |
| POST | `/api/users` | admin | `{username,password,role}` |
| DELETE | `/api/users/{id}` | admin | Remove a user |

## Runtime configuration

| Method | Path | Role | Description |
|--------|------|------|-------------|
| GET | `/api/config` | readonly | Effective non-secret config for UI display |
| PUT | `/api/config` | admin | Update editable runtime settings |

`PUT /api/config` body:

```json
{
  "ai": {
    "provider": "openai-compatible",
    "base_url": "https://api.openai.com/v1",
    "model": "gpt-4o-mini",
    "api_key": "sk-..."
  },
  "remote": {
    "default": "native",
    "meshcentral_url": "https://mesh.example.com",
    "meshcentral_user": "admin",
    "meshcentral_token_key": "hex-login-token-key"
  }
}
```

Secret fields (`ai.api_key`, `remote.meshcentral_token_key`) are write-only:
omit them or send `""` to keep the existing value. They are encrypted in
`app_settings` and never returned by the API; `GET /api/config` only returns
boolean `*_set` flags.

## Org hierarchy

| Method | Path | Role | Description |
|--------|------|------|-------------|
| GET | `/api/clients` | readonly | List clients |
| POST | `/api/clients` | admin | `{name}` |
| DELETE | `/api/clients/{id}` | admin | Delete (cascades to sites) |
| GET | `/api/sites?client_id=` | readonly | List sites |
| POST | `/api/sites` | admin | `{client_id,name}` |
| DELETE | `/api/sites/{id}` | admin | Delete site |
| PATCH | `/api/devices/{id}/site` | admin | `{site_id}` reassign a device |

## Script library

| Method | Path | Role | Description |
|--------|------|------|-------------|
| GET | `/api/scripts` | readonly | List scripts |
| GET | `/api/scripts/{id}` | readonly | One script |
| POST | `/api/scripts` | admin | Create (`name,shell,body,category,...`) |
| PUT | `/api/scripts/{id}` | admin | Update |
| DELETE | `/api/scripts/{id}` | admin | Delete |
| POST | `/api/devices/{id}/run-script` | tech | `{script_id, require_approval}` → job |

## Checks & results

| Method | Path | Role | Description |
|--------|------|------|-------------|
| GET | `/api/checks?agent_id=&policy_id=` | readonly | List checks |
| POST | `/api/checks` | admin | Create (`type,name,config,warn_threshold,err_threshold,interval_sec`, `agent_id` or `policy_id`) |
| PUT | `/api/checks/{id}` | admin | Update |
| DELETE | `/api/checks/{id}` | admin | Delete |
| GET | `/api/checks/{id}/results?agent_id=&limit=` | readonly | Result history |

## Tasks (scheduled automation)

| Method | Path | Role | Description |
|--------|------|------|-------------|
| GET | `/api/tasks?agent_id=&policy_id=` | readonly | List tasks |
| POST | `/api/tasks` | admin | Create (`name, script_id` or `shell+script`, `schedule_kind, interval_sec, run_at_minute`, `agent_id` or `policy_id`) |
| PUT | `/api/tasks/{id}` | admin | Update |
| DELETE | `/api/tasks/{id}` | admin | Delete |

## Policies

| Method | Path | Role | Description |
|--------|------|------|-------------|
| GET | `/api/policies` | readonly | List policies |
| POST | `/api/policies` | admin | `{name,description}` |
| DELETE | `/api/policies/{id}` | admin | Delete |
| GET | `/api/policies/{id}/assignments` | readonly | List assignments |
| POST | `/api/policies/{id}/assignments` | admin | `{scope_type: client\|site\|agent, scope_id}` |
| DELETE | `/api/policies/assignments/{aid}` | admin | Remove assignment |

## Alerts

| Method | Path | Role | Description |
|--------|------|------|-------------|
| GET | `/api/alerts?status=&limit=` | readonly | List alerts |
| POST | `/api/alerts/{id}/ack` | tech | Acknowledge |
| POST | `/api/alerts/{id}/resolve` | tech | Resolve |

## API tokens (for MCP / integrations)

| Method | Path | Role | Description |
|--------|------|------|-------------|
| GET | `/api/api-tokens` | admin | List tokens (no secrets) |
| POST | `/api/api-tokens` | admin | `{name, role}` → token (shown once) |
| DELETE | `/api/api-tokens/{id}` | admin | Revoke a token |

## Device groups & destructive actions

| Method | Path | Role | Description |
|--------|------|------|-------------|
| GET | `/api/groups` | readonly | List device groups |
| POST | `/api/groups` | admin | `{name}` |
| DELETE | `/api/groups/{id}` | admin | Delete a group (audited) |
| PATCH | `/api/devices/{id}/group` | admin | `{group_id}` assign/clear group |
| DELETE | `/api/devices/{id}` | admin | Delete a device (audited; never via AI/MCP) |

## MCP endpoint

| Path | Auth | Description |
|------|------|-------------|
| `/mcp` | bearer (role ≥ tech) + optional IP allowlist | MCP server over Streamable HTTP for external AI clients. See [11-mcp](11-mcp.md). |

## WebSocket endpoints

| Path | Description |
|------|-------------|
| `/ws/agent` | Agent connection (see [05-agent-protocol](05-agent-protocol.md)) |
| `/ws/remote?token=&role=agent\|viewer` | Native remote relay (token from a remote session) |
