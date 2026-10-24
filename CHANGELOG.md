# Changelog

All notable changes to OpenRMM are documented here. Format loosely follows
[Keep a Changelog](https://keepachangelog.com/); versions track the agent/release
tag.

## [Unreleased]

## [0.7.2] - 2026-10-24

### Security

- **A managed console's owner can no longer step around single sign-on before
  first using it.** An account only became single sign-on only once its first
  single sign-on linked it. Until then it kept a password sign-in and "Forgot
  password", which no provider second factor guards: the first OpenRMM Cloud
  customer set a password through "Forgot password" and signed in without the
  portal or its two-factor authentication.
  - An account can now be marked single sign-on only before its first sign-in
    (`PUT /api/users/{id}/sso`, `users.manage`, audited as `user.sso_required`;
    migration 022). A marked account is treated exactly like a linked one:
    password sign-in is refused with `sso_required` and password reset sends
    nothing, and marking ends the account's sessions. OpenRMM Cloud's
    provisioning marks the owner's account as soon as it creates it.
    `RMM_OIDC_ENABLED=false` is still the break-glass.
  - The login screen asks who, then how: `POST /api/auth/method` answers `sso`
    for an account that signs in through single sign-on, and the screen sends
    it to the provider by itself. Everyone else, an unknown name included, gets
    the password field. The separate button stays by default, for a local
    account's first single sign-on; `RMM_OIDC_SHOW_BUTTON=false` hides it (Cloud
    consoles), and automatic accounts always show it.

## [0.7.1] - 2026-10-24

### Fixed

- **Guest access to a Mac no longer loops.** Every remote session on a Mac
  went through one LaunchAgent job, restarted for each new session, which
  killed the session already running. A guest's session therefore ended the
  technician's session it depends on, which ended the guest's, and the two
  kept replacing each other; two technicians on one Mac did the same. Each
  session now runs as a launchd job of its own, and jobs of ended sessions are
  removed when the next one starts. Privacy grants are unaffected.

## [0.7.0] - 2026-10-24

### Security

- **`rmm-sign` no longer prints the private keys.** The `-key` and `-key-p256`
  flags took their defaults from `RMM_SIGNING_KEY` and `RMM_SIGNING_KEY_P256`,
  and the flag package prints defaults in `-h` and in every usage error. With
  a key loaded for signing, a mistyped flag put it on the terminal or in a
  build log. The keys are now read from the environment after parsing, only by
  the operation that signs; a test runs help, bad flags and bad keys with
  stand-in keys in the environment and fails if one shows up.
- **The installer and the uninstaller no longer run a planted `rmm-agent` or
  planted tools as root.** On a Mac, `/usr/local/bin` usually belongs to the
  account that owns Homebrew, and `sudo` keeps the caller's PATH, which puts
  it first.
  - `install.sh` ran `/usr/local/bin/rmm-agent -print-version` whenever no agent
    was at the new location, even on a fresh install. It now treats that file
    as an install to migrate only when the LaunchDaemon in
    `/Library/LaunchDaemons` runs it, and otherwise leaves it alone.
  - `uninstall.sh` ran `-action stop` and `-action uninstall` on both agent
    locations. It now stops the service with `launchctl` or `systemctl` and
    never runs the agent.
  - `uninstall.sh` takes its tools only from the system's directories, as
    `install.sh` now does.
  - Found by an independent review; no exploitation is known.

- **Built with Go 1.26.10.** Server, agents, the remote sidecar on Linux and
  Windows, and the images move from Go 1.26.9. It distrusts compromised
  intermediate certificates in `crypto/x509` (GO-2026-6809), which govulncheck
  traced to the alert-mail client's TLS connection to its SMTP relay.
- **`github.com/go-jose/go-jose/v4` 4.1.6** (GHSA-mgcc-cww5-fp2q). The single
  sign-on verifier reads the provider's signing keys through it, and it now
  refuses RSA keys larger than 4096 bits.

