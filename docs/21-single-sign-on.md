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

That property is also what makes it safe to point a hosted control plane at a
customer's instance later: the control plane can say who someone is, and cannot
give itself their fleet.

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

Register `https://<your console>/api/auth/oidc/callback` as a redirect URI at
the provider, as a **confidential** client using the **authorization code** flow
with PKCE.

The issuer must be `https://`. Discovery, the token exchange and the JWKS fetch
all go over it; plain HTTP would put the client secret and the authorization
code on the wire, so the server refuses to start rather than doing it.

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

`RMM_OIDC_AUTO_PROVISION=true` together with `RMM_OIDC_DEFAULT_ROLE=admin` is
refused at startup rather than warned about.

An account created this way has **no password**: bcrypt cannot verify against
the empty hash it is given, so local sign-in for it fails the way any wrong
password does. Give it one through the normal reset if you want both.

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

The console is a single-page app that authenticates with a bearer token, not a
cookie, so the callback cannot simply set a session and redirect. It sends the
browser back with a token in the URL **fragment**.

Fragments are never sent to a server. The token therefore stays out of proxy
logs, access logs and `Referer` headers on its way to the page, which a query
string would not. The page reads it, exchanges it for a session and strips it
from the address bar so a refresh or a shared link cannot replay it.

The `redirect_to` parameter accepts a path and nothing else. Anything absolute
or protocol-relative is discarded rather than repaired: an open redirect on a
login route is the classic ingredient of a phishing link that starts on the real
console domain.

## When it goes wrong

Failures send the browser back to the login screen with a generic message. The
specific reason is logged and audited, never shown — "no account for
bob@example.com" on a login page is an account enumerator.

Audit actions: `user.sso.login`, `user.sso.linked`, `user.sso.provisioned`,
`user.sso.denied`.

If discovery fails at startup the server still starts, logs it, and shows no SSO
button. Agents, the scheduler and local sign-in do not depend on the provider,
and an RMM that will not boot because someone else's identity service is down is
an RMM that has adopted their outage.

## What did not change

- **Local sign-in still works**, always. Keep at least one local admin: it is
  the way back in when the provider is unreachable or misconfigured, and
  locking yourself out of your own fleet to save one password is a bad trade.
- **Roles, scopes and permissions** are unchanged and still local.
- **Disabling a user** in the console is still enough. A disabled account is not
  returned by the SSO lookup, so someone removed here cannot sign back in
  through the provider.
- **Two-factor** is the provider's business for SSO accounts. The console's own
  TOTP still guards local sign-in.
