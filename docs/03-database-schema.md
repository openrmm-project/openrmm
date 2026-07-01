# 03 — Database Schema

PostgreSQL. The canonical schema is embedded in the server
(`internal/store/schema.sql`) and applied idempotently on every start. A copy
lives at `db/schema.sql` for manual `psql` use.

## Tables

### `agents`
Managed endpoints.
| Column | Type | Notes |
|--------|------|-------|
| id | uuid PK | agent identity |
| hostname, os, arch, version | text | reported at enrollment |
| description | text | operator-editable label, shown in the device list (e.g. the user's name) |
| note | text | operator-editable private observation, shown only in device detail |
| status | text | `online` / `offline` |
| token_hash | text | SHA-256 of the agent's auth token |
| site_id | uuid FK → sites | nullable; the agent's place in the org hierarchy |
| group_id | uuid FK → device_groups | nullable |
| tags | text[] | free-form labels |
| mesh_id | text | MeshCentral node id (nullable) |
| last_seen | timestamptz | heartbeat |
| created_at | timestamptz | |

### `device_groups`
**Departments**: a group bound to a site (`id`, `name`, `site_id` FK → sites
cascade, `created_at`). Company > Site > Department — a group never spans
companies/sites; optional. An agent's `group_id` must point at a department of
the agent's own site.

### `enroll_tokens`
One-time / limited-use enrollment tokens (token, uses_left, expires_at).

### `users`
Console accounts. `password_hash` (bcrypt), `role` (role key such as
`admin`/`tech`/`readonly` or a custom role), `totp_secret` (encrypted, nullable)
and `totp_enabled` for optional TOTP 2FA.

### `roles` / `role_permissions`
Role definitions and their permission keys. `roles.role_key` is the stable key
stored in `users.role` and `api_tokens.role`; `system=true` marks the built-in
`readonly`, `tech`, and `admin` roles. `role_permissions` is keyed by
(`role_key`, `permission`) and stores granular module permissions such as
`devices.view`, `devices.remote`, `jobs.create`, `jobs.manage`, `roles.manage`.

### `user_recovery_codes`
One-time 2FA recovery codes (id, user_id, `code_hash` SHA-256, `used_at`
nullable). Issued when 2FA is enabled, single-use; cleared on disable/admin-reset.

### `user_scopes`
Per-user access scope. Each row grants a user access to a Company (client), a
Site, a Department (`group`), or a specific device; a user with no rows is
**unrestricted**. Enforced on read + write paths and on the AI/MCP surface.
| Column | Type | Notes |
|--------|------|-------|
| id | uuid PK | |
| user_id | uuid FK → users | cascade delete |
| scope_type | text | `client` / `site` / `group` / `agent` |
| scope_id | uuid | id of the client/site/group/agent |
| | | unique on (user_id, scope_type, scope_id) |

### `api_tokens`
Hashed API tokens for the UI/integrations (token_hash, name, role key, nullable
`user_id`). The role key resolves to the same permission set as a user role. When
bound to a user, the token inherits that user's access scope. The bootstrap
`RMM_ADMIN_TOKEN` is also accepted without a row and uses the built-in `admin`
permission set.

### `jobs`
Units of work targeted at an agent.
| Column | Type | Notes |
|--------|------|-------|
| id | uuid PK | |
| agent_id | uuid FK → agents | cascade delete |
| type | text | `run_script` (extensible) |
| description | text | human summary |
| shell | text | `bash`/`sh`/`powershell`/`cmd` |
| script | text | source to run |
| status | text | `awaiting_approval`→`pending`→`sent`→`running`→`completed`/`failed`, or `rejected` |
| requires_approval | bool | true for AI proposals |
| created_by | text | actor (`admin`, `api`, `ai:<actor>`) |
| approved_by | text | nullable |
| task_id | uuid | set when the job was produced by a scheduled task (nullable) |
| batch_id | uuid | bulk deployment: jobs created together share it; one batch per company (nullable) |
| exit_code | int | nullable until finished |
| stdout, stderr | text | result output |
| created_at, updated_at | timestamptz | |

### `inventory`
Latest snapshot per agent (`agent_id` PK, `data` jsonb, `collected_at`).

### `remote_sessions`
Brokered remote-access sessions (id, agent_id, provider, status, token,
connect_url, created_by, expires_at).

### `audit_log`
Immutable record of sensitive actions (actor, action, target, detail jsonb,
created_at). Actions include `job.propose`, `job.approve`, `job.reject`,
`job.result`, `remote.start`.

### `chat_sessions` / `chat_messages`
Conversations with the AI. `chat_sessions.created_by` owns the session (private
to its creator). `chat_messages` stores `role`, `content`, `tool_calls` (jsonb),
`tool_call_id`, `name` — mirroring the OpenAI-compatible schema so history can be
replayed to the model verbatim.

### `clients` / `sites`
The org hierarchy. `clients` (id, name, created_at) is a customer/organization;
`sites` (id, `client_id` FK → clients, name, created_at) is a location under a
client. An agent's `site_id` places it in this tree; deleting a client cascades
to its sites, and a deleted site nulls the agents'/enroll-tokens' `site_id`.

### `scripts`
Reusable script library: `id`, `name`, `description`, `category`,
`shell` (`bash`/`sh`/`powershell`/`cmd`), `body`, `default_args`, `created_at`,
`updated_at`. Run on a device via `/run-script`, which creates a job.

### `policies` / `policy_assignments`
A **policy** (id, name, description) bundles checks and tasks.
`policy_assignments` attaches a policy to a scope: `scope_type`
(`client`/`site`/`agent`) + `scope_id`. An agent inherits every policy whose
scope covers it (its own id, its site, or its client).

### `checks`
A monitoring check, owned **either** by an agent **or** a policy — exactly one of
`agent_id` / `policy_id` is set.
| Column | Type | Notes |
|--------|------|-------|
| id | uuid PK | |
| agent_id | uuid FK → agents | nullable (direct check) |
| policy_id | uuid FK → policies | nullable (inherited check) |
| type | text | `diskspace`/`memory`/`cpuload`/`ping`/`winservice`/`script` |
| name | text | |
| config | jsonb | type-specific (path / host / service / inline script) |
| warn_threshold, err_threshold | double | nullable |
| interval_sec | int | run cadence (default 300) |
| enabled | bool | default true |
| created_at | timestamptz | |

### `check_results`
Latest status + history per (check, agent): `id`, `check_id` FK → checks,
`agent_id` FK → agents, `status` (`passing`/`warning`/`failing`), `value` (double,
nullable), `output`, `ran_at`. Indexed by (check_id, agent_id, ran_at desc).

### `tasks`
Scheduled automation, owned by an agent or a policy (like checks).
| Column | Type | Notes |
|--------|------|-------|
| id | uuid PK | |
| agent_id / policy_id | uuid | exactly one set |
| name | text | |
| script_id | uuid FK → scripts | nullable; inline `shell`+`script` is the fallback |
| shell, script | text | inline source when no `script_id` |
| schedule_kind | text | `interval` / `daily` |
| interval_sec | int | for `interval` |
| run_at_minute | int | for `daily`: minutes since midnight (UTC) |
| enabled | bool | default true |
| created_at | timestamptz | |

Each dispatched run links back via `jobs.task_id`.

### `alerts`
Raised by a failing/warning check or an offline agent.
| Column | Type | Notes |
|--------|------|-------|
| id | uuid PK | |
| agent_id | uuid FK → agents | nullable |
| source_type | text | `check` / `task` / `agent` |
| source_id | uuid | the check/task id (nullable) |
| severity | text | `info` / `warning` / `error` |
| message | text | |
| status | text | `new` / `acked` / `resolved` |
| created_at, resolved_at | timestamptz | |

A partial unique index (`uq_alert_open` on `source_type, source_id, agent_id`
where `status <> 'resolved'`) guarantees **at most one open alert per source**.

### `sessions`
Browser login sessions (id, `user_id` FK → users, `token_hash` unique,
`expires_at`, created_at). A bearer login token from `POST /api/auth/login` maps
to a row here; logout / password-change revoke them.

### `app_settings`
Editable runtime configuration as key/value (`key` PK, `value`, `is_secret`,
`updated_at`). Secret values (AI API key, MeshCentral token key, TOTP secrets) are
encrypted (`enc:v1:`) before storage and never returned by the API.

## Relationships

```
clients 1───* sites 1───* agents
device_groups 1───* agents
policies 1───* policy_assignments   (scope: client | site | agent)
policies 1───* checks / tasks        (or owned directly by an agent)
agents 1───* jobs 0..1───* tasks     (jobs.task_id, for scheduled runs)
agents 1───1 inventory
agents 1───* check_results / alerts / remote_sessions
checks 1───* check_results
roles 1───* role_permissions
users 1───* sessions / user_scopes / user_recovery_codes
chat_sessions 1───* chat_messages
```
