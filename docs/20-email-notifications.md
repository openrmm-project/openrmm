# 20 — Email notifications

Alerts reach an operator's inbox, not only the console. Delivery is **queued in
the database and drained by a background worker**, and messages are addressed to
**users** — never to a configured address list.

Both of those are deliberate. Sending inline with the scheduler tick would let
one hung SMTP server stall check evaluation for the whole fleet; an address list
would let a misconfiguration mail one client's hostnames to another client's
contact, which is the worst failure this feature could have. A user carries an
access scope the delivery query can enforce.

## What is sent, and when

| Event | Trigger | Default |
|---|---|---|
| `check_failed` | a check transitions to failing **or** warning | on |
| `check_recovered` | a failing check passes again, or an offline device reports back | on |
| `agent_offline` | a device stops reporting past the offline threshold | on |
| `job_failed` | a job returns a non-zero exit | **off** |

Mail goes out on a **state transition only**, never on a routine evaluation.
This is enforced in the store rather than the scheduler: `RaiseAlert` inserts
`ON CONFLICT … DO NOTHING` and returns whether *this* call opened the alert, and
`ResolveAlertsForSource` returns how many it closed. A check failing every 30s
therefore mails once per incident instead of once per tick.

`job_failed` has no open/resolve cycle to key on — a failure is a one-off event —
which is why it is opt-in: a bad script pushed to the fleet would otherwise mail
once per device.

## Who receives a message

`RecipientsForAgentEvent` requires all of:

1. an address on the user (`users.email`, optional — console-only accounts work
   without one; it is also where a password reset for that account is sent, which
   is why only `users.email.manage` may change it),
2. an enabled subscription, or no row at all for a default-on event,
3. the **`alerts.view`** permission the user's role carries, and
4. the device inside the user's **access scope**.

Condition 4 uses the same predicate that authorizes *reading* a device, so mail
can never reveal a device the recipient could not open. Condition 3 is why a
`readonly` account with an address stays silent: mailing an alert the recipient
cannot open in the console is a leak, not a notification.

Absence of a subscription row means "use the default", so an event type added
later does not silently start mailing everyone.

## The outbox

`notifications` is a durable queue (`pending | processing | sent | failed`).
Rows survive a restart, which matters because restarts and outages correlate —
the alert nobody has read yet is exactly the one a crash must not drop.

The worker claims batches with `FOR UPDATE SKIP LOCKED`, so a second server
replica can run the same worker without sending anything twice. A claim is a
**lease**: the row moves to `processing` with a `lease_token` and a five-minute
`lease_until`, and the send is only marked against that token. A worker that
dies between claiming a message and sending it therefore returns it to the queue
when the lease lapses, instead of leaving it claimed forever — `FOR UPDATE SKIP
LOCKED` alone protects concurrent workers, not a worker that stops existing.
`rmm_notifications_processing` counts what is currently leased.

A `dedupe_key` (unique when set) stops the same alert being queued twice by two
paths racing to notify about it.

A failed send is retried with exponential backoff from 2 minutes (a greylisting
server rejects the first attempt on purpose), and is abandoned after **5
attempts** — an address that bounces forever must not keep a worker busy. The
row stays as the record of what was not delivered.

If SMTP is not configured, the worker leaves rows pending rather than failing
them: they go out once an operator finishes the setup.

### Knowing when delivery has stopped

The outbox is the thing to watch, because alert delivery failing is the failure
that hides every other failure — it is noticed by *not* being told about
anything, which is to say it is not noticed.

`/metrics` (token-gated) exposes three gauges:

| Gauge | Meaning |
|---|---|
| `rmm_notifications_pending` | queued, not yet delivered |
| `rmm_notifications_failed` | abandoned after 5 attempts; never retried again |
| `rmm_notifications_oldest_pending_seconds` | age of the oldest undelivered message |

Alert on the last one. A healthy outbox drains within a worker tick (30s), so
minutes of backlog means mail has stopped leaving, whatever the cause.

The worker's logs are the second signal, and they speak on **transitions**, not
on every 30s tick:

- The **first** failed send is logged, naming the transport host, the attempt
  number and the retry delay — not only the fifth and final one, which used to
  mean roughly 30 minutes of backoff with nothing in the log. Repeats of the same
  error stay quiet; a *different* error speaks again.
