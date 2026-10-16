# 00 — Overview

## Vision

A self-hosted RMM where the primary interface is a **conversation with an AI**
that can manage fleets of computers: inspect them, deploy software, configure
devices, run scripts, and generate reports — all in natural language, with a
human in the loop for anything that changes an endpoint.

What it stands on:

- **Its own agent and remote access**: one cross-platform Go agent and a built-in
  remote-desktop engine, with MeshCentral available as an alternative provider.
- **Modern UI**: an API-first backend with a bundled React console.
- **AI-native**: a chat + tool-calling engine is a first-class subsystem.
- **BYO-model**: connect any OpenAI-compatible API, including local models, so
  command data never has to leave your infrastructure.

## Positioning

Most AI-copilot RMMs on the market (Atera, SuperOps, Acronis RMM, Neo Agent)
are **closed SaaS**, and open-source, self-hosted ones with built-in AI are still
few. This project's take is *a modern RMM, AI-native and privacy-first*: any
OpenAI-compatible model, including a local one, and a human approval in front of
every action the AI proposes.

## What is built (this repo)

As of v0.6.2, verified end-to-end against PostgreSQL:

- Agent enrollment + persistent WebSocket + heartbeat + inventory.
- Job lifecycle: propose → **human approval** → dispatch → result, fully audited.
- AI chat engine with a tool-calling loop and read/propose tools; reports render
  in chat with a client-side **CSV download**, never written to the endpoint.
- Pluggable remote-access layer: **native** (relay + built-in media engine),
  **files** (file transfer without screen capture), **terminal** (an elevated,
  in-process shell), **MeshCentral**, and optional **RustDesk** deep links.
  The native sidecar does screen capture + input over a tile-delta codec on
  **Windows** (GDI; chat, multi-monitor, UAC, logoff survival, a Ctrl+Alt+Delete
  button), **Linux/Xorg** (X11 capture via MIT-SHM + uinput input, with
  seat-following for the login screen) and **macOS** (CGO
  capture and input in a signed `.app` bundle; asks for Screen Recording and
  Accessibility one at a time). **File transfer**, in-session **chat** and the
  **clipboard in both directions** work on all three, and the operator can pick
  which display to stream. A session can **lock the host's physical
  keyboard/mouse** (Windows LL hooks / Linux evdev grab) so the local user can't
  interfere while the technician works. Each operator gets one desktop session
  per device, and everyone opening a device sees who else is connected.
- **Elevated remote Terminal** (`devices.terminal`): a real PTY (Unix) / ConPTY
  (Windows) shell that runs as the agent's service account (root/SYSTEM), bridged
  to an xterm.js viewer.
- **Granular roles/permissions** (built-in `readonly`/`tech`/`admin` plus custom
  roles) + optional **TOTP 2FA** + **per-user access scope** (Company/Site/
  Department/device), enforced on read, write, and the AI/MCP surface. Users can
  change their own password in-app, reset a forgotten one by email, and be
  disabled without being deleted.
- **Single sign-on** through any OpenID Connect provider, with MFA required from
  the provider's signed claims when configured — see
  [21-single-sign-on](21-single-sign-on.md).
- **MCP server** for external AI clients, with scoped API tokens or a built-in
  **OAuth 2.1** authorization server (dynamic registration, PKCE, SSO on the
  consent page, an applications screen to approve and revoke) — see
  [11-mcp](11-mcp.md) and [17-mcp-oauth](17-mcp-oauth.md).
- The AI and MCP clients can also **propose configuration** (tasks, checks,
  policies — created disabled) and **moves or deletions of devices**, carried out
  only when a person approves them.
- **Alert email** with per-user subscriptions, a durable outbox and retries,
  through the instance's own SMTP relay or an operator's shared service — see
  [20-email-notifications](20-email-notifications.md).
- REST API, token auth, an **append-only audit trail** (triggers, a non-owner
  database role, a hash chain; filterable, exportable as CSV, searchable by the
  assistant), encrypted runtime secrets (AES-256-GCM).
- **Fleet inventory reports** (software / hardware / machines) aggregated in the
  database by Company / Site / Department, scope-filtered, with client-side CSV
  export and a matching `inventory_report` AI/MCP tool — see [15-reports](15-reports.md).
- **Patch management**: missing-patch inventory (Windows KBs, Linux advisories),
  a catalog with approval + classification, patch policies (maintenance windows,
  auto-approve, exclusions), reboot orchestration and a compliance report — all
  approval/scope/audit-gated. See [16-patch-management](16-patch-management.md).
- React frontend (`openrmm/`) wired to the backend, translated into six
  languages; cross-platform Go agent that **self-updates** from the server
  (`/dl` ETag, Ed25519-signed artifacts).
- Production Compose with file-backed secrets, an npm-built frontend and
  enforcing CSP, split loopback ingress behind a trusted TLS edge, versioned
  checksum-validated migrations (`-migrate-only` for a separate owner step), and
  age/rclone-aware backup/verify/restore scripts. Server, console and signed
  `/dl` artifacts are also published as container images (GHCR).

## What is intentionally NOT built yet

- **Hardware-accelerated codecs / E2E-encrypted media** — the native engine uses a
  tile-delta JPEG codec today (homegrown, **no third-party fork**); hardware
  codecs (VP8/9/AV1) and media end-to-end encryption are future work. The
  **macOS** engine does not reach the login window, and **Linux
  requires an Xorg session** (Wayland blocks screen capture — see
  [08-remote-access](08-remote-access.md)).
- **Rich osquery inventory, script sandboxing/signing** — see
  [09-roadmap](09-roadmap.md).
- **A distributed/HA control plane** — the supported topology is one server
  replica. The agent hub, native relay, and scheduler are process-local and have
  no shared coordination layer.
- **Complete platform observability** — `/healthz` is liveness, `/readyz` pings
  the database, and a token-gated `/metrics` exposes a small Prometheus snapshot;
  there is no dependency graph, tracing, or built-in log/backup alerting.
- **OS trust signing** — release artifacts have OpenRMM Ed25519 signatures,
  checksums, SBOM and CI provenance, but no Windows Authenticode or Apple
  Developer ID signing/notarization.

## Non-negotiable principle

The AI **proposes**, humans **dispose**. The model has no path to execute
anything on an endpoint without an explicit human approval. This is enforced in
code, not policy. See [07-guardrails-security](07-guardrails-security.md).
