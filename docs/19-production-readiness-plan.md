# 19 — Production Readiness Plan

A prioritized remediation plan to take OpenRMM from "security-hardened build" to a
production deployment. Items are grouped by priority. Each carries an **owner**
(code = changeable in this repo; ops = requires your infrastructure/secrets/prod
access) and an **acceptance** check.

Status legend: ✅ done · 🟡 in progress · ⬜ not started.

## Current state (2026-07-27) — resume here

Still a **development / localhost** deployment (`http://192.168.0.40:3005`, one
Windows test endpoint). Since the 07-21 snapshot below:

- ✅ Shipped through `v0.4.8`; the Windows installer verifies Ed25519 in-process
  and no longer needs OpenSSL (`CHANGELOG.md`).
- ✅ Remote desktop, file transfer and terminal **work** after the two fixes in
  0.2 — verified against the Windows endpoint, including an elevated ConPTY
  session under SYSTEM.
- ⚠️ **The signing key was rotated on 07-26 and custody is currently split:**
  `deploy/secrets/update-signing.key.env` holds the original pair
  (`Itu2LJ3p…`), while the GitHub secrets and the artifacts now served from
  `deploy/public/` use a newer pair (`yfWWMRQ…`). Reconcile to one pair before
  bootstrapping any fleet — nothing is pinned to the old key yet, so adopting
  the new one and updating the key file is the cheap path.
- ⚠️ `/dl/` is signed **from a workstation**, not by the release workflow. That is
  fine for the lab and not acceptable for production.
- ⛔ **GitHub Actions quota is exhausted** (jobs fail instantly with a billing
  annotation), so no signed release can be cut until billing is resolved. This
  blocks 0.1 entirely.

### Snapshot of 2026-07-21 (kept for history)

This is a **development / localhost** deployment (single node, no real device
fleet yet); production is a future goal. What landed this session:

- ✅ Signed-update pipeline built and its release-gate failures fixed; **GitHub
  secrets set** (`RMM_UPDATE_PUBLIC_KEY` at repo level, `RMM_SIGNING_KEY` in the
  `release` Environment). No approval reviewer yet (private repo, Free plan).
- ✅ Keyring rotation, `/readyz` + `/metrics`, container-scan CI gate, runtime bug
  fixes (offline reconcile, blank Audit log), AGPL-3.0 license. See `CHANGELOG.md`.
- 🟡 **Release `v0.4.5` was building** (run `29858165595`, tag → commit `64c90db`).
  If it hasn't finished: `gh run watch <id> --repo P1N-GU/openrmm`. The Intel
  (`macos-13`) sidecar was queued.

**Immediate next action after the release publishes** (the one thing that unblocks
remote access + auto-update locally):

```bash
gh release download v0.4.5 --repo P1N-GU/openrmm --dir deploy/public --clobber
# verify, then it is served at /dl/ (deploy/public is the prod bind-mount)
cd deploy/public && sha256sum -c SHA256SUMS
```

