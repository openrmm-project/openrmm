# 04 — API Reference

Base URL: the console/API origin (the same origin as the SPA in the bundled
deployment). In a split-ingress deployment, agents use `RMM_PUBLIC_URL` while MCP
OAuth metadata uses `RMM_MCP_PUBLIC_URL`; see
[`deploy/README-production.md`](../deploy/README-production.md).

## Authentication

All `/api/*` routes require a bearer token:

```
Authorization: Bearer <token>
```

Valid tokens: a **session token** from `POST /api/auth/login` (or from
`POST /api/auth/oidc/exchange` after single sign-on), an **API token**
(`api_tokens`), or the bootstrap `RMM_ADMIN_TOKEN`. The console itself signs in
only with a password (plus second factor) or SSO; it does not accept a pasted
token. Each carries a role key
(`admin`/`tech`/`readonly` or custom), which resolves to granular permissions —
see the Permission column below and [10-monitoring-policies](10-monitoring-policies.md).
WebSocket endpoints authenticate via their own handshake/token and do not use this
header.

Errors are JSON: `{"error":"message"}` with an appropriate HTTP status.

**Access scope.** When the caller's token is bound to a user with a restricted
access scope, all list endpoints (devices, jobs, alerts, checks, tasks,
check-results, clients, sites) are **filtered to in-scope devices**, and
per-resource actions return `403` for out-of-scope targets. Role and scope are
orthogonal: even a powerful custom role remains scoped when bound to a restricted
user. The bootstrap admin token and users/tokens without scope rows see the whole
fleet. See
[07-guardrails-security](07-guardrails-security.md).

## Roles & permissions

Built-in roles are seeded on startup:

| Role | Default permissions |
|------|---------------------|
| `readonly` | `devices.view`, `devices.remote`, `clients.view` (remote desktop only; no file transfer/terminal; not purely observational) |
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
| GET | `/readyz` | none | Readiness probe: pings the database (2 s timeout) → `ready`, or **503** while it is unreachable |
| GET | `/metrics` | `Authorization: Bearer $RMM_METRICS_TOKEN` | Prometheus text format. **404** unless `RMM_METRICS_TOKEN` is set |

`/healthz` checks only that the HTTP process can answer; `/readyz` adds the
database and nothing else. Neither tests the scheduler, agent hub, AI provider,
relay, backup, or edge.

`/metrics` exposes online agents, goroutines, database pool connections,
monitoring errors (`rmm_alert_processing_errors_total`,
`rmm_check_dispatch_failures_total`, `rmm_check_timeouts_total`),
`rmm_audit_write_failures_total` (any value above zero means the audit trail has
gaps) and the alert outbox (`rmm_notifications_pending`, `_processing`,
`_failed`, `_oldest_pending_seconds`). See
[20-email-notifications](20-email-notifications.md) for what to alert on.

## Enrollment

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| POST | `/api/enroll-tokens` | `enrollment.manage` | Create an enroll token |

Body: `{ "description": "lab", "uses": 1, "ttl_hours": 24, "site_id": "…",
"group_id": "…" }` (all optional; `uses` defaults to 1). Prefer one-use tokens for
individual endpoints; a multi-use token is an intentionally broader enrollment
capability. Returns the token object — **the token value is only shown here**.

`site_id` and `group_id` decide where the agent lands: give `group_id` to enrol
straight into a department. The department must belong to the given site — an
inconsistent pair is rejected, by the handler and again by a database trigger.
The installer's `--site` flag does not affect this; it is accepted and discarded,
because the destination is a property of the token, not of the command line.

A caller restricted by access scope must bind the token to a site directly in its
site/client scope. Unbound tokens, or tokens for a site reached only through a
single agent/group scope, are rejected because they could enroll devices outside
the caller's authority.

