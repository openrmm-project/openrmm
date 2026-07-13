# 07 — Guardrails & Security

An AI that can run code as SYSTEM/root across a fleet is a serious risk surface.
These controls are built in.

## Human-in-the-loop (enforced in code)

- AI actions go through proposal tools (`propose_script`, bulk deploy, or
  high-level action tools), which **always** set `requires_approval=true` → job
  status `awaiting_approval`. This applies to both the built-in chat and the
  **MCP** surface (external AI like Claude/ChatGPT) — same permission-filtered
  tool catalog, same gate.
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
- **Scheduled tasks** are the one pre-approved path: they require `tasks.manage`,
  are authorized at creation and fully audited; disable a task to stop it.

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

## Authentication

- API: bearer tokens. The bootstrap `RMM_ADMIN_TOKEN` should be a long random
  secret; rotate it by issuing scoped `api_tokens` and retiring the bootstrap.
- Users: bcrypt password hashes. A user can **change their own password**
  (`POST /api/me/password`): the current password is verified, a minimum length is
  enforced, the change is audited (`user.password.change`), and the user's **other
  sessions are revoked**. Optional **two-factor (TOTP, RFC 6238)** per
  user — `internal/auth/totp.go` (HMAC-SHA1, ±1 step, constant-time compare).
  Setup returns a QR/secret; once enabled, login requires a `code` and returns
  `401 {totp_required:true}` until a valid code is supplied. The TOTP secret is
  encrypted at rest (see below). Enabling 2FA issues **10 one-time recovery
  codes** (shown once, stored SHA-256-hashed, single-use) usable in place of the
  TOTP code at login; a user can regenerate them, and a user manager can reset a
  locked-out user's 2FA.
- Editable runtime secrets (AI API key, MeshCentral token key, TOTP secrets) are
  write-only fields gated by `settings.manage`, **encrypted with AES-256-GCM**
  (`enc:v1:` prefix, `internal/settings`) before storage; `/api/config` returns
  only boolean `*_set` flags.
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

## Threat-model notes / hardening TODO

These are known gaps to close before production — documented, not silently
omitted:

1. **TLS** — terminate HTTPS/WSS at a reverse proxy (Caddy/nginx/Traefik). Tokens
   and traffic must not cross the wire in clear text. For production, prefer the
   **split-ingress topology** ([`deploy/README-production.md`](../deploy/README-production.md)):
   three doors on three ports so the firewall can keep the **agents** door public
   (agents live on untrusted networks) while the **console** door is restricted to
   your IPs/VPN and the **MCP** door is exposed per your choice — the app never
   holds the IP allowlist.
2. ~~**RBAC / multi-tenancy**~~ — **DONE**: default and custom roles with granular
   permissions plus per-user Company/Site/Department/device access scope, enforced
   on read and write paths and on the AI/MCP surface. See *RBAC & per-user access
   scope* above.
3. **Script sandboxing** — scripts run with the agent's privileges. Consider an
   allowlist of vetted scripts, signing, or a constrained execution mode for
   AI-authored scripts.
4. **Rate limiting / quotas** on the chat and job endpoints.
5. **Relay authorization** — `/ws/remote` validates the session token; add
   principal binding and consider end-to-end encryption in the media layer (the
   native engine is homegrown/CGO-free, so media E2E is future work — no fork).
6. **Agent token rotation** and revocation (disable a compromised agent).
7. **Output size limits** — cap stdout/stderr persisted per job.

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

**Remaining (tracked):**
- **Relay auth** (`/ws/remote`): authenticated only by a short-lived (2 min)
  session token in the URL, with no principal binding. Browser WebSockets can't
  send custom auth headers, so a URL token is the standard pattern — but it can
  leak via logs/history. Mitigated by the short TTL; binding the session to the
  technician principal + agent identity is still pending.
- **TLS** must be terminated by a reverse proxy / tunnel (see docs/11-mcp.md).
- **Script sandboxing** and **agent token rotation/revocation** remain roadmap
  items.