### Fixed

- **Agents install on a stock macOS and on Linux with OpenSSL 1.1.**
  `install.sh` checked the Ed25519 signatures with `openssl pkeyutl -rawin`,
  which only OpenSSL 3 has, so a production install was refused on every Mac
  without Homebrew's OpenSSL (macOS ships LibreSSL) and on Ubuntu 20.04 and
  RHEL 8. CI never saw it, because its macOS job installs OpenSSL 3 first.
  - Each release now signs `SHA256SUMS` a second time with ECDSA P-256
    (`SHA256SUMS.p256.sig`), which any `openssl dgst -verify` can check.
  - Without OpenSSL 3, `install.sh` checks that signature with the key in the
    new `--manifest-public-key` flag, then the agent's SHA-256 in the manifest.
    The agent, now proven to be the published one, then checks its own Ed25519
    signature under the pinned update key (`-verify-own-signature`).
  - `install.sh` now takes its tools only from the system's directories
    (`/usr/sbin:/usr/bin:/sbin:/bin`). `sudo` on macOS keeps the caller's
    PATH, where Homebrew's directories, owned by an ordinary account, usually
    come first, so root could run an `openssl` or `curl` planted there. As a
    result every Mac installs through the P-256 signature, Homebrew's OpenSSL 3
    included: **a server that enrols Macs needs `RMM_MANIFEST_PUBLIC_KEY`**,
    which deploy/QUICKSTART.md gives for the published release.
  - The server publishes `RMM_MANIFEST_PUBLIC_KEY` and the console adds the
    flag to the Linux and macOS install commands. Updates are unchanged: the
    agent still pins only the Ed25519 keys and checks every update in Go.
  - CI now runs signed installs on macOS with the system's LibreSSL and
    `/bin/bash`, and on Linux with a stand-in for OpenSSL 1.1.
  - Releases need a new key pair, `RMM_SIGNING_KEY_P256` and
    `RMM_MANIFEST_PUBLIC_KEY` (docs/18-update-signing-runbook.md).
- **macOS: the remote module now appears in Privacy & Security ▸
  Accessibility.** It asked through `CGRequestPostEventAccess`. On macOS 15.8
  that puts a consent sheet on screen, but the app never shows up in the list
  the sheet sends the person to, so it had to be added by hand. It now asks
  through `AXIsProcessTrustedWithOptions`, which adds it to the list. Either
  grant enables mouse and keyboard. Sidecar 0.17.2.
- **WAL archiving works under `deploy/docker-compose.prod.yml`.** The
  `rmm_pgwal` volume was created by Docker as root, and postgres runs as uid
  70, so `archive_command` failed on every segment with `Permission denied`.
  Nothing was archived, so point-in-time recovery had nothing to replay, and
  PostgreSQL kept every unarchived segment in `pg_wal`, which grew until the
  disk filled. A one-shot `postgres-wal-init` service now hands the volume to
  postgres before it starts. It runs as root with only `CHOWN`, no network and
  a read-only filesystem. On an existing install, the next `up -d` fixes the
  volume, and postgres then archives the backlog from `pg_wal` and frees it.
  Each segment is copied to the archive before a checkpoint frees it in
  `pg_wal`, so on a nearly full disk make room first. `ship-wal.sh` prunes
  the archive as before. CI now checks that segments
  reach the archive and none fails. Self-hosted Compose installs only;
  OpenRMM Cloud does not use this file.
- **The restore helpers run the stack's pinned postgres.** `restore-pitr.sh`,
  `standby-bootstrap.sh` and `drill-restore.sh` looked up the postgres image
  with a pattern that never matched `docker compose config`, or did not look
  it up at all, so they pulled an unpinned `postgres:16-alpine`. They now take
  the image Compose resolves, digest included, and stop if it cannot be
  resolved.