## Devices

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/devices` | `devices.view` | List all agents → `{ "devices": [...] }` |
| GET | `/api/devices/{id}` | `devices.view` | One agent |
| GET | `/api/devices/{id}/inventory` | `devices.view` | Latest inventory snapshot |
| GET | `/api/devices/{id}/remote-capabilities` | `devices.view` | On-demand conservative native-desktop capability preflight |
| GET | `/api/devices/{id}/checks` | `checks.view` | The enabled checks that run on this device — assigned to it or inherited from a policy on its department, site or client (what the scheduler runs) — each with `policy_name` and this device's `status`, `value`, `output`, `last_result_at`. Direct ones first |
| GET | `/api/devices/{id}/tasks` | `tasks.view` | The enabled scheduled tasks that run on this device, direct or inherited, each with `policy_name` and `last_run_at` here |
| POST | `/api/devices/{id}/remote` | `devices.remote` / `devices.files` / `devices.terminal` | Start a remote session |
| PATCH | `/api/devices/{id}` | `devices.manage` | Set `{description, note}` (identification fields, scope-checked) |
| PATCH | `/api/devices/{id}/remote-ids` | `devices.manage` | Set external provider identifiers such as `mesh_id` |
| GET | `/api/remote/presence?agent_id=` | `devices.view` | Live remote attachments (who is connected to which device, with `user_name`), for the fleet or one device; scope-filtered. Informational: sessions are never refused because someone else is connected |
| POST | `/api/devices/{id}/revoke` | `devices.delete` | Revoke device identity, close live control/relay connections, and require fresh enrollment |
| POST | `/api/devices/{id}/rotate-credential` | `devices.delete` | Rotate an online trusted device's credential over its authenticated control channel |
| POST | `/api/devices/{id}/cancel-credential-rotation` | `devices.delete` | Request cancellation of a staged replacement; completes only after the live agent confirms restoration |

`POST /devices/{id}/remote` body: `{ "provider": "native" }` — remote desktop
requires `devices.remote`; in-band file transfer inside the native viewer is
disabled unless the caller also has `devices.files`. Also supports `files`
(file transfer only, no screen capture; requires `devices.files`), `terminal`
(an elevated shell; requires `devices.terminal`), `meshcentral`, or `rustdesk`
(optional, off by default; reads the device's RustDesk id on demand and returns a
`rustdesk://<id>` deep link — see [08-remote-access](08-remote-access.md)); omitted
 → `RMM_REMOTE_PROVIDER`. Native/files/terminal returns a one-time
`viewer_ticket` plus session id; external providers return their `connect_url`.
Only one credential rotation may be pending for a device. After the agent
durably stores a replacement credential it acknowledges and reconnects with it;
the reconnect commits the replacement. Rotation and cancellation require agent
version `0.4.5` or later. A cancellation first fences replacement-token promotion, then remains
pending until the agent durably restores its prior credential and the server
acknowledges that confirmation.

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
| POST | `/api/jobs/{id}/complete` | `jobs.manage` | Force-finalize an in-flight job as completed (or failed with `{"success":false}`) when the agent's result never arrived — audited as `job.complete_manual` |
| GET | `/api/jobs/batch/{batch_id}` | `jobs.view` | The jobs in a bulk batch (scope-filtered) |
| POST | `/api/jobs/batch/{batch_id}/approve` | `jobs.manage` | Approve every awaiting job in the batch |
| POST | `/api/jobs/batch/{batch_id}/reject` | `jobs.manage` | Reject every awaiting job in the batch |

`POST /jobs` body:
```json
{ "agent_id": "...", "shell": "bash", "script": "...",
  "description": "...", "require_approval": true }
```
`require_approval` defaults to `true`. `false` is honored only when the caller
also has `jobs.manage` **and** the server explicitly enables
`RMM_ALLOW_UNAPPROVED_JOBS=true`; otherwise the request remains approval-gated.
AI proposal tools never use this bypass.

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

## Dashboard

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/dashboard` | any signed-in principal | Counts over what the caller may see: `devices` `{total, online}` (with `devices.view`) and `jobs` `{awaiting, failed, failed_window, awaiting_items}` (with `jobs.view`). `failed` covers the last `failed_window` hours; `awaiting_items` are the 5 newest jobs waiting for approval, without script or output |

## Audit

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/audit` | `audit.view`, unscoped | Audit entries, newest first → `{entries, total, limit, offset, next_cursor?}` |
| GET | `/api/audit/facets` | `audit.view`, unscoped | Distinct actors and action families present, for building filters → `{actors, actions}` |
| GET | `/api/exports/csv?token=` | the account the link was minted for | Stream a CSV export (audit or inventory) described by a signed, expiring token |

