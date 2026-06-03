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

---

## What is built now: the Go reference sidecar (`cmd/rmm-remote`)

The native provider is no longer a no-op. The agent now launches a **media
sidecar** — a separate process that does capture + input and rides the existing
relay. This is the exact integration the RustDesk fork plugs into: **swap the
sidecar binary and the agent / relay / viewer are unchanged.**

- **`cmd/rmm-remote`** (Go, CGO-free): `media_windows.go` captures the primary
  screen via GDI (`BitBlt` into a top-down DIB) → JPEG, and injects input via
  `SendInput`. `media_other.go` is a stub on Linux/macOS (the RustDesk fork, or
  a later CGO build, fills those in).
- **Agent** (`cmd/agent/remote.go`): on `remote_start` it derives the relay URL
  from its own server host (the server's `RelayURL` uses the public URL, which
  may be `localhost`), downloads the sidecar from `/dl/rmm-remote-<os>-<arch>`
  if missing, and launches it pointing at the relay.
- **Viewer** (`openrmm` → `RemoteViewer`): connects to `/ws/remote?role=viewer`,
  renders the JPEG frames on a `<canvas>`, and forwards mouse/keyboard as JSON.

### Relay wire format (the contract any sidecar must implement)
The relay just pipes frames between the two peers, so the format is whatever the
sidecar and viewer agree on:

| Direction | WS message | Payload |
|---|---|---|
| agent → viewer | **binary** | a full-screen **JPEG** frame |
| viewer → agent | **text** | a JSON input event |

Input events: `{"t":"m","x":0..1,"y":0..1}` (move), `{"t":"b",x,y,"button":0/1/2,"down":bool}` (click), `{"t":"w","dy":deltaY}` (wheel), `{"t":"k","code":VK,"down":bool}` (key). Coordinates are normalized 0..1 across the primary screen.

### Known limitations of the reference engine (intentional, fork closes them)
- **Session-0 isolation:** the agent runs as a service in session 0, which
  **cannot capture the interactive desktop**. For now run the agent
  interactively in the user session for native remote, or spawn the sidecar via
  `WTSQueryUserToken` + `CreateProcessAsUser` (a follow-up). The RustDesk fork
  handles this natively.
- Full-frame JPEG (no inter-frame delta, no H.264) → higher bandwidth; fine on a
  LAN, not a WAN. The fork brings VP8/9/AV1.
- No end-to-end encryption on the media (the relay sees frames). The fork keeps
  RustDesk's NaCl E2E crypto, so the relay only ever sees ciphertext.
- Primary monitor only; `keyCode`→VK mapping is approximate.

## RustDesk fork runbook (Phase B — drop-in replacement for the sidecar)
1. Fork `rustdesk/rustdesk`. Pin a release tag; track upstream via cherry-pick.
2. **Controlled (endpoint) side** — replace the hbbs/hbbr rendezvous with a
   WebSocket dial to our relay: on launch, read `-relay <ws-url>` (the same URL
   the Go sidecar gets), connect as `role=agent`, and carry RustDesk's existing
   (already E2E-encrypted) frames over that socket instead of hbbr. Keep `scrap`
   (capture), libvpx/dav1d (codecs), `enigo` (input), sodiumoxide (crypto).
3. **Viewer** — prefer the RustDesk **web client (WASM)** embedded in the RMM UI,
   connecting to `/ws/remote?role=viewer`; or ship the Flutter viewer.
4. **Build** per OS (Windows/macOS/Linux) + code-sign; publish as
   `rmm-remote-<os>-<arch>` so the agent downloads it exactly like the Go sidecar.
5. **Session security:** bind the relay token to the technician principal and the
   agent identity on connect (closes docs/07 H1), on top of RustDesk's E2E crypto.

No changes to `cmd/agent`, the relay, or the session brokering are needed — the
fork only has to speak the `-relay` launch contract and connect to `/ws/remote`.
