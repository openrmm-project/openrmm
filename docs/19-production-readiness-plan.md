# 19 — Production Readiness Plan

A prioritized remediation plan to take OpenRMM from "security-hardened build" to a
production deployment. Items are grouped by priority. Each carries an **owner**
(code = changeable in this repo; ops = requires your infrastructure/secrets/prod
access) and an **acceptance** check.

Status legend: ✅ done · 🟡 in progress · ⬜ not started.

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

> **This also fixes remote access.** The reported remote-access / file-transfer /
> terminal failure is **not a code bug**: `/dl/` currently serves 11 binaries with
> **zero `.sig`** files, so a strict-mode agent's sidecar download fails signature
> verification (`enforceArtifactSignature` → `.sig` returns HTTP 404) and the
> session never starts. Publishing signed artifacts (0.1) resolves it.

### 0.2 Runtime bug fixes — code ✅ (deploy pending)
- Stale online agents reconciled to offline (`scheduler` + `store.MarkAgentOffline`). ✅
- Blank Audit log fixed (`ACTION_META` global export + null-actor guard). ✅
- Remote access: root cause is 0.1, not code. ✅ (diagnosed)
- **Acceptance:** after deploying the rebuilt server + frontend, a real host shows
  correct online/offline state and the Audit tab renders; remote flows work once
  0.1 lands.

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
  set): DB pool, online-agent and goroutine gauges. ✅
- **Remaining ⬜:** structured logging + log aggregation; a backup-failure alert;
  richer metrics (job queue depth, relay sessions) as needs emerge.
- **Acceptance:** a scraper collects `/metrics`; orchestrator gates routing on
  `/readyz`; backup failures alert.

### 1.2 HA / resilience — ops + code ⬜ (the biggest gap)
Today: one server replica; the in-memory hub, relay and scheduler are not
distributed.
- Short term (ops): supervision + auto-restart; **document and test RTO/RPO** for a
  single node.
- Medium term (code, large): externalize hub/scheduler state to allow multiple
  replicas — only if the SLA requires it.
- **Acceptance:** RTO/RPO defined and validated by a restore drill.

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
