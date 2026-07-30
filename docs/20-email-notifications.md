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
   without one),
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

`notifications` is a durable queue (`pending | sent | failed`). Rows survive a
restart, which matters because restarts and outages correlate — the alert nobody
has read yet is exactly the one a crash must not drop.

The worker claims batches with `FOR UPDATE SKIP LOCKED`, so a second server
replica can run the same worker without sending anything twice. A failed send is
retried with exponential backoff from 2 minutes (a greylisting server rejects the
first attempt on purpose), and is abandoned after **5 attempts** — an address
that bounces forever must not keep a worker busy. The row stays as the record of
what was not delivered.

If SMTP is not configured, the worker leaves rows pending rather than failing
them: they go out once an operator finishes the setup.

## Configuration

SMTP lives **only in the database**, unlike the AI/MeshCentral settings which
overlay an env baseline: there is no SMTP env config to overlay, and an operator
fixing a rejected password should not need a redeploy. It is read fresh on every
use, so a correction takes effect on the next queued message.

Settings are under **Users & Settings → Email** (`settings.manage`):

| Field | Notes |
|---|---|
| Host | Clearing it turns email off **and discards the stored password** |
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
save before testing. It is the only place mail is sent on a request path; the
transport timeout (20s) bounds it.

## Endpoints

```
GET    /api/config/smtp          settings.manage   transport, password as a boolean
PUT    /api/config/smtp          settings.manage   blank password preserves
POST   /api/config/smtp/test     settings.manage   {"to": "…"}, blank = caller's own address
GET    /api/me/notifications     any user          address + merged preferences
PUT    /api/me/notifications     any user          {"email": …, "subscriptions": {…}}
PUT    /api/users/{id}/email     users.manage      onboard a technician's address
POST   /api/users                users.manage      accepts an optional "email"
```

Preferences are **self-service**: the address only decides where the caller's own
alerts go, and grants no visibility their access scope does not already allow.
Setting *someone else's* address needs `users.manage` and is an onboarding
convenience — a technician should hear about their devices before their first
sign-in. Every address change is audited (`user.email.update`), as are SMTP edits
(`config.smtp.update`) and tests (`config.smtp.test`); the SMTP audit records
which fields were touched, never their values.

## Schema

Migration `003_email_notifications.sql` adds `users.email` (optional, unique
case-insensitively among users that have one), `notification_subscriptions`
(one row per user per event type), the `notifications` outbox, and
`password_resets`.

## Password reset

Reset shares the SMTP **transport** and deliberately not the **outbox**.

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
be the person already holding a stolen one. The token is consumed before the new
password is validated: it is single-use either way, so a rejected password cannot
be used to probe which tokens are live — the user just asks for a new link.

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
