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
`settings.manage`, and `roles.manage`. The AI autonomy permissions
(`ai.autonomy.query`, `ai.autonomy.run`, `mcp.autonomy.query`,
`mcp.autonomy.run`, and `ai.autonomy.manage` to release scripts) are held by the
built-in `admin` only; give them to others through a custom role
([23-ai-autonomy](23-ai-autonomy.md)). The
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
| `linuxservice` | Linux systemd service (`config.service`) | pass when loaded and active |
| `macservice` | macOS launchd service (`config.service`, `config.state`) | pass when running, or loaded if requested |
| `script` | exit code (`config.shell`, `config.script`) | 0=passing, 1=warning, else failing |

Linux and macOS service checks require **agent 0.10.0 or later**. Older
agents show an unknown check state with an upgrade message; no execution is
sent to them. In mixed-platform policies, these checks apply only to the
matching OS, including coverage and inbound results. Fleet alert defaults are
independent of these check types.

- Linux requires **systemd**. Enter one unit such as `sshd` (normalized to
  `sshd.service`), `nginx.service`, or `worker@blue.service`. Only literal
  service names are accepted, with no patterns, paths or commands. A loaded,
  active unit passes, including active/exited oneshot services.
- macOS uses **launchd**. A label such as `com.apple.notifyd` defaults to
  `system/com.apple.notifyd`. Explicit `system/label`, `gui/UID/label` and
  `user/UID/label` targets are supported. `config.state` defaults to `running`;
  choose `loaded` for on-demand jobs that need not run continuously. A missing
  service or inaccessible domain fails rather than being reported healthy.

The agent reads status with fixed argument lists, without a shell. Status
queries have a ten-second limit and a 64 KiB output budget. Only a status
summary is returned; launchd output can contain environment secrets and is
never forwarded to the server or model. These checks do not start, stop,
enable, or restart services.

What applies to one device — its own checks and tasks plus those its policies
bring — is listed in the device drawer's **Checks** and **Tasks** tabs, with each
check's state on that machine and each task's last run there
(`GET /api/devices/{id}/checks` and `/tasks`). Those tabs show only enabled rows,
the set the scheduler runs; disabled ones and unreviewed AI proposals are on the
Checks and Scheduled tasks screens.

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
| `daily` | `run_at_minute` (minutes since midnight in `timezone`) | once per day at that time |
| `weekly` | `days_of_week` (bitmask, bit N = `time.Weekday` N, 0=Sun) + `run_at_minutes[]` | on each selected weekday, at every listed time |

**Time zone.** `timezone` is the IANA zone (`America/New_York`, `Europe/Lisbon`,
`UTC`) that `run_at_minute`, `run_at_minutes` and the weekdays are read in,
daylight saving included, whatever zone the server or its container runs in. The
console starts a new task in the browser's zone and shows the zone next to every
time. A time a spring-forward day skips runs when that day's clock reaches it; a
time a fall-back day repeats runs once. A task created without a zone, and every
task from before zones existed, is UTC; an edit that sends no zone keeps the one
the task has.

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
site, a department, or a specific agent (`policy_assignments`). An agent
inherits every policy whose scope covers it (its own id, its department, its
site, or its client). The console's policy drawer assigns to any of the four
(`POST /api/policies/{id}/assignments`, where a department is `scope_type:
group`) and shows a department with its site, since two sites may each have one
of the same name. Resolution happens
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

### Switching a policy on and off

A policy has its own switch (`policies.enabled`, migration 032). Switched off,
it applies **nothing**: none of its tasks, checks or server alert rules reach
any device, whatever each one's own flag says. Every query that decides what a
device gets honours the switch — effective tasks and checks, the check
scheduler, "run now" on a policy task, the devices a check covers, and the
server rule that governs a device — and the sweep that closes the alerts of
checks that stopped applying closes those of a switched-off policy too.
Switching it off is one step (`POST /api/policies/{id}/deactivate`, audited as
`policy.deactivate`); its tasks and checks keep their own flags, so switching
it on again restores exactly what applied before. A person's new policy starts
switched on, as policies always did.

Switching a policy off stops future application of its configuration. It does
not cancel a job that was already created from an approved task or an authorized
manual run; that job may still be delivered after the device reconnects.

Switching a policy on goes through a **review**. `GET /api/policies/{id}/review`
returns everything the click puts into effect — the scopes, every task with its
script and schedule, every check with its settings, and which of them are AI
proposals — sealed with a fingerprint. The console shows it in one dialog, and
`POST /api/policies/{id}/activate { "fingerprint": "…" }` switches the policy
on and approves, in the same transaction, every AI proposal it carries. The
server re-reads the policy with its rows locked and refuses the activation
(409) when it no longer matches the review — an assistant that proposed one
more task while the dialog was open, or a colleague who edited a script — and
the console reloads the review for a second reading. A task or check a person
switched off stays off. Activating takes `policies.manage` and an unrestricted
principal, as every policy mutation does, plus `tasks.view` and `checks.view`:
nobody switches on what they cannot read. Approving the AI tasks or checks
inside also takes `tasks.manage` or `checks.manage`, so the activation never
makes an approval its caller could not make alone. The review shows only what
its reader may see — without `tasks.view` or `checks.view` that section is left
out and the review carries no fingerprint, and a principal confined to some
clients sees only the scopes it controls, as the assignment listing does. Audited as `policy.activate`
with the policy name and the number of tasks and checks approved.

### Policies proposed by AI Chat and MCP

`propose_policy` creates a policy **in one piece**: its name, where it applies
(clients, sites, departments or devices), and the tasks and checks or server
alert rules it carries, with the same fields and validation as `propose_task`
and `propose_check` (at most 25 of each). All of it is written in one
transaction, **switched off** and attributed (`proposed_by`), and a refusal
anywhere in the request writes nothing. The database refuses to switch on an
AI policy nobody reviewed (`policies_proposal_needs_review`), as it already did
for tasks and checks. A person reads it in Policies and activates it as above;
to reject it, they delete it. No tool assigns or activates an existing policy,
so the model can never widen the reach of something already running.

Migration 032 keeps what applied applying: before it, a policy had no review of
its own and a person reviewed an AI policy by assigning it (no tool assigns), so
an AI policy that has an assignment is marked reviewed (`approved_by =
'migration:032'`); an unassigned one, which applied to nothing, now waits
switched off.

AI-proposed tasks and checks outside a policy are approved through the same
kind of review: switching one on in the console opens a dialog with its target,
schedule or settings and the script itself, and confirming it approves and
enables it (`task.approve` / `check.approve` in the audit log).

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

## Server alert rules

Checks also manages connectivity, failed jobs and patches, pending reboots and outdated agents. These rules run on the server, use the broadest assigned scope first, and fall back to configurable fleet defaults. See [Alert rules](24-alert-rules.md) for precedence, configuration, API and email selection. Alerts remains the incident inbox.
