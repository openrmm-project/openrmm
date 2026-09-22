# 07 — Guardrails & Security

An AI that can run code as SYSTEM/root across a fleet is a serious risk surface.
These controls are built in.

## Human-in-the-loop (enforced in code)

- AI actions go through proposal tools (`propose_script`, bulk deploy, or
  high-level action tools), which **always** set `requires_approval=true` → job
  status `awaiting_approval`. This applies to both the built-in chat and the
  **MCP** surface (external AI like Claude/ChatGPT) — same permission-filtered
  tool catalog, same gate.
- **Configuration is proposed too.** `propose_task`, `propose_check` and
  `propose_policy` write the row **disabled**, stamped with `proposed_by`. The
  scheduler selects `WHERE enabled`, so a proposal never runs; a person enables it
  in the console, and that act *is* the review. See
  [Configuration proposals](#configuration-proposals-the-second-gate) below.
- **Every ad-hoc script run requires approval by default.** `POST /api/jobs` and
  `run-script` ignore `require_approval:false` unless the deployer explicitly sets
  `RMM_ALLOW_UNAPPROVED_JOBS=true`.
- A job in `awaiting_approval` is **never** dispatched. Only
  `POST /api/jobs/{id}/approve` (a human + bearer token) moves it to `pending`
  and triggers dispatch.
- The UI must show the full script (dry-run preview) before the approve action.

### Destructive actions are permission-gated, human-only

- **Deleting a device or group** requires `devices.delete` or `clients.manage`
  respectively (`DELETE /api/devices/{id}`, `DELETE /api/groups/{id}`) — a
  deliberate human action in the tool, audited (`device.delete` / `group.delete`).
  The AI/MCP surface has **no** delete tool, so external AI can never delete anything.
- **Scheduled tasks** are the one pre-approved path: the scheduler dispatches a
  due task through `jobs.RunAutomated`, which creates the job already approved.
  They require `tasks.manage`, are authorized at creation and fully audited;
  disable a task to stop it. Because that path skips approval by design, a task
  the AI authored may not reach it until a person has enabled it — see below.

## Configuration proposals (the second gate)

Letting the AI create a task would have undone the gate above, and it is worth
being explicit about why. A task is a script with a schedule, and the scheduler
runs one **pre-approved and immediately, forever**. An AI able to create an
*enabled* task would therefore have been strictly more powerful than one able to
propose a script: the same arbitrary code, with no approval, repeating.

So configuration reuses the switch that already existed:

- `propose_task` / `propose_check` / `propose_policy` write the row with
  `enabled = false` and set `proposed_by` to the proposing actor.
- The scheduler's own queries already read `WHERE enabled`, so a proposal is
  inert **with no scheduler change at all**.
- A person reviews it on the existing Tasks / Checks / Policies screens, which
  badge it as an *AI proposal*, and enabling it records `approved_by`.

Two columns, because they answer different questions. `proposed_by` is permanent
provenance and survives acceptance — months later the row still says an AI wrote
it. `approved_by` names who released it. "Awaiting review" is therefore
`proposed_by <> '' AND approved_by = ''`.

A **CHECK constraint** refuses to enable a row that is proposed but not approved.
That guard lives in the database rather than in the tool, because the REST
handlers are a second path into the same rows, and a guard written in only one of
them is exactly the bug the column exists to prevent.

### Patches: install yes, approve never

`install_patches` acts rather than proposes. It is safe for a reason that has
nothing to do with the AI: `DispatchInstall` calls `ClaimApprovedPatches`, which
claims only identifiers a person already approved, so an unapproved id installs
nothing. Approving a patch is the human decision about what enters the fleet, and
there is deliberately **no tool for it** — an assistant that could approve and
then install would make the guarantee circular.

### Why the gate does not depend on who is asking

Configuration is not created enabled even for an unrestricted administrator. The
model reads inventory, hostnames and alert text, all of which come from the
monitored machines. "An admin asked" is not evidence that an admin *authored* the
request, so the gate is not conditioned on the caller.

## MCP surface controls

The `/mcp` endpoint (external AI) adds: a built-in **IP allowlist**
(`RMM_MCP_ALLOWED_IPS`), the `mcp.use` permission, and dedicated **revocable
API tokens** (issue per connector — never share the bootstrap/admin token). MCP
and the built-in AI Chat advertise and execute only tools allowed by the
principal's permissions (`devices.view`, `reports.view`, `patches.view`,
`jobs.create`, `jobs.manage`, etc.) and access scope. See [11-mcp](11-mcp.md).