`/api/audit` filters: `actor`, `action`, `target`, `since` and `until` (RFC 3339;
a malformed time is **400**, never silently ignored), `limit`, and either
`offset` or the opaque `cursor` from the previous page's `next_cursor`. Prefer the
cursor: the trail is append-only and newest-first, so an offset drifts while
others work. `next_cursor` is present only when there is more to read. A caller
with a restricted access scope gets **403** — the trail is global.

Export links are minted by the console and by the AI's report tools. Nothing is
stored: fetching the link re-runs its query against the caller's **current**
access, so a link fails with **403** for another account or after the caller's
scope changed. Cells that start like a spreadsheet formula are prefixed with an
apostrophe.

## Chat (AI)

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/chat/sessions` | `ai_chat.use` | List conversations |
| POST | `/api/chat/sessions` | `ai_chat.use` | Create a conversation `{ "title": "..." }` |
| GET | `/api/chat/sessions/{id}/messages` | `ai_chat.use` | Full message history |
| DELETE | `/api/chat/sessions/{id}` | `ai_chat.use` | Delete a conversation owned by the caller |
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
| POST | `/api/auth/bootstrap` | admin token | Create the first admin only when no users exist; `Authorization: Bearer $RMM_ADMIN_TOKEN` is required so an empty instance cannot be claimed by whoever reaches it first; password must meet the current 12-character policy |
| POST | `/api/auth/login` | none | `{username,password,code?}` → seven-day session `{token, role, permissions, user}`. `code` is a TOTP **or** a one-time recovery code. If 2FA is on and `code` is missing/invalid → `401 {totp_required:true}` |
| POST | `/api/auth/logout` | any | Revoke the session token |
| POST | `/api/auth/password-reset` | none, rate limited | `{email}` → always **202**, answered before any lookup so neither the reply nor its timing says whether the account exists; mails a single-use link to a matching account. Throttled per account (one per five minutes, five a day). No mail for an SSO-linked account while SSO is on. See [20-email-notifications](20-email-notifications.md) |
| POST | `/api/auth/password-reset/confirm` | none, rate limited | `{token, password}`; the password is checked against the policy **before** the link is spent |
| POST | `/api/auth/verify-email` | none, rate limited | `{token}` — confirm an address from its link. Unknown, used, expired and outdated links get one answer |
| GET | `/api/me` | any | Current user/principal with `role` and `permissions` |
| GET | `/api/users` | `users.manage` | List users |
| POST | `/api/users` | `users.manage` | `{username,password,role,scopes?}` (`scopes`: array of `{scope_type,scope_id}`; `scope_type` = `client`/`site`/`group`/`agent`). An optional `email` additionally requires `users.email.manage` |
| PUT | `/api/users/{id}/email` | `users.manage` + `users.email.manage` | `{email}` — where their alerts go, and where a password reset link is sent (unique; blank clears) |
| POST | `/api/users/{id}/email/verify` | `users.manage` + `users.email.manage` | Send that user a new confirmation link → **202** `{status:"sent"}`, or `{status:"already_confirmed"}` |
| PATCH | `/api/users/{id}` | `users.manage` | `{role}` update a user's role |
| POST | `/api/users/{id}/disable` | `users.manage` | Withdraw access without deleting: sessions end immediately, bound API tokens stop, alert mail stops, no password reset is issued. Cannot target yourself or the built-in admin |
| POST | `/api/users/{id}/enable` | `users.manage` | Restore access |
| DELETE | `/api/users/{id}` | `users.manage` | Remove a user (cascades; prefer disable for a person who left) |
| GET | `/api/users/{id}/scopes` | `users.manage` | List a user's access-scope assignments |
| PUT | `/api/users/{id}/scopes` | `users.manage` | Replace scope assignments (`[]` = unrestricted; supports `client`/`site`/`group`/`agent`). Audited as `user.scopes.update`, flagging the `[]` case as `full_access` — it is the widest grant this endpoint makes |

### Account

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| POST | `/api/me/password` | any | `{current_password, new_password}` — change your own password (verifies current; min 12 chars and rejects a small common-password set; revokes your other sessions; audited `user.password.change`) |
| GET | `/api/me/notifications` | a user session | Your alert subscriptions, your address (read-only here), whether alert delivery is configured, and whether your address must be / is confirmed |
| PUT | `/api/me/notifications` | a user session | `{subscriptions: {event: bool}}`. Changing the address here is refused (**403**): that needs `users.email.manage` |
| POST | `/api/me/email/verify` | a user session | Send yourself a new confirmation link |

### Two-factor (TOTP)

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| POST | `/api/me/2fa/setup` | any real user | `{current_password, code?}` generates/stages a candidate → `{secret, uri}`; `code` is the active TOTP when rotating |
| POST | `/api/me/2fa/enable` | any real user | `{code}` confirms and atomically promotes the pending secret → `{totp_enabled, recovery_codes}` |
| POST | `/api/me/2fa/disable` | any real user | `{current_password, code}` disables 2FA and clears pending/active secrets and recovery codes |
| POST | `/api/me/2fa/recovery-codes` | any | `{code}` re-issues recovery codes → `{recovery_codes}` (invalidates the old set) |
| POST | `/api/users/{id}/2fa/reset` | `users.manage` | Disable a user's 2FA + clear recovery codes (unlock a locked-out user) |

The TOTP secret is encrypted at rest (AES-256-GCM). Setup writes
`totp_pending_secret`; it never changes an already-active secret. Rotation
requires current password + current TOTP at setup, then a code from the new
candidate at enable. Promotion and recovery-code replacement are atomic.
Enabling 2FA returns **10
one-time recovery codes** (shown once; stored hashed with SHA-256). At login, a
recovery code is accepted in the same `code` field as the TOTP code and is
consumed on use. A user manager can reset a locked-out user's 2FA; per-user 2FA
recovery does not require user management permission if the user kept their codes.

The console's two-factor card (Settings ▸ Users) asks for the current password
to start setup and for the password and a current code to disable.

A code is accepted once: the last accepted time step is stored per user, so a
code seen over a shoulder cannot sign in again within its window.

### Single sign-on (OIDC)

Present only when SSO is configured (`RMM_OIDC_*`); see
[21-single-sign-on](21-single-sign-on.md).

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/api/auth/oidc` | none | `{enabled, label}` — whether the login screen offers the button |
| GET | `/api/auth/oidc/start?redirect_to=` | none | Start a sign-in: sets a host-only binding cookie and redirects to the provider. `redirect_to` accepts a path only |
| GET | `/api/auth/oidc/callback` | none (provider redirect) | Finish it. Every failure lands on the console with a generic message; the reason is logged and audited |
| POST | `/api/auth/oidc/exchange` | the binding cookie + console `Origin` | `{code}` — trade the single-use, 60-second code from the callback for a session `{token, user, role, permissions}` |
| GET | `/api/auth/oidc/begin?state=` | none | Used by the MCP consent page, which runs on another origin: sets the binding cookie on the console origin, then redirects to the provider |

