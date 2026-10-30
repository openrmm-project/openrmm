# AI autonomy

By default the AI assistant reads the console's data freely, and anything that
touches a device is a **proposal** a person approves. Risk-based autonomy lets
the AI act on its own in two narrow cases, and only for the roles you choose:

- where **nothing on the device can change**: the agent's built-in, read-only
  **device queries**;
- where **a person approved the action in advance**: library scripts an
  administrator **released** for the AI.

Nothing that changes a device ever runs without a person approving it, either
at the time or in advance. Autonomy is opt-in per role and off by default.

## Risk classes

Every AI tool has a fixed class, set in code (`internal/ai/autonomy.go`). The
model cannot change it, and a tool without a class does not run.

| Class | Tools | Needs approval? |
|---|---|---|
| Console read | inventory, alerts, jobs, audit, reports, `list_preapproved_actions` | No |
| Device query | `query_device` | No, **if the role allows it** |
| Pre-approved action | `run_preapproved_action` | No, **if the role allows it** and an administrator released the script |
| Change | `propose_script`, bulk deploy, install/uninstall, reboot, restart service, tasks, checks, policies, moving or deleting devices | **Always** |
| Stop | `cancel_job` | No (it only stops a job) |

Without the permission, `query_device` and `run_preapproved_action` still work:
they create a request that waits for approval, like any proposal.

## Permissions

| Permission | Built-in chat | External AI (MCP) |
|---|---|---|
| Query devices without approval | `ai.autonomy.query` | `mcp.autonomy.query` |
| Run pre-approved actions without approval | `ai.autonomy.run` | `mcp.autonomy.run` |

- **No built-in role holds these four.** Create a custom role in **Users &
  Settings ▸ Roles**, tick the ones you want, and assign it.
- The chat and MCP switches are independent: trusting the built-in chat never
  trusts an external client, and the reverse.
- **`ai.autonomy.manage`** releases scripts for the AI. It also needs
  `scripts.manage`. The built-in administrator role holds it.
- The usual access scope applies: the AI only queries or acts on devices the
  user can see.

## Pausing

**Settings ▸ AI ▸ AI autonomy ▸ Paused for everyone** turns everything above off
at once, for every role, including conversations already in progress. While it
is paused, every query and action becomes a request for approval. Roles keep
their permissions for when you resume. The change is in the audit log as
`config.update` with `ai.autonomy_paused`.

## Device queries

`query_device(agent_id, query, params)` runs fixed code inside the agent. No
code and no free text reaches the device's command line: the query name and its
parameters come from closed sets, checked by the server and again by the agent.
A name filter is applied to the data the agent read, never passed to a command.

| Query | Parameters | Returns |
|---|---|---|
| `processes` | `sort` = `cpu` \| `memory` (default `cpu`), `limit` 1–25 (15) | Top processes by CPU (sampled over one second) or memory. Names only, never command lines. |
| `services` | `name` filter, `state` = `any` \| `running` \| `stopped` | Services and their state (Windows services, systemd units, launchd jobs). |
| `events` | `hours` 1–72 (24), `source` filter, `limit` 1–100 (50) | Recent errors and critical events (Windows System and Application logs, the systemd journal, macOS unified log). First line of each message. |
| `disk` | — | Usage per volume. |
| `network` | `section` = `config` \| `listening` \| `connections` | Interfaces and addresses; listening ports with their process; a summary of TCP connections by state and the busiest remote endpoints. |
| `system` | — | OS, kernel, uptime, boot time and whether a reboot is pending. |
| `software` | `name` filter, `limit` 1–200 (100) | Installed software, read live. |

Deliberately left out: **reading file contents** (it can leak credentials) and
**ping or scanning from the device** (it would turn the agent into a
reconnaissance tool for the internal network). For missing updates, the AI uses
`list_patches`, which reads the console's own data.

