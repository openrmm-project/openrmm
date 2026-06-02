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

## Phase 3 — Remote access (dual → native)
- Finish the MeshCentral token-login embed (turnkey in-browser sessions).
- Agent-side RustDesk provisioning (install as service, set password, report ID).
- Fork RustDesk for the native media engine on top of the existing relay; make
  `native` a first-class provider. Drop it if it underperforms.

## Phase 4 — Frontend
Modern UI per `FRONTEND_DESIGN_BRIEF.md`: dashboard, devices, device detail with
remote-launch, jobs + approval queue with dry-run preview, the AI chat, and the
audit log.

## Cross-cutting hardening
TLS everywhere, rate limiting, script output caps, agent revocation, signed
script library, off-box audit shipping. See
[07-guardrails-security](07-guardrails-security.md).

## Suggested next step
Phase 2 inventory (osquery) + device groups — they give the AI far richer
context to reason about, which compounds the value of the chat already built.
