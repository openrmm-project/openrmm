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
   configured age. A device that reports the patch later, or reports it again
   after an install, takes the most specific decision that covers it (device,
   then site, then client, then global); without one it is pending. The newest
   decision replaces the narrower ones inside its scope, so a fleet-wide
   approval is not undone by a site's older decline on that site's next scan.
   Approving again moves failed installs back to approved, which retries them.
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
  maintenance window in the policy's time zone, and reboot mode.
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
GET  /api/patches[?approval=pending|approved|declined]
POST /api/patches/{id}/approve           { scope_type, scope_id, decision }
GET  /api/reports/compliance
GET/POST/PUT/DELETE /api/patch-policies...
GET  /api/patch-policies/{id}/review
POST /api/patch-policies/{id}/activate   { fingerprint }
```

Each catalog row counts the in-scope devices in every state (`pending_count`,
`approved_count` waiting to install, `installing_count`, `failed_count`,
`installed_count`, `declined_count`) and sums them up as `approval`:

- `pending`: no device has a decision;
- `approved`: approved on every device that reports it, whatever the install
  did since (waiting, installing, failed or installed);
- `declined`: declined on every device that reports it;
- `partial`: a mix, such as approved for one client and declined for another,
  or undecided on some devices.

`?approval=` lists the patches with at least one device in that state, so a
partial patch appears in each list it has devices in. The console's Catalog tab
shows the same lists (All, Pending, Approved, Declined).

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

### Targeting and exceptions

Patch policy assignments take the same `os` and `role` filters and the global
scope as policies (docs/10), and a patch policy can leave chosen devices out
(`GET/POST /api/patch-policies/{id}/exclusions`,
`DELETE /api/patch-policies/{id}/exclusions/{agentID}`, audited as
`patch.policy.exclude` / `patch.policy.include`). `PatchPoliciesForAgent`
resolves them with the same reach definition as policies.

### Patch policies proposed by AI Chat and MCP

`propose_patch_policy` (with `patches.policies.manage`, unrestricted principals
only) proposes a patch policy — the classifications to auto-approve and after how
many days, exclusions, the maintenance window in an IANA time zone, reboot
behaviour, and the clients, sites or devices it covers. It is written in one
transaction **switched off** and attributed (`proposed_by`); until a person
activates it, it approves, installs and reboots nothing (`PatchPoliciesForAgent`
selects `WHERE enabled`). `list_patch_policies` lets the model see what already
exists. The database refuses to switch on an AI proposal nobody reviewed
(`patch_policies_proposal_needs_review`), and `PUT /api/patch-policies/{id}`
answers 409 when an edit tries to: the console's Activate button shows the
settings and scopes in a review sealed with a fingerprint, and
`POST /api/patch-policies/{id}/activate` refuses (409) a review that no longer
matches. Audited as `patch.policy.activate`. The REST API ignores `proposed_by`
and `approved_by` in a request — provenance is recorded by the server — and
rejects an unknown classification or reboot mode, a weekday outside 0–6, a
window minute outside 0–1439 or an empty exclusion.

## Policy behavior

- `window_days` uses `0=Sunday` through `6=Saturday`.
- The window's days and start/end minutes are read in the policy's `timezone`
  (an IANA zone such as `America/New_York`, daylight saving included), never in
  the server process's zone. The console starts a new policy in the browser's
  zone. A policy created without a zone, and every policy from before zones
  existed, is UTC; an edit that sends no zone keeps the one it has.
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
- A maintenance window has one zone for all of a policy's endpoints, not each
  endpoint's own, and does not model holidays, blackout calendars or
  deadline/force-after-N-hours behavior.
- Reboot suppression detects active OpenRMM native relay sessions, not arbitrary
  local user activity or third-party remote sessions.
- Compliance is based on the latest reported data and can be stale; platform
  health/alert delivery remains an operator responsibility.

## Compliance table

Patches > Compliance includes the device description from Devices. The search
filters hostnames and descriptions. Click any data column heading to sort, then
click it again to reverse the order. Counts sort numerically and last scans
chronologically; empty values stay last in either direction. Row actions stay
attached to the displayed device. The report API adds `description` to each
row without changing existing fields or the caller’s access scope.

## OS reboot observations

Building on the patch/reboot workflow in PR #35 and pending-reboot alerts in
PR #242, upgraded agents include an optional `reboot` observation in their
20-second heartbeat. It contains `status` (`required`, `not_required`, or
`unknown`), a bounded reason (2048 bytes), and an optional boot identity (128
bytes). The server timestamps receipt and preserves the first positive detection
until a conclusive negative observation. Omitted fields from older agents leave
state unchanged. An unknown observation records the failed check but retains
any previous positive flag and its first-detected time.

Linux checks `/var/run/reboot-required`. An absent marker is conclusive only on
Debian/Ubuntu-family systems; other distributions and macOS report unknown.
Windows checks Component Based Servicing, Windows Update, and pending file
renames. Registry access failures are unknown unless another check positively
requires a restart. These are OS indicators, not a guarantee that every installed
application has no outstanding restart requirement.

The Devices list has a sortable restart-status column and filters; its detail
shows the reason, first detection, last check, and request and observed boot-change times.
An observation older than two minutes, an offline device, or an unknown check
cannot display a healthy status. A retained positive flag remains visible as
last known required. Compliance uses the same observation freshness threshold;
otherwise clean devices that are offline or have legacy or unknown reboot telemetry are unknown.

Sending a reboot command records a request and never clears `reboot_pending`.
Concurrent or repeated manual requests are rejected for 15 minutes (HTTP 409).
A failed queue delivery restores the previous request state so it can be retried.
Automatic policy reboots require either a positive OS observation received within
the last two minutes or an explicit patch-installation result requiring reboot.
This patch source preserves support for macOS, DNF and older agents without
inventing an OS observation or freshness timestamp. Migration 044 preserves
existing patch-derived pending flags; unknown observations retain that source,
while a conclusive `not_required` observation clears it.

Automatic delivery is limited to one request per OS pending episode or distinct
patch-installation result, even after 15 minutes or a changed boot identity. A persistent marker cannot cause an
endless automatic reboot cycle. An operator may explicitly retry after the
15-minute cooldown. A conclusive clear followed by a new requirement starts a
new OS episode. A new patch-installation request ID also starts a new eligible
generation, without resetting the original first-detected pending time. This
allows later patch cycles on platforms that cannot report an OS clear. Result
identities are retained per agent, so duplicate or delayed A-B-A results cannot
reopen eligibility or resurrect a cleared flag. Empty or oversized IDs share one
conservative lifetime slot per agent; they never produce a new generation on
each replay. The existing window, cooldown and active-session guards still apply.
Reboot-pending alerts continue using the retained flag and first-detected time.

A changed reliable boot identity records a boot change observed by the server,
independently of any reboot request. It does not confirm that a particular
request caused the boot: a report can arrive after an unrelated restart.
Currently Linux supplies `/proc/sys/kernel/random/boot_id`; Windows and macOS do
not report a boot identity. A new heartbeat or cleared reboot indicator alone
is not proof that a boot occurred. An observed boot change can still leave an
outstanding restart requirement. API access remains device-scoped; boot identity,
patch source and dispatch reservation metadata are not exposed to console users.
