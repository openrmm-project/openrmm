# 04 — API Reference

Base URL: `RMM_PUBLIC_URL` (default `http://localhost:8080`).

## Authentication

All `/api/*` routes require a bearer token:

```
Authorization: Bearer <token>
```

Valid tokens: a **session token** from `POST /api/auth/login`, an **API token**
(`api_tokens`), or the bootstrap `RMM_ADMIN_TOKEN`. Each carries a role key
(`admin`/`tech`/`readonly` or custom), which resolves to granular permissions —
see the Permission column below and [10-monitoring-policies](10-monitoring-policies.md).
WebSocket endpoints authenticate via their own handshake/token and do not use this
header.

Errors are JSON: `{"error":"message"}` with an appropriate HTTP status.

**Access scope.** When the caller's token is bound to a user with a restricted
access scope, all list endpoints (devices, jobs, alerts, checks, tasks,
check-results, clients, sites) are **filtered to in-scope devices**, and
per-resource actions return `403` for out-of-scope targets. Unrestricted callers
and the built-in `admin` role see the whole fleet. See
[07-guardrails-security](07-guardrails-security.md).

## Roles & permissions

Built-in roles are seeded on startup:

| Role | Default permissions |
|------|---------------------|
| `readonly` | `devices.view`, `devices.remote`, `clients.view` (remote desktop only; no file transfer) |
| `tech` | AI chat, MCP, jobs view/create/manage, alerts, scripts view, patch operations, reports, device metadata/remote/files/terminal, clients view, checks/tasks/policies view |
| `admin` | every permission, including users, roles, config, enrollment, API tokens, audit, clients/sites management, delete devices |

Custom roles are managed with:

The UI generates the technical role `key` from the display `name` when creating a
role. Direct API clients still send the `key` explicitly because users and API
tokens store that stable identifier.

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/roles` | `roles.manage` or `users.manage` or `api_tokens.manage` | List roles with permission keys |
| GET | `/api/permissions` | `roles.manage` | List the permission catalog |
| POST | `/api/roles` | `roles.manage` | Create a custom role `{key,name,description,permissions}` |
| PUT | `/api/roles/{key}` | `roles.manage` | Update a custom role |
| DELETE | `/api/roles/{key}` | `roles.manage` | Delete a custom role not assigned to users/tokens |

## Health

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/healthz` | none | Liveness probe → `ok` |

## Enrollment

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| POST | `/api/enroll-tokens` | `enrollment.manage` | Create an enroll token |

Body: `{ "description": "lab", "uses": 5, "ttl_hours": 24 }` (all optional;
`uses` defaults to 1). Returns the token object — **the token value is only shown
here**.

## Devices

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/devices` | `devices.view` | List all agents → `{ "devices": [...] }` |
| GET | `/api/devices/{id}` | `devices.view` | One agent |
| GET | `/api/devices/{id}/inventory` | `devices.view` | Latest inventory snapshot |
| POST | `/api/devices/{id}/remote` | `devices.remote` / `devices.files` / `devices.terminal` | Start a remote session |
| PATCH | `/api/devices/{id}` | `devices.manage` | Set `{description, note}` (identification fields, scope-checked) |

`POST /devices/{id}/remote` body: `{ "provider": "native" }` — remote desktop
requires `devices.remote`; in-band file transfer inside the native viewer is
disabled unless the caller also has `devices.files`. Also supports `files`
(file transfer only, no screen capture; requires `devices.files`), `terminal`
(an elevated shell; requires `devices.terminal`), `meshcentral`, or `rustdesk`
(optional, off by default; reads the device's RustDesk id on demand and returns a
`rustdesk://<id>` deep link — see [08-remote-access](08-remote-access.md)); omitted
→ `RMM_REMOTE_PROVIDER`. Returns a `RemoteSession` including `connect_url`.

