# 24 — Alert rules

Monitoring alerts are configured through rules in **Checks** (the console's
*Verificações*). The Alerts screen is the inbox: acknowledge, silence and resolve
incidents. It no longer hides rules of its own.

Before this, two alert sources were built in and could not be changed: a device
offline for 3 minutes always raised an `error`, and nothing else the server knows
(a failed scheduled task, a failed patch, a reboot left pending, an outdated
agent) raised anything at all. A fleet with many laptops was flooded with offline
alerts it could not turn off.

## Two kinds of rule

| Kind | Types | Runs on | One device can have |
|---|---|---|---|
| **Agent checks** | `diskspace`, `memory`, `cpuload`, `ping`, `winservice`, `linuxservice`, `macservice`, `script` | the agent, on its interval | any number; each raises its own alert |
| **Server rules** | `connectivity`, `job_failure`, `patch_failure`, `reboot_pending`, `agent_outdated` | the server, from what it already knows | one per type: the one in force (below) |

Both live in the `checks` table, owned by a device (`agent_id`) or a policy
(`policy_id`), and are created, edited, enabled and reviewed the same way. A
server rule is never sent to the agent.

### Server rule types

| Type | Raises when | Settings (`config`) | Resolves when |
|---|---|---|---|
| `connectivity` | no heartbeat for `after_min` minutes (3 – 10 080) | `after_min` | the device reconnects |
| `job_failure` | the latest run of a scheduled task (`scope: tasks`) or of any job (`scope: all`) failed | `scope` | a later run of the same task or script succeeds |
| `patch_failure` | an update failed to install on the device | — | no failed update is left (installed, retried or excluded) |
| `reboot_pending` | a reboot has been pending for `after_days` days (0 – 90) | `after_days` | the device restarts |
| `agent_outdated` | the agent is behind the server by at least `behind` (`major`, `minor` or `patch`) | `behind` | the agent updates |

Every server rule also carries:

- `mode`: `alert`, or `ignore` — a rule that says **not** to alert, which is how a
  scope turns a type off (laptops that sleep);
- `severity`: `info`, `warning` or `error`.

```json
{"mode": "alert", "severity": "error", "after_min": 1440}
{"mode": "ignore"}
```

## Which server rule is in force

For each device and each server type, exactly one rule decides, chosen in this
order:

1. **The broadest scope wins.** A rule from a policy assigned to the device's
   **client** prevails over one assigned to its **site**, which prevails over its
   **department**, then a policy assigned to the device itself, then a rule owned
   by the device directly. A client-wide rule therefore holds for every device of
   that client; to treat servers and laptops differently, set the rules at the
   department or site level and leave the client without one.
2. At the same level, the **more sensitive** rule wins: `alert` over `ignore`, a
   shorter delay, a higher severity, then the lower id (so the result never
   depends on row order).
3. With no rule at all, the **fleet default** for the type applies.

Only enabled rules count, and an AI proposal counts only once a person accepted
it: an unreviewed proposal cannot silence anything.

When a rule is overridden on some of the devices it covers, the rule editor says
by which rule and on how many devices (`GET /api/checks/{id}/overrides`).

### Fleet defaults

`alert_rule_defaults` holds one row per server type, editable in Checks ▸ Fleet
defaults. They are the starting point, applied wherever no rule decides:

| Type | Default |
|---|---|
| `connectivity` | alert after 3 minutes, `error` — what every device did before |
| `job_failure` | ignore |
| `patch_failure` | ignore |
| `reboot_pending` | ignore |
| `agent_outdated` | ignore |

Fleet defaults preserve existing behavior until someone adds a rule or edits a default. See the scope-provenance upgrade notes below for device-owned silences and historical jobs.

## Alerts from server rules

| Type | `source_type` | `source_id` | `condition_key` |
|---|---|---|---|
| `connectivity` | `agent` | device id | `connectivity` (unchanged, so open offline alerts carry over) |
| `job_failure` | `rule` | device id | `job:task:<task id>` or `job:script:<script id>` or `job:adhoc` |
| `patch_failure` | `rule` | device id | `patch_failure` |
| `reboot_pending` | `rule` | device id | `reboot_pending` |
| `agent_outdated` | `rule` | device id | `agent_outdated` |

`alerts.rule_id` records the rule that raised the alert (`NULL` for a fleet
default), so an alert shows its rule and links to it, and email can be chosen
per rule. Because the alert's identity is the device and the condition, not the
rule, a rule taking over from another keeps the same open alert instead of
opening a second one. When the rule in force switches to `ignore`, an open alert
of that type resolves as `inapplicable`.

Agent checks are unchanged (`source_type: check`, `condition_key: health`), and
their alerts carry `rule_id` = the check.