## Audit trail

Every sensitive transition writes an immutable `audit_log` row:
`job.propose`, `job.approve`, `job.reject`, `job.result`, `remote.start` — with
actor, target, and detail. Surface this in the UI and ship it off-box for
tamper-evidence in production.

Account changes are recorded the same way: `user.role.update`, `user.disable` /
`user.enable`, and `user.scopes.update`. The last one carries how many scopes the
user had before and after and whether the result is **full fleet access** —
because the widest grant this API can make is an *empty* scope list, which is
byte-identical to a restriction that selected nothing. It was the only user
mutation that could hand over the entire fleet without leaving a trace of who
did it.

### Retention

Only the **machine-written** entries are ever purged: `job.automated` and
`job.result`. Everything a person did is kept regardless of age.

The split is by what an entry *is*, not by when it happened, because the two
kinds have nothing in common but a table. The human record — who approved a
script, who changed a scope, who moved mail delivery — is the reason the log
exists, and it is small: measured on a real instance, an audit row costs 550
bytes with its index.

The other two are telemetry, and they scale with the fleet rather than with the
number of people. `job.automated` is one row per scheduled task, per device, per
run: a tenant with 200 devices and three hourly tasks writes 14,400 a day, about
2.9 GB a year, and `job.result` doubles it. `job.result` is also duplicative —
the `jobs` table already holds status, exit code, stdout and stderr.

`RMM_AUDIT_RETENTION_DAYS` (default 90) sets the window. **0 disables the purge
entirely**, unlike the monitoring knobs which replace zero with a default: an
audit log that deletes nothing is a legitimate configuration and has to be
reachable. Values below 30 are raised to 30 — a shorter window is far more
likely to be a mistake, or someone shortening the trail on purpose, than a real
requirement.

Every purge writes an `audit.purge` entry recording how many rows went and the
cutoff applied. Without it a gap in the trail would be indistinguishable from
tampering; with it, the log explains its own holes.

### Append-only, in three layers

Each covers what the one before it cannot, and the difference is worth being
precise about — the obvious single measure is much weaker than it looks.

**1. Triggers.** `audit_log` refuses `UPDATE` outright and refuses `DELETE`
except through `purge_machine_audit()`. This stops an application bug and a
hurried manual statement. It does **not** stop someone holding the credentials
the application uses: an owner can `ALTER TABLE ... DISABLE TRIGGER` and then do
as they please.

**2. Privilege separation.** Migration 012 creates an `openrmm_app` role that can
append to and read `audit_log`, and cannot update or delete it. Point
`RMM_DATABASE_URL` at that role and a compromised server *cannot erase what it
did* — the credentials that could are not in its memory. The purge still works,
through a `SECURITY DEFINER` function that can only remove aged machine entries.

The role is created without a password, so the migration carries no secret.

**Migrations must run separately.** Applying one needs ownership of the tables;
serving does not — and if the serving process held the owner credential it could
drop the triggers and delete the trail, which is the whole thing this prevents.
So the owner credential is used for a few seconds and never enters the process
that serves HTTP:

```sh
RMM_DATABASE_URL=<owner dsn> rmm-server -migrate-only   # applies and exits
RMM_DATABASE_URL=<app dsn>   rmm-server                 # serves
```

