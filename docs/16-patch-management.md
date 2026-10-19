# 16 — Patch Management

> **Status: implemented.** Backend, agent, scheduler, REST API, scope/RBAC,
> audit, compliance report and console screens exist. Agent 0.4.2+ is required.

## Implemented flow

1. The scheduler requests scans roughly every six hours for online agents, or an
   operator requests one with `POST /api/devices/{id}/patches/scan`.
2. The agent shells out to the native OS update mechanism and sends a
   `patch_report` of currently missing updates.
3. The server upserts the global `(os, identifier)` catalog and per-device state.
4. An operator creates an approval/decline decision at global, client, site or
   device scope, or a policy auto-approves matching classifications after the
   configured age.
5. Manual or in-window policy installation claims only identifiers currently
   approved for that device, marks them installing, and sends an explicit
   `patch_install` list. An empty list never means "install everything".
6. `patch_result` records each outcome and sets `reboot_pending` when needed.
7. A manual reboot is delayed 60 seconds. It refuses an active native/files/
   terminal session unless `force:true`; automatic policy reboot always waits
   until no such session is active.

Patch installation is a dedicated protocol operation, not a `jobs.kind=patch`
job and not the normal 10-minute script-job lifecycle. There is no streaming
install progress; status changes when the result frame arrives.

## Agent implementations

The agent remains CGO-free and invokes native tools:

- Windows: PowerShell/WUA;
- Debian/Ubuntu: apt tooling;
- Fedora/RHEL family: dnf update metadata;
- macOS: `softwareupdate`.

Metadata quality differs by OS: CVEs and classification are as complete as
what each update mechanism reports.

## Data model

- `patches`: catalog deduplicated by `(os, identifier)`, with title,
  classification, KB, CVEs, reboot flag and first/last seen.
- `agent_patches`: device state (`missing`, `approved`, `installing`, `installed`,
  `failed`, `excluded`), available version, timestamps and error.
- `patch_approvals`: latest manual/policy `approve` or `decline` decision at
  global, client, site or agent scope.
- `patch_policies`: enabled flag, auto-approve classifications/age, exclusions,
  server-time maintenance window and reboot mode.
- `patch_policy_assignments`: policy to client/site/agent.
- `agents.last_patch_scan` and `agents.reboot_pending`.

## API and permissions

Read state with `patches.view`; scan, approve, install and reboot require
`patches.manage`; policy CRUD requires `patches.policies.manage`. Compliance can
be read with `reports.view` or `patches.view`.

Relevant routes:

```text
GET  /api/devices/{id}/patches
POST /api/devices/{id}/patches/scan
POST /api/devices/{id}/patches/install   { identifiers: [...] }
POST /api/devices/{id}/reboot            { force?: false }
GET  /api/patches
POST /api/patches/{id}/approve           { scope_type, scope_id, decision }
GET  /api/reports/compliance
GET/POST/PUT/DELETE /api/patch-policies...
```

All device operations are access-scope checked. A restricted principal cannot
approve globally. Patch policy definitions are global: restricted principals can
see intersecting assignments but cannot create/update/delete global definitions.
Audit events include scans, approvals/declines, installs, policy changes and
reboots.

The AI/MCP catalog provides `list_patches` (read) and `install_patches`. The
second one acts, and is bounded by the approval step rather than by the tool:
`DispatchInstall` claims through `ClaimApprovedPatches`, so it can only install
identifiers a person **already approved**, and an unapproved one installs nothing.
It must name identifiers explicitly — an empty list is rejected rather than
treated as "install everything".

There is deliberately **no tool to approve a patch**. Approval is the human
decision about what enters the fleet, and an assistant that could approve and then
install would make the bound above circular. Operators approve through the scoped
human API/UI or preconfigured policy automation.

## Policy behavior

- `window_days` uses `0=Sunday` through `6=Saturday`.
- Start/end minutes use the **server process's local time**, not each endpoint's
  timezone.
- An empty day list permits any day.
- Exclusions match identifiers/classifications as implemented by the store.
- Within an open assigned policy window, the scheduler installs approved patches
  for online endpoints. It reboots only after no approved identifiers remain and
  at least one open policy allows reboot.
- Scheduler/hub state is process-local. Patch automation supports the documented
  single server replica only; multiple schedulers would not be a supported HA
  deployment.

## Remaining limitations

- No streamed percentage/progress or cancellation for a running package-manager
  operation.
- No durable dispatch queue equivalent to job-result outbox for patch requests;
  offline manual scan/install returns `503`, and policy work waits for a later
  scheduler tick.
- Maintenance windows are server-local-time and do not model endpoint timezone,
  holidays, blackout calendars or deadline/force-after-N-hours behavior.
- Reboot suppression detects active OpenRMM native relay sessions, not arbitrary
  local user activity or third-party remote sessions.
- Compliance is based on the latest reported data and can be stale; platform
  health/alert delivery remains an operator responsibility.