## Runtime configuration

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/config` | any | Effective non-secret config for UI display; `server.version` is the release the binary was built from (`dev` when unstamped) |
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

### Email transports

Two profiles; details in [20-email-notifications](20-email-notifications.md).

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/config/smtp` | `settings.manage` | The account transport (password resets, confirmations). Read-only: it comes from the environment |
| POST | `/api/config/smtp/test` | `settings.manage` | Test it — mails the caller's own address only |
| GET | `/api/config/smtp/alerts` | `settings.manage` | The alerts transport; `enabled` says whether alert mail will leave |
| PUT | `/api/config/smtp/alerts` | `settings.manage` | Set it. A blank host turns alert mail off; a sender or login without a host is **400** (`alerts_need_own_server`); a private relay address is refused unless `RMM_SMTP_ALERTS_ALLOW_PRIVATE=true` |
| POST | `/api/config/smtp/alerts/test` | `settings.manage` | Test the alerts transport |

## Org hierarchy

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/clients` | `clients.view` | List clients |
| POST | `/api/clients` | `clients.manage` | `{name}` |
| PATCH | `/api/clients/{id}` | `clients.manage` | `{name}` rename a client |
| DELETE | `/api/clients/{id}` | `clients.manage` | Delete with its sites, departments and their enrollment tokens; its devices become unassigned |
| GET | `/api/sites?client_id=` | `clients.view` | List sites |
| POST | `/api/sites` | `clients.manage` | `{client_id,name}` |
| PATCH | `/api/sites/{id}` | `clients.manage` | `{name}` rename a site |
| DELETE | `/api/sites/{id}` | `clients.manage` | Delete a site with its departments and enrollment tokens. **409** while it still has devices, with the count in the message — move or delete them first |
| PATCH | `/api/devices/{id}/site` | `clients.manage` | `{site_id}` reassign a device |
| POST | `/api/devices/bulk/move` | `clients.manage` | `{agent_ids, site_id, group_id?}` — all or none; up to 500 devices; every device and the destination must be in the caller's scope; each device audited as `device.move` |
| POST | `/api/devices/bulk/delete` | `devices.delete` | `{agent_ids, confirm_count}` — `confirm_count` must equal the number of distinct devices; up to 500; each deleted and audited on its own, failures listed in `failed` |

## Script library

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/scripts` | `scripts.view` | List scripts |
| GET | `/api/scripts/{id}` | `scripts.view` | One script |
| POST | `/api/scripts` | `scripts.manage` | Create (`name,shell,body,category,...`) |
| PUT | `/api/scripts/{id}` | `scripts.manage` | Update |
| DELETE | `/api/scripts/{id}` | `scripts.manage` | Delete |
| GET | `/api/script-categories` | `scripts.view` | Registered categories **and** those only a script carries, with script counts → `{categories:[{name,count}]}` |
| POST | `/api/script-categories` | `scripts.manage` | Register a category (`{name}`) so it exists before any script is filed under it. 409 when the name is taken, compared without case. Audited as `script.category.create` |
| PUT | `/api/script-categories/{name}` | `scripts.manage` | Rename the category and every script filed under it (`{name:"new"}`). Onto a name in use this **merges** the two. 404 when no such category. Audited as `script.category.rename` |
| DELETE | `/api/script-categories/{name}` | `scripts.manage` | Remove the category and return its scripts to uncategorized. The scripts are kept. Audited as `script.category.delete` |
| POST | `/api/devices/{id}/run-script` | `jobs.create` | `{script_id, require_approval}` → approval-gated job. `require_approval:false` is honored only when the caller also has `jobs.manage` and the server allows unapproved jobs. |

