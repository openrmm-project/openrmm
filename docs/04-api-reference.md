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

**Access scope.** When the caller's token is bound to a user with a restricted
access scope, all list endpoints (devices, jobs, alerts, checks, tasks,
check-results, clients, sites) are **filtered to in-scope devices**, and
per-resource actions return `403` for out-of-scope targets. Unrestricted/admin
callers see the whole fleet. See [07-guardrails-security](07-guardrails-security.md).

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
| PATCH | `/api/devices/{id}` | tech: set `{description, note}` (identification fields, scope-checked) |

`POST /devices/{id}/remote` body: `{ "provider": "native" }` — also `terminal`
(an elevated shell; requires the **tech** role), `meshcentral`, or `rustdesk`
(optional, off by default; reads the device's RustDesk id on demand and returns a
`rustdesk://<id>` deep link — see [08-remote-access](08-remote-access.md)); omitted
→ `RMM_REMOTE_PROVIDER`. Returns a `RemoteSession` including `connect_url`.

## Jobs

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/jobs?agent_id=&limit=` | List jobs (newest first) |
| POST | `/api/jobs` | Create/propose a job |
| POST | `/api/jobs/bulk` | tech: mass deployment — one script to many devices |
| GET | `/api/jobs/{id}` | One job (with result) |
| POST | `/api/jobs/{id}/approve` | Approve → dispatch |
| POST | `/api/jobs/{id}/reject` | Reject a proposal |
| POST | `/api/jobs/{id}/cancel` | Reject if pending, or request cancel of a running job |
| GET | `/api/jobs/batch/{batch_id}` | tech: the jobs in a bulk batch (scope-filtered) |
| POST | `/api/jobs/batch/{batch_id}/approve` | tech: approve every awaiting job in the batch |
| POST | `/api/jobs/batch/{batch_id}/reject` | tech: reject every awaiting job in the batch |

`POST /jobs` body:
```json
{ "agent_id": "...", "shell": "bash", "script": "...",
  "description": "...", "require_approval": true }
```
`require_approval` defaults to `true`. With `false`, the job is dispatched
immediately (use only for trusted automation, never for AI-authored scripts).

### Bulk deployment (`POST /jobs/bulk`)

```json
{ "agent_ids": ["..."], "site_id": "...", "group_id": "...",
  "shell": "bash", "script": "...", "description": "..." }
```
Targets are the **union** of `agent_ids` + a `site_id` + a `group_id`
(department), intersected with the caller's access scope. The targets are then
**split by company** — a deployment never spans companies — into **one
approval-gated batch per company**. So a "deploy to all companies" request fans
out into several batches, each approved/run within its own company. Approval
stays mandatory. Response:
```json
{ "batches": [{ "client_id": "...", "batch_id": "...", "count": 3 }],
  "companies": 1, "count": 3 }
```
Review/approve a batch as one unit via `/api/jobs/batch/{batch_id}` (jobs share a
`batch_id`). Same engine backs the AI `propose_bulk_deploy` tool — see
[06-ai-chat](06-ai-chat.md).

## Audit

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/audit?limit=` | Recent audit entries (newest first) — **admin only** |

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
approval in the UI. Chat sessions are **private to their creator** (owned via
`created_by`). When the assistant returns a Markdown table or ` ```csv ` block,
the UI offers an in-chat **CSV download** (client-side; nothing is written to any
endpoint) — see [06-ai-chat](06-ai-chat.md).

## Auth & users

| Method | Path | Role | Description |
|--------|------|------|-------------|
| POST | `/api/auth/bootstrap` | none | Create the first admin (only when no users exist) |
| POST | `/api/auth/login` | none | `{username,password,code?}` → `{token, role, user_id}`. `code` is a TOTP **or** a one-time recovery code. If 2FA is on and `code` is missing/invalid → `401 {totp_required:true}` |
| POST | `/api/auth/logout` | any | Revoke the session token |
| GET | `/api/me` | any | Current `{actor, role}` |
| GET | `/api/users` | admin | List users |
| POST | `/api/users` | admin | `{username,password,role,scopes?}` (`scopes`: array of `{scope_type,scope_id}`) |
| DELETE | `/api/users/{id}` | admin | Remove a user |
| GET | `/api/users/{id}/scopes` | admin | List a user's access-scope assignments |
| PUT | `/api/users/{id}/scopes` | admin | Replace scope assignments (`[]` = unrestricted) |

### Account

| Method | Path | Role | Description |
|--------|------|------|-------------|
| POST | `/api/me/password` | any | `{current_password, new_password}` — change your own password (verifies current; min 8 chars; revokes your other sessions; audited `user.password.change`) |

### Two-factor (TOTP)

| Method | Path | Role | Description |
|--------|------|------|-------------|
| POST | `/api/me/2fa/setup` | any | Generate a secret → `{secret, uri}` (render as QR) |
| POST | `/api/me/2fa/enable` | any | `{code}` confirms the secret, enables 2FA → `{totp_enabled, recovery_codes}` |
| POST | `/api/me/2fa/disable` | any | `{code}` disables 2FA (and clears recovery codes) |
| POST | `/api/me/2fa/recovery-codes` | any | `{code}` re-issues recovery codes → `{recovery_codes}` (invalidates the old set) |
| POST | `/api/users/{id}/2fa/reset` | admin | Disable a user's 2FA + clear recovery codes (unlock a locked-out user) |

The TOTP secret is encrypted at rest (AES-256-GCM). Enabling 2FA returns **10
one-time recovery codes** (shown once; stored hashed with SHA-256). At login, a
recovery code is accepted in the same `code` field as the TOTP code and is
consumed on use. An admin can reset a locked-out user's 2FA; per-user 2FA
recovery does not require admin if the user kept their codes.

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
    "meshcentral_token_key": "hex-login-token-key",
    "meshcentral_enabled": true,
    "rustdesk_enabled": false
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
| PATCH | `/api/clients/{id}` | admin | `{name}` rename a client |
| DELETE | `/api/clients/{id}` | admin | Delete (cascades to sites) |
| GET | `/api/sites?client_id=` | readonly | List sites |
| POST | `/api/sites` | admin | `{client_id,name}` |
| PATCH | `/api/sites/{id}` | admin | `{name}` rename a site |
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

## Departments (device groups) & destructive actions

A **department** is a device group **under a site** (Company > Site >
Department); it never spans companies/sites. Optional.

| Method | Path | Role | Description |
|--------|------|------|-------------|
| GET | `/api/groups` | readonly | List departments (each carries its `site_id`) |
| POST | `/api/groups` | admin | `{name, site_id}` — `site_id` is **required** |
| DELETE | `/api/groups/{id}` | admin | Delete a department (audited) |
| PATCH | `/api/devices/{id}/group` | admin | `{group_id}` assign/clear — the department must be in the device's own site |
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
