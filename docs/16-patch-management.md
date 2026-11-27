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

## Scan attempts, freshness and compatibility

A scan request is reserved in `agent_patch_scan_state` before it is sent. The
server assigns its request ID, attempt time and six-minute deadline. A device
can have one pending scan. A second request returns HTTP 409; a device that
cannot receive the request returns HTTP 503 and its reservation is released.
The fleet endpoint returns actual `dispatched`, `busy`, `offline` and `failed`
counts, after checking all selected devices against the caller's scope. A 202
response means the command was queued for the agent, not that scanning started
or that the catalog was refreshed. Disconnecting the current transport releases
its pending request; a replaced transport cannot release newer work.

The regular six-hour scheduler uses cached APT metadata by default. Failed,
timed-out and offline attempts retry based on the last attempt, initially after
15 minutes, doubling up to six hours. Polling and scheduling expire abandoned
requests. Manual requests may retry sooner, but cannot replace a pending scan.
An agent declines overlapping scans with the safe `busy` category instead of
queuing them behind a potentially long installation or reboot operation.

Device patch responses and compliance rows include `scan_state`. It contains
`request_id`, `refresh_metadata`, `status`, `last_attempt`, `deadline`,
`last_success`, `last_metadata_refresh`, `scan_mode`, `error_code`,
`consecutive_failures` and `freshness`. All times come from the server. The
historical `last_metadata_refresh` records the last successful explicitly
refreshed catalog; it does not certify a later cached scan. No file modification
time is treated as proof of freshness. `agents.last_patch_scan` remains the last
successful scan for existing consumers.

The console distinguishes cached, explicitly refreshed, native OS and legacy
sources. Only a successful refreshed or native scan within 24 hours can support
a clean compliance status. Pending, failed, stale, cached and legacy results
cannot newly establish compliance. Known missing security/critical patches and
failed installs retain their critical status even when freshness is unknown.
The scan covers OS packages, not Docker image releases or locally installed
application updates. A native OS scan is labelled native, not represented as a
verified APT catalog refresh.

A matching successful report reconciles inventory and attempt state in one
transaction. Failed scans preserve every prior missing, approved, excluded,
installing and failed row, including installation diagnostics and approvals.
Cached and legacy reports merge findings conservatively: omission alone cannot
remove a known missing package or downgrade a known security classification.
Only successful explicitly refreshed/native reports reconcile absent findings;
confirmed installation results can separately mark a package installed.
Non-authoritative reports preserve known versions, titles and KBs, merge CVEs,
and retain reboot requirements, including catalog entries shared by other
devices. They cannot reopen installed patches or interrupt installing claims.
An authoritative report predating a later install claim or install result (success or failure)
also cannot reopen or delete that work. The server dates claims and results separately for
this comparison. This is observation ordering, not an execution-order guarantee:
a claim predating a scan reservation can still be delivered to the agent later.
The agent's mutex continues to prevent simultaneous native operations.

The protocol adds optional `request_id` and `refresh_metadata` fields to
`patch_scan`, and optional `request_id`, `scan_mode` and `error_code` fields to
`patch_report`. Empty requests and older reports still decode. A missing ID
cannot complete tracked pending work, especially an explicitly fresh request.
Old, duplicate and expired IDs cannot replace inventory or clear a newer request.
Unsolicited legacy reports outside pending work are merged without claiming
freshness. Upgrade both server and agents to use tracked scans: an older agent
that cannot echo IDs can leave a request pending until its timeout. The console
handles older APIs with missing state fields as unknown freshness.

## Explicit APT metadata refresh

The console's **Refresh package metadata** option, or
`{ "refresh_metadata": true }`, enables a private APT catalog for that scan.
The agent creates a temporary parent and `lists` directory with mode 0755 so
APT's `_apt` sandbox can traverse them. An extra `-c scan.conf`, read after
normal configuration, clears `APT::Update::Pre-Invoke`,
`APT::Update::Post-Invoke` and `APT::Update::Post-Invoke-Success` hooks. APT uses
that private lists directory, disables binary package caches, sets retries to
zero, sets HTTP/HTTPS timeouts to 25 seconds, and sets
`APT::Update::Error-Mode=any` so a partial refresh is a failure.

