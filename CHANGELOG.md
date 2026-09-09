# Changelog

All notable changes to OpenRMM are documented here. Format loosely follows
[Keep a Changelog](https://keepachangelog.com/); versions track the agent/release
tag.

## [Unreleased]

### Added

- **OIDC authentication-context policy.** `RMM_OIDC_ACR_VALUES` requests and
  validates acceptable signed `acr` values; `RMM_OIDC_REQUIRED_AMR` requires
  every configured authentication method before SSO account resolution. Includes
  downgrade/recovery-code tests and optional interoperability tests with the
  Cloud portal provider. Existing local sign-in and role assignment are unchanged.

- **An agent can enrol straight into a department.** The enrolment token has
  always carried where the agent lands — `enroll_tokens.site_id` is read at
  registration and written to the agent — but departments were never part of it,
  so every machine arrived at the site and someone moved it by hand afterwards.
  The token now carries the department too, and the site detail lets you choose
  it before minting the token.

  The installers are untouched. The command an operator copies does not describe
  the placement, the token does; `install.sh` has always accepted and discarded
  its `--site` argument for that reason.

  A department belongs to exactly one site, and an agent's must be its own
  site's — the rule `SetAgentSite` already applies when a device moves. A token
  naming an inconsistent pair is refused where it is written (migration `010`),
  and enrolment ignores a department outside the site it is placing the agent in,
  so the state that rule prevents cannot be created by either route.

- **A department can be renamed.** It could be created and deleted and nothing
  in between, so fixing a typo meant deleting it — which unfiles every device
  under it — and building it again. `PATCH /api/groups/{id}` moves only the
  label; the devices and the site it belongs to are untouched. Audited as
  `group.rename`.


### Added

- **Monitoring and alerts survive the things that used to lose them.** The old
  loop dispatched a check, waited for a result, and raised an alert the moment
  one threshold reading crossed the line. A dropped agent left a check that had
  been sent and would never be answered; a value oscillating around its
  threshold produced an alert per reading; an alert nobody could silence kept
  mailing; and a crash between "picked a notification off the queue" and "sent
  it" lost the message.

  Migration `008` adds the state each of those needs, leaving the existing check
  and alert rows intact.

  **Checks now have a dispatch record.** `check_executions` tracks each one sent
  to an agent with a deadline, and a partial unique index allows only one pending
  execution per (check, agent) — so a slow agent is never sent the same check
  twice. Passing the deadline marks it `timed_out` rather than leaving it pending
  forever, and `rmm_check_timeouts_total` counts it. `check_states` carries the
  per-agent streak, so a decision is made from a run of readings rather than the
  latest one.

  **Alerts stop flapping.** A check declares how many consecutive failures raise
  it (`failure_count`), how many consecutive successes clear it
  (`recovery_count`), a `hysteresis` band a recovering value must clear by, and a
  `cooldown_sec` before the same condition may raise again. The defaults (2, 2,
  2, 300s) turn a value oscillating on the threshold into one alert instead of
  one per reading.

  **An alert is a lifecycle, not a row that appears and disappears.** It carries
  a `condition_key` so the same condition deduplicates into one alert with an
  occurrence count and first/last-seen times, the observed and threshold values
  that raised it, who acknowledged or resolved it and how, and a `version` for
  optimistic concurrency so two operators acting at once cannot silently
  overwrite each other. `alert_events` keeps the timeline. An alert that returns
  after being resolved records what it reopened from.

  **Alerts can be silenced.** `alert_suppressions` holds a `silence` or
  `maintenance` window with a reason and an author, so a known-noisy condition or
  a planned maintenance window stops mailing without anyone deleting the alert or
  disabling the check. The console gained acknowledge, suppress, silence and bulk
  actions, and an alert timeline.

  **Queued mail is leased, not just claimed.** A worker takes a notification with
  a lease token and a 5-minute expiry; a crash mid-send returns the message to the
  queue when the lease lapses instead of losing it, and a `dedupe_key` unique
  index stops the same alert being mailed twice. `rmm_notifications_processing`
  joins the queue gauges.

  **Denormalized scope on the alert** (`agent_hostname`, `site_id`, `client_id`,
  `group_id`) so the alert list can be filtered and paginated by scope without a
  join per row — and so an alert still names its device after the device is gone.

  New endpoints: `GET /api/alerts/{id}`, `GET /api/alerts/{id}/events`,
  `POST /api/alerts/{id}/suppress`, `POST /api/alerts/bulk`,
  `GET|POST /api/alert-suppressions`, `DELETE /api/alert-suppressions/{id}`, and
  `GET /api/checks/{id}/instances`.

  **Retention.** Monitoring history is pruned daily with 90-day defaults, tunable
  per install with `RMM_ALERT_RETENTION_DAYS`, `RMM_CHECK_RESULT_RETENTION_DAYS`
  and `RMM_NOTIFICATION_RETENTION_DAYS`. The purge never age-deletes an active
  alert, a pending check execution, or queued mail — retention trims history, it
  does not drop work still in flight.

  **An observation carries its own identity.** Deduplicating a retried
  observation was inferred: one whose timestamp and content matched the alert's
  current state was read as a repeat and added no occurrence. That cannot tell
  "the same observation delivered twice" from "two distinct observations that
  happen to agree" — and under concurrency the second is real. Twelve
  simultaneous observations of one condition recorded eleven occurrences,
  because two landed in the same microsecond. A caller that can be retried now
  supplies a stable key (the scheduler uses the check state it is applying) and
  the alert remembers the last one applied; a caller that supplies none always
  counts, which is the honest answer when there is nothing to deduplicate
  against.

### Fixed

- **The Windows enrollment command had to be scrolled to be read.** It is a
  single line carrying the server URL and the token, and the code block scrolls
  long lines sideways rather than wrapping them — right for a script, wrong for a
  command an operator has to check before pasting it into an elevated PowerShell.
  The block now soft-wraps where it is used for install commands; what the Copy
  button puts on the clipboard is unchanged, because it copies the command rather
  than the rendering.


## [0.5.0] - 2026-08-11

### Changed

- **User row actions moved into a three-dot menu.** Email, Scope, Reset 2FA,
  Disable and Remove sat side by side on every row, so the widest row drove the
  column and **Remove** was one slip away from **Disable**. They are now one
  kebab per user, with the two irreversible actions below a separator, using the
  same `Dropdown`/`MenuItem` the device, site and client rows already use.

### Fixed

- **Windows patch titles preserve accented characters.** The Windows Update
  scanner handed PowerShell's code-page bytes directly to the JSON decoder, so
  localized titles such as `Atualização de Segurança` were stored as
  `Atualiza��o de Seguran�a`. The agent now decodes redirected PowerShell output
  at capture time, before JSON can replace the original bytes irreversibly.
  Existing corrupted titles are refreshed by the next patch scan.

- **Dropdown menus were clipped by the card they opened from.** Every card that
  holds a table sets `overflow-hidden` to keep the corners rounded, and the menu
  was positioned absolutely inside it — so the last row's menu was cut off (150px
  of a 161px menu, in the users table). Menus are now positioned `fixed` against
  their trigger, which no ancestor's overflow can clip, and open upward when the
  trigger sits near the bottom of the window. This affected the device, site and
  client menus too, not just the new one.

