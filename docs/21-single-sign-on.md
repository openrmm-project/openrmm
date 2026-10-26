# Single sign-on (OpenID Connect)

The console has only ever known local users: a row in `users` with a bcrypt
hash. That is fine for one operator and wrong for everyone else. An MSP already
has an identity provider, and a second directory of technicians is a second
place to forget to remove someone who left.

This adds OpenID Connect as a way to **authenticate**, and deliberately not as
a way to **authorise**.

| question | answered by |
|---|---|
| who is this? | the identity provider |
| what may they do? | this instance — `role` and the access scope, exactly as before |

Keeping the second answer local is the whole design. An identity provider that
could also assign roles would be a provider able to make itself an admin of
every instance it signs into. It cannot. An account arrives with the role an
operator gave it, and a provider that claims otherwise is ignored — no claim is
read for permissions, ever.

This also keeps account permissions under the control of the OpenRMM operator
even when the identity provider is managed by another organization.

## Turning it on

```bash
RMM_OIDC_ENABLED=true
RMM_OIDC_ISSUER=https://your-idp.example.com        # exactly as the provider publishes it
RMM_OIDC_CLIENT_ID=openrmm-console
RMM_OIDC_CLIENT_SECRET=…                            # or RMM_OIDC_CLIENT_SECRET_FILE
```

Everything else has a working default:

| variable | default | what it does |
|---|---|---|
| `RMM_OIDC_SCOPES` | `openid,profile,email` | `openid` is added back if you drop it — without it there is no ID token to verify |
| `RMM_OIDC_REDIRECT_URL` | `<RMM_PUBLIC_URL>/api/auth/oidc/callback` | must match what you registered with the provider |
| `RMM_OIDC_USERNAME_CLAIM` | `email` | which claim becomes the console username |
| `RMM_OIDC_AUTO_PROVISION` | `false` | create an account on first successful sign-in |
| `RMM_OIDC_DEFAULT_ROLE` | `readonly` | the role such an account is created with |
| `RMM_OIDC_BUTTON_LABEL` | `Single sign-on` | so the button can read "Sign in with Okta" |
| `RMM_OIDC_SHOW_BUTTON` | `true` | `false` hides the button; always shown with `RMM_OIDC_AUTO_PROVISION=true` (see below) |
| `RMM_OIDC_ACR_VALUES` | empty | comma-separated acceptable authentication contexts; sent as `acr_values` and checked against the signed `acr` claim (any match) |
| `RMM_OIDC_REQUIRED_AMR` | empty | comma-separated authentication methods; every entry must occur in the signed `amr` array |

Register `https://<your console>/api/auth/oidc/callback` as a redirect URI at
the provider, as a **confidential** client using the **authorization code** flow
with PKCE.

The issuer must be `https://`. Discovery, the token exchange and the JWKS fetch
all go over it; plain HTTP would put the client secret and the authorization
code on the wire, so the server refuses to start rather than doing it.

## Requiring MFA from an OIDC provider

Register a confidential client with your OIDC provider for each OpenRMM
instance. Configure its exact callback URL and separate credentials. Control
which users the provider allows to authorize the client; authorization at the
provider does not grant an OpenRMM role.

For an authenticator-backed sign-in:

```bash
RMM_OIDC_ACR_VALUES=urn:example:acr:mfa
RMM_OIDC_REQUIRED_AMR=otp,mfa
```

Some providers report `pwd` or `email` for the initial proof and `otp,mfa` after
an authenticator challenge. A recovery code may report `recovery,mfa`, **not**
`otp`. To permit recovery-code login, require only `mfa` instead of `otp,mfa`.
Merely enabling TOTP on the account does not prove MFA in an existing session.

An `acr_values` request alone is not enforcement: providers may return a weaker
context. RMM rejects missing, malformed or insufficient claims **before** linking,
provisioning or creating a console session. Empty settings preserve compatibility
with providers that do not emit these claims. Replace the example ACR value with
one your provider actually issues.

These controls apply to new SSO logins, not existing console sessions or local
password login. Local TOTP and account permissions remain separate. Provider
logout/suspension blocks new grants but does not revoke an already-created RMM
session; revoke/disable it in the instance when immediate termination is needed.

