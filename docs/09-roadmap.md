# 09 — Roadmap

Phased plan toward a complete RMM, and where it stands at v0.7.2. Phases 0–4 are
**done**; what remains is listed under each phase and at the end.

## ✅ Phase 0 — Skeleton (done)
Agent enrollment, persistent WebSocket, heartbeat, job queue in Postgres,
`run_script` job executed on the endpoint, result returned. Verified end-to-end.

## ✅ Phase 1 — AI chat (done)
Tool-calling loop; permission-filtered read tools + approval-gated proposal tools;
guardrails (approval, audit). The product's differentiator.

## ✅ Phase 2 — Real RMM (done)
- **Client → Site → Agent hierarchy** + site-bound enrollment tokens.
- **RBAC** — users (bcrypt) + sessions; built-in and custom roles with granular
  permissions; API tokens. Plus **TOTP 2FA** and **per-user access scope**
  (Company/Site/Department/device), enforced on read + write paths and on the
  AI/MCP surface.
- **Script library** + run-on-device.
- **Monitoring checks** (diskspace, memory, cpuload, ping, winservice, script)
  with thresholds, history, and a server-driven scheduler.
- **Scheduled tasks** (interval/daily/weekly) dispatched as pre-approved automation.
- **Policies** assigned to client/site/department/agent; agents inherit checks/tasks.
- **Alerts** with auto-raise/auto-resolve, dedup, ack/resolve, offline detection.
- **Rich inventory** via gopsutil (CPU, memory, disks, platform, uptime).
- **Agent as a system service** (install/uninstall via kardianos/service).

- **Departments** (device groups under a site), enrolment straight into one,
  and bulk move/delete of devices in the console.
- **High-level AI actions** (`install_software`, `uninstall_software`,
  `restart_service`, `install_printer`, `flush_dns`, `reboot_device`) — each
  generates an OS-specific, approval-gated job.

Remaining Phase 2 nice-to-haves:
- osquery-grade software inventory; tags for filtering devices in the UI.

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
  - **Input lock**: a viewer toggle blocks the host's physical keyboard/mouse
    (Windows `WH_KEYBOARD_LL`/`WH_MOUSE_LL` hooks; Linux evdev `EVIOCGRAB`) while
    the technician's injected input still works — covers the login screen too.
  - `native` is a first-class provider; the viewer also opens in a browser tab.
- **Elevated remote Terminal** (`terminal` provider, `devices.terminal`): a real
  PTY (Unix) / ConPTY (Windows) shell running as the agent's service account
  (root/SYSTEM), bridged to an xterm.js viewer.
- **macOS**: a CGO sidecar in a signed `.app` bundle run as the
  console user through a LaunchAgent; capture, input, file transfer, chat and
  display selection, with Screen Recording and Accessibility requested one at a
  time.
- **Session conveniences**: the clipboard in both directions, display selection,
  a Ctrl+Alt+Delete button on Windows, one desktop session per operator per
  device ("Continue here" moves it), and a notice of who else is connected.
- **Remaining:** hardware-accelerated codecs (VP8/9/AV1) + media E2E (homegrown,
  no fork); the macOS login window; an optional Wayland backend (XDG portal +
  PipeWire, needs CGO + a consent prompt, and still can't capture the login
  screen).
  (XDamage-based change detection was tried and reverted — it doesn't fire under a
  compositor like GNOME/mutter; MIT-SHM is the compositor-safe capture speedup.)

## ✅ Phase 4 — Frontend (done)
React SPA (`openrmm/`) per [`14-frontend-design-brief.md`](14-frontend-design-brief.md), wired to the backend:
dashboard, devices, device detail with remote-launch, jobs + approval queue with
dry-run preview, AI chat (with in-chat CSV report download), audit, users/2FA/scope,
and a **Reports** tab (fleet software/hardware/machines inventory by
client/site/department, column picker + CSV — see [15-reports](15-reports.md)).

## ✅ Agent self-update (done)
Agents upgrade themselves from the server: each compares the
`/dl/rmm-agent-<os>-<arch>` ETag with the build it last applied and, on change,
downloads + validates + atomically swaps + reloads (re-exec on Unix; detached
service restart on Windows). Publish with `make agent-dist`; tune with
`RMM_AUTO_UPDATE` / `RMM_UPDATE_INTERVAL`. See
[02-getting-started](02-getting-started.md#updating-agents-self-update).

## ✅ Patch management (done)
A first-class patch subsystem: **missing-patch inventory** (Windows KBs, Linux
advisories/CVEs), a **catalog with per-patch approval + classification**,
**patch policies** (maintenance windows, auto-approve security after N days,
exclusions), **post-patch reboot orchestration**, and **compliance reporting** —
all behind the existing approval/scope/audit guardrails, with a Patches tab, a
device-drawer Patches view and a read-only `list_patches` AI tool. Requires agent
≥ 0.4.2. Design + per-OS notes in [16-patch-management](16-patch-management.md).

## ✅ Identity and access (done)
- **Single sign-on** through any OpenID Connect provider, with MFA requirements
  checked on the provider's signed claims and a break-glass switch — see
  [21-single-sign-on](21-single-sign-on.md).
- Password reset by email, address confirmation, disabling a user without
  deleting them; a TOTP code is accepted once.
- **MCP OAuth 2.1** so cloud connectors (Claude, ChatGPT) sign in natively, with
  SSO on the consent page and an applications screen to approve and revoke — see
  [17-mcp-oauth](17-mcp-oauth.md).

## ✅ Alerting and audit (done)
- **Alert email**: per-user subscriptions, transition-only sending, a durable
  outbox with retries, the instance's own SMTP relay or an operator's shared
  service (confirmed addresses only) — see
  [20-email-notifications](20-email-notifications.md).
- **Append-only audit trail**: triggers, a non-owner database role, a hash chain;
  server-side filters and cursor paging, CSV export links, and `query_audit` /
  `export_audit` for the assistant.

## ✅ AI proposals beyond scripts (done)
The assistant and MCP clients propose **configuration** (tasks, checks,
policies — created disabled) and **moves or deletions of devices**; a person
approves each one. Fleet and audit tools count in the database and hand back
download links instead of reading rows into the model.

## ✅ Console in six languages (done)
English, Portuguese (Brazil), Spanish, German, Japanese and Simplified Chinese,
each with the full key set.

## ✅ Release and distribution (done)
Signed agent and sidecar artifacts (Ed25519, staged key rotation without
reinstalling), SBOM and build provenance; server, console and `/dl` artifacts
published as container images; installers and uninstallers for Windows, Linux
and macOS. See [18-update-signing-runbook](18-update-signing-runbook.md).

## Cross-cutting hardening
Done: TLS at the edge, per-IP rate limiting, request and agent output caps,
agent credential revocation and rotation, append-only audit. Remaining: a
server-side output cap for non-reference agents, a signed script library,
off-box audit shipping, script/terminal sandboxing, OS code signing
(Authenticode, Apple Developer ID). See
[07-guardrails-security](07-guardrails-security.md) and
[19-production-readiness-plan](19-production-readiness-plan.md).

## Suggested next step
osquery-grade inventory and device tags — they give the AI far richer context to
reason about, which compounds the value of the chat already built. After that:
an HA decision for the control plane, and the remote engine's remaining
platforms (Wayland, the macOS login window).