- **Disabling a user did nothing, and opening API tokens blanked the console.**
  The "Disable this user?" confirmation was pasted into `APITokensPanel` instead
  of `UsersPanel`. The button set state nobody rendered, so clicking Disable was
  silently inert — no dialog, no request, nothing in the audit log; and the
  panel that did render it referenced three identifiers it does not declare, so
  opening the **API tokens** tab threw `ReferenceError: confirmDisable is not
  defined` and React unmounted the whole console to a white page.

  The build gives each source file its own block scope, so this compiled and
  shipped. A test now walks every screen and fails on any component that reads
  another component's `useState` bindings, which is the general form of the
  mistake rather than this one instance of it.

- **Emptying a user's scope silently granted them the whole fleet.** Unchecking
  the last box while still in "Restricted to…" left the picker showing
  `0 selected`, and saving reported *"Scope updated · full access"* — because a
  restricted selection of nothing serializes to the same empty list as full
  access, and the API cannot tell the two apart. An operator narrowing someone's
  access could widen it to everything by unchecking one box.

  The picker's mode now belongs to the form, which refuses to save
  restricted-but-empty: granting the fleet has to go through the **Full access**
  radio. A failed scope load closes the editor instead of presenting an empty
  selection that saves as full access.

- **A user scoped to a deleted device could never have their scope changed
  again.** `user_scopes.scope_id` is polymorphic — client, site, department or
  device depending on `scope_type` — so no foreign key covered it, and deleting a
  device left the rows naming it behind. The console loads the stored scopes into
  the editor and sends them back on save, so every save for that account failed
  with `scope not found` — including the save that would have removed the stale
  row. The entry could not be seen either: the tree counted it (*"1 selected"*)
  but had no device to draw, so it looked like nothing was selected.

  Migration `006` purges stranded rows and adds triggers on `clients`, `sites`,
  `device_groups` and `agents` so a scope row dies with its target. Triggers
  rather than cleanup in the delete handlers, because deleting a company cascades
  to its sites and departments and a cascade never reaches application code.

  An account left with **no** rows at all is disabled rather than promoted —
  an empty scope set means unrestricted, so deleting the last thing a restricted
  technician could see would otherwise hand them the whole fleet. Deliberately
  granting full access is unaffected; the rule lives in the pruning path only.