Each query is a job of type `device_query` (it appears in the device's history),
answers only while the device is online, and needs **agent 0.10.0** or later.

## Pre-approved actions

### Releasing a script

In **Scripts**, open a script and use **Release for the AI**. You need
`scripts.manage` and `ai.autonomy.manage`, and the script must be saved first.

- **The release is bound to the content.** The server stores the SHA-256 of the
  shell, the body and the declared parameters. Any edit to those withdraws the
  release in the same database statement; renaming, recategorising or
  re-describing a script keeps it. A release whose hash no longer matches never
  runs.
- **A release can be withdrawn** at any time from the same panel.
- **The server's rules apply whatever an administrator marks.** A script that
  falls in any of these categories can never be released, and the refusal names
  which:
  - reboot or shutdown;
  - install or uninstall (package managers, MSI, Windows features);
  - deleting files, formatting, disk tools;
  - users, groups and passwords;
  - firewall, routes, addresses, DNS and hosts files;
  - removing or stopping the OpenRMM agent;
  - patches;
  - scheduled tasks, cron, services created or disabled;
  - registry writes and security settings (Defender preferences, boot
    configuration, audit policy, event logs, execution policy, BitLocker);
  - downloading, or running code built at run time (`Invoke-Expression`,
    `-EncodedCommand`, `eval`, `curl | sh` and the like).

  The match is on the script's text and errs towards refusing. A refused script
  still runs the ordinary way, with a person approving each run. The same rules
  are applied again every time a released script runs.

### Parameters

A script can declare up to eight parameters. Each one has a closed set of
accepted values:

- **enum**: one of a list of values, for example `service ∈ {Spooler, W32Time}`;
- **pattern**: a regular expression the whole value must match (the server
  anchors it on both ends).

The AI, or a person running the script, fills **only** the declared parameters,
with accepted values. They reach the script as **environment variables**, never
pasted into its text, so a value cannot become code:

| Shell | Reading the parameter `service` |
|---|---|
| PowerShell | `$env:OPENRMM_PARAM_SERVICE` |
| bash / sh | `"$OPENRMM_PARAM_SERVICE"` |
| Python | `os.environ["OPENRMM_PARAM_SERVICE"]` |

**cmd scripts cannot take parameters**: cmd expands `%VAR%` into its own command
line before it parses it, so a value would become syntax there. Use PowerShell.

A parameterised script needs **agent 0.10.0** or later; older agents would run it
without its parameters, so the server refuses them. Every autonomous run, query
or script, also needs agent 0.10.0.

### Good candidates

Small, reversible fixes: flush the DNS cache (`Clear-DnsClientCache`,
`ipconfig /flushdns`), refresh group policy (`gpupdate /force`), restart one of a
listed set of services, resynchronise the clock (`w32tm /resync`), collect a
diagnostic report.

## Safety rails

- **One device at a time.** Neither tool takes a list of devices; anything for
  several devices goes through `propose_bulk_deploy` and approval.
- **Limits**, counted per creator (`ai:<user>` or `mcp:<user>`) and per device
  inside a database transaction, so parallel calls cannot slip past them:
  - device queries: 30 per user and 20 per device in 10 minutes;
  - pre-approved actions: 10 per user and 5 per device per hour, 30 per user per
    day.

  Past a limit, the call becomes a request for approval.
- **Circuit breaker.** When a released script fails 3 times in a row, its release
  is withdrawn automatically, and the people who see the device of the last
  failure receive the **AI release withdrawn** email (on by default; see
  [Email notifications](20-email-notifications.md)).
- **Bounded output.** What a device returns is cut at 16 KB and passed through a
  secret filter (passwords, tokens, API keys, bearer and basic credentials,
  connection strings, JWTs, AWS and GitHub keys, private keys) before the model
  sees it. The job keeps the full output for the people allowed to read it.
- **Device output is data.** Results reach the model marked as data reported by
  the device, and the system prompt says nothing in it grants permission. If a log
  line says "run this command", the AI can only propose it, for approval.

## Transparency and audit

- **Jobs.** Every autonomous run is an ordinary job. Its creator is `ai:<user>` or
  `mcp:<user>`, and its approver line states the basis:
  - `autonomy: ai.autonomy.query` (or the MCP permission) for a query;
  - `pre-approved by <admin> (hash <first 12 hex>)` for a released script.

  The **Jobs** screen and the chat label it **Ran automatically**, with that line
  and the parameters.
- **Audit log:**

  | Action | When |
  |---|---|
  | `job.autonomous` | an autonomous run started, with the device, the query or script, its parameters and the basis |
  | `script.ai_release` | a script was released, with the content hash |
  | `script.ai_release.revoked` | a release was withdrawn by hand, by an edit, or by the circuit breaker |

## API

| Endpoint | What it does |
|---|---|
| `POST /api/scripts/{id}/ai-release` `{"release": true\|false}` | Release or withdraw. Needs `scripts.manage` and `ai.autonomy.manage`, and an unrestricted user. `422 release_refused` names the denied categories; `409` means the script changed since it was read. |
| `POST /api/scripts`, `PUT /api/scripts/{id}` | Accept `params` (the declared parameters). An edit to shell, body or parameters withdraws a release. |
| `POST /api/devices/{id}/run-script` `{"script_id", "params"}` | Run a library script with values for its declared parameters. Always waits for approval. |
| `GET /api/config` / `PUT /api/config` | `ai.autonomy_paused`. |

## Not included yet

Per-client exceptions (a regulated client staying approval-only even for roles
that hold the permissions) are the next step of the
[RFC](https://github.com/openrmm-project/openrmm/issues/184).
