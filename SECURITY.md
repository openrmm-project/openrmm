# Security

OpenRMM runs scripts on managed machines and exposes an AI/MCP surface, so it is
designed defense-first. This summarizes the posture; details live in
[`docs/07-guardrails-security.md`](docs/07-guardrails-security.md) and
[`docs/11-mcp.md`](docs/11-mcp.md).

## Core guarantees (enforced in code)

- **AI proposes, humans dispose.** The AI and the MCP surface can only create
  approval-gated job proposals (`propose_script`, bulk deploy, or high-level
  proposal tools). They have **no** direct execute or delete tool. Nothing reaches
  a machine from an AI proposal without a human approval. Scheduled tasks and
  patch policies are separate, explicitly preconfigured automation paths.
- **Destructive actions are permission-gated**, deliberate, and audited
  (device/group deletion never via AI/MCP).
- **Approval is mandatory by default** for every ad-hoc/AI script run (bypass
  only via the explicit `RMM_ALLOW_UNAPPROVED_JOBS=true`).
- **RBAC**: built-in and custom roles with granular permissions, enforced per
  route, plus **per-user access scope** (Company/Site/Department/device) filtered
  on read + write paths and on the AI/MCP surface. AI/MCP tools are advertised and
  executed only when the principal has the required permission, and restricted
  users can't see or touch out-of-scope devices.
- **Two-factor (TOTP)**: optional per-user 2FA (RFC 6238). Setup/rotation stores a
  pending encrypted candidate and does not replace an active factor until the new
  code is confirmed; rotation requires the password and current TOTP. Recovery
  codes are one-time and hashed.
- **Secrets**: agent/session/API/OAuth tokens are random and stored hashed
  (SHA-256); enrollment tokens are limited-use bootstrap capabilities stored
  until consumed/expired; passwords use bcrypt. Runtime configuration secrets in
  `app_settings` and TOTP secrets in `users` are AES-256-GCM encrypted. Admin and
  agent comparisons are constant-time. Configuration secrets are write-only;
  enrollment tokens, TOTP setup secrets and recovery codes are intentionally
  returned once by their creation endpoints.
- **Per-IP rate limiting** on auth (10/min), MCP (60/min) and the API
  (600/min, tunable via `RMM_API_RATE_LIMIT`) to blunt brute-force and
  token-guessing.
- **Resource caps** against memory-exhaustion: API/MCP request bodies are
  capped at 1 MiB, and the agent/relay WebSockets cap per-message reads
  (8 MiB / 32 MiB) so no single peer can force an unbounded allocation.
- **Signed agent/sidecar updates** are supported for `/dl` artifacts with
  Ed25519 detached signatures. Roll out in `warn`, then enforce with
  `RMM_UPDATE_VERIFY=strict`.
- **Application-immutable audit log** of sensitive actions. A PostgreSQL
  administrator can still alter it, so export logs off-box for tamper evidence.

## Deploying securely

- Use the production Compose topology with all published backend listeners bound
  to loopback. Terminate **TLS** and enforce console/MCP source ACLs at the trusted
  edge; UFW rules on loopback-only backend ports are not the ACL boundary.
- Provision `RMM_ADMIN_TOKEN`, the database password, and the stable
  `RMM_SETTINGS_ENCRYPTION_KEY` as secret files with
  `deploy/ops/provision-secrets.sh`. Do not put shell substitutions in an env
  file. Rotate from the bootstrap token to per-user
  logins and scoped API tokens after bootstrap.
- For the MCP endpoint: dedicated **revocable API tokens**, the built-in
  **IP allowlist** (`RMM_MCP_ALLOWED_IPS`), and `mcp.use`. See docs/11-mcp.md.
- Tighten `RMM_CORS_ORIGINS` from the `*` default if you wish (safe for
  bearer-token auth, which uses no cookies).
- Keep `RMM_ALLOW_UNAPPROVED_JOBS` unset (approval stays mandatory).
- For production agents, publish signed `/dl` artifacts. The current installers
  require HTTPS, OpenSSL 3, and `--update-public-key`/`-UpdatePublicKey`, and
  install the service with strict update verification and TLS required. Lab mode
  is explicit and is not a production fallback.

## Known limitations

- Scripts and the terminal run with the agent service's root/SYSTEM privileges;
  there is no sandbox.
- Native remote media is trusted at the relay and is not end-to-end encrypted.
- The supported control plane is a single server replica. Hub/relay/scheduler
  state is process-local; there is no HA or distributed agent hub.
- `/healthz` is liveness only. There is no readiness/dependency probe, metrics
  endpoint, tracing, built-in log shipping, or platform alert delivery.
- Release Ed25519 signatures are not Windows Authenticode or Apple
  code-signing/notarization. Platform trust prompts and TCC requirements remain.
- The current console's 2FA card has not yet been updated to send the password
  and current-code fields required by the hardened setup/disable API. Use the API
  directly for those operations until the UI is aligned.

## Reporting a vulnerability

This is a self-hosted, open-source project. Please report security issues
privately to the maintainer rather than opening a public issue.
