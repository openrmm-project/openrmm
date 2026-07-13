# 10 — Org, RBAC, Monitoring, Tasks, Policies & Alerts

This document covers the "classic RMM" subsystems added on top of the MVP,
inspired by Tactical RMM. All verified end-to-end against PostgreSQL.

## Org hierarchy: Client → Site → Agent

- **Client** is a customer/organization; **Site** is a location under a client;
  an **Agent** belongs to a site.
- An **enrollment token can be bound to a site** (`site_id`), so agents land in
  the right place automatically at enrollment.
- Reassign a device with `PATCH /api/devices/{id}/site`.

This hierarchy is also what **policies** target (see below).

## RBAC & authentication

Roles are named permission sets, not a fixed hierarchy. Built-in roles are seeded
on startup and custom roles can be added from **Settings → Roles**.

| Built-in role | Default access |
|---------------|----------------|
| `readonly` | Dashboard, Devices, Clients & Sites, Settings self-service; remote desktop only (`devices.remote`, no `devices.files`) |
| `tech` | Operational modules: AI Chat, MCP, Jobs, Alerts, Checks/Tasks/Policies view, Scripts view, Patches, Devices remote/files/terminal, Reports |
| `admin` | Every permission: users, roles, config, enrollment, API tokens, audit, clients/sites management, device deletion |

Permissions are module/action keys such as `ai_chat.use`, `mcp.use`,
`jobs.create`, `jobs.manage`, `alerts.manage`, `checks.view`, `devices.terminal`,
`settings.manage`, and `roles.manage`. The
backend enforces them on every route; the UI uses the same `permissions` list from
login and `/api/me` to hide navigation/actions.

Authentication accepts three credential types as `Authorization: Bearer <token>`:
1. **Session token** — from `POST /api/auth/login` (user + bcrypt password).
2. **API token** — issued rows in `api_tokens` with a role assignment, optionally
   bound to a user for access scope.
3. **Bootstrap admin token** — `RMM_ADMIN_TOKEN` (always admin; for first setup
   and automation).

First-run: `POST /api/auth/bootstrap` creates the first admin user (only allowed
while no users exist).

## Script library

Reusable scripts (`name`, `category`, `shell`, `body`, `default_args`). Run one
on a device with `POST /api/devices/{id}/run-script { script_id, require_approval }`
— this creates a job on the normal approval-gated path.

## Monitoring checks

A **check** is owned by an agent (direct) **or** a policy (inherited). Types:

| Type | Measures | Threshold semantics |
|------|----------|---------------------|
| `diskspace` | free %, on `config.path` | alert when **below** warn/err |
| `memory` | free % | alert when **below** warn/err |
| `cpuload` | CPU usage % | alert when **above** warn/err |
| `ping` | host reachable (`config.host`) | fail when unreachable |
| `winservice` | Windows service running (`config.service`) | fail when not running |
| `script` | exit code (`config.shell`, `config.script`) | 0=passing, 1=warning, else failing |

The **server** owns scheduling and threshold evaluation: the scheduler sends a
`run_check` to the online agent when a check is due (per `interval_sec`); the
agent returns a raw value/output; the server computes `passing`/`warning`/
`failing`, stores a `check_result`, and raises or resolves the alert. Checks are
**enabled on creation** by default.

## Scheduled tasks

A **task** runs a script (from the library via `script_id`, or inline) on a
schedule. The scheduler dispatches due tasks as **pre-approved** jobs
(`tasks.manage` automation — distinct from AI-proposed scripts, which always
require human approval). Each run is linked via `jobs.task_id`. Tasks may be
owned by an agent or a policy.

**Schedule kinds** (`schedule_kind`):

| Kind | Fields | Fires |
|------|--------|-------|
| `interval` | `interval_sec` | every N seconds |
| `daily` | `run_at_minute` (minutes since UTC midnight) | once per day at that time |
| `weekly` | `days_of_week` (bitmask, bit N = `time.Weekday` N, 0=Sun) + `run_at_minutes[]` | on each selected weekday, at every listed time |

Weekly is due-computed in `scheduler.taskDue` by finding the most recent
`(active weekday × time)` occurrence at/before now (scanning back ≤8 days) and
comparing it to the last run — so every day/time slot fires once and a missed
slot still runs on the next tick.

**Run on demand.** `POST /api/tasks/{id}/run` (`tasks.manage`) enqueues the task
immediately on all its current target agents, outside the schedule (offline
agents get it on reconnect); `GET /api/tasks/{id}/targets` returns the agent
count so the UI can confirm before firing. Both resolve a policy task's agents
via `AgentIDsForPolicy` and are narrowed to the caller's access scope.

## Policies (inheritance)

A **policy** bundles checks and tasks and is **assigned to a scope**: a client, a
site, or a specific agent (`policy_assignments`). An agent inherits every policy
whose scope covers it (its own id, its site, or its client). Resolution happens
in the store (`ChecksForAgent` / `TasksForAgent`), so one policy can push a
baseline of checks/tasks to a whole client at once.

## Alerts

Raised automatically when a check is `failing`/`warning` or an agent goes offline
(no heartbeat for >3 min). A partial unique index guarantees **at most one open
alert per source**, so alerts don't spam. When the check recovers (or the agent
returns), the alert **auto-resolves**. Operators can `ack` or `resolve` manually.

Severities: `info` / `warning` / `error`. Statuses: `new` / `acked` / `resolved`.

## Endpoint summary

See [04-api-reference](04-api-reference.md) for the full list. New groups:
`/api/auth/*`, `/api/users`, `/api/clients`, `/api/sites`, `/api/scripts`,
`/api/checks`, `/api/tasks`, `/api/policies`, `/api/alerts`.