- **Notices at the top of a remote session are readable again.** Their
  background used an opacity on a palette colour, which Tailwind leaves out
  when the colour is a CSS variable. They were see-through over the black
  viewer, and with the light theme their dark text disappeared in the black
  band of *Fit to window*. They now have a solid background.

### Added

- **Guest access to a remote session.** A technician can invite someone without
  an account to see a device's screen, or control it, for 15 minutes to 24
  hours (Device ▸ Remote ▸ Share access…). The invitation is a link and a
  six-digit code sent separately; the first correct code burns the link, and
  five wrong ones revoke it. By default a guest who closed the page may use the
  same link and code again until the invitation expires; each time issues a new
  credential, ends the previous one and waits for the technician again. "Allow
  only one use" makes the link single-use. The guest then waits until the technician who
  invited them accepts them from the top of their own open session on the
  device, and leaves when that session ends. The guest gets a session of their
  own, and the relay filters every frame of it: screen and monitor choice, plus
  mouse and keyboard in control mode, and never files, clipboard or the
  shortcuts. The technician, the guests and the person at the machine share one
  chat: the server carries lines between the technician's session and the
  guests'. Sidecar 0.18 opens no second chat window for a guest's session, and
  shows a guest's lines under their name in a colour of their own. Off
  by default (Settings ▸ Remote access ▸ Guest access); the new
  `devices.remote.share` permission is held only by the administrator role
  until a custom role opts in. Migrations 020 and 021 add `remote_shares`. See
  docs/08-remote-access.md.
- **The chat names the person at the machine by their account** (sidecar
  0.18): in the device's chat window instead of "You", and in the console
  instead of the machine's name.
- **Self-hosting from the published images.** `deploy/docker-compose.images.yml`
  runs the release images from `ghcr.io/openrmm-project`, pinned by version and
  digest, on top of the production Compose file. It fills the agents door's
  `/dl` from the `openrmm-artifacts` image, whose agents are signed with the
  release key. `deploy/docker-compose.caddy.yml` and `deploy/Caddyfile` add
  Caddy as an optional TLS edge with Let's Encrypt certificates, with
  source-range limits for the console and MCP. `deploy/QUICKSTART.md` walks
  through it. CI brings the stack up from the published images on every
  deployment change and checks all three doors, down to a first-admin bootstrap.
- **docs.openrmm.io.** The guides in `docs/`, the self-hosting and production
  guides, `SECURITY.md`, `CONTRIBUTING.md` and this changelog are published as
  a documentation site with search, a sidebar by topic, and an edit link to
  each page's source file. The files stay the source. `website/` holds only
  the site's configuration and the script that copies the files in. A merge
  to `main` that touches a guide republishes the site. A pull request builds it
  and fails on a broken link.

### Dependencies

- `github.com/go-chi/chi/v5` 5.5.0.

## [0.6.2] - 2026-10-15

### Added

- **Checks and Tasks tabs on a device.** The device drawer had no way to see
  what monitors or runs on the machine: the Checks and Scheduled tasks screens
  list definitions, and a policy check shows its policy rather than the devices
  it reaches. Two tabs next to Jobs now list the enabled checks and tasks that
  apply to the device — assigned to it or inherited from a policy on its
  department, site or client, the set the scheduler runs — with each check's
  state on this machine and each task's last run here. Backed by
  `GET /api/devices/{id}/checks` and `/tasks` (`checks.view` / `tasks.view`,
  scoped like every device route).
- **Assign a policy to a department from the console.** The API and the
  scheduler always accepted a department (`scope_type: group`), but the policy
  drawer offered clients, sites and devices only, and listed a department
  assignment made through the API by its bare id. The picker now has a
  Departments group, and a department reads as its name with its site — two
  sites may each have a "Finance".

### Changed

- **The Email settings tab says it is for alerts.** It configures only the
  alert-mail relay — password resets are the operator's — but its headings
  ("Email", "Mail server", "Sender & credentials") read like the instance's
  mail settings. It is now *Alert email*, with *Alert mail server*, *Alert
  sender and SMTP login* and *Test an alert*, in all six languages.

