# Changelog

All notable changes to OpenRMM are documented here. Format loosely follows
[Keep a Changelog](https://keepachangelog.com/); versions track the agent/release
tag.

## [Unreleased]

### Added

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

### Changed

- The agent capability gates share one version comparison
  (`models.AgentVersionAtLeast`), so credential rotation and the Python floor
  cannot drift on what a missing or malformed version means — it means "too
  old", which is the only safe reading of a string the endpoint reports about
  itself.

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
