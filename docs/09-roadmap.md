# 09 — Roadmap

Phased plan from the current MVP to a complete RMM. Phases 0–1 are **done**.

## ✅ Phase 0 — Skeleton (done)
Agent enrollment, persistent WebSocket, heartbeat, job queue in Postgres,
`run_script` job executed on the endpoint, result returned. Verified end-to-end.

## ✅ Phase 1 — AI chat (done)
Tool-calling loop; read tools + the gated `propose_script`; guardrails (approval,
audit). The product's differentiator.

## ✅ Phase 2 — Real RMM (done)
- **Client → Site → Agent hierarchy** + site-bound enrollment tokens.
- **RBAC** — users (bcrypt) + sessions; roles admin/tech/readonly; API tokens.
  Plus **TOTP 2FA** and **per-user access scope** (Company/Site/device), enforced
  on read + write paths and on the AI/MCP surface.
- **Script library** + run-on-device.
- **Monitoring checks** (diskspace, memory, cpuload, ping, winservice, script)
  with thresholds, history, and a server-driven scheduler.
- **Scheduled tasks** (interval/daily) dispatched as pre-approved automation.
- **Policies** assigned to client/site/agent; agents inherit checks/tasks.
- **Alerts** with auto-raise/auto-resolve, dedup, ack/resolve, offline detection.
- **Rich inventory** via gopsutil (CPU, memory, disks, platform, uptime).
- **Agent as a system service** (install/uninstall via kardianos/service).

Remaining Phase 2 nice-to-haves:
- osquery-grade software inventory; device groups/tags filtering in the UI.
- High-level AI action tools (`install_software`, `install_printer(model, ip)`).
- Patch management.

## ✅ Phase 3 — Remote access (done; codecs maturing)
- MeshCentral token-login embed (turnkey in-browser sessions).
- **Native media engine built** on the relay — a **CGO-free, no-fork** sidecar
  (`cmd/rmm-remote`) sharing a **tile-delta codec** (`encoder.go`):
  - **Windows**: GDI capture + SendInput, in-session chat, multi-monitor, UAC
    handling (SYSTEM in the active session), and logoff survival.
  - **Linux/Xorg**: pure-Go X11 capture (MIT-SHM when local) + `/dev/uinput` input,
    with **seat-following** (login greeter ↔ user session). Requires an **Xorg**
    session — Wayland blocks capture (the viewer shows a notice; switch with
    `scripts/linux-force-xorg.sh`).
  - **Bidirectional free-path file transfer** with a file manager on **both**
    Windows and Linux.
  - `native` is a first-class provider; the viewer also opens in a browser tab.
- **Elevated remote Terminal** (`terminal` provider, tech-only): a real PTY (Unix)
  / ConPTY (Windows) shell running as the agent's service account (root/SYSTEM),
  bridged to an xterm.js viewer.
- **Remaining:** hardware-accelerated codecs (VP8/9/AV1) + media E2E (homegrown,
  no fork); macOS capture; an optional Wayland backend (XDG portal + PipeWire,
  needs CGO + a consent prompt, and still can't capture the login screen).
  (XDamage-based change detection was tried and reverted — it doesn't fire under a
  compositor like GNOME/mutter; MIT-SHM is the compositor-safe capture speedup.)

## ✅ Phase 4 — Frontend (done)
React SPA (`openrmm/`) per `FRONTEND_DESIGN_BRIEF.md`, wired to the backend:
dashboard, devices, device detail with remote-launch, jobs + approval queue with
dry-run preview, AI chat (with in-chat CSV report download), audit, users/2FA/scope.

## ✅ Agent self-update (done)
Agents upgrade themselves from the server: each compares the
`/dl/rmm-agent-<os>-<arch>` ETag with the build it last applied and, on change,
downloads + validates + atomically swaps + reloads (re-exec on Unix; detached
service restart on Windows). Publish with `make agent-dist`; tune with
`RMM_AUTO_UPDATE` / `RMM_UPDATE_INTERVAL`. See
[02-getting-started](02-getting-started.md#updating-agents-self-update).

## Cross-cutting hardening
TLS everywhere, rate limiting, script output caps, agent revocation, signed
script library, off-box audit shipping. See
[07-guardrails-security](07-guardrails-security.md).

## Suggested next step
Phase 2 inventory (osquery) + device groups — they give the AI far richer
context to reason about, which compounds the value of the chat already built.
