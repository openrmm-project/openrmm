# Security

RMM-AI runs scripts on managed machines and exposes an AI/MCP surface, so it is
designed defense-first. This summarizes the posture; details live in
[`docs/07-guardrails-security.md`](docs/07-guardrails-security.md) and
[`docs/11-mcp.md`](docs/11-mcp.md).

## Core guarantees (enforced in code)

- **AI proposes, humans dispose.** The AI and the MCP surface can only
  `propose_script`, which creates an approval-gated job. They have **no** execute
  or delete tool. Nothing reaches a machine without a human approval.
- **Destructive actions are admin-only**, deliberate, and audited (device/group
  deletion never via AI/MCP).
- **Approval is mandatory by default** for every ad-hoc/AI script run (bypass
  only via the explicit `RMM_ALLOW_UNAPPROVED_JOBS=true`).
- **RBAC**: `admin` / `tech` / `readonly`, enforced per route, plus **per-user
  access scope** (Company/Site/device) filtered on read + write paths and on the
  AI/MCP surface — restricted users can't see or touch out-of-scope devices.
- **Two-factor (TOTP)**: optional per-user 2FA (RFC 6238); secret encrypted at rest.
- **Secrets**: tokens are 256-bit random and stored hashed (SHA-256); passwords
  use bcrypt; editable runtime secrets (incl. TOTP secrets) are encrypted with
  AES-256-GCM in `app_settings`; the admin/agent token comparisons are
  constant-time; no secret is logged or returned by the API.
- **Per-IP rate limiting** on auth (10/min), MCP (60/min) and the API (120/min)
  to blunt brute-force and token-guessing.
- **Immutable audit log** of every sensitive action.

## Deploying securely

- Terminate **TLS** at a reverse proxy (Caddy/Traefik/nginx) or use a private
  network (**Tailscale**) — don't expose plain HTTP.
- Set a strong `RMM_ADMIN_TOKEN` (`openssl rand -hex 32`); rotate to per-user
  logins and scoped API tokens after bootstrap.
- For the MCP endpoint: dedicated **revocable API tokens**, the built-in
  **IP allowlist** (`RMM_MCP_ALLOWED_IPS`), and role ≥ `tech`. See docs/11-mcp.md.
- Tighten `RMM_CORS_ORIGINS` from the `*` default if you wish (safe for
  bearer-token auth, which uses no cookies).
- Keep `RMM_ALLOW_UNAPPROVED_JOBS` unset (approval stays mandatory).

## Known limitations (tracked, not yet hardened)

- Native remote relay authenticates by a short-lived URL token without principal
  binding; the full fix ships with the (not-yet-built) media engine.
- Scripts run with the agent's privileges; no sandbox yet.
- No agent token rotation/revocation yet.

## Reporting a vulnerability

This is a self-hosted, open-source project. Please report security issues
privately to the maintainer rather than opening a public issue.
