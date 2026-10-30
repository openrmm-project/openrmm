# 11 — MCP Server (connect external AI: Claude, ChatGPT, …)

The RMM exposes its AI tools as a **Model Context Protocol (MCP)** server over
**Streamable HTTP** at `/mcp`. Any MCP-capable client — Claude (Desktop/web),
ChatGPT, and others — can connect and manage the fleet in natural language.

It reuses the **exact same permission-filtered tool catalog and implementations**
as the built-in chat (`ai.ToolsForPermissions` / `ai.Execute`), so both surfaces
behave identically. Write-capable tools **propose**: jobs land in
`awaiting_approval`, and tasks, checks and policies land disabled and stamped for
review. External AI never executes a free-form script and never deletes. The
tools that act are bounded: `install_patches` by what a person already approved,
and — only for roles granted `mcp.autonomy.query` / `mcp.autonomy.run`, which are
separate from the built-in chat's — `query_device` (read-only built-in queries)
and `run_preapproved_action` (scripts an administrator released). Without those
permissions they create requests for approval. See
[23-ai-autonomy](23-ai-autonomy.md).

Verified end-to-end: `initialize` → `tools/list` → `tools/call` over HTTP, with
scoped tokens and IP allowlisting.

## Endpoint & auth

- **URL**: `https://<your-host>/mcp`
- **Auth**: `Authorization: Bearer <token>` with `mcp.use` permission.
  Use a **dedicated, revocable API token** (not the bootstrap/admin token).
  Mint one in the UI (recommended) or via the API:
  - **UI**: *Settings ▸ API tokens ▸ New token* — pick a role that has `mcp.use`
    (the built-in `tech` does), optionally bind it to a user to inherit that
    user's access scope, then copy the secret (shown once). Requires
    `api_tokens.manage`.
  - **API**:
    ```bash
    curl -X POST https://host/api/api-tokens \
      -H "Authorization: Bearer $ADMIN" -H 'Content-Type: application/json' \
      -d '{"name":"claude-desktop","role":"tech"}'
    # -> returns {"token":"..."}  (shown once; store it)
    ```
  Revoke anytime from the same UI panel or `DELETE /api/api-tokens/{id}`.
- **Access scope**: if the API token is bound to a user with a restricted access
  scope, the MCP tools see and act on **only** that user's Companies/Sites/devices —
  the same enforcement as the in-app AI (see `06-ai-chat` and `07-guardrails-security`).
  An unbound token with a full-access role sees the whole fleet.
- **Tool permissions**: MCP only advertises and runs tools allowed by the token's
  role permissions. For example, inventory reports require `reports.view`, patch
  reads require `patches.view`, proposing jobs requires `jobs.create`, and
  canceling jobs requires `jobs.manage`.

## Connecting a client

Since this project is self-hosted/open-source, **how you expose `/mcp` is the
deployer's choice.** Two well-trodden, secure paths:

### Private network (Tailscale) — recommended for own-infra clients
Keep the service off the public internet and expose the **TLS MCP edge** only in
the tailnet. Do not point production clients at the raw app `:8080`; that bypasses
the split ingress and trusted proxy assumptions. Plain HTTP is acceptable only
for an explicit isolated lab. Cloud clients cannot reach a tailnet, so use the
public TLS option below when required.

### Public exposure (for cloud clients) — reverse proxy + TLS
Front the loopback MCP listener with Caddy/Traefik/nginx terminating TLS on a
real domain. For example, when the configured host MCP port is `7443`:
```
mcp.yourdomain.com {
    reverse_proxy 127.0.0.1:7443
}
```
Then add `https://mcp.yourdomain.com/mcp` as a custom connector in the client.
Set `RMM_MCP_PUBLIC_URL=https://mcp.yourdomain.com` when using OAuth and, if
behind the proxy, `RMM_TRUST_PROXY_HEADERS=true`.

**Cloud connectors (Claude web / ChatGPT) — use OAuth.** These clients expect the
OAuth connector flow rather than a pasted token. Turn on the built-in OAuth 2.1
Authorization Server with `RMM_MCP_OAUTH_ENABLED=true` (HTTPS
`RMM_MCP_PUBLIC_URL` required): the user logs into OpenRMM (password + 2FA) and consents, and the
client receives a short-lived, scoped, refreshable token bound to that user — no
static secret to paste. See [17-mcp-oauth](17-mcp-oauth.md).