Then reinstall the test agent once with `--update-public-key <pub>` (the public key
is in `deploy/secrets/update-signing.key.env`, gitignored), or run it `--lab-mode`
for dev. **Do not lose `deploy/secrets/update-signing.key.env`** — it is the only
readable copy of the signing private key (GitHub secrets can't be read back).

## Phase 0 — Go-live blockers

### 0.1 Activate the signed-update chain — ops ⬜
See [18-update-signing-runbook](18-update-signing-runbook.md).

- Put `RMM_SIGNING_KEY` / `RMM_UPDATE_PUBLIC_KEY` in GitHub; create the protected
  `release` Environment with required reviewers.
- Cut a signed `v0.4.5`; publish binaries **plus `.sig`**, `SHA256SUMS` and
  `SHA256SUMS.sig` to the production `/dl/` (today it serves unsigned binaries).
- Bootstrap the fleet with `--update-public-key`; return
  `RMM_ALLOW_LEGACY_AGENT_RELAY=false`.
- **Acceptance:** devices report 0.4.5; agent logs `security: verified update
  signature`; a follow-up `v0.4.6` auto-updates within `RMM_UPDATE_INTERVAL`.

> **Note (2026-07-26): the earlier claim that remote access was fixed by 0.1 was
> wrong.** This plan previously stated that the remote-access / file-transfer /
> terminal failure was "not a code bug" and that publishing signed artifacts
> would resolve it. Investigation on 2026-07-26 disproved that: unsigned `/dl/`
> was a real gap, but it was not what broke remote access. Two independent code
> bugs were, both reproduced and fixed — see 0.2.

### 0.2 Runtime bug fixes — code ✅ (deploy pending)
- Stale online agents reconciled to offline (`scheduler` + `store.MarkAgentOffline`). ✅
- Blank Audit log fixed (`ACTION_META` global export + null-actor guard). ✅
- **Remote access: two real code bugs, both fixed 2026-07-26.** ✅ (not 0.1, as
  this plan previously claimed)
  1. **Relay handshake dropped every browser viewer.** The console sends the
     viewer ticket and auth token as extra `Sec-WebSocket-Protocol` entries
     alongside `rmm-v1`, but `handleRelayWS` upgraded with an `Upgrader` that
     declared no `Subprotocols`, so the 101 echoed none. RFC 6455 4.1 requires
     the client to fail the connection in that case: every viewer authenticated,
     upgraded, paired and was closed with 1006, taking the agent peer with it.
     Affected remote desktop, file transfer and terminal alike. The Go sidecar
     was immune (ticket in `Authorization`, no subprotocol offered), which is why
     the failure looked server-side and intermittent. Fixed with a relay-specific
     upgrader that negotiates `rmm-v1`, covered by a regression test.
  2. **CSP blocked the styles xterm.js injects.** With the relay fixed, the
     terminal connected and streamed shell output but rendered a blank black
     screen: xterm appends `<style>` elements at runtime (cell dimensions, theme,
     selection) and rewrites their `textContent` on resize, and `style-src-elem
     'self'` blocked them — the text was in the DOM and invisible. Hashes cannot
     cover dynamic content and xterm has no nonce API, so style elements now
     allow `'unsafe-inline'`; `script-src` stays `'self'`.
- **⚠️ `deploy/nginx.prod.conf` still carries the old CSP** in its console
  `add_header` and will reproduce bug 2 in production even with the fix above,
  because header and meta policies are enforced independently (the intersection
  wins). Apply the same `style-src-elem` change there before go-live.
- **Diagnosis note for the future:** a headless viewer speaking the relay
  protocol isolates backend from browser in minutes — it proved the agent,
  ConPTY, relay and input path were all correct while the console was still
  black. Reach for that before assuming the endpoint is at fault.
- **Acceptance:** after deploying the rebuilt server + frontend, a real host shows
  correct online/offline state and the Audit tab renders; remote desktop, file
  transfer and terminal all work over the production ingress.

### 0.3 Backup and restore — ops ⬜
- Automated Postgres dump **plus a tested restore** into a clean environment.
- Back up `RMM_SETTINGS_ENCRYPTION_KEY` **separately** — without it, DB-backed AI,
  MeshCentral and TOTP secrets are unrecoverable.
- **Acceptance:** a full restore from a dump works; the settings key is custodied
  off-host.

### 0.4 Edge smoke test — ops ⬜
- Valid TLS, source ACLs at the edge, backend ports on loopback (already supported
  by the prod Compose), enforcing CSP, secrets via `*_FILE`.
- **Acceptance:** backend ports are not directly reachable; a basic external scan
  is clean.

## Phase 1 — Production-grade

### 1.1 Observability — code ✅ (partial) / ⬜ (remainder)
- `/readyz` readiness probe (DB ping, 503 when down); prod container healthcheck
  now points at it. ✅
- Token-gated `/metrics` (Prometheus text, disabled unless `RMM_METRICS_TOKEN` is
  set): DB pool, online-agent and goroutine gauges, plus the notification outbox
  (`rmm_notifications_pending`, `_failed`, `_oldest_pending_seconds`). ✅
- **Remaining ⬜:** structured logging + log aggregation; a backup-failure alert;
  richer metrics (job queue depth, relay sessions) as needs emerge.

**Alert on the outbox first.** Alert delivery failing is the failure that hides
every other failure: you notice it by not being told about anything, which is to
say you do not notice it. A healthy outbox drains within a worker tick (30s), so
`rmm_notifications_oldest_pending_seconds > 600` means mail has stopped leaving,
whatever the cause — a dead relay, a rejected password, a half-filled alerts
profile. `rmm_notifications_failed` rising means messages were abandoned after
five attempts and are never coming back.
- **Acceptance:** a scraper collects `/metrics`; orchestrator gates routing on
  `/readyz`; backup failures alert.

### 1.2 HA / resilience — ops + code 🟡
**Decision: accept a single node.** Horizontal replication of the application
does not address the risks that actually matter here — every replica shares one
PostgreSQL, so it changes nothing about data loss or restore time, and most real
downtime is host- or database-level. The investment goes to the data layer
instead.

Landed (2026-07-27), all verified against throwaway clusters:
- ✅ Continuous WAL archiving (`archive_mode` + `archive_timeout`, default 300s)
  with `deploy/postgres/archive-wal.sh`. **RPO ≈ 5 minutes**, down from ~24h.
  Proven by destroying a cluster's data volume and recovering to a chosen instant
  — a later erroneous write and a `DROP TABLE` were correctly discarded.
- ✅ Physical base backups (`deploy/ops/basebackup.sh`) — required because a
  logical dump cannot replay WAL — plus offsite shipping and safe pruning
  (`ship-wal.sh`, which prunes against the oldest retained base backup).
- ✅ `deploy/ops/restore-pitr.sh` for recovery to a point in time.
- ✅ `deploy/ops/drill-restore.sh`: timed, isolated, appends to `drill.log`.
- ✅ Optional warm standby (`docker-compose.standby.yml` +
  `standby-bootstrap.sh`), verified streaming live and accepting writes after
  `pg_promote()`. Failover is manual on purpose — automatic promotion without
  fencing risks a split brain, which is worse than minutes of downtime.

Remaining ⬜:
- **Run the drill and write the numbers here** — "RTO ≈ X min, RPO ≈ Y min".
  Until that line exists this item is not done; the tooling only makes the number
  cheap to obtain.
- Schedule `basebackup.sh` (weekly) and `ship-wal.sh` (hourly) alongside the
  existing `openrmm-backup.timer`.

If a customer SLA ever forces multiple app replicas, the staged path is:
Postgres advisory lock around the scheduler tick (small, `scheduler.go` is one
30s ticker) → sticky session routing at the ingress so a relay pair lands on one
replica (config, not code) → a distributed hub behind the existing 7-method
interface in `internal/hub/hub.go` (medium; the hard part is connection presence
and the reconnect lease, not routing).

- **Acceptance:** RTO/RPO written above and validated by a drill.

### 1.3 Container vulnerability gate — code ✅
- CI `container-scan` builds both images and fails on fixable HIGH/CRITICAL CVEs
  (Trivy). ✅
- **Follow-up:** pin the Trivy image by `@sha256` digest to match the repo's
  action/image pinning policy.
- **Acceptance:** a fixable HIGH/CRITICAL CVE fails CI.

## Phase 2 — Defense in depth (backlog)

- **Script/terminal sandboxing** — they run as root/SYSTEM; at minimum reinforce
  approval/audit. code ⬜
- **End-to-end encryption** of native media (relay/edge are trusted today). code ⬜
- **Console 2FA card** — send the hardened password/current-code fields the API
  already implements. code ⬜
- **Key-rotation governance** — scheduled rotation (now possible without reinstall,
  see 18) + runbooks. ops ⬜

## Minimum viable production

Phase 0 complete + Phase 1.1 (basic observability) + an explicit HA decision
(accept single-node with a defined RTO, or invest in distribution). Advanced 1.2
and Phase 2 can follow the launch, weighed by risk.