## Jobs

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/jobs?agent_id=&limit=` | `jobs.view` | List jobs (newest first) |
| POST | `/api/jobs` | `jobs.create` | Create/propose an approval-gated job |
| POST | `/api/jobs/bulk` | `jobs.create` | Mass deployment — one script to many devices, approval-gated |
| GET | `/api/jobs/{id}` | `jobs.view` | One job (with result) |
| POST | `/api/jobs/{id}/approve` | `jobs.manage` | Approve → dispatch |
| POST | `/api/jobs/{id}/reject` | `jobs.manage` | Reject a proposal |
| POST | `/api/jobs/{id}/cancel` | `jobs.manage` | Reject if pending, or request cancel of a running job |
| GET | `/api/jobs/batch/{batch_id}` | `jobs.view` | The jobs in a bulk batch (scope-filtered) |
| POST | `/api/jobs/batch/{batch_id}/approve` | `jobs.manage` | Approve every awaiting job in the batch |
| POST | `/api/jobs/batch/{batch_id}/reject` | `jobs.manage` | Reject every awaiting job in the batch |

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
`jobs.create` can create jobs only; approving, rejecting, canceling, or bypassing
approval requires `jobs.manage`.

Review/approve a batch as one unit via `/api/jobs/batch/{batch_id}` (jobs share a
`batch_id`). Same engine backs the AI `propose_bulk_deploy` tool — see
[06-ai-chat](06-ai-chat.md).

## Audit

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/audit?limit=` | `audit.view` | Recent audit entries (newest first) |

## Chat (AI)

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/chat/sessions` | `ai_chat.use` | List conversations |
| POST | `/api/chat/sessions` | `ai_chat.use` | Create a conversation `{ "title": "..." }` |
| GET | `/api/chat/sessions/{id}/messages` | `ai_chat.use` | Full message history |
| POST | `/api/chat/sessions/{id}/messages` | `ai_chat.use` | Send a user message; runs the tool-calling loop |

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

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| POST | `/api/auth/bootstrap` | none | Create the first admin (only when no users exist) |
| POST | `/api/auth/login` | none | `{username,password,code?}` → `{token, role, permissions, user}`. `code` is a TOTP **or** a one-time recovery code. If 2FA is on and `code` is missing/invalid → `401 {totp_required:true}` |
| POST | `/api/auth/logout` | any | Revoke the session token |
| GET | `/api/me` | any | Current user/principal with `role` and `permissions` |
| GET | `/api/users` | `users.manage` | List users |
| POST | `/api/users` | `users.manage` | `{username,password,role,scopes?}` (`scopes`: array of `{scope_type,scope_id}`; `scope_type` = `client`/`site`/`group`/`agent`) |
| PATCH | `/api/users/{id}` | `users.manage` | `{role}` update a user's role |
| DELETE | `/api/users/{id}` | `users.manage` | Remove a user |
| GET | `/api/users/{id}/scopes` | `users.manage` | List a user's access-scope assignments |
| PUT | `/api/users/{id}/scopes` | `users.manage` | Replace scope assignments (`[]` = unrestricted; supports `client`/`site`/`group`/`agent`) |

### Account

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| POST | `/api/me/password` | any | `{current_password, new_password}` — change your own password (verifies current; min 8 chars; revokes your other sessions; audited `user.password.change`) |

### Two-factor (TOTP)

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| POST | `/api/me/2fa/setup` | any | Generate a secret → `{secret, uri}` (render as QR) |
| POST | `/api/me/2fa/enable` | any | `{code}` confirms the secret, enables 2FA → `{totp_enabled, recovery_codes}` |
| POST | `/api/me/2fa/disable` | any | `{code}` disables 2FA (and clears recovery codes) |
| POST | `/api/me/2fa/recovery-codes` | any | `{code}` re-issues recovery codes → `{recovery_codes}` (invalidates the old set) |
| POST | `/api/users/{id}/2fa/reset` | `users.manage` | Disable a user's 2FA + clear recovery codes (unlock a locked-out user) |

The TOTP secret is encrypted at rest (AES-256-GCM). Enabling 2FA returns **10
one-time recovery codes** (shown once; stored hashed with SHA-256). At login, a
recovery code is accepted in the same `code` field as the TOTP code and is
consumed on use. A user manager can reset a locked-out user's 2FA; per-user 2FA
recovery does not require user management permission if the user kept their codes.

## Runtime configuration

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/config` | any | Effective non-secret config for UI display |
| PUT | `/api/config` | `settings.manage` | Update editable runtime settings |

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

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/clients` | `clients.view` | List clients |
| POST | `/api/clients` | `clients.manage` | `{name}` |
| PATCH | `/api/clients/{id}` | `clients.manage` | `{name}` rename a client |
| DELETE | `/api/clients/{id}` | `clients.manage` | Delete (cascades to sites) |
| GET | `/api/sites?client_id=` | `clients.view` | List sites |
| POST | `/api/sites` | `clients.manage` | `{client_id,name}` |
| PATCH | `/api/sites/{id}` | `clients.manage` | `{name}` rename a site |
| DELETE | `/api/sites/{id}` | `clients.manage` | Delete site |
| PATCH | `/api/devices/{id}/site` | `clients.manage` | `{site_id}` reassign a device |

## Script library

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/scripts` | `scripts.view` | List scripts |
| GET | `/api/scripts/{id}` | `scripts.view` | One script |
| POST | `/api/scripts` | `scripts.manage` | Create (`name,shell,body,category,...`) |
| PUT | `/api/scripts/{id}` | `scripts.manage` | Update |
| DELETE | `/api/scripts/{id}` | `scripts.manage` | Delete |
| POST | `/api/devices/{id}/run-script` | `jobs.create` | `{script_id, require_approval}` → approval-gated job. `require_approval:false` is honored only when the caller also has `jobs.manage` and the server allows unapproved jobs. |