- **`scope not found` now names the scope that was not found.** A request carries
  several, and the one that fails is usually a stale reference the console sent
  back without the operator knowing it was there.

### Security

- **Scope changes are audited.** `user.scopes.update` records the actor, the
  target, how many scopes there were before and after, and whether the result is
  full fleet access. Role changes, disables and address changes were all
  audited; the one mutation that could hand over the entire fleet left no trace
  of who did it.

- **Accented characters were destroyed in script output.** `Olá, Miquéias!`
  reached the database as `Ol<?>, Miqu<?>ias!`. A redirected child on Windows
  writes the system code page, not UTF-8 — Python uses cp1252 on a pt-BR
  install — so each accented byte was invalid UTF-8 on its own, and marshalling
  it replaced the byte with U+FFFD. That substitution is **lossy**: nothing
  downstream could recover the character, and rows already stored stay corrupted.

  Python is now asked for UTF-8 stdio (`PYTHONIOENCODING`, honoured since 2.6 —
  not `-X utf8`, whose Python 3.7 requirement would fail the whole script on an
  older interpreter). Output that still fails the UTF-8 check is decoded from the
  code page the OS reports rather than discarded, which covers cmd, Windows
  PowerShell and anything else that ignores the request.

- **Truncation split characters in half.** The output cap sliced bytes, so a
  limit landing inside a multi-byte character produced an invalid tail — which
  the JSON layer then replaced. This corrupted UTF-8 output from Linux and macOS
  agents too, not only Windows. Truncation is now rune-aware, and the byte-slicing
  helper is gone so the next call site cannot reintroduce it.

- **Run script: the library picker looked like it bound the job to a saved
  script.** It only ever copied the script's body into the editor — nothing
  about the job references the library, and the editor was always free to type
  in — but the picker kept displaying the chosen name afterwards, so an operator
  writing something ad-hoc went looking for a way to deselect it. It now springs
  back to "Start from a saved script…" once the copy is made, and says what it
  does.