An orchestrated deployment should run migrations with the owner's credentials,
configure the restricted application role, and only then start the server.
Generate separate database credentials for each installation.

Starting the server as the restricted role with a migration outstanding fails
with the command to run, rather than an opaque permission error.

Until layer 2 is switched on, layers 1 and 3 are in force and the server says so
at every start.

**3. A hash chain.** Every human-record entry carries a SHA-256 of its contents
plus the previous entry's hash, so editing or removing one breaks every link
after it. `verify_audit_chain()` reports the first break.

This makes tampering **evident**; it does not prevent it. Whoever can write to
the table can recompute the chain. It earns its place next to layer 2 rather
than instead of it: with the application unable to delete, a rewrite needs the
separate owner credentials, and even then it has to be complete and consistent
to go unnoticed. Anchoring the head hash somewhere outside the database would
close that last gap; that is not done here.

Only the human record is chained. Machine-written entries are excluded because
retention deletes them, and a chain that a routine purge severs would raise an
alarm on every housekeeping run — which is an alarm nobody reads.

A failed audit *write* is counted in `rmm_audit_write_failures_total`. Any value
above zero means entries were lost — the only signal there is, since a trail
cannot record its own absence.

## Authentication

- API: bearer tokens. The bootstrap `RMM_ADMIN_TOKEN` should be a long random
  secret; rotate it by issuing scoped `api_tokens` and retiring the bootstrap.
- Users: bcrypt password hashes. **Password policy:** at least 12 characters,
  with an uppercase letter and a symbol, and not the username or a well-known
  password. A symbol is anything that is not a letter, a digit or a space —
  defined by exclusion so accented and non-Latin keyboards are accepted rather
  than rejected by an ASCII allowlist. The rule lives in one place
  (`passwordProblem`, `internal/api/api_auth.go`) and its error names the
  specific failure, so an operator is not left re-reading the policy guessing
  which part they missed.
- A user can **change their own password** (`POST /api/me/password`): the current
  password is verified, the policy is enforced, the change is audited
  (`user.password.change`), and the user's **other sessions are revoked**.
- **Disabling an account** (`POST /api/users/{id}/disable`, `users.manage`) is the
  non-destructive counterpart to deleting one: access stops, the account and its
  audit trail stay. Live sessions are ended immediately — a disable that leaves an
  open tab working is not a disable — API tokens bound to that user stop, alert
  mail stops, and no password reset is issued. Unbound API tokens are unaffected.
  You cannot disable your own account or the built-in `admin`. Deletion still
  exists and still cascades; prefer disable for a person who left, so what the
  account did remains on the record. Optional **two-factor (TOTP, RFC 6238)** per
  user — `internal/auth/totp.go` (HMAC-SHA1, ±1 step, constant-time compare).
  Setup returns a QR/secret; once enabled, login requires a `code` and returns
  `401 {totp_required:true}` until a valid code is supplied. The TOTP secret is
  encrypted at rest (see below). Enabling 2FA issues **10 one-time recovery
  codes** (shown once, stored SHA-256-hashed, single-use) usable in place of the
  TOTP code at login; a user can regenerate them, and a user manager can reset a
  locked-out user's 2FA.
- TOTP enrollment and rotation use an encrypted **pending** candidate. Setup
  requires the current password and, when a factor is already active, its current
  TOTP. The old factor remains active until a code from the candidate is verified
  and promotion/recovery-code replacement commits atomically.
- Editable runtime config secrets (AI API key, MeshCentral token key) are
  write-only fields gated by `settings.manage`; TOTP secrets are stored on the
  user. Both use **AES-256-GCM** (`enc:v1:` prefix, `internal/settings`) before
  storage; `/api/config` returns only boolean `*_set` flags for config secrets.
- Agents: a random 32-byte token minted at enrollment, stored **hashed**
  (SHA-256) server-side; compared in constant time. Enroll tokens are
  single/limited-use and can expire.