## Checks & results

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/checks?agent_id=&policy_id=` | `checks.view` | List checks |
| POST | `/api/checks` | `checks.manage` | Create (`type,name,config,warn_threshold,err_threshold,interval_sec`, `agent_id` or `policy_id`). Flap policy is optional: `failure_count` (1–100, default 2), `recovery_count` (1–100, default 2), `hysteresis` (0–100, default 2), `cooldown_sec` (0–604800, default 300), `timeout_sec` (5–86400, default 180) |
| PUT | `/api/checks/{id}` | `checks.manage` | Update |
| DELETE | `/api/checks/{id}` | `checks.manage` | Delete |
| GET | `/api/checks/{id}/results?agent_id=&limit=` | `checks.view` | Result history |
| GET | `/api/checks/{id}/instances` | `checks.view` | Per-agent state of this check: streaks, last value/output, pending execution and its deadline |

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
| PUT | `/api/policies/{id}` | `policies.manage` | Rename a policy / rewrite its description (`{name,description}`). Relabels only — checks, tasks and assignments are untouched. Audited as `policy.update` |
| DELETE | `/api/policies/{id}` | `policies.manage` | Delete |
| GET | `/api/policies/{id}/assignments` | `policies.view` | List assignments |
| POST | `/api/policies/{id}/assignments` | `policies.manage` | `{scope_type: client\|site\|agent, scope_id}` |
| DELETE | `/api/policies/assignments/{aid}` | `policies.manage` | Remove assignment |

## Alerts

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/alerts?status=&limit=` | `alerts.view` | List alerts |
| GET | `/api/alerts/{id}` | `alerts.view` | One alert with its lifecycle fields (occurrence count, first/last seen, observed vs threshold value, ack/resolve attribution, `version`) |
| GET | `/api/alerts/{id}/events` | `alerts.view` | The alert's timeline from `alert_events` |
| POST | `/api/alerts/{id}/ack` | `alerts.manage` | Acknowledge |
| POST | `/api/alerts/{id}/resolve` | `alerts.manage` | Resolve |
| POST | `/api/alerts/{id}/suppress` | `alerts.manage` | Silence this alert (`{note, silence_until, expected_version}`) |
| POST | `/api/alerts/bulk` | `alerts.manage` | One action over many: `{ids[], action: ack\|resolve\|suppress, note, silence_until, expected_versions{}}` |
| GET | `/api/alert-suppressions` | `alerts.view` | Active silence / maintenance windows |
| POST | `/api/alert-suppressions` | `alerts.manage` | Open a window (`kind: silence\|maintenance`, `ends_at`, `reason`) |
| DELETE | `/api/alert-suppressions/{id}` | `alerts.manage` | Cancel a window early |

