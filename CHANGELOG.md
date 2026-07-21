# Changelog

All notable changes to OpenRMM are documented here. Format loosely follows
[Keep a Changelog](https://keepachangelog.com/); versions track the agent/release
tag.

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