Pass these variables into the **server process/container**, not only the host
shell. Compose deployments need explicit `environment` entries or an `env_file`
in the server service; restart the server after changing the configuration.

## Who gets an account

On a successful sign-in, in order:

1. **The identity is already linked** — sign in. The link is
   `(issuer, subject)`, never the email address: addresses get reassigned when
   people leave, and `sub` is the one claim OIDC guarantees is stable and unique
   within an issuer. Matching on email means whoever inherits `alice@` inherits
   Alice's console.
2. **A local account has the same username** — link it, once. This is the
   migration path: create people the way you always have, and they start
   signing in through the provider without losing their role or scope. An
   account already linked to a different identity is never relinked; that is an
   account takeover, not a convenience.
3. **Nobody** — create the account only if `RMM_OIDC_AUTO_PROVISION=true`.

Auto-provisioning is off by default and the reason is worth stating plainly:
with it on, **everyone your provider will authenticate has an account here**,
which for a Google Workspace or Entra tenant is the whole company. Leave it off
unless the provider only holds people who should be in this console.

When the username is email-shaped, the first link or automatic provisioning is
accepted only when the token carries the same `email` with
`email_verified=true`. Identities already linked by `(issuer, sub)` do not depend
on the provider continuing to send that optional claim.

`RMM_OIDC_AUTO_PROVISION=true` together with `RMM_OIDC_DEFAULT_ROLE=admin` is
refused at startup rather than warned about.

An account created this way has **no password**: bcrypt cannot verify against
the empty hash it is given, so local sign-in for it fails the way any wrong
password does.

**While SSO is on, an account linked to it has no password path at all** —
created by SSO or linked on its first SSO sign-in alike. Password sign-in (the
console and the MCP OAuth form) is refused with `sso_required`, and password
reset sends nothing. Otherwise a password, or a reset mail read by whoever holds
the mailbox, would step around the provider's MFA, conditional access and
offboarding. Setting `RMM_OIDC_ENABLED=false` is the break-glass: linked accounts
sign in with their passwords again.

**An account can be single sign-on only before its first sign-in.** Until the
first single sign-on links an account, it used to keep both paths: a password
sign-in and "Forgot password". For a managed service whose owner is meant to
sign in only through its portal and the portal's second factor, that was a way
around both, and one a customer found on day one. `PUT /api/users/{id}/sso`
with `{"required": true}` (`users.manage`, audited as `user.sso_required`)
marks the account; from then on it is treated exactly like a linked one, and
the first single sign-on links it as before. `{"required": false}` clears the
mark, without unlinking an identity already linked. OpenRMM Cloud's
provisioning marks the owner's account as soon as it creates it. The users list
reports either state as `sso`.

**The login screen asks who, then how.** The first step sends the username to
`POST /api/auth/method`, which answers `sso` when single sign-on is on and the
account signs in through it (linked or marked), and `password` otherwise — for
an unknown name too, so it reveals only the single sign-on accounts, whose
names are their owners' addresses. An `sso` answer sends the browser to the
provider at once; nobody has to know which button is theirs. The separate
button stays by default: it is how a local account that is neither linked nor
marked does its first single sign-on and gets linked. `RMM_OIDC_SHOW_BUTTON=false`
hides it where every single sign-on account is already linked or marked, as
OpenRMM Cloud's consoles do; with `RMM_OIDC_AUTO_PROVISION=true` it is always
shown, since a first-time user has no account the first step could recognise.

Marking an account ends its sessions, as disabling one does: a session opened
with a password before the mark would otherwise outlive it by up to seven days.