- **Auto-update could never complete without signature verification.** With
  `-update-verify off` — what `install.ps1 -LabMode` sets when no update key is
  pinned — the agent downloaded each new build and threw it away. The mode makes
  the signature step return nothing by design, and the next step demanded the
  very proof it had just skipped: *"a verified Ed25519 candidate signature is
  required"*. Every interval, forever, visible only to whoever read the agent's
  own log.

  Signature proofs are now required only when signatures are being verified.
  Under `off` the rollback transaction is recorded with hashes alone, which is
  strictly more than it had before — an agent in that mode already installs
  whatever `/dl/` serves without checking who signed it. `strict` and `warn` are
  untouched, and a transaction *recorded* with signatures is always validated
  with them, so flipping an agent to `off` cannot turn a signed record into an
  unchecked one.

  The Windows and Unix swap paths also renamed the `.sig` sidecars
  unconditionally and failed the whole update when a file the mode guarantees
  will not exist was missing.

### Added

- **A script's category is picked, not retyped.** The field was free text with no
  list behind it, so nothing offered what already existed and one category became
  two that differed by a keystroke — with no way to put them back together short
  of opening every affected script and retyping the name identically.

  Migration `007` gives categories a registry of their own, seeded with the ones
  already in use, so a category can be **named before any script is filed under
  it** — the Categories window has **Add**. `scripts.category` still holds the
  name rather than a foreign key: the table is a catalogue, not the owner of the
  relationship, so a script written straight through the API can still carry a
  name nobody registered and listing unions both.

  The editor's Category field became a picker over that list that can also name a
  new one, and the library gained a category filter.

  `PUT /api/script-categories/{name}` rewrites every script filed under the old
  name, and a **Categories** window lists them for renaming. Renaming onto a name
  already in use **merges** the two — the repair for a category a typo split in
  half — and the console says so before doing it, because afterwards they are not
  separable. Emptying a category returns its scripts to uncategorized and keeps
  the scripts. Both are audited.

- **Policies can be edited.** A policy's name and description were write-once:
  the console could create one and delete it, and nothing in between. Fixing a
  typo in a policy assigned across the fleet meant rebuilding it and losing its
  checks, tasks and assignments on the way. `PUT /api/policies/{id}` and an
  **Edit** button in the policy drawer now rename and re-describe in place;
  everything hanging off the policy is untouched, and the change is audited as
  `policy.update`.

  The create form also gained the **Description** field. The API had accepted one
  since the beginning and the form never sent it, so every policy read "No
  description" permanently — there was no later opportunity to set it.

- **A policy's checks and tasks can be changed from the policy.** The drawer
  listed them as plain rows, with the enabled toggle wired to an empty handler
  and rendered `disabled`, so the only way to change anything a policy contained
  was to leave, find it again on the Checks or Scheduled tasks screen, and edit
  it there. Each row now has **Edit**, tasks can be enabled and disabled in place
  (behind the same confirmation the tasks screen uses), and each tab can add a
  new one with the policy pre-selected as its target.

  The forms are the ones those screens already use, reached through `window`
  because the build gives each source file its own block scope — a second copy
  would have drifted from the first.

- **Users can be disabled instead of deleted.** Deletion cascades — sessions,
  scopes, API tokens, notification subscriptions all go with it — which is right
  for an account created by mistake and wrong for a person who left, where the
  account must stop working while the record of what it did survives.

  Disabling ends live sessions immediately (a disable that leaves an open tab
  working is not a disable), stops API tokens bound to that user, stops alert
  mail, and refuses a password reset — otherwise a way back in. Unbound API
  tokens are unaffected. It records **when** access was withdrawn and re-disabling
  keeps the original timestamp, because that is the value an incident review
  reads. You cannot disable your own account or the built-in admin, the same two
  guards the role and delete paths already carry.

- **Software reports include Manufacturer.** Agents now collect the Windows
  Publisher, Linux package Maintainer/Vendor, or macOS signing identity/source;
  Reports → Software exposes it in the table, search, column picker, CSV, REST,
  and AI inventory output. Existing inventory remains compatible and shows a
  blank value until the next agent inventory refresh.

