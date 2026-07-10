# 17 — MCP OAuth 2.1 (OpenRMM as its own Authorization Server)

> **Status: implemented** behind `RMM_MCP_OAUTH_ENABLED` (off by default).
> Discovery metadata, Dynamic Client Registration, the login+consent
> authorization endpoint (PKCE S256), the token endpoint (code exchange +
> refresh with rotation), and `/mcp` acceptance of OAuth access tokens are all
> built and verified end-to-end. Enable it only with an **HTTPS** `RMM_PUBLIC_URL`.

## Why

Static bearer tokens ([11-mcp](11-mcp.md)) work for Claude **Desktop** (via the
`mcp-remote` bridge), but **cloud** MCP clients — Claude **web**, ChatGPT — are
built around the **OAuth** connector flow, not a pasted token. To let them connect
natively, the RMM becomes **its own OAuth 2.1 Authorization Server (AS)** *and*
Resource Server (RS). No separate server, no external IdP required: the OAuth
endpoints are added to the same Go app and reuse the existing users, TOTP 2FA and
per-user access scope.

## Design decisions

- **Self-contained AS**, hand-rolled and minimal (no heavy dependency like
  `ory/fosite`) — matches the codebase (CGO-free, few deps, hand-rolled auth).
- **Opaque tokens** (cryptographically random), stored **hashed (SHA-256)** —
  the same pattern as `api_tokens` / `sessions`, no JWT signing to manage.
- **PKCE S256 required**; public clients (`token_endpoint_auth_method: none`).
- **Dynamic Client Registration** (RFC 7591) so cloud connectors self-register.
- Tokens are **bound to a user** → they inherit that user's **role + access
  scope** and require `mcp.use`, so the existing permission/scope enforcement on
  `/mcp` is unchanged — only the credential's origin differs.
- **Opt-in**: everything is gated by `RMM_MCP_OAUTH_ENABLED` (off by default).
  The issuer is `RMM_PUBLIC_URL`, which **must be HTTPS** when enabled.
- Lives on the **MCP door** ([`deploy/README-production.md`](../deploy/README-production.md)):
  `/.well-known/*`, `/oauth/*` and `/mcp` share one public port.

## The flow (Claude web as the client)

```
1. GET /.well-known/oauth-protected-resource      -> points at this AS
   GET /.well-known/oauth-authorization-server     -> authorize/token/register URLs
2. POST /oauth/register (DCR)                       -> client_id
3. browser -> GET /oauth/authorize?client_id&redirect_uri&code_challenge&state
      user logs in to OpenRMM (password + 2FA) and CONSENTS
   <- 302 redirect_uri?code=...&state=...           (short-lived code)
4. POST /oauth/token {code, code_verifier, ...}     -> access_token (+ refresh_token)
5. POST /mcp  Authorization: Bearer <access_token>  -> tools, bound to the user's
      role + scope; refreshed via the refresh_token
```

## Data model (`oauth_clients` / `oauth_codes` / `oauth_tokens`)

- `oauth_clients` — registered connectors (`client_id`, name, `redirect_uris`).
- `oauth_codes` — short-lived authorization codes (hashed), with the PKCE
  `code_challenge`, bound `user_id`, `redirect_uri`, `scope`; deleted on exchange.
- `oauth_tokens` — access + refresh tokens (both hashed), bound to a `user_id`,
  with `expires_at` (access, 1h) and `refresh_expires_at` (refresh, 30d). An
  expired/absent access token stops authenticating; the row is swept once both
  are expired.

## Endpoints

| Endpoint | Auth | Purpose |
|----------|------|---------|
| `GET /.well-known/oauth-authorization-server` | none | RFC 8414 AS metadata **(done)** |
| `GET /.well-known/oauth-protected-resource` | none | RFC 9728 resource metadata **(done)** |
| `POST /oauth/register` | none | Dynamic Client Registration (Phase 2) |
| `GET/POST /oauth/authorize` | user login + consent | authorization code (Phase 3) |
| `POST /oauth/token` | PKCE | code→token, refresh (Phase 3) |
| `POST /mcp` | Bearer (API token **or** OAuth access token) | tools (Phase 4) |

## Phased plan

1. ✅ **Foundation** — discovery metadata, `oauth_*` schema, `RMM_MCP_OAUTH_ENABLED`.
2. ✅ **Registration** — `POST /oauth/register` (validate + store clients).
3. ✅ **Authorize + token** — login+consent page, PKCE code issue, `/oauth/token`
   (code exchange + refresh with rotation), audited (`oauth.authorize` / `oauth.token`).
4. ✅ **Resource acceptance** — `authMiddleware` also accepts a valid `oauth_tokens`
   access token (→ the bound user's role/scope), so `/mcp` works with the OAuth
   credential.
5. ✅ **Hardening** — the scheduler sweeps expired codes/tokens hourly
   (`PurgeExpiredOAuth`, only once both access **and** refresh are expired); the
   refresh token has its own bounded lifetime (`refresh_expires_at`, 30 days);
   and an OAuth access token is **narrowed to `/mcp`** — it returns `403` on the
   rest of the API/UI (it authenticates the MCP resource only, still gated by the
   bound user's role + scope). **Remaining:** connect a real Claude web / ChatGPT
   connector to confirm interop.

Verified end-to-end (curl): register → authorize (login+consent) → code → token
(PKCE checked; wrong verifier → `invalid_grant`) → `/mcp initialize` with the
access token → `refresh_token` rotation.

## Security notes

- Codes are single-use, short-TTL, PKCE-bound; tokens are hashed at rest and
  expire; refresh rotates. Redirect URIs are validated against the registered set.
- The approval guardrail is unchanged: an OAuth-authenticated client still only
  **proposes** jobs — a human approves. It inherits the bound user's scope, so a
  scoped user's connector sees only that user's fleet.
