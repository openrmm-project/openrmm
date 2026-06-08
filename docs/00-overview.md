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
- Pluggable remote-access layer with two providers (native relay + built-in media
  engine, and MeshCentral). The native sidecar does screen capture, input, chat,
  bidirectional file transfer, multi-monitor, UAC handling, and logoff survival.
- **RBAC** (admin/tech/readonly) + optional **TOTP 2FA** + **per-user access scope**
  (Company/Site/device), enforced on read, write, and the AI/MCP surface.
- REST API, token auth, immutable audit log, encrypted runtime secrets (AES-256-GCM).
- React frontend (`openrmm/`) wired to the backend; cross-platform Go agent.

## What is intentionally NOT built yet

- **Hardware-accelerated codecs / E2E-encrypted media** — the native engine uses
  JPEG today; the RustDesk-fork plan ([12-native-remote-fork](12-native-remote-fork.md))
  is the upgrade path. Non-Windows hosts have a capture stub.
- **Rich osquery inventory, patch management, 2FA recovery codes, script
  sandboxing/signing** — see [09-roadmap](09-roadmap.md).

## Non-negotiable principle

The AI **proposes**, humans **dispose**. The model has no path to execute
anything on an endpoint without an explicit human approval. This is enforced in
code, not policy. See [07-guardrails-security](07-guardrails-security.md).