### Fixed

- **The assistant can propose disk and CPU checks.** `propose_check` offered the
  types `disk_space` and `cpu_load`, but the server accepts only `diskspace` and
  `cpuload`, so every disk or CPU check the AI or an MCP client proposed was
  refused as an unsupported type. The tool now offers the server's names, still
  accepts the old ones, and a test fails if it offers a type the server refuses.
- **Settings ▸ Configuration shows the real server version.** It read "0.2.1"
  on every release since that number was typed into the handler. The version is
  now stamped into the binary at build time (`-X main.version`): the image
  workflow passes the tag's version as the Dockerfile's `VERSION` build arg,
  `make server` uses `VERSION`, and an unstamped build says `dev`.
  `rmm-server -version` prints it.
- **One screen failing no longer blanks the console.** A screen that threw
  while rendering unmounted the whole application, leaving a white page and no
  navigation. Each screen, and each Settings tab, now fails on its own with a
  message and a Retry, and the rest of the console keeps working.
- **The assistant can hand over the complete inventory.** `inventory_report`
  told the model to call `export_inventory` for the full data, and the server
  could run it, but the tool was never declared in the catalog — so neither the
  chat nor an MCP client could call it, and a large report ended in a tool the
  model did not have. It is now offered to every caller with `reports.view`, and a
  test fails if `Execute` handles a tool the catalog does not declare.

### Dependencies

- `golang.org/x/crypto` 0.58.0, `golang.org/x/sys` 0.49.0, `golang.org/x/text`
  0.43.0 and `github.com/go-chi/httprate` 0.16.1.
- The macOS remote module is the 0.6.1 build, unchanged (sidecar 0.17.1): its
  source did not change, and keeping the same signed bundle keeps the Screen
  Recording and Accessibility grants on every Mac.

## [0.6.1] - 2026-10-13

### Security

- **Built with Go 1.26.9.** Server, agents, the remote sidecar on Linux and
  Windows, and the images move from Go 1.26.6, clearing eleven
  standard-library advisories reported by govulncheck (net/http, net/textproto,
  crypto/tls, html/template). None was found reachable from outside — the
  server speaks plain HTTP/1.1 behind its proxy — but every binary carried them.
- **A single sign-on link only signs in the browser that started it.** The
  provider callback used to hand the console a bearer token in the address
  fragment, so a copied or phished callback link signed in whoever opened it.
  It now carries a single-use code, valid 60 seconds, that is exchanged only by
  the browser holding the HttpOnly binding cookie set when sign-in started
  (migration 019). Deploy the API and the console together.
- **Standard users on a Windows endpoint can no longer read the agent's
  token.** The installer gave `BUILTIN\Users` read access to
  `C:\ProgramData\rmm-agent`, and `state.json` — the agent's bearer token — and
  the result outboxes inherited it, so any local user could impersonate the
  device to the server. The directory now grants SYSTEM and Administrators
  only. The agent re-applies that ACL on every start, so endpoints that update
  themselves are repaired without running the installer again.
- **A custom role with `users.manage` can no longer act on stronger accounts.**
  An unscoped caller skipped the permission comparison entirely and could change
  an administrator's email, reset their second factor and take the account
  over. Nobody manages an account that holds a permission they lack. Requesting
  another user's email confirmation now goes through the same check.
- **Single sign-on no longer claims a local account through a fallback claim.**
  When the configured username claim was missing, the username came from
  `preferred_username` or `sub`, which a user may choose at the provider, and a
  match adopted the local account of that name — `admin` included. A fallback
  username can still name a new account, never an existing one.
- **A two-factor code works once.** A code was valid for its whole ~90-second
  window, so one seen over a shoulder or relayed by a phishing page signed in
  again. The last accepted time step is stored per user (migration 018).
- **Approve and reject racing on one job have a single winner.** Both used to
  succeed, leaving the job recorded as rejected and sent to the device anyway.
