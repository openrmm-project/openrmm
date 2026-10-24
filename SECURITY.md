# Security

OpenRMM runs scripts on managed machines and exposes an AI/MCP surface, so it is
designed defense-first. This summarizes the posture; details live in
[`docs/07-guardrails-security.md`](docs/07-guardrails-security.md) and
[`docs/11-mcp.md`](docs/11-mcp.md).

## Core guarantees (enforced in code)

- **AI proposes, humans dispose.** The AI and the MCP surface create
  approval-gated job proposals (`propose_script`, bulk deploy, or high-level
  proposal tools). They have **no** tool that runs a script or deletes anything
  itself. Nothing reaches a machine from an AI proposal without a human approval.
- **Configuration is proposed too.** `propose_task`, `propose_check` and
  `propose_policy` write the row **disabled** and stamped `proposed_by`; the
  scheduler only ever selects `WHERE enabled`, and a database CHECK constraint
  refuses to enable a proposal nobody has reviewed. This matters because a task is
  a script with a schedule that the scheduler runs pre-approved — an AI able to
  create an enabled one would have been more powerful than one able to propose a
  script.
- **The one tool that acts is bounded by a human decision.** `install_patches`
  installs, but `ClaimApprovedPatches` limits it to identifiers a person already
  approved; an unapproved one installs nothing. There is deliberately **no tool to
  approve a patch** — an assistant that could approve and then install would make
  that bound circular.
- Scheduled tasks and patch policies remain separate, explicitly preconfigured
  automation paths.
- **Destructive actions are permission-gated**, deliberate, and audited. The
  AI/MCP can only **propose** moving or deleting devices; a person approves it
  under Devices ▸ Pending operations, with their own permissions and scope checked
  again at that moment, and must type the device count to approve a deletion.
  Groups, sites and companies are never deleted through the AI/MCP.
- **Approval is mandatory by default** for every ad-hoc/AI script run (bypass
  only via the explicit `RMM_ALLOW_UNAPPROVED_JOBS=true`).
- **RBAC**: built-in and custom roles with granular permissions, enforced per
  route, plus **per-user access scope** (Company/Site/Department/device) filtered
  on read + write paths and on the AI/MCP surface. AI/MCP tools are advertised and
  executed only when the principal has the required permission, and restricted
  users can't see or touch out-of-scope devices.
- **Two-factor (TOTP)**: optional per-user 2FA (RFC 6238). Setup/rotation stores a
  pending encrypted candidate and does not replace an active factor until the new
  code is confirmed; rotation requires the password and current TOTP. A code is
  accepted once (the last accepted time step is stored). Recovery codes are
  one-time and hashed.
- **Single sign-on (OIDC)**, optional: the provider says who the person is; roles
  and scope stay in OpenRMM. `RMM_OIDC_ACR_VALUES` / `RMM_OIDC_REQUIRED_AMR` require
  MFA from the provider's signed claims. The callback hands the console a
  single-use, 60-second code that only the browser that started the sign-in can
  exchange. While SSO is on, a linked account has no password path (sign-in,
  reset). The console never accepts a pasted token.
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
- **Append-only audit log** of sensitive actions, in three layers: triggers refuse
  updates and deletes (only aged machine entries are purged); run the server as a
  role that does not own the table and a compromised server cannot erase the
  trail; and a hash chain over the human record makes a rewrite by a database
  administrator detectable. The server logs at every start which layers are in
  force. Export logs off-box as well: the chain detects a rewrite, it does not
  prevent one.

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
  require HTTPS and `--update-public-key`/`-UpdatePublicKey` (on Linux and macOS,
  OpenSSL 3 or the manifest's P-256 key in `--manifest-public-key`), and
  install the service with strict update verification and TLS required. Lab mode
  is explicit and is not a production fallback.

## Known limitations

- Scripts and the terminal run with the agent service's root/SYSTEM privileges;
  there is no sandbox.
- Native remote media is trusted at the relay and is not end-to-end encrypted.
- The supported control plane is a single server replica. Hub/relay/scheduler
  state is process-local; there is no HA or distributed agent hub.
- `/readyz` checks the database only, and the token-gated `/metrics` is a small
  snapshot. There is no tracing, built-in log shipping, or alerting on the
  platform's own health.
- Release Ed25519 signatures are not Windows Authenticode or Apple Developer ID
  signing/notarization (the macOS bundle is ad-hoc signed, or with a persistent
  self-signed identity when the build has one). Platform trust prompts, Defender
  exclusions and TCC requirements remain.

## Reporting a vulnerability

This is a self-hosted, open-source project. Please report security issues
privately to the maintainer rather than opening a public issue.
