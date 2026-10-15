# 10 — Org, RBAC, Monitoring, Tasks, Policies & Alerts

This document covers the "classic RMM" subsystems added on top of the MVP. All
verified end-to-end against PostgreSQL.

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

First-run: `POST /api/auth/bootstrap` creates the first admin user. It needs
`Authorization: Bearer $RMM_ADMIN_TOKEN` and is refused once any user exists —
an empty instance is otherwise claimable by whoever reaches it first.

## Script library

Reusable scripts (`name`, `category`, `shell`, `body`, `default_args`). Run one
on a device with `POST /api/devices/{id}/run-script { script_id, require_approval }`
— this creates a job on the normal approval-gated path.

**Categories** are a catalogue of their own (`script_categories`), so one can be
named before any script is filed under it; `scripts.category` still stores the
name. The editor picks from what exists or names a new one, rather than accepting
free text — free text is how a category becomes two that differ by a keystroke.
Renaming a category rewrites every script filed under it, and renaming onto a
name already in use **merges** the two, which is the only repair for a split that
already happened. Removing one returns its scripts to uncategorized; the scripts
are kept. All three are audited (`script.category.create` / `.rename` /
`.delete`).

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

### A dispatch is tracked, not fired and forgotten

Each `run_check` becomes a row in `check_executions` with a deadline
(`timeout_sec`, default 180s). A partial unique index allows only **one pending
execution per (check, agent)**, so an agent that is slow or has dropped is never
sent the same check again while the first is still owed. Passing the deadline
marks the execution `timed_out` — counted by `rmm_check_timeouts_total` — rather
than leaving it pending forever.

### One alert per condition, not per reading

A single reading no longer decides anything. `check_states` keeps the per-agent
streak and the check declares its own flap policy:

| Field | Default | Meaning |
|-------|---------|---------|
| `failure_count` | 2 | consecutive failures before the alert is raised |
| `recovery_count` | 2 | consecutive successes before it clears |
| `hysteresis` | 2 | band a recovering value must clear the threshold by |
| `cooldown_sec` | 300 | quiet period before the same condition may raise again |

A value oscillating on its threshold therefore produces **one** alert instead of
one per reading. `GET /api/checks/{id}/instances` shows this state per agent.

## Scheduled tasks

A **task** runs a script (from the library via `script_id`, or inline) on a
schedule. The scheduler dispatches due tasks as **pre-approved** jobs
(`tasks.manage` automation — distinct from AI-proposed scripts, which always
require human approval). Each run is linked via `jobs.task_id`. Tasks may be
owned by an agent or a policy.

That pre-approved dispatch is exactly why a task the AI wrote does not simply
start running. `propose_task` and `propose_check` create the row **disabled** and
stamped with `proposed_by`; the scheduler selects `WHERE enabled`, so it stays
inert until a person enables it on this screen — and enabling it is the review,
recorded as `approved_by`. The database refuses to enable an unreviewed proposal.
Rows waiting for review are badged **AI proposal** in the console. See
[07-guardrails-security](07-guardrails-security.md#configuration-proposals-the-second-gate).

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

`PUT /api/policies/{id}` renames a policy and rewrites its description. It is a
relabel: the checks, tasks and assignments hanging off it are untouched, which
is the point — a typo in the name of a policy assigned across the fleet used to
be fixable only by rebuilding the policy, losing everything it contained.
Audited as `policy.update`.

The policy drawer manages what a policy **contains**, not only what it is
assigned to: each check and task can be edited in place, tasks can be enabled and
disabled there, and a new one can be added with the policy pre-selected as its
target. The forms are the ones the Checks and Scheduled tasks screens use.
Changing a policy's contents takes `checks.manage` / `tasks.manage`; renaming the
policy itself takes `policies.manage`.

## Alerts

Raised automatically when a check is `failing`/`warning` or an agent goes offline
(no heartbeat for >3 min). A partial unique index guarantees **at most one open
alert per source**, so alerts don't spam. When the check recovers (or the agent
returns), the alert **auto-resolves**. Operators can `ack` or `resolve` manually.

Severities: `info` / `warning` / `error`. Statuses: `new` / `acked` / `resolved`.

An alert is a lifecycle rather than a row that appears and disappears. It
deduplicates on a `condition_key` and counts occurrences with first/last-seen
times; it records the observed and threshold values that raised it, who
acknowledged or resolved it and how, and what it reopened from when a resolved
condition returns. `alert_events` keeps the timeline
(`GET /api/alerts/{id}/events`).

Two operators acting at once cannot silently overwrite each other: the alert
carries a `version` and the mutating endpoints accept `expected_version`. Bulk
actions (`POST /api/alerts/bulk`) take a map of expected versions for the same
reason.

**Silencing.** A `silence` or `maintenance` window (`alert_suppressions`) stops a
known-noisy condition or a planned outage from mailing, with a reason and an
author, without deleting the alert or disabling the check. Silence one alert with
`POST /api/alerts/{id}/suppress`, or open a standing window with
`POST /api/alert-suppressions`.

**Retention.** Monitoring history is pruned daily, 90 days by default, tunable
with `RMM_ALERT_RETENTION_DAYS`, `RMM_CHECK_RESULT_RETENTION_DAYS` and
`RMM_NOTIFICATION_RETENTION_DAYS`. The purge never age-deletes an active alert, a
pending check execution, or queued mail — it trims history, it does not drop work
still in flight.

## Endpoint summary

See [04-api-reference](04-api-reference.md) for the full list. New groups:
`/api/auth/*`, `/api/users`, `/api/clients`, `/api/sites`, `/api/scripts`,
`/api/checks`, `/api/tasks`, `/api/policies`, `/api/alerts`.