- Giving up after 5 attempts names the message and the host it failed on.
- A transport that cannot deliver at all logs once, with a reason that separates
  "nothing configured" from "a sender is set but no host" from "a host is set but
  no sender" — the last being the half-filled alerts profile that would otherwise
  stop every alert in complete silence. Recovery is logged once too.

## Configuration

The two transports are configured in two different places, and the split is the
point.

| Profile | Configured in | Carries |
|---|---|---|
| Account | `RMM_SMTP_*` env, **no console form** | password resets, and nothing else |
| Alerts | `smtp.alerts.*` in the database, editable in Settings ▸ Alert email | notifications — only through a server set there |

### Why account mail is env-only

Password-reset mail is an authentication artefact, not a preference. Its sender
and credentials are set by the server operator. An editable copy in
`app_settings` would let anyone with `settings.manage` repoint it. So the
account transport has no console form
and **no write endpoint** — `PUT /api/config/smtp` does not exist, and
`settings.SaveSMTP` refuses the profile with `ErrAccountProfileReadOnly`.

The cost is honest: correcting a rejected password means changing the
environment and restarting, rather than editing a form. That is the right trade
for the credential that guards account recovery.

Set it with:

```
RMM_SMTP_HOST=smtp.example.com
RMM_SMTP_PORT=587
RMM_SMTP_USERNAME=no-reply@example.com
RMM_SMTP_PASSWORD=…          # or RMM_SMTP_PASSWORD_FILE=/run/secrets/smtp_password
RMM_SMTP_FROM=no-reply@example.com
RMM_SMTP_FROM_NAME=OpenRMM
RMM_SMTP_TLS_MODE=starttls   # starttls | tls | none
RMM_SMTP_SKIP_VERIFY=false
```

An empty `RMM_SMTP_HOST` means password-reset mail is off; the reset endpoint
still answers 202 (its enumeration defence) and logs that no transport is
configured.

### The alerts transport

Alert mail is the tenant's own: their relay, their sender, edited in the
console and stored in the database under `smtp.alerts.*`. It is read fresh on
every use, so a correction takes effect on the next message.

**Alerts never use the account transport** — not its relay, not its
credentials, not its sender. On a hosted instance the account transport is the
operator's relay, signing for the operator's domain. When alerts could ride it
with a sender of their own choosing, any tenant administrator could set
`billing@<operator domain>` and mail anyone, DKIM-signed, as the operator. So:

- **No host** — alert mail is **off**. Nothing is queued while it is off (a
  backlog would otherwise be sent all at once the day a relay appears); the
  alerts themselves are still in the console.
- **A host** — alerts leave through that relay, with its own credentials and
  sender.
- **A sender or login without a host** is refused on save
  (`alerts_need_own_server`). A sender stored that way by an older version is
  ignored.

If the alerts relay fails, alerts retry on it and are **never rerouted** through
the account transport.

### The operator's shared alert service

A hosted operator can deliver alerts for instances whose administrators set no
relay of their own, through one service of the operator's, set in the
environment like the account transport:

```
RMM_ALERTS_SMTP_HOST=smtp.mail.example.com
RMM_ALERTS_SMTP_PORT=587
RMM_ALERTS_SMTP_USERNAME=alerts
RMM_ALERTS_SMTP_PASSWORD=…           # or RMM_ALERTS_SMTP_PASSWORD_FILE
RMM_ALERTS_SMTP_FROM=alerts@notify.example.com
RMM_ALERTS_SMTP_FROM_NAME=OpenRMM Alerts · acme
RMM_ALERTS_SMTP_TLS_MODE=starttls
RMM_ALERTS_HOURLY_LIMIT=120          # per instance
```

Alert mail then goes, in order, through the tenant's own relay (Settings >
Email), else this service, else nowhere. **Never the account transport.**

The alert text is the tenant's — hostnames, check names — while the sender
domain is the operator's, so the service holds itself to rules a tenant's own
relay does not need:

- **Fixed sender.** `RMM_ALERTS_SMTP_FROM` and its display name; nothing in the
  console changes them. Put the instance's name in the display name.
