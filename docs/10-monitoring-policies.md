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

Three roles, ranked: `readonly` < `tech` < `admin`.

| Role | Can |
|------|-----|
| readonly | GET everything |
| tech | + run scripts, approve/reject jobs, start remote, ack/resolve alerts, use AI chat |
| admin | + manage users, clients/sites, scripts, checks, tasks, policies, enroll tokens |

Authentication accepts three credential types as `Authorization: Bearer <token>`:
1. **Session token** — from `POST /api/auth/login` (user + bcrypt password).
2. **API token** — issued rows in `api_tokens` (role-scoped).
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
schedule (`interval` or `daily`). The scheduler dispatches due tasks as
**pre-approved** jobs (admin-defined automation — distinct from AI-proposed
scripts, which always require human approval). Each run is linked via
`jobs.task_id`. Tasks may be owned by an agent or a policy.

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