- **CSV exports neutralise spreadsheet formulas** in cells that endpoints
  report (hostnames, software names), which otherwise ran when a technician
  opened the export in Excel.
- **The production compose takes the client address from the edge only.**
  nginx recorded the edge's address for every client, so a few failed sign-ins
  anywhere locked everyone out and `RMM_MCP_ALLOWED_IPS` saw only the edge. It
  now trusts `X-Forwarded-For` from the pinned edge network (`EDGE_SUBNET`) or
  `OPENRMM_TRUSTED_PROXY`. Pinning the network recreates it: run
  `docker compose down` before `up -d` when upgrading.

### Fixed

- **Alert mail is delivered again.** Monitoring alerts queued no mail since
  0.6.0: the recipient query returned three columns and read two.
- **A job runs at most once on the device.** The server re-sends a job still
  marked sent on every reconnect; the agent now recognises one it already ran
  or is running, and a job interrupted by an agent restart is reported as
  failed instead of being repeated.
- **Two-factor QR codes scan in every authenticator.** The console drew them
  without a quiet zone, and some apps (Aegis among them) could not read them;
  the QR library was replaced as well.

## [0.6.0] - 2026-09-29

### Security

- **Revoking an MCP application now kills its tokens at once.** OAuth tokens
  carried no link to their application, so removing one would have left its
  tokens working until they expired — and there was no way to remove one at
  all. Revocation deletes the application's tokens, codes and SSO grants in one
  transaction, and authentication joins the application, so a token of a
  removed or unapproved application stops working immediately.
- **The consent page warns about unknown applications.** Anyone can register an
  application under any name; when it returns to a host that is neither a
  known MCP client nor on the administrator's list, the page says so before the
  user authorizes it.

- **The MCP OAuth consent page can no longer be framed** (`X-Frame-Options:
  DENY`, `frame-ancestors 'none'`), so its Authorize button cannot be hidden
  under another page and clicked by deception. It also names the host the
  client redirects to.

- **Accounts linked to single sign-on have no password path while SSO is on.**
  Password reset worked for them — by design — so whoever could read the
  mailbox could set a password and sign in around the provider's MFA,
  conditional access and offboarding. With `RMM_OIDC_ENABLED=true`, a linked
  account is now refused password sign-in (console and MCP OAuth form) with
  `sso_required`, gets no reset mail, and cannot redeem a link issued earlier.
  `RMM_OIDC_ENABLED=false` is the break-glass. Keep a local admin that is never
  used to sign in through the provider: the first SSO sign-in links it.
- **Reset mail is throttled per account** (one per five minutes, five a day).
  The per-IP limit alone let anyone flood one mailbox from many addresses
  through the operator's relay. Extra requests get the same 202 and send nothing.
- **The console's alerts relay must be a public address.** The server dialled
  whatever a settings administrator typed and showed back the answer, a way to
  probe its own network. Private, loopback, link-local and reserved addresses
  are refused on save and on the address actually dialled;
  `RMM_SMTP_ALERTS_ALLOW_PRIVATE=true` allows an internal relay.

- **Alert mail no longer rides the account (password-reset) transport.** With
  no alerts server of its own, the alerts profile used the account relay — and
  honoured a sender of the tenant's choosing. On a hosted instance that relay
  signs for the operator's domain, so any tenant administrator could set
  `billing@<operator domain>` and mail anyone as the operator. Alerts now leave
  only through a server set in Settings ▸ Email: with none, alert mail is off
  and nothing is queued; a sender or login without a host is refused on save
  (`alerts_need_own_server`); a sender stored that way before is ignored. The
  account transport's test button now mails only the caller's own address.
  **Upgrading:** an instance that relied on alerts sharing the account relay
  must enter that server in Settings ▸ Email, or alert mail stops.