## Reports

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/reports/software?scope=&id=` | `reports.view` | Scope-filtered software inventory |
| GET | `/api/reports/hardware?scope=&id=` | `reports.view` | Scope-filtered hardware inventory |
| GET | `/api/reports/machines?scope=&id=` | `reports.view` | Scope-filtered machine inventory |
| GET | `/api/reports/compliance` | `reports.view` or `patches.view` | Patch compliance rows in caller scope |

Report `scope` is `client`, `site`, or `group`; an explicitly requested scope
must be visible to the caller.

## Patch management

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/devices/{id}/patches` | `patches.view` | Missing/current patch state for a device |
| POST | `/api/devices/{id}/patches/scan` | `patches.manage` | Ask an online agent to scan; offline returns `503` |
| POST | `/api/devices/{id}/patches/install` | `patches.manage` | `{identifiers:[...]}`; dispatch only explicitly approved identifiers |
| POST | `/api/devices/{id}/reboot` | `patches.manage` | `{force?:false}`; default rejects an active native relay session |
| GET | `/api/patches` | `patches.view` | Scope-filtered fleet catalog |
| POST | `/api/patches/{id}/approve` | `patches.manage` | `{scope_type,scope_id,decision}`; decision is `approve` or `decline` |
| GET/POST | `/api/patch-policies` | `patches.policies.manage` | List/create global patch policy definitions |
| PUT/DELETE | `/api/patch-policies/{id}` | `patches.policies.manage` | Update/delete a global definition |
| GET/POST | `/api/patch-policies/{id}/assignments` | `patches.policies.manage` | List/add client/site/agent assignment |
| DELETE | `/api/patch-policies/assignments/{aid}` | `patches.policies.manage` | Delete an assignment |

Patch policy definitions are global and cannot be mutated by a restricted
principal. Install is a dedicated agent protocol action, not a script job; there
is no streaming progress/cancel endpoint. See [16-patch-management](16-patch-management.md).