Checks on an offline device stay quiet whatever its connectivity rule says: they
are not dispatched, their timeouts mark them unknown, and alerts they had open
are closed for the outage.

## Email

The event types are the same (`check_failed`, `check_recovered`,
`agent_offline`, …). Each user also chooses **which rules** reach their inbox:

- **All rules** (the default, as before);
- **Selected rules**: only alerts from the checks and rules picked in Settings ▸
  Email notifications, plus the fleet defaults when "Built-in defaults" is ticked.

The custom-rule picker shows up to five rows in a scroll area, with a search
by rule name or translated check type. Searching narrows only the visible list;
selections outside the filter remain selected and are saved together. The
built-in-defaults switch stays outside the scroll area.

`notification_rule_filters (user_id, mode, include_defaults)` and
`notification_rule_selections (user_id, check_id)` hold the choice. Scope and
the `alerts.view` permission still apply on top of it: no choice can reveal a
device the user cannot open.

The rule filter applies to incident emails carrying an alert id. Direct job-failure notices and AI-release-revocation notices keep their own event subscription and scope checks; disable those event subscriptions to stop them.

## Schema (migration 028)

```sql
CREATE TABLE alert_rule_defaults (
    type        TEXT PRIMARY KEY CHECK (type IN ('connectivity','job_failure','patch_failure','reboot_pending','agent_outdated')),
    config      JSONB NOT NULL,
    updated_by  TEXT NOT NULL DEFAULT '',
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE alerts ADD COLUMN rule_id UUID;
ALTER TABLE agents ADD COLUMN reboot_pending_since TIMESTAMPTZ;
CREATE TABLE notification_rule_filters (
    user_id          UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    mode             TEXT NOT NULL DEFAULT 'all' CHECK (mode IN ('all','selected')),
    include_defaults BOOLEAN NOT NULL DEFAULT true,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE notification_rule_selections (
    user_id  UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    check_id UUID NOT NULL REFERENCES checks(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, check_id)
);
```

## API

| Method | Path | Permission | Description |
|---|---|---|---|
| POST/PUT | `/api/checks`, `/api/checks/{id}` | `checks.manage` | The server rule types, with the `config` above |
| GET | `/api/checks/{id}/overrides` | `checks.manage`, unrestricted | `{covered, overrides: [{check_id, name, scope_type, devices}]}` |
| GET | `/api/alert-defaults` | `checks.view` | The fleet default of each server type |
| PUT | `/api/alert-defaults/{type}` | `checks.manage`, unrestricted | Replace one default; audited `alert_default.update` |
| GET/PUT | `/api/me/notifications` | signed in | Also `rules: {mode, include_defaults, check_ids}` |

## AI Chat and MCP

Both channels use `propose_check` to propose any agent check or server rule for a device or policy. The five server types use the same validated config as the console. Proposals are disabled and stamped with their author. They have no effect, including `mode: ignore`, until an authorized person reviews and enables them in Checks. Existing ownership and scope checks apply, and there is no tool that changes fleet defaults or approves its own proposal.

Rule ids on alerts are historical references: removing a check does not turn an existing custom-rule incident into a fleet-default incident for email filtering. The evaluator updates the reference when another rule actually takes over.

## Scope provenance and upgrade notes

An incident keeps its original scope snapshot. Moving a device to another client, site or department retires the previous incident with a neutral note when it is next reconciled. A destination incident has its own history and does not link to a predecessor in another scope. The server checks the device scope revision under a database lock before accepting prefetched rule metadata; stale input is retried on a later scheduler pass.

Migration 028 adds a private monitoring scope timestamp to agents, advances it whenever a site or department changes (including moving away and back), and captures it when jobs are queued. Failed-job rules consider only jobs queued in the current scope, up to 30 days back. Jobs queued before this migration have no verified provenance and do not produce these new rule alerts.

Agent-owned and directly owned check silences are bound to the same scope revision. They do not follow a moved device or reveal their private reasons in the destination. Device-owned silences created before this migration must be reviewed and recreated; global administrators can still see and cancel their historical entries. Client, site, department and policy maintenance retain their configured shared applicability.

Migration 028 ships in 0.10.0. A test database that applied an earlier pre-release draft of it must be recreated: an applied migration is never edited, so a changed checksum is refused.

## Validation

Local validation covers config bounds and malformed input, rule version handling, restricted global writes, notification selection validation, all nine locale tables and console interaction flows. PostgreSQL-backed tests exercise migration upgrades/reapplication, scope precedence, agent result rejection, proposal review, incident continuity, notification scope and moved-device isolation. The router E2E test uses real authentication and persistence without an HTTP listener. Browser smoke flows run in CI only; no services are started by the local unit suite.