Connecting an MCP client works through the provider too: the OAuth consent page
offers the same single sign-on button, see
[17-mcp-oauth](17-mcp-oauth.md#signing-in-through-single-sign-on).

## What is checked

Verification is delegated to `github.com/coreos/go-oidc`, which does the part
that must not be improvised: fetching and caching the provider's keys, checking
the signature and algorithm, and validating issuer, audience and expiry.

On top of that, every sign-in carries three separate defences, and each one
stops a different attack:

| | stops |
|---|---|
| `state` | a callback that did not come from a sign-in this server started (CSRF) |
| `nonce` | an ID token minted for a different sign-in being replayed at this callback |
| PKCE (S256) | an authorization code being redeemed by anyone but the browser that asked for it, even if the code leaks through a log or a `Referer` |

All three are stored in `oidc_login_states` rather than held in memory, so a
sign-in survives a restart, and the row is **deleted as it is read** — a
replayed callback finds nothing.

The state is stored as its SHA-256, like sessions and API tokens: a dump of that
table must not hand anyone a usable in-flight login.

## Getting the session back to the console

The callback sends a **single-use code** in the URL fragment (`sso_code`),
valid for 60 seconds. The page strips the fragment and posts the code to
`/api/auth/oidc/exchange`; only this response returns the normal bearer session.
Legacy `sso_token` fragments are ignored and cannot replace a tab's session.

The login starts by setting a host-only, HttpOnly, SameSite=Lax cookie (Secure
on HTTPS). Both the provider callback and the code exchange require this browser
binding. The exchange also checks the console Origin and consumes the code
atomically with session creation. A copied callback or fragment cannot sign a
different browser in; an invalid code preserves an existing tab session.

MCP sign-in first visits the console origin to set the same browser binding.
Deploy the API and console together; migration 19 creates the handoff table.
The console origin comes from `RMM_OIDC_REDIRECT_URL` when agent and console
origins differ. The destination pathname already includes any frontend base path.

The `redirect_to` parameter accepts a path and nothing else. Anything absolute
or protocol-relative is discarded rather than repaired: an open redirect on a
login route is the classic ingredient of a phishing link that starts on the real
console domain.

## When it goes wrong

Failures send the browser back to the login screen with a generic message. The
specific reason is logged and audited, never shown — "no account for
bob@example.com" on a login page is an account enumerator.

Audit actions: `user.sso.login`, `user.sso.linked`, `user.sso.provisioned`,
`user.sso.denied`, `user.sso_required`.

If discovery fails at startup the server still starts, logs it, and shows no SSO
button. Agents, the scheduler and local sign-in do not depend on the provider,
and an RMM that will not boot because someone else's identity service is down is
an RMM that has adopted their outage.

## What did not change

- **Local sign-in still works** for accounts not linked to SSO. Keep at least
  one local admin that is never used to sign in through the provider — the
  first SSO sign-in with a matching username links the account, and a linked
  account loses password sign-in while SSO is on. That admin is the way back in
  when the provider is unreachable or misconfigured; `RMM_OIDC_ENABLED=false` is
  the other.
- **Roles, scopes and permissions** are unchanged and still local.
- **Disabling a user** in the console is still enough. A disabled account is not
  returned by the SSO lookup, so someone removed here cannot sign back in
  through the provider.
- **Two-factor** is the provider's business for SSO accounts — in the console
  and when authorizing MCP clients alike. The console's own TOTP still guards
  local sign-in. So **require MFA at the provider**: set
  `RMM_OIDC_REQUIRED_AMR` (for example `mfa`) or `RMM_OIDC_ACR_VALUES`, as
  described above. Without either the console accepts whatever the provider
  did, a single password included, and the server says so in its log at
  startup.

## OpenRMM Cloud: the plan's device limit

An identity provider that names `openrmm_entitlement_endpoint` in its discovery
document is an OpenRMM Cloud portal. The console then calls that endpoint every
15 minutes with its own client id and secret (HTTP Basic) and its device count,
and gets back how many devices its plan covers, when the limit starts to apply
and the portal page where the plan is changed.

- The endpoint is used only over HTTPS on the issuer's own host, and redirects
  are not followed: the request carries the client secret.
- An enrolment past the limit is refused, and the installer prints the reason.
  Devices already enrolled are never affected; deleting a device frees its place.
- The last answer is stored, so a restart or a portal outage keeps the limit the
  console last knew. An answer the console cannot read changes nothing.
- Any other identity provider has no such field: a self-hosted console has no
  device limit.
