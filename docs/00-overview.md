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
- AI chat engine with a tool-calling loop and read/propose tools.
- Pluggable remote-access layer with three providers (native relay, MeshCentral,
  RustDesk) and a working native relay transport.
- REST API, token auth, immutable audit log.
- Cross-platform Go agent that executes scripts and reports results.

## What is intentionally NOT built yet

- **Frontend** — see [`FRONTEND_DESIGN_BRIEF.md`](../FRONTEND_DESIGN_BRIEF.md).
- **Remote-desktop media engine** — screen capture/encoding/input for the native
  provider (recommended: fork RustDesk). The relay transport is ready; the media
  module plugs into it. See [08-remote-access](08-remote-access.md).
- **Multi-tenant RBAC, rich osquery inventory, scheduling/cron jobs, patch
  management** — see [09-roadmap](09-roadmap.md).

## Non-negotiable principle

The AI **proposes**, humans **dispose**. The model has no path to execute
anything on an endpoint without an explicit human approval. This is enforced in
code, not policy. See [07-guardrails-security](07-guardrails-security.md).