- **Confirmed recipients only.** A tenant administrator types the addresses in;
  without a check they could point mail signed with the operator's domain at
  anyone. So an address receives nothing through the service until its owner
  clicks a confirmation link, mailed through the service when the address is
  set (and again on request from the user's notification settings, or by an
  administrator). The link is single use, expires in 48 hours, and confirms
  only the address it was sent to; changing an address clears its
  confirmation. Unconfirmed recipients are left out when alerts are queued and
  checked again at delivery. Confirmations are throttled per address (one per
  five minutes, five a day, whichever account asks) and per instance (100 a
  day).
- **Hourly budget.** At most `RMM_ALERTS_HOURLY_LIMIT` alerts an hour leave an
  instance; the rest waits in the outbox for the next hour, and the transition
  is logged once each way.
- **A footer** under each alert names the instance it came from and says the
  content is that instance's.
- The alerts **test button** sends only to the caller's own confirmed address.

Use a sending domain of its own for this service (for example
`notify.example.com`), separate from the account transport's, so complaints
about alert volume never touch the reputation password resets depend on.

**The alerts relay must be a public address.** It is typed in by a settings
administrator, and the server dialling wherever they point it — with the answer
shown back by the test button — is a way to probe the network the server sits
in. Loopback, private (RFC 1918, IPv6 ULA), link-local (cloud metadata),
carrier-grade NAT and reserved addresses are refused on save
(`alerts_host_not_public`) and again at connect time, on the address actually
dialled, so a name that later resolves somewhere private is caught too. A
self-hosted instance with an internal relay sets
`RMM_SMTP_ALERTS_ALLOW_PRIVATE=true`. The account transport, set by the operator
in the environment, is not restricted.

**Blocked mail ports:** some cloud providers block outbound 25, 465 and 587.
On those, every relay — the account transport, the shared service and a
tenant's own — has to accept another port (many transactional mail services
offer 2525 or 2587; Microsoft 365 and Gmail do not).

Settings are under **Users & Settings → Email** (`settings.manage`), one tab per
profile:

| Field | Notes |
|---|---|
| Host | Account: clearing it turns email off **and discards the stored password**. Alerts: blank turns alert mail off and clears the profile |
| Port | Defaults to 587 |
| Encryption | `starttls` (default) · `tls` (implicit, usually 465) · `none` |
| Certificate check | Skip only for a private CA on a trusted network |
| From / From name | The sender address is required whenever a host is set |
| Username / Password | Blank username = a relay that does not authenticate |

The password is **write-only**: it is encrypted at rest with the same instance
key as the other secrets, and the API returns only a `password_set` boolean.
Blank on save preserves the stored value.

Configuration is rejected at save time — not discovered hours later as a stuck
outbox — if the TLS mode is invalid, the sender is missing, or credentials would
be sent over an unencrypted link. That last rule means **switching an
authenticated server to `none` fails while a password is stored**; clear the host
(which drops the password) and re-enter the server if that is really intended.

**Send test** delivers one message synchronously with the *stored* settings, so
save before testing. Each tab tests **its own** transport, resolved the way
delivery resolves it — testing the account transport and calling it an alert test
is how a broken alerts relay stays hidden. The account transport's test mails
**only the caller's own address** (the bootstrap admin token excepted): it is the
operator's relay, and an open recipient would let a settings administrator send
the operator's mail to anyone. It is the only place mail is sent on a
request path; the transport timeout (20s) bounds it.

## Endpoints

```
GET    /api/config/smtp             settings.manage   account transport (read-only, from env); "read_only": true
                                                     no PUT: the account profile is env-backed
POST   /api/config/smtp/test        settings.manage   caller's own address only (blank = it)
GET    /api/config/smtp/alerts      settings.manage   alerts profile; "enabled" = alert mail will leave
PUT    /api/config/smtp/alerts      settings.manage   blank host = alert mail off; sender without host = 400
POST   /api/config/smtp/alerts/test settings.manage   tests the alerts transport
GET    /api/me/notifications        any user          address (read-only) + merged preferences
PUT    /api/me/notifications        any user          {"subscriptions": {…}}
PUT    /api/users/{id}/email        users.manage + users.email.manage
POST   /api/users                   users.manage      an optional "email" also needs users.email.manage
```

