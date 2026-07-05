# 11 — MCP Server (connect external AI: Claude, ChatGPT, …)

The RMM exposes its AI tools as a **Model Context Protocol (MCP)** server over
**Streamable HTTP** at `/mcp`. Any MCP-capable client — Claude (Desktop/web),
ChatGPT, and others — can connect and manage the fleet in natural language.

It reuses the **exact same permission-filtered tool catalog and implementations**
as the built-in chat (`ai.ToolsForPermissions` / `ai.Execute`), so both surfaces
behave identically. Write-capable tools only create **approval-gated** job
proposals. External AI can propose, never execute directly, never delete.

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

### Most secure: private network (Tailscale) — recommended for own-infra clients
Keep the server **off the public internet** entirely. Put the server and the
machine running the AI client (e.g. Claude Desktop) on the same Tailscale
tailnet, then point the client at `http://<tailnet-ip-or-magicdns>:8080/mcp`.
No open ports, no public TLS to manage. (Cloud clients like Claude web / ChatGPT
can't reach a tailnet — use them only if you choose public exposure below.)

### Public exposure (for cloud clients) — reverse proxy + TLS
Front the server with Caddy/Traefik/nginx terminating TLS on a real domain:
```
rmm.yourdomain.com {
    reverse_proxy 127.0.0.1:8080
}
```
Then add `https://rmm.yourdomain.com/mcp` as a custom connector in the client.
Set `RMM_PUBLIC_URL=https://rmm.yourdomain.com` and, if behind the proxy,
`RMM_TRUST_PROXY_HEADERS=true`.

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
4. **Approval guardrail.** External AI can only create job proposals → human
   approves in the tool. It has no direct execute/delete tools. See
   [07-guardrails-security](07-guardrails-security.md).
5. **Least privilege.** The `/mcp` group requires `mcp.use`, not broad admin
   access.

## Recommended hardening checklist

- [ ] Private network (Tailscale) **or** reverse proxy + valid TLS cert.
- [ ] `RMM_MCP_ALLOWED_IPS` set to your client / proxy egress IPs.
- [ ] Dedicated API token per connector with only the needed role; rotate/revoke
  as needed.
- [ ] `RMM_ALLOW_UNAPPROVED_JOBS=false` (default) — approval stays mandatory.
- [ ] Watch the audit log for `apitoken.create` / `job.propose` / `job.approve`.

## What the AI can and cannot do over MCP

| Capability | Allowed? |
|------------|----------|
| List devices, read inventory, fleet summary, job status | ✅ only with matching permissions |
| Propose scripts/bulk/high-level actions | ✅ only with `jobs.create`; awaits human approval |
| Execute a script directly | ❌ |
| Delete a device or group | ❌ (permission-gated human action in the tool) |