Only a successful `apt-get update` is followed by `apt-get -s -o
Debug::NoLocking=true upgrade`, using the same configuration and catalog. This
never invokes install, dist-upgrade, remove or reboot, and never changes host
catalogs, caches, sources, keys or trust configuration. A failed refresh has no
cached fallback. Commands use fixed Go argument arrays without a shell. The
whole scan has a four-minute deadline; each command's stdout and stderr are
bounded to 4 MiB. Linux cancellation kills the process group, including acquire
children, and command/pipeline waiting is bounded before temporary cleanup.

Scan errors are fixed categories (`busy`, `timeout`, `permission`, `output_limit`,
`refresh_failed`, `scan_failed`, `unsupported`, `offline`,
`refresh_unverified`). Raw repository output, URLs, credentials and runner errors
are not stored in scan state or sent in scan reports. Package installation stays
separate and follows existing human/policy approval rules. Windows, macOS and
DNF retain their native scan mechanisms. No CVE matching engine or application
feed is added.

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
- `agent_patch_scan_state`: the current attempt and separate successful scan /
  verified catalog timestamps (migration 044).

## API and permissions

Read state with `patches.view`; scan, approve, install and reboot require
`patches.manage`; policy CRUD requires `patches.policies.manage`. Compliance can
be read with `reports.view` or `patches.view`.

Relevant routes:

```text
GET  /api/devices/{id}/patches
POST /api/devices/{id}/patches/scan      { refresh_metadata?: false }
POST /api/patches/scan                   { agent_ids?: [...], refresh_metadata?: false }
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

The AI/MCP catalog provides `list_patches` (read) and `install_patches`.
`list_patches` retains its `patches` array and also returns `scan_state`, so a
read-only consumer can distinguish findings from unverified or failed scans. The
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
- Cached APT scans retain older findings conservatively. Request a fresh scan
  to reconcile findings resolved outside OpenRMM. OS-native classification and
  repository completeness still depend on their providers.

## Compliance table

Patches > Compliance includes the device description from Devices. The search
filters hostnames and descriptions. Click any data column heading to sort, then
click it again to reverse the order. Counts sort numerically and last scans
chronologically; empty values stay last in either direction. Row actions stay
attached to the displayed device. The report API adds `description` to each
row without changing existing fields or the caller’s access scope.


## Focused regression tests

The protocol, agent runner, PostgreSQL, API and console tests cover safe error
categories, isolated paths and cleared hooks, failed refresh without simulation,
bounded output, Linux acquire-child cancellation, concurrent reservations,
timeouts/backoff, stale and legacy reports, scope checks, conservative metadata
merging and approval preservation. `openrmm/scripts/smoke.mjs` exercises the real
Chrome console against HTTP fixtures, including conflict, pending, failed,
legacy and zero-dispatch fleet outcomes.

`TestIsolatedAptRealFixture` is an additional opt-in test for a disposable Debian
container with networking disabled. It creates a synthetic file repository and
installed-package status, runs real APT update plus simulation, checks all three
hook canaries, and verifies the synthetic host catalogs/caches/status are
unchanged. It never installs a package. With a Go/apt-capable test image and
Go modules already cached, run:

```sh
docker run --rm --network none --tmpfs /tmp:rw,exec,size=1g \
  -e OPENRMM_TEST_APT=1 -e GOTOOLCHAIN=local \
  -v "$PWD:/src" -v openrmm-go-modules:/go/pkg/mod \
  -w /src <go-and-apt-test-image> \
  go test -run '^TestIsolatedAptRealFixture$' -count=1 -v ./cmd/agent
```

Ordinary tests skip that real APT fixture unless explicitly enabled. No test
executes the workstation's package manager by default.