- **Scripts can be written in Python.** `python` joins bash, sh, powershell and
  cmd everywhere a shell is chosen — the script library, the run-on-device form,
  bulk deployment, scheduled tasks, script checks and the AI tools — and the
  console highlights it as its own language.

  The agent stages the script as an `openrmm-script-*.py` file (mode 0600,
  removed when the run ends, including on timeout or cancel) rather than passing
  it with `-c`: a traceback then names a file and a line instead of `<string>`,
  Windows' ~32k command-line cap stops mattering, and a script full of quotes
  and newlines stops being a quoting problem. The interpreter is resolved at run
  time — `python3`, then `python`, then `py -3` on Windows — with `python3`
  first because on a machine with both, `python` may still be Python 2, under
  which a Python 3 script fails in ways that read like a syntax error in the
  operator's code. A device with no interpreter exits **127** and names what was
  tried, which is deliberately distinguishable from a script that failed.

  Python needs agent **0.4.9 or later**. Older agents answer "unsupported shell"
  only after dispatch, so the server refuses the job up front and names the
  version to install. A bulk deployment naming any outdated device is refused
  whole, listing the devices, rather than creating jobs for part of a selection
  behind an approval granted for all of it.

  The audit trail records which interpreter ran, on the way out
  (`job.propose`, `job.automated`, `job.batch.propose`) and on the way back
  (`job.result`, alongside the exit code) — so "which devices have no Python"
  is answerable from the log alone rather than by joining back to the job row.

- **Alerts can leave through their own SMTP transport.** There used to be one
  mail configuration, so monitoring mail and password resets left the same
  server under the same sender. **Users & Settings → Email** now has a tab per
  transport: the account one carries resets, the alerts one carries
  notifications. Fill in only a From address and alerts go out as `alerts@`
  through the same relay; fill in a host and they move to a separate relay
  entirely. Leave it empty and nothing changes, which is what an upgrade does.

  When the alerts profile names its own host, **nothing is inherited** — the
  account profile's username and password are not carried to a server the
  operator named separately, and an alerts profile with no host stores no
  password at all. If the alerts relay fails, alerts retry there and are never
  rerouted through the account transport: a silent fallback would send mail by a
  path the operator chose not to use, and hide that the relay is broken. Each
  tab's **Send test** exercises its own transport.

### Changed

- **Passwords now require an uppercase letter and a symbol**, alongside the
  existing 12-character minimum. A symbol is anything that is not a letter, a
  digit or a space — defined by exclusion so accented and non-Latin keyboards are
  accepted rather than rejected by an ASCII allowlist.

  The error names the specific failure ("needs an uppercase letter") instead of
  restating the whole policy and leaving the operator to guess which part they
  missed, and the create-user form says what is still missing rather than showing
  a greyed-out button with no explanation — which is what prompted this.

- The agent capability gates share one version comparison
  (`models.AgentVersionAtLeast`), so credential rotation and the Python floor
  cannot drift on what a missing or malformed version means — it means "too
  old", which is the only safe reading of a string the endpoint reports about
  itself.

### Observability

- **The notification outbox is now visible.** `/metrics` gained
  `rmm_notifications_pending`, `rmm_notifications_failed` and
  `rmm_notifications_oldest_pending_seconds`. Alert delivery failing is the
  failure that hides every other failure — it is noticed by not being told about
  anything — and until now nothing exposed it. A healthy outbox drains within a
  worker tick, so alert on the age gauge.
- **The worker speaks when delivery breaks.** The *first* failed send is logged
  with the transport host, attempt number and retry delay, instead of only the
  fifth — which meant ~30 minutes of backoff with nothing in the log. Repeats of
  the same error stay quiet so an outage does not flood it. A transport that
  cannot deliver at all now logs once, with a reason that distinguishes "nothing
  configured" from a half-filled profile (a host with no sender, say) that would
  previously have stopped every alert in complete silence; recovery is logged
  too. Log lines name the relay, which matters now that there are two.

### Tests / CI