**Preferences** are self-service: which events a user hears about grants no
visibility their access scope does not already allow. **The address is not.**
It is where a password reset link is delivered, so whoever can rewrite it can
take over an account that has no second factor. That takes `users.email.manage`
— held by `admin`, and deliberately *not* implied by `users.manage`, so a
delegated user manager can create and scope technicians without it.

Address changes are audited (`user.email.update`), as are alerts-profile edits
(`config.smtp.alerts.update`) and both test sends (`config.smtp.test` /
`config.smtp.alerts.test`); the SMTP audit records which fields were touched,
never their values. There is no `config.smtp.update` action, because the account
transport cannot be edited through the API at all.

## Schema

Migration `003_email_notifications.sql` adds `users.email` (optional, unique
case-insensitively among users that have one), `notification_subscriptions`
(one row per user per event type), the `notifications` outbox, and
`password_resets`.

Migration `004_user_email_permission.sql` grants `users.email.manage` to `admin`.
The alerts transport needs no schema: both profiles live in `app_settings`.

## Password reset

Reset uses the **account** transport (never the alerts one) and deliberately not
the **outbox**.

Queuing a reset would be wrong three times over. `notifications.body` is stored
and never pruned, so the link — and therefore a working token — would sit in the
database forever, defeating the point of storing only the token's hash. The
worker drains on a 30s cadence with minutes of backoff, long enough that a user
requests a second link before the first arrives. And `Queue` fans out by device
scope and subscription: a message with no agent is dropped outright, and a
`readonly` account, lacking `alerts.view`, could never reset its password.

So `notify.SendPasswordReset` calls the transport directly and persists nothing.

```
POST /api/auth/password-reset          unauthenticated, rate limited
POST /api/auth/password-reset/confirm  unauthenticated, rate limited
```

**Enumeration resistance is the design constraint.** The request endpoint is
unauthenticated, so any difference between "account exists" and "it doesn't"
turns it into an oracle for testing an address list. It therefore answers
`202 Accepted` with one fixed message in every case — unknown address, malformed
address, SMTP down — and responds *before* doing any work. The lookup, the token
and the SMTP wait all happen in a detached goroutine, because each takes time
that varies with whether the account exists and an attacker can measure that.
Failures go to the log, never to the caller.

Tokens last **one hour**, are single-use, and are stored only as a SHA-256 hash.
Requesting a new link marks the outstanding one used, so at most one is live per
account. Redemption is a single `UPDATE … WHERE used_at IS NULL AND expires_at >
now() RETURNING user_id`, so two concurrent redemptions of the same token
resolve to exactly one winner rather than a read-then-write race. Expired,
already-used and never-issued are one indistinguishable answer.

Consuming a link **signs out every session**. Unlike a self-service password
change there is no caller session to preserve, and whoever prompted the reset may
be the person already holding a stolen one. The new password is checked
**before** the token is redeemed, so a password that fails the rules leaves the
link usable, as the error says. Looking a token up without redeeming it tells a
caller nothing: a token is 256 random bits, so whether a guess is live is not a
question anyone can ask.

**Reset mail is throttled per account**: one mail per account every five
minutes and five a day. The route's per-IP limit does not protect a mailbox —
from many addresses anyone could send one person an endless stream of reset
mail through the operator's relay, spending its quota and its domain's
reputation. Requests past the limit get the same `202` and send nothing.

**Accounts linked to single sign-on get no reset** while SSO is on
(`RMM_OIDC_ENABLED=true`): no mail is sent, and a link issued before SSO was
turned on is refused with `sso_required`. A password would otherwise let whoever
reads the mailbox sign in around the provider's MFA and its offboarding. See
[21-single-sign-on](21-single-sign-on.md).

The link is `<RMM_PUBLIC_URL>/?reset=<token>`; the console strips the token from
the address bar on load so it does not linger in history or leak as a `Referer`.
A reset link takes precedence over a live session, so an already-signed-in tab
cannot swallow it.

## Not implemented yet

Bodies are plain text only; there is no HTML alternative and no digest/grouping,
so a fleet-wide incident sends one message per affected device to each in-scope
recipient. The `notifications` outbox has no retention policy — sent rows
accumulate, which is survivable because the partial index keeps the worker's
claim query small, but it will want pruning eventually.