## Checks & results

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/checks?agent_id=&policy_id=` | `checks.view` | List checks |
| POST | `/api/checks` | `checks.manage` | Create (`type,name,config,warn_threshold,err_threshold,interval_sec`, `agent_id` or `policy_id`) |
| PUT | `/api/checks/{id}` | `checks.manage` | Update |
| DELETE | `/api/checks/{id}` | `checks.manage` | Delete |
| GET | `/api/checks/{id}/results?agent_id=&limit=` | `checks.view` | Result history |

## Tasks (scheduled automation)

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/tasks?agent_id=&policy_id=` | `tasks.view` | List tasks |
| GET | `/api/tasks/{id}/targets` | `tasks.view` | Count of agents a run would hit (in caller scope) |
| POST | `/api/tasks` | `tasks.manage` | Create (`name, script_id` or `shell+script`, `schedule_kind` + `interval_sec` / `run_at_minute` / `days_of_week`+`run_at_minutes`, `agent_id` or `policy_id`) |
| PUT | `/api/tasks/{id}` | `tasks.manage` | Update |
| POST | `/api/tasks/{id}/run` | `tasks.manage` | Run now on all target agents (returns `{queued}`) |
| DELETE | `/api/tasks/{id}` | `tasks.manage` | Delete |

## Policies

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/policies` | `policies.view` | List policies |
| POST | `/api/policies` | `policies.manage` | `{name,description}` |
| DELETE | `/api/policies/{id}` | `policies.manage` | Delete |
| GET | `/api/policies/{id}/assignments` | `policies.view` | List assignments |
| POST | `/api/policies/{id}/assignments` | `policies.manage` | `{scope_type: client\|site\|agent, scope_id}` |
| DELETE | `/api/policies/assignments/{aid}` | `policies.manage` | Remove assignment |

## Alerts

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/alerts?status=&limit=` | `alerts.view` | List alerts |
| POST | `/api/alerts/{id}/ack` | `alerts.manage` | Acknowledge |
| POST | `/api/alerts/{id}/resolve` | `alerts.manage` | Resolve |

## API tokens (for MCP / integrations)

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/api-tokens` | `api_tokens.manage` | List tokens (no secrets) |
| POST | `/api/api-tokens` | `api_tokens.manage` | `{name, role}` → token (shown once) |
| DELETE | `/api/api-tokens/{id}` | `api_tokens.manage` | Revoke a token |

## Departments (device groups) & destructive actions

A **department** is a device group **under a site** (Company > Site >
Department); it never spans companies/sites. Optional.

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/groups` | `clients.view` | List departments (each carries its `site_id`) |
| POST | `/api/groups` | `clients.manage` | `{name, site_id}` — `site_id` is **required** |
| DELETE | `/api/groups/{id}` | `clients.manage` | Delete a department (audited) |
| PATCH | `/api/devices/{id}/group` | `clients.manage` | `{group_id}` assign/clear — the department must be in the device's own site |
| DELETE | `/api/devices/{id}` | `devices.delete` | Delete a device (audited; never via AI/MCP) |

## MCP endpoint

| Path | Auth | Description |
|------|------|-------------|
| `/mcp` | bearer with `mcp.use` + optional IP allowlist | MCP server over Streamable HTTP for external AI clients. Tool catalog/results are filtered by the caller's permissions and access scope. See [11-mcp](11-mcp.md). |

## WebSocket endpoints

| Path | Description |
|------|-------------|
| `/ws/agent` | Agent connection (see [05-agent-protocol](05-agent-protocol.md)) |
| `/ws/remote?token=&role=agent\|viewer` | Native remote relay (token from a remote session) |