- **DB-backed tests in `internal/settings` and `internal/api` were never running
  in CI.** The "Database migrations" workflow is the only job with a Postgres
  service, and both its path filter and its package list named `internal/store`
  alone — so an integration test added anywhere else passed locally and ran
  nowhere. Both now include `internal/settings` and `internal/api`. The
  workflow's own comment warned against an allowlist of test *names* for exactly
  this reason; the package list had the same flaw.
- **The worker's log decisions are now tested, and one was wrong.** `drain` keyed
  its "first failure" line on the message's attempt count, so a single outage —
  which hits every queued message at once — would have logged one line per
  message, the flood the suppression exists to prevent. It is keyed on the error
  instead, and reset after a successful send so a later identical outage still
  speaks. Found by the test, not by review.
- `internal/notify` joined the DB-backed CI job; `drain` went from 0% to 70.8%,
  the package from 25.5% to 45.5%.
- A test pins the console's copy of the default role permissions against the Go
  catalog. That copy had already drifted once — `users.email.manage` was added to
  the backend and not the mirror, hiding the address editor from administrators
  who held the permission.
- The SMTP endpoints gained end-to-end coverage through the real router with
  real session tokens: each route writes its own profile and leaves the other
  untouched, `settings.manage` guards all six, a scoped principal cannot edit
  instance-wide mail config, passwords never appear in a response or an audit
  detail, and `users.email.manage` is genuinely required by the address route.
  Both key assertions were verified by mutation — breaking the route binding and
  removing the permission middleware each make them fail.

### Security

- **A user's email address is now administrator-only to change.** It is where a
  password reset link is delivered, so it is an authentication factor, not a
  contact detail — and it used to be self-service, which meant a user could
  redirect their own reset, and anyone with `users.manage` could redirect
  someone else's and take over an account that had no second factor.

  Setting it now requires the new **`users.email.manage`** permission, granted
  to `admin` and deliberately not implied by `users.manage`, so a delegated user
  manager can still create and scope technicians. `PUT /api/me/notifications` no
  longer accepts an address and rejects an attempt to change one; choosing
  *which* events you receive stays self-service. `POST /api/users` requires the
  permission when an address is supplied.

## [0.4.8] - 2026-07-24

### Changed
- **Windows installer no longer depends on OpenSSL.** `install.ps1` now verifies
  Ed25519 signatures with a small managed verifier embedded in the script and
  compiled in-process via `Add-Type` (SHA-256 uses .NET directly). This removes
  the per-endpoint OpenSSL dependency introduced in 0.4.7 — nothing extra to
  install or keep patched on the fleet — while preserving the security property
  that verification is done by code independent of the untrusted download (the
  agent binary never self-verifies). The `-OpenSslMsiUrl`/`-OpenSslMsiSha256`
  parameters and the OpenSSL auto-provisioning are removed. Linux/macOS keep
  using the system `openssl`, which is always present there.

### Security / Tests
- The embedded verifier is cross-checked against Go's `crypto/ed25519` in CI on
  both PowerShell editions (7 and Windows PowerShell 5.1) — genuine signatures
  must verify, tampered ones (bad signature, message, key, or non-canonical `S`)
  must be rejected (`scripts/test-installer-verifier.ps1`). A fail-closed startup
  self-test using an RFC 8032 vector aborts the install if the verifier ever
  misbehaves.

## [0.4.7] - 2026-07-24

### Changed
- **Windows installer auto-provisions OpenSSL.** `install.ps1` needs OpenSSL 3.x
  for Ed25519 signature verification (a verifier independent of the untrusted
  download — the agent binary must not self-verify). Instead of failing when it
  is absent, the installer now provisions it automatically: it probes `PATH` and
  common locations (including Git for Windows' bundled `openssl`), then tries
  Chocolatey, then winget, then an MSI via `msiexec` (the only path that also
  works under `SYSTEM`). The MSI source prefers, in order, `-OpenSslMsiUrl`, then
  the server's own `<server>/dl/openssl3-win64.msi` (drop an OpenSSL 3 MSI there
  for a durable, same-origin source that works offline/under SYSTEM), then a
  best-effort public URL. `-OpenSslMsiSha256` integrity-checks the download
  (via `Get-FileHash`) before `msiexec` runs.

