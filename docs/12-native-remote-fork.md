# 12 — Native Remote Desktop (RustDesk fork) — Architecture Plan

This is a **plan**, not shipped code. It describes how to build the native
desktop-remote ("media engine") by forking RustDesk and connecting it to the
relay that already exists in this repo (`internal/remote/relay.go`,
`/ws/remote`). The session brokering, token issuing and `remote_start` push are
already implemented and tested — this plan fills in the media engine that rides
on top.

## Why a fork (not from scratch)
A production remote desktop = screen capture + video codecs (VP8/VP9/AV1) + input
injection + NAT traversal + end-to-end crypto. RustDesk already has all of it,
battle-tested, in Rust. Writing this from scratch is years of work. **Forking is
the pragmatic path** and — because this project ships **open source** — RustDesk's
**AGPL-3.0** is fully compatible (the fork stays open under AGPL; keep it as a
separate process so it never relinks the Go code).

## What we reuse from RustDesk
| Concern | RustDesk component (Rust) |
|---|---|
| Screen capture | `scrap` |
| Video codecs | libvpx (VP8/9), libaom/dav1d (AV1), hardware enc where present |
| Input injection | `enigo` |
| End-to-end crypto | NaCl `secretbox` / sodiumoxide (session is encrypted peer-to-peer) |
| Viewer | RustDesk Flutter client, or its **web client (WASM)** for in-browser |

## The key architectural decision: replace hbbs/hbbr with our relay
RustDesk normally uses **hbbs** (ID/rendezvous) + **hbbr** (relay). This RMM
already has a relay and brokers sessions itself, so the fork should **bypass
hbbs/hbbr and use our relay** as the transport:

```
POST /api/devices/{id}/remote {provider:"native"}        [already built]
  -> mint token, push remote_start{token, relay_url} to agent over its WS
  -> return viewer connect_url

Agent's forked RustDesk module  --(role=agent)-->  /ws/remote ?token=...
Viewer (forked web client)      --(role=viewer)--> /ws/remote ?token=...
  -> relay pipes the (already end-to-end-encrypted) RustDesk frames between them
```

So the fork's job is narrow: **swap RustDesk's signaling/transport layer for a
WebSocket to our relay**, keeping its media + crypto intact. The relay only ever
sees ciphertext (RustDesk's E2E encryption stays on), which also closes the
relay-token-leak risk noted in docs/07.

## Components to build
1. **Forked "controlled" module on the endpoint** (Rust binary). On
   `remote_start`, it dials `relay_url` (WebSocket, role=agent), performs the
   RustDesk key exchange, captures the screen, encodes, and streams. The Go agent
   **supervises it as a separate process** (launch/stop) — this keeps the Go
   agent CGO-free and the AGPL code isolated.
2. **Forked viewer** — preferably the **RustDesk web client (WASM)** embedded in
   the RMM UI, connecting to `/ws/remote` (role=viewer). Gives in-browser desktop
   with no extra install for the technician. (Native Flutter viewer is the
   alternative.)
3. **Transport shim** in both: replace RustDesk's hbbs/hbbr rendezvous with the
   WebSocket-to-relay path + our session token.
4. **Session security**: bind the relay token to the technician principal and the
   agent identity on connect (fixes docs/07 H1), on top of RustDesk's E2E crypto.

## Integration points already in place (no rework needed)
- Relay transport + pairing by token: `internal/remote/relay.go`.
- Session broker + `remote_start{session_id, token, relay_url}`: `internal/remote/remote.go` (native provider), pushed to the agent over its WebSocket.
- Agent already receives `remote_start` (today it just logs it: `cmd/agent/main.go`) — replace that log with "launch the forked module pointing at relay_url".

## Phased delivery
- **Phase A — Bundle (fastest path to a picture on screen):** ship RustDesk mostly
  unmodified + its server, RMM launches/links sessions. Proves the experience;
  two server stacks temporarily. Low risk.
- **Phase B — Deep fork on our relay:** retarget signaling/transport to
  `/ws/remote`, drop hbbs/hbbr, embed the WASM viewer in the RMM UI. The unified
  "native" vision.
- **Phase C — Polish:** hardware encoding, file transfer, clipboard, multi-monitor,
  session recording, unattended-access provisioning by the agent.

## Build / distribution notes
- Rust toolchain; per-OS builds (Windows/macOS/Linux) + code signing.
- Ship the module as a sidecar binary the Go agent downloads/launches — keeps the
  agent a single CGO-free Go binary and the AGPL component cleanly separated.
- Keep RustDesk's end-to-end encryption ON so the relay is zero-trust.

## Risks / unknowns
- Effort: weeks–months (Rust media work), the largest single feature.
- WASM viewer performance vs native; hardware-codec availability per endpoint.
- Tracking upstream RustDesk changes after forking (pin a version, cherry-pick).
- Validate the transport shim against the relay's framing (binary WS messages).

## Recommendation
Start with **Phase A** to get a working picture quickly and validate the relay
end-to-end with real media, then invest in **Phase B** for the unified native
product. Until then, **MeshCentral** (already integrated) covers real desktop
sessions.