## API tokens (for MCP / integrations)

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/api-tokens` | `api_tokens.manage` | List tokens (no secrets) |
| POST | `/api/api-tokens` | `api_tokens.manage` | `{name, role, user_id?, expires_in_days?}` → token (shown once); binding inherits user scope, `0`/omitted expiry means no expiry |
| DELETE | `/api/api-tokens/{id}` | `api_tokens.manage` | Revoke a token |
| GET | `/api/oauth-apps` | `api_tokens.manage` | Registered MCP OAuth applications (hosts, status, active tokens, last use) and the registration policy |
| POST | `/api/oauth-apps/{id}/approve` | `api_tokens.manage`, unscoped | Approve a pending application |
| DELETE | `/api/oauth-apps/{id}` | `api_tokens.manage`, unscoped | Revoke an application: its tokens, codes and SSO grants go in one transaction |
| PUT | `/api/config/mcp-oauth` | `api_tokens.manage`, unscoped | Registration policy: any application or only allowed hosts, and whether new ones need approval |

## Delegation rules

A scoped principal with `users.manage` or `api_tokens.manage` cannot use those
permissions to escape its authority. User lists are filtered; the principal can
manage only users whose role permissions are a subset of its own and whose every
scope is inside its scope. It cannot create an unrestricted user, delegate the
global `admin` role, or assign a role with extra permissions. A scoped principal
may issue API tokens only for itself, with a non-admin role no broader than its
own; the token inherits that user's scope.

## Departments (device groups) & destructive actions

A **department** is a device group **under a site** (Company > Site >
Department); it never spans companies/sites. Optional.

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/groups` | `clients.view` | List departments (each carries its `site_id`) |
| POST | `/api/groups` | `clients.manage` | `{name, site_id}` — `site_id` is **required** |
| PATCH | `/api/groups/{id}` | `clients.manage` | `{name}` rename a department (audited as `group.rename`) |
| DELETE | `/api/groups/{id}` | `clients.manage` | Delete a department (audited) |
| PATCH | `/api/devices/{id}/group` | `clients.manage` | `{group_id}` assign/clear — the department must be in the device's own site |
| DELETE | `/api/devices/{id}` | `devices.delete` | Delete a device (audited). AI/MCP can only *propose* it — see below |

### Device operations proposed by the AI or MCP clients

`propose_move_devices` and `propose_delete_devices` store a proposal and change
nothing. A person decides it here; approving carries it out with **the
approver's** permissions and access scope, checked again at that moment, and
each device's audit entry records the proposal and who proposed it.

| Method | Path | Permission | Description |
|--------|------|------------|-------------|
| GET | `/api/device-operations` | `devices.view` | Waiting proposals first, then recent ones. A scoped caller sees only proposals whose devices are all in their scope |
| POST | `/api/device-operations/{id}/approve` | `clients.manage` for a move, `devices.delete` for a delete | A delete needs `{confirm_count}` equal to its device count. A refusal (permission, scope, count) leaves it waiting; **409** once someone else decided it |
| POST | `/api/device-operations/{id}/reject` | same | Discard it; nothing changes |

## MCP endpoint

| Path | Auth | Description |
|------|------|-------------|
| `/mcp` | bearer with `mcp.use` + optional IP allowlist | MCP server over Streamable HTTP for external AI clients. Tool catalog/results are filtered by the caller's permissions and access scope. See [11-mcp](11-mcp.md). |

## MCP OAuth (optional)

These routes exist only when `RMM_MCP_OAUTH_ENABLED=true`, which also requires an
HTTPS `RMM_MCP_PUBLIC_URL`:

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/.well-known/oauth-authorization-server` | none | Authorization-server metadata |
| GET | `/.well-known/oauth-protected-resource` | none | MCP protected-resource metadata |
| POST | `/oauth/register` | none | Dynamic Client Registration |
| GET/POST | `/oauth/authorize` | user credentials + consent | PKCE authorization code |
| GET | `/oauth/authorize/sso` | none (starts a provider sign-in) | Single sign-on for the consent form; returns with a five-minute, single-use grant bound to the client, redirect URI and PKCE challenge |
| POST | `/oauth/token` | code verifier or refresh token | Issue/rotate opaque OAuth tokens |

OAuth access tokens authenticate `/mcp` only and are rejected on the REST API.
See [17-mcp-oauth](17-mcp-oauth.md).

## WebSocket endpoints

| Path | Description |
|------|-------------|
| `/ws/agent` | Agent connection (see [05-agent-protocol](05-agent-protocol.md)) |
| `/ws/remote?session=&role=agent\|viewer` | Native remote relay. Agent sends `Authorization: Remote-Agent <ticket>`; viewer sends its one-time ticket and bearer identity in `Sec-WebSocket-Protocol`. Tickets are never accepted in a viewer URL. In split ingress, the agent uses the agents origin and the browser uses the console origin. |
