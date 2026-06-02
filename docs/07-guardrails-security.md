# 07 — Guardrails & Security

An AI that can run code as SYSTEM/root across a fleet is a serious risk surface.
These controls are built in.

## Human-in-the-loop (enforced in code)

- AI actions go through `propose_script`, which **always** sets
  `requires_approval=true` → job status `awaiting_approval`. This applies to both
  the built-in chat and the **MCP** surface (external AI like Claude/ChatGPT) —
  same tool catalog, same gate.
- **Every ad-hoc script run requires approval by default.** `POST /api/jobs` and
  `run-script` ignore `require_approval:false` unless the deployer explicitly sets
  `RMM_ALLOW_UNAPPROVED_JOBS=true`.
- A job in `awaiting_approval` is **never** dispatched. Only
  `POST /api/jobs/{id}/approve` (a human + bearer token) moves it to `pending`
  and triggers dispatch.
- The UI must show the full script (dry-run preview) before the approve action.

### Destructive actions are admin-only, human-only

- **Deleting a device or group** is an `admin`-only REST action
  (`DELETE /api/devices/{id}`, `DELETE /api/groups/{id}`) — a deliberate human
  action in the tool, audited (`device.delete` / `group.delete`). The AI/MCP
  surface has **no** delete tool, so external AI can never delete anything.
- **Scheduled tasks** are the one pre-approved path: they are admin-defined
  automation, authorized at creation and fully audited; disable a task to stop it.

## MCP surface controls

The `/mcp` endpoint (external AI) adds: a built-in **IP allowlist**
(`RMM_MCP_ALLOWED_IPS`), a minimum role of `tech`, and dedicated **revocable API
tokens** (issue per connector — never share the admin token). See
[11-mcp](11-mcp.md).

## Audit trail

Every sensitive transition writes an immutable `audit_log` row:
`job.propose`, `job.approve`, `job.reject`, `job.result`, `remote.start` — with
actor, target, and detail. Surface this in the UI and ship it off-box for
tamper-evidence in production.

## Authentication

- API: bearer tokens. The bootstrap `RMM_ADMIN_TOKEN` should be a long random
  secret; rotate it by issuing scoped `api_tokens` and retiring the bootstrap.
- Editable runtime secrets (AI API key, MeshCentral token key) are admin-only,
  write-only API fields. They are encrypted before storage in `app_settings` and
  `/api/config` returns only boolean `*_set` flags.
- Agents: a random 32-byte token minted at enrollment, stored **hashed**
  (SHA-256) server-side; compared in constant time. Enroll tokens are
  single/limited-use and can expire.
- Remote sessions: short-lived (2 min) random tokens, scoped to one session.

## Threat-model notes / hardening TODO

These are known gaps to close before production — documented, not silently
omitted:

1. **TLS** — terminate HTTPS/WSS at a reverse proxy (Caddy/nginx/Traefik). Tokens
   and traffic must not cross the wire in clear text.
2. **RBAC / multi-tenancy** — current auth is a single admin role. Per-tenant and
   per-group scoping is on the roadmap; until then treat every token as full
   admin.
3. **Script sandboxing** — scripts run with the agent's privileges. Consider an
   allowlist of vetted scripts, signing, or a constrained execution mode for
   AI-authored scripts.
4. **Rate limiting / quotas** on the chat and job endpoints.
5. **Relay authorization** — `/ws/remote` validates the session token; add
   per-role checks and consider end-to-end encryption in the media layer
   (a RustDesk fork brings this).
6. **Agent token rotation** and revocation (disable a compromised agent).
7. **Output size limits** — cap stdout/stderr persisted per job.

## Safe-script guidance for the AI

The system prompt instructs the model to prefer idempotent, non-destructive
scripts and to explicitly flag any unavoidable destructive operation. This is
guidance, not a guarantee — the human approval gate is the real control.

## Security audit (2026-06) — status

An independent audit confirmed the core posture is sound: **no SQL injection**
(all queries parameterized), the **approval guardrail holds** (AI/MCP can only
`propose_script`; deletes are admin-only and unreachable by AI), **RBAC route
grouping is correct**, and **tokens/secrets are well handled** (256-bit random,
stored hashed; bcrypt passwords; constant-time admin/agent compares; `/api/config`
exposes no secrets).

**Fixed in this pass:**
- 5xx responses no longer leak internal error detail (logged server-side,
  generic message to the client).
- `RMM_ADMIN_TOKEN` must be ≥ 32 chars (rejected at startup otherwise).
- Login runs a dummy bcrypt compare for unknown users (mitigates enumeration).
- Agent caps job stdout/stderr at 1 MiB each (DoS / DB-bloat guard).
- **Per-IP rate limiting** (`httprate`): 10/min on `/api/auth/*` (anti
  brute-force), 60/min on `/mcp`, 120/min across the authenticated API (anti
  token-guessing). Returns `429`. The key respects `RMM_TRUST_PROXY_HEADERS`.

**Remaining (tracked):**
- **Relay auth** (`/ws/remote`): authenticated only by a short-lived (2 min)
  session token in the URL, with no principal binding. Browser WebSockets can't
  send custom auth headers, so a URL token is the standard pattern — but it can
  leak via logs/history. Mitigated by the short TTL; a full fix (bind to the
  technician principal + agent identity) lands with the native media engine,
  which is not built yet, so no real sessions traverse the relay today.
- **TLS** must be terminated by a reverse proxy / tunnel (see docs/11-mcp.md).
- **Script sandboxing**, **agent token rotation/revocation**, and per-group RBAC
  remain roadmap items.