- Remote sessions: short-lived (2 min) random tokens, scoped to one session.

## RBAC & per-user access scope

- **Roles are named permission sets**, not a fixed hierarchy. Built-ins are
  `readonly`, `tech`, and `admin`; custom roles are stored in `roles` and
  `role_permissions`. The backend returns `403` when a permission is missing and
  the UI gates navigation/actions from the same `permissions` list returned by
  login and `/api/me`.
- Device remote capabilities are split: `devices.remote` for remote desktop,
  `devices.files` for file transfer, and `devices.terminal` for elevated terminal.
  The **elevated remote Terminal** (`provider:"terminal"`) opens a root/SYSTEM
  shell — equivalent to running arbitrary scripts — so it is separate from remote
  desktop observation.
- **Per-user access scope** (`user_scopes` table): a user is either *unrestricted*
  (sees the whole fleet) or *restricted* to one or more assignments at
  Company / Site / Department / specific-device granularity. Scope is enforced
  on **both** the read path (list endpoints are filtered — devices, jobs,
  alerts, checks, tasks, check-results, clients, sites) and the write/action path, so a restricted user
  cannot see or touch out-of-scope devices. **The AI/MCP respects the same scope**
  (see `06-ai-chat`). Chat sessions are owned (`created_by`) and private to their
  creator.
- **An empty scope set means unrestricted**, which makes "restrict this user to
  nothing" indistinguishable from "give this user everything" in the payload. The
  console therefore refuses to save a *restricted* selection with nothing picked
  — granting the fleet has to go through the explicit **Full access** choice —
  and the API audits the empty case as `full_access`. The same asymmetry is why
  losing every scope row to a deleted target disables the account instead
  (see [03-database-schema](03-database-schema.md)).
- **Delegation cannot widen authority.** A scoped user manager cannot create an
  unrestricted user, delegate global `admin`, assign permissions it lacks, or
  manage a user whose scopes escape its own. A scoped API-token manager can issue
  only a permission-subset token bound to itself. Role and scope are orthogonal.
- The built-in `readonly` role includes `devices.remote`; it can start remote
  desktop and is therefore not purely observational. It lacks file transfer and
  terminal. Use a custom role without `devices.remote` for strict observation.

## Threat-model notes / hardening TODO

These are required deployment controls and known limitations — documented, not
silently omitted:

1. **TLS** — terminate HTTPS/WSS at a trusted edge. Tokens and traffic must not
   cross the client-facing wire in clear text. Production Compose publishes its
   three listeners on loopback by default; source ACLs belong to that TLS edge,
   not to UFW rules on loopback backend ports. Prefer the
   **split-ingress topology** ([`deploy/README-production.md`](../deploy/README-production.md)):
   three doors on three ports so edge policy can keep the **agents** door public
   (agents live on untrusted networks) while the **console** door is restricted to
   your IPs/VPN and the **MCP** door is exposed per your choice. The app still
   offers `RMM_MCP_ALLOWED_IPS` as defense in depth.
2. ~~**RBAC / multi-tenancy**~~ — **DONE**: default and custom roles with granular
   permissions plus per-user Company/Site/Department/device access scope, enforced
   on read and write paths and on the AI/MCP surface. See *RBAC & per-user access
   scope* above.
3. **Script sandboxing** — scripts run with the agent's privileges. Consider an
   allowlist of vetted scripts, signing, or a constrained execution mode for
   AI-authored scripts.
4. **Per-user quotas** are not implemented. Per-IP limits exist for auth, MCP and
   the authenticated API, but they are not usage/account quotas.
5. ~~**Relay authorization**~~ — **DONE**: role-specific one-time tickets are
   principal-bound and supplied only in the WebSocket handshake. End-to-end media
   encryption remains future work (the relay is still the trust boundary).
6. ~~**Agent token rotation and revocation**~~ — **DONE**: revocation fences live
   control/relay attachment; rotation is single-flight and commits on reconnect.