- **The console's sign-in no longer accepts a pasted token.** Its "API token"
  tab took `RMM_ADMIN_TOKEN` or any API token, skipping the second factor and
  the SSO policy, recording the work as `admin` rather than as a person, and
  turning a leaked automation token into a console login. People now sign in
  with a password (and second factor) or SSO; tokens remain for the API and
  automation, which accept them exactly as before.

- **The first admin needs the operator's admin token.** `POST
  /api/auth/bootstrap` now requires `Authorization: Bearer $RMM_ADMIN_TOKEN`.
  Before, an empty instance belonged to whoever called it first — and a hosted
  instance is reachable before its owner knows the URL (Certificate
  Transparency logs, the load balancer answering by Host header). Scripts that
  created the first user anonymously must add the header.

### Fixed

- **The dashboard counted only what it had downloaded.** "Awaiting approval"
  and "Recent failures" were counted over the latest 100 jobs, so a job
  waiting longer than that dropped out of the count, and "recent" had no
  window. The console also downloaded every device and those jobs, with
  their scripts and output, every few seconds just to count them. A new
  `GET /api/dashboard` counts in the database over what the caller may see
  — every waiting job, failures in the last 24 hours — and lists the newest
  waiting jobs without their content.
- **The dashboard's recent activity listed 200 entries.** It asked for 8 in a
  way the API client ignored; it now shows the latest 8, and the client
  refuses a malformed filter instead of dropping it.
- **Ten audit actions showed as raw identifiers in the console** (two-factor
  changes, reboot requests, remote sessions moved between tabs, MCP
  application approvals and revocations). They now read in every language,
  and a test ties every action the server records to a label.

- **A rejected password no longer burns the reset link.** The confirm step
  redeemed the token before checking the password, then said "the link is still
  valid, try again" — it was not. The password is now checked first.
- **Screens now see the server's error codes**, so "alert mail needs your own
  server" and "this account signs in with single sign-on" appear translated
  instead of as the server's English sentence.

- **Enrollment commands from the console work on HTTPS servers.** The install
  commands the console shows had no `--update-public-key` / `-UpdatePublicKey`,
  and both installers refuse a production install without one, so every
  enrollment copied from the console failed. The server now reads
  `RMM_UPDATE_PUBLIC_KEY` (base64; comma-separated during a rotation), refuses
  to start on a malformed one, and publishes it in `/api/config`; the console
  adds it to both commands and warns when an HTTPS server has none.
  `docker-compose.prod.yml` now requires it.

- **A root service no longer runs from a directory another account can own.**
  On Windows a standard user may create folders under `C:\ProgramData` and owns
  what they create, so pre-creating `C:\ProgramData\rmm-agent` left the
  directory of a SYSTEM service — and of the script an administrator runs to
  uninstall it — under their control. The installer now breaks ACL inheritance
  and writes the entries explicitly (SYSTEM and Administrators full control,
  Users read and execute), on every run, which also repairs endpoints installed
  earlier. On macOS the agent moved to `/usr/local/libexec/openrmm/`, because
  Homebrew gives `/usr/local` to the logged-in administrator; existing installs
  are migrated on the next run and the installer refuses a target directory that
  is not root-owned. The update signature is checked at download and says
  nothing about a binary swapped afterwards, which is what these directories
  allowed. The macOS remote-desktop bundle's parent directory is taken over the
  same way: it lives under `/Library/Application Support`, which is
  group-writable by administrators, and the bundle holds Screen Recording and
  Accessibility.

- **Linux and macOS can be uninstalled.** `deploy/public/uninstall.sh` (served at
  `/uninstall.sh`) stops and removes the service, the binaries, the enrolment
  state and, on macOS, the remote-desktop bundle, its LaunchAgent and the privacy
  grants. `-action uninstall` only ever removed the service definition, so a
  decommissioned endpoint kept credentials that still authenticated to the
  server. `--keep-state` preserves the enrolment. Removal is local and revokes
  nothing: the device still has to be deleted in the console.

- **macOS remote access stopped claiming a permission it did not have.** A
  privacy grant includes the bundle's code identity: after an ad-hoc-signed
  sidecar update the Privacy panes could still list OpenRMM as allowed while
  macOS denied capture and input to the new build. The archive now carries the
  sealed designated requirement when a persistent signing identity is used. The
  agent keeps grants only when that marker matches and otherwise clears the stale
  rows after a successful replacement, so System Settings can ask for the new
  build instead of showing a switch that no longer authorizes it.

- **macOS remote keyboard shortcuts use the endpoint's conventions.** Control is
  translated to Command for a Mac endpoint, so Ctrl+C from a Windows or Linux
  technician reaches the Mac as ⌘C. Paste releases locally-held modifiers,
  writes the Mac pasteboard synchronously and queues ⌘V with the rest of the
  input stream. The Force Quit button (the Mac equivalent of Ctrl+Alt+Delete)
  continues to send ⌘⌥Esc and reports stale Accessibility grants explicitly.
  Permission requests now run on AppKit's main thread, and a host-side alert
  links directly to Screen Recording and Accessibility when macOS suppresses
  its own consent sheet. The Force Quit button can retry after Accessibility is
  granted during an open session instead of staying disabled until reconnect.

- **The macOS sidecar's bundle identifier is `io.openrmm.remote`.** Reverse-DNS
  should name a domain the project controls, and `com.openrmm.remote` did not.
  Endpoints installed under the old name are migrated: the old LaunchAgent is
  booted out, its plist removed and its privacy rows reset. The agent also
  rewrites its LaunchAgent plist whenever it drifts, because agents update
  themselves and installers do not re-run. Sidecar 0.13.1.

- **A refused or failed macOS sidecar update no longer takes remote access with
  it.** When an update cannot be installed and a working module is present, the
  session uses the installed one and the update is retried later.

- **The Windows enrollment command had to be scrolled to be read.** It is a
  single line carrying the server URL and the token, and the code block scrolls
  long lines sideways rather than wrapping them — right for a script, wrong for a
  command an operator has to check before pasting it into an elevated PowerShell.
  The block now soft-wraps where it is used for install commands; what the Copy
  button puts on the clipboard is unchanged, because it copies the command rather
  than the rendering.

- **One remote desktop session per operator per device.** The same operator
  could open the same device's desktop in several tabs, each fighting the others
  for mouse and keyboard while the endpoint captured the screen once per tab.
  The server now refuses the second one (409 `remote_session_active`, and close
  code 4409 at the relay, so simultaneous tabs cannot both get in); the console
  offers **Continue here**, which moves the session and tells the old tab
  instead of letting it reconnect and take it back (4410, audited as
  `remote.replace`). Other operators on the same device, the same operator on
  other devices, and terminal or file-transfer sessions are unaffected.

- **The console smoke test no longer depends on the machine's language.**
  Headless Chrome took the language from the environment, so on a non-English
  machine every English assertion failed.

- **The OpenRMM logo shows on Windows and Linux.** The executables carried no
  icon, so Explorer, Task Manager and Programs and Features (whose entry points
  at the agent's icon) all showed Windows' blank one, and the chat window a host
  user sees had none either. `rmm-agent.exe` and `rmm-remote.exe` now embed the
  logo (`rsrc_windows_*.syso`), the Windows chat window uses it, and the Linux
  chat window sets `_NET_WM_ICON` from icons built into the sidecar.
  `make icons` regenerates all of them from the logo.

- **A leftover Windows service no longer blocks installing the agent.** An
  `rmm-agent` service whose program was gone — left by `sc.exe delete` while the
  Services console held it open — made every later `install.ps1` stop with
  "Service exists but its executable is missing", after a misleading "rolling
  back". The installer now removes an orphan only when it points at the expected
  agent path, refuses a service from another folder, and deletes a service
  created by a failed fresh install instead of leaving another orphan.

### Added

- **Alerts through the operator's shared service, to confirmed addresses
  only.** A hosted operator can deliver alert mail for instances that set no
  relay of their own (`RMM_ALERTS_SMTP_*`), with a fixed sender the console
  cannot change. Because a tenant administrator types the recipients in, an
  address receives nothing through the service until its owner confirms it by
  a single-use link (48 h; a changed address needs a new confirmation;
  throttled per address and per instance). Each instance also has an hourly
  budget (`RMM_ALERTS_HOURLY_LIMIT`), and every alert carries a footer naming
  the instance it came from. The console shows confirmation status on the
  users screen and in each user's notification settings, with a way to send
  the link again, and the email screen shows the shared service when the
  instance has no relay of its own. Migration `017_email_verification`.

- **MCP applications screen.** Settings ▸ API tokens lists every registered
  MCP application (hosts it returns to, status, active tokens, last use) with
  Approve and Revoke, and sets the policy: any application, or only those on
  allowed hosts, and optionally approval for new ones. The default stays open,
  without approval. New migration `016_oauth_app_controls`. Registrations,
  consent refused for lack of `mcp.use`, and rejected SSO grants (with the
  reason: replayed, carried to another client, expired) are audited;
  refusals before sign-in go to the server log.

- **Single sign-on on the MCP OAuth consent page.** Accounts linked to SSO
  have no password path while SSO is on, so they could not authorize MCP
  clients. The consent page now offers the provider: the sign-in goes through
  it with the same MFA and account rules as the console, and comes back with a
  five-minute, single-use grant bound to that client, redirect URI and PKCE
  challenge. New migration `015_oauth_sso`. Consent is now refused, with a
  message, to a role without `mcp.use` (either sign-in path) instead of issuing
  a token `/mcp` would refuse. The server warns at startup when SSO requires no
  MFA from the provider, since SSO accounts skip the console's own 2FA.

- **OIDC authentication-context policy.** `RMM_OIDC_ACR_VALUES` requests and
  validates acceptable signed `acr` values; `RMM_OIDC_REQUIRED_AMR` requires
  every configured authentication method before SSO account resolution. Includes
  downgrade/recovery-code tests and optional OIDC interoperability tests.
  Existing local sign-in and role assignment are unchanged.
  First-time linking of an email-shaped username now also requires the provider's
  matching, signed `email_verified=true` claim.

- **Security toolchain patch level.** Local builds, CI, releases and production
  images now converge on Go 1.26.6, including its standard-library security fixes.

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

- **macOS asks for the permissions it needs, and keeps the answers.** The sidecar
  never requested Screen Recording or Accessibility — it only checked them, and
  macOS neither prompts nor lists an app that does not ask, so every Mac needed
  someone to add it by hand. It now requests both, and the viewer explains what
  is missing instead of showing a black screen or a session that ignores clicks.
  The agent preserves privacy grants while the bundle keeps the same sealed code
  identity and clears stale rows when that identity changes. Docs cover what
  signing the sidecar yourself changes for a self-hosted install, and why
  official builds are not self-signed.

- **Paste into a remote session.** The technician's clipboard follows their own
  paste keystroke: the viewer catches the browser's `paste` event, which carries
  the text, so no clipboard-read permission is involved, and the sidecar applies
  the paste shortcut the endpoint's platform uses — ⌘V on macOS, Ctrl+V
  elsewhere. Forwarding a raw Ctrl+V pasted nothing on a Mac. Text only, 256 KB
  cap, and strictly one direction: the endpoint's clipboard is never read back.
  Sidecar 0.14.0.

- **`openrmm-artifacts` image on every release.** The release workflow packages
  the signed files it just published into `ghcr.io/<owner>/openrmm-artifacts`
  (`X.Y.Z`, `X.Y`, `latest`), which Kubernetes tenants serve at `/dl/`.

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

- **Accented characters were destroyed in script output.** `Olá, café!`
  reached the database as `Ol<?>, caf<?>!`. A redirected child on Windows
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