### Production: put `/mcp` on its own door
For a real deployment, use the **split-ingress topology**
([`deploy/README-production.md`](../deploy/README-production.md)): `/mcp` gets its
**own port/door**, separate from the console (UI + REST API) and the agents' WS.
That lets you expose the MCP door **publicly** for cloud clients (Claude web,
ChatGPT) *or* restrict it to **specific IPs** for on-prem clients — a trusted-edge
ACL independent of the console, which stays locked to your own IPs/VPN. Backend
listeners bind loopback by default; UFW on those loopback ports is not the source
ACL mechanism. Keep
`RMM_MCP_ALLOWED_IPS` as an app-level second layer only for clients with stable,
known egress IPs; leave it empty for cloud connectors with dynamic egress.

## Security controls (layered)

1. **IP allowlist (built in).** Restrict `/mcp` to known source IPs/CIDRs:
   ```
   RMM_MCP_ALLOWED_IPS=203.0.113.10,10.0.0.0/8
   ```
   Empty = no app-level restriction. When set, non-matching sources get `403`.
   Behind a proxy, set `RMM_TRUST_PROXY_HEADERS=true` so `X-Forwarded-For` is
   used (only with a trusted proxy — otherwise it is spoofable).
2. **Dedicated scoped tokens.** Issue a per-connector API token with a role that
   has `mcp.use` (the built-in `tech` role is the default); revoke it
   independently of everything else. Never put the bootstrap/admin token in a client.
3. **TLS everywhere** for public exposure; or no public exposure at all (Tailscale).
4. **Approval guardrail.** External AI proposes; a human releases. Jobs are
   created `awaiting_approval`; tasks, checks and policies are created **disabled**
   and stamped `proposed_by`, and the database refuses to enable one nobody has
   reviewed. Moving or deleting devices is a proposal too, carried out only when
   a person approves it under Devices ▸ Pending operations. No tool runs a script
   or deletes anything itself. The tools that act without a further approval are
   `install_patches`, which can only install what a person had already approved
   (there is no tool to approve a patch), and `cancel_job`, which stops a job and
   never starts one. See
   [07-guardrails-security](07-guardrails-security.md).
5. **Least privilege.** The `/mcp` group requires `mcp.use`, not broad admin
   access.
6. **Single replica.** MCP requests can use durable PostgreSQL state, but the
   wider control plane's hub/relay/scheduler are not distributed. The supported
   deployment is one server replica.

## Recommended hardening checklist

- [ ] Private network (Tailscale) **or** reverse proxy + valid TLS cert.
- [ ] For known/on-prem clients: `RMM_MCP_ALLOWED_IPS` set to their stable egress
  IPs. For cloud connectors with dynamic egress: leave it empty and rely on OAuth,
  scoped permissions, TLS, and the approval guardrail.
- [ ] Dedicated API token per connector with only the needed role; rotate/revoke
  as needed.
- [ ] **Set an expiry** on the connector token (Settings ▸ API tokens ▸ *Expires*,
  or `expires_in_days` on the API). An expired token stops authenticating, so a
  leaked secret has a bounded blast radius — strongly recommended for **public /
  cloud** exposure where an IP allowlist can't apply (cloud clients connect from
  the provider's shared, dynamic egress IPs).
- [ ] Bind the token to a **scoped user** so it only sees that user's fleet.
- [ ] `RMM_ALLOW_UNAPPROVED_JOBS=false` (default) — approval stays mandatory.
- [ ] Watch the audit log for `apitoken.create` / `job.propose` / `job.approve`.

## What the AI can and cannot do over MCP

| Capability | Allowed? |
|------------|----------|
| List devices, read inventory, fleet summary, job status | ✅ only with matching permissions |
| List tasks, checks and policies | ✅ with `tasks.view` / `checks.view` / `policies.view` |
| Fleet inventory and audit search, counted in the database | ✅ with `reports.view` / `audit.view` (audit: unscoped accounts only) |
| Hand the operator a CSV of the inventory or audit trail | ✅ same permissions; a download link that works only for the account that asked |
| Propose scripts/bulk/high-level actions | ✅ only with `jobs.create`; awaits human approval |
| Propose a task, check or policy | ✅ with `tasks.manage` / `checks.manage` / `policies.manage` — created **disabled**; it never runs until a person enables it |
| Install patches | ✅ with `patches.manage`, but **only ones a person already approved**; unapproved identifiers install nothing |
| Approve a patch | ❌ no such tool — approval is always a human decision |
| Enable a task, check or policy it proposed | ❌ the database refuses an unreviewed proposal |
| Execute a script directly | ❌ |
| Move or delete devices | ⚠️ propose only, with `clients.manage` / `devices.delete`; a person approves it, and a deletion needs the device count typed |
| Delete a group, site or company | ❌ |