7. **Output size limits** — the reference agent caps stdout and stderr at 1 MiB
   each. A server-side cap remains desirable for non-reference agent clients.
8. **Single replica / observability** — hub, relay and scheduler are process-local;
   there is no supported HA mode. `/healthz` is liveness only and there is no
   metrics/tracing/readiness endpoint or built-in log/backup alerting.

## Safe-script guidance for the AI

The system prompt instructs the model to prefer idempotent, non-destructive
scripts and to explicitly flag any unavoidable destructive operation. This is
guidance, not a guarantee — the human approval gate is the real control.

## Security audit (2026-06) — status

An independent audit confirmed the core posture is sound: **no SQL injection**
(all queries parameterized), the **approval guardrail holds** (AI/MCP can only
create approval-gated job proposals; deletes are permission-gated and unreachable by AI), **RBAC route
permission checks are correct**, and **tokens/secrets are well handled** (256-bit random,
stored hashed; bcrypt passwords; constant-time admin/agent compares; `/api/config`
exposes no secrets).

**Fixed in this pass:**
- 5xx responses no longer leak internal error detail (logged server-side,
  generic message to the client).
- `RMM_ADMIN_TOKEN` must be ≥ 32 chars (rejected at startup otherwise).
- Login runs a dummy bcrypt compare for unknown users (mitigates enumeration).
- Agent caps job stdout/stderr at 1 MiB each (DoS / DB-bloat guard).
- API/MCP request bodies are capped at 1 MiB (`http.MaxBytesReader`), and the
  agent/relay WebSockets cap per-message reads (8 MiB / 32 MiB via
  `SetReadLimit`) — a single peer can't force an unbounded allocation.
- **Per-IP rate limiting** (`httprate`): 10/min on `/api/auth/*` (anti
  brute-force), 60/min on `/mcp`, and a safety net across the authenticated API
  (anti token-guessing) that defaults to 600/min and is tunable via
  `RMM_API_RATE_LIMIT` (the single-page UI polls and fires parallel queries on
  navigation, so it needs headroom). Returns `429`, which the frontend retries
  with backoff. The key respects `RMM_TRUST_PROXY_HEADERS`.

**Second pass (multi-tenancy / scope):**
- A review of the new per-user scope feature found cross-tenant **read-path leaks**
  (list endpoints `/jobs`, `/alerts`, `/checks`, `/tasks`, `/check-results` returned
  all tenants even when the action path was scoped) and **globally readable chats**.
  Both fixed: list endpoints are now scope-filtered and chat sessions are owned by
  their creator. Sensitive routes are now permission-gated (`audit.view`,
  `policies.*`, `clients.*`, `scripts.*`, `alerts.manage`) and scope-gated where
  they act on devices. Verified live (readonly→403 for restricted modules, scoped
  user sees 0 out-of-scope jobs/alerts/clients).
- **Two-factor (TOTP)** added for user logins; secrets encrypted at rest.

**Relay auth (hardened):**
- **Relay auth** (`/ws/remote`): each native session has separate, one-time
  viewer and agent tickets, stored only as hashes. Viewer tickets are supplied
  only in the WebSocket handshake rather than the URL; the viewer additionally
  presents its authenticated session identity, which must match the principal that
  created the remote session. An agent-only URL-ticket bridge is opt-in through
  `RMM_ALLOW_LEGACY_AGENT_RELAY` for upgrades and is disabled by default. Both
  ticket roles are invalidated by expiry or device revocation.

**Remaining (tracked):**
- **TLS** and source ACLs must be enforced by the trusted edge in production (see
  the deploy runbook).
- **Script sandboxing** remains a roadmap item; privileged jobs and the terminal
  intentionally run with the system-service account today.
- Native relay media is not end-to-end encrypted; the relay and TLS edge remain
  trust boundaries.
- Release Ed25519 signatures are not Authenticode or Apple notarization.