## [0.4.6] - 2026-07-23

First published signed release. v0.4.5 was tagged but never published: the
release workflow's Intel macOS sidecar job queued on a retiring `macos-13`
runner for ~24h and was cancelled, so the `publish` job never ran and `/dl/`
kept serving unsigned binaries. This release ships the signed artifacts.

### Security / Fixed
- **Zero fixable HIGH/CRITICAL CVEs in the images.** Bumped the Go build image
  to `golang:1.26.5-alpine` (clears 14 stdlib CVEs in the server image) and the
  frontend base to `nginx:1.29-alpine`, and run `apk upgrade --no-cache` in both
  the server and frontend stages to pull Alpine's latest patched packages
  (OpenSSL, libexpat, libpng, c-ares, curl, nghttp2, libxml2 — 37 fixable CVEs,
  2 CRITICAL). Trivy now reports 0 HIGH/CRITICAL on both images.
- **govulncheck clean.** Bumped `golang.org/x/text` 0.38.0 → 0.39.0 to fix
  GO-2026-5970 (infinite loop on invalid input).

### Changed
- **Release pipeline unblocked.** Dropped the Intel (amd64) macOS sidecar from
  the release matrix so `publish` no longer blocks on a retiring `macos-13`
  runner, and added a 30m job timeout. The release ships Linux, Windows and
  mac-arm64 artifacts. **Intel Macs receive no updated native sidecar** until the
  darwin capture path is migrated to ScreenCaptureKit (so amd64 can build on
  `macos-14`) — tracked in docs/19 Phase 2.

## [0.4.5] - 2026-07-21

### Security / Added
- **Rotatable update key set (keyring).** The agent now trusts a *set* of Ed25519
  update keys and accepts an update signed by any of them. This enables staged
  signing-key rotation (pin new alongside old → switch signing → drop old) with
  **no fleet reinstall**. Installers accept several `--update-public-key` values
  and verify the download under any pinned key.
- **Release signing behind a protected GitHub Environment.** The `publish` job now
  declares `environment: release`.

### Added
- **Observability:** `/readyz` readiness probe (DB ping, 503 when down; the prod
  container healthcheck now targets it) and a token-gated `/metrics` endpoint
  (disabled unless `RMM_METRICS_TOKEN` is set) exposing DB-pool, online-agent and
  goroutine gauges — no new dependency.
- **CI container scan:** a blocking `container-scan` job (Trivy) that fails on
  fixable HIGH/CRITICAL CVEs in the server and frontend images.
- **License:** GNU AGPL-3.0 (`LICENSE`), stated in the README and `package.json`.
- **Docs:** signed-update runbook + staged key rotation (`docs/18`), production
  readiness plan (`docs/19`).

### Fixed
- Console showed disconnected devices as **online**: the scheduler sweep now
  reconciles stale `online` rows to `offline` (`MarkAgentOffline`).
- **Blank Audit log** screen: export `ACTION_META` through the global and guard a
  null actor before `startsWith`.
- Release/installers rejected **OpenSSL 4**; accept "OpenSSL 3 or newer" (Ed25519
  works in both). Cleared a shellcheck SC2015 finding in `install.sh`.
- macOS sidecar build under Go 1.25 cgo: wrapped the variadic
  `CGEventCreateScrollWheelEvent` and moved NULL handling for opaque
  CoreGraphics/CoreFoundation types into C shims.
- Intel macOS sidecar: the macOS 15 SDK removed `CGDisplayCreateImage`, so the
  amd64 build is pinned to the `macos-13` runner (the symbol still ships at
  runtime). Long-term fix: migrate the darwin capture path to ScreenCaptureKit.

### Notes
- The reported remote-access / terminal / file-transfer failure was **not a code
  bug**: `/dl/` served unsigned binaries (no `.sig`), so strict-mode agents
  rejected the sidecar download. Publishing signed artifacts to `/dl/` resolves it.
