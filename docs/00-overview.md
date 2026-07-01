# 00 — Overview

## Vision

A self-hosted RMM where the primary interface is a **conversation with an AI**
that can manage fleets of computers: inspect them, deploy software, configure
devices, run scripts, and generate reports — all in natural language, with a
human in the loop for anything that changes an endpoint.

Inspired by **Tactical RMM** (own agent + integrated remote access) and
**MeshCentral** (robust agent/remote backend), but:

- **Modern UI** (you design it; the backend is UI-agnostic and API-first).
- **AI-native**: a chat + tool-calling engine is a first-class subsystem.
- **BYO-model**: connect any OpenAI-compatible API, including local models, so
  command data never has to leave your infrastructure.

## Positioning

The AI-copilot RMMs on the market (Atera, SuperOps, Acronis RMM, Neo Agent) are
**closed SaaS**. None are open-source + self-hosted + bring-your-own-model. This
project targets exactly that niche: *the modern Tactical RMM, AI-native and
privacy-first*.

## What is built (this repo)

The **backend MVP**, verified end-to-end against PostgreSQL:

- Agent enrollment + persistent WebSocket + heartbeat + inventory.
- Job lifecycle: propose → **human approval** → dispatch → result, fully audited.
- AI chat engine with a tool-calling loop and read/propose tools; reports render
  in chat with a client-side **CSV download**, never written to the endpoint.
- Pluggable remote-access layer with three providers: **native** (relay + built-in
  media engine), **terminal** (an elevated, in-process shell), and **MeshCentral**.
  The native sidecar does screen capture + input over a CGO-free tile-delta codec on
  **Windows** (GDI; chat, multi-monitor, UAC, logoff survival) and **Linux/Xorg**
  (X11 capture via MIT-SHM + uinput input, with seat-following for the login screen).
  **File transfer** works on both Windows and Linux, and a session can **lock the
  host's physical keyboard/mouse** (Windows LL hooks / Linux evdev grab) — covering
  the login screen — so the local user can't interfere while the technician works.
- **Elevated remote Terminal** (`devices.terminal`): a real PTY (Unix) / ConPTY
  (Windows) shell that runs as the agent's service account (root/SYSTEM), bridged
  to an xterm.js viewer.
- **Granular roles/permissions** (built-in `readonly`/`tech`/`admin` plus custom
  roles) + optional **TOTP 2FA** + **per-user access scope** (Company/Site/
  Department/device), enforced on read, write, and the AI/MCP surface. Users can
  change their own password in-app.
- REST API, token auth, immutable audit log, encrypted runtime secrets (AES-256-GCM).
- **Fleet inventory reports** (software / hardware / machines) aggregated in the
  database by Company / Site / Department, scope-filtered, with client-side CSV
  export and a matching `inventory_report` AI/MCP tool — see [15-reports](15-reports.md).
- **Patch management**: missing-patch inventory (Windows KBs, Linux advisories),
  a catalog with approval + classification, patch policies (maintenance windows,
  auto-approve, exclusions), reboot orchestration and a compliance report — all
  approval/scope/audit-gated. See [16-patch-management](16-patch-management.md).
- React frontend (`openrmm/`) wired to the backend; cross-platform Go agent that
  **self-updates** from the server (`/dl` ETag).

## What is intentionally NOT built yet

- **Hardware-accelerated codecs / E2E-encrypted media** — the native engine uses a
  CGO-free tile-delta JPEG codec today (homegrown, **no third-party fork**); hardware
  codecs (VP8/9/AV1) and media end-to-end encryption are future work. **macOS**
  capture is still a stub, and **Linux requires an Xorg session** (Wayland blocks
  screen capture — see [08-remote-access](08-remote-access.md)).
- **Rich osquery inventory, script sandboxing/signing** — see
  [09-roadmap](09-roadmap.md).

## Non-negotiable principle

The AI **proposes**, humans **dispose**. The model has no path to execute
anything on an endpoint without an explicit human approval. This is enforced in
code, not policy. See [07-guardrails-security](07-guardrails-security.md).
