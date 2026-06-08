# 08 — Remote Access

Remote access is **pluggable**: a single `Provider` interface
(`internal/remote`) with three implementations. Callers never depend on a
specific tool — if the native tool doesn't pan out, drop it and MeshCentral keeps
working.

```go
type Provider interface {
    Name() string
    StartSession(ctx, agent, createdBy) (*RemoteSession, error)
}
```

`POST /api/devices/{id}/remote { "provider": "..." }` returns a `RemoteSession`
with a `connect_url` the UI acts on.

## Providers

### `meshcentral`
- Builds a connect URL into a configured MeshCentral server for the agent's node.
- **Seamless login-token embed** (like Tactical RMM): when a token key is
  configured, the server mints a MeshCentral login token (AES-256-GCM,
  `internal/remote/meshtoken.go`) so the iframe opens without a second login.
  Without the key it falls back to a deep link that works once you're logged into
  MeshCentral.
- License: MeshCentral is **Apache-2.0** — no constraints on bundling.

#### Setup (to test MeshCentral)
1. Run a MeshCentral server. In its `config.json`, enable:
   `"AllowLoginToken": true` and `"AllowFraming": true`.
2. Grab the server's **login token key** (the `loginTokenKey`, hex). Configure
   the RMM:
   ```
   RMM_MESHCENTRAL_URL=https://your-mesh-server
   RMM_MESHCENTRAL_TOKEN_KEY=<loginTokenKey hex>   # omit for deep-link mode
   RMM_MESHCENTRAL_USER=admin                       # userid becomes user//admin
   ```
3. Install the MeshCentral agent on the endpoint and find its **node id**.
4. Map the device to its node:
   `PATCH /api/devices/{id}/remote-ids {"mesh_id":"node//..."}`.
5. Start a session: `POST /api/devices/{id}/remote {"provider":"meshcentral"}`
   → returns a `connect_url` (open it / embed it in an iframe).

> Note: the login-token crypto matches MeshCentral's `encodeCookie`
> (`iv||tag||ciphertext`, base64 with `+/`→`@$`) and passes a round-trip test,
> but the exact payload acceptance can only be confirmed against a live server —
> validate on your instance; the deep-link mode works regardless.

### `native` (built-in)
- A session **broker + relay** built into the server (`internal/remote/relay.go`).
  On `StartSession` it mints a token, pushes `remote_start{token, relay_url}` to
  the agent, and returns a viewer `connect_url`.
- Both peers connect to `/ws/remote?token=...&role=agent|viewer`; the relay pairs
  them by token and pipes frames. This is the **transport plane** — the
  equivalent of RustDesk's `hbbr` relay.
- **Media engine (built):** a CGO-free **sidecar** (`cmd/rmm-remote`) that the
  agent spawns on demand. **Windows** captures via **GDI BitBlt** + **SendInput**;
  **Linux** captures via pure-Go **X11** (`GetImage` over the display socket) +
  **XTEST** input injection (`media_linux.go` / `x11_linux.go`) — the same native
  technique, no fork. macOS has no CGO-free capture path yet (stub). The viewer is
  a plain browser client (`openrmm/src/screens-devices.jsx`, also openable in a new
  tab). A **multiplexed protocol** rides one WebSocket: binary `0x01`=video frame,
  `0x02`=file chunk; JSON text for input, chat, file ops, and `{t:"kf"}` (the
  viewer asks for a keyframe on (re)connect).
- **Codec — tile-delta (CGO-free, OS-agnostic):** `encoder.go` splits each
  captured RGBA frame into a grid of 64-px tiles and JPEG-encodes only the tiles
  that changed since the previous frame; a keyframe sends the whole screen as one
  JPEG. Idle/static screens cost almost nothing on the wire, so the frame rate
  rises (20 fps) without saturating the link. Because the encoder works purely on
  the pixel buffer, **any per-OS capturer feeds the same codec** — no fork. The
  payload is `[flags][frameW][frameH][tileCount]` then per tile
  `[x][y][w][h][jpegLen][jpeg]` (big-endian); the viewer composites tiles onto a
  `<canvas>`. Unit-tested in `encoder_test.go` (keyframe, no-change → no send,
  changed-tile localization, forced keyframe).
- **Windows extras:** multi-monitor selection, **in-session chat** (native dark
  Win32 window on the host, rounded/minimizable), **bidirectional free-path file
  transfer** with a RustDesk-style file manager (browse / create folder / copy /
  cut / delete / upload / download), **UAC handling** (sidecar runs as SYSTEM in
  the active console session via `CreateProcessAsUser`; `PromptOnSecureDesktop=0`),
  and **logoff survival** (desktop-following via `OpenInputDesktop`/`SetThreadDesktop`
  + viewer auto-reconnect). The agent keeps the sidecar current via an **ETag**
  check. The **Linux** engine currently does screen + mouse + keyboard only (chat /
  file-transfer / multi-seat are Windows-only for now); keyboard maps DOM keyCodes
  to X keysyms (US layout, best-effort) resolved through the server keymap.
- **Still maturing:** tiles are JPEG-encoded (works everywhere, no codec deps)
  rather than a real video codec (VP8/9/AV1) — fine for desktops, heavier for
  full-motion video. The Linux engine assumes a TrueColor little-endian X server
  and captures the first screen (RandR multi-monitor stitched); Wayland sessions
  need XWayland. macOS capture (CoreGraphics) needs CGO and stays a stub. End-to-end
  media encryption is still open (today the relay is the trust boundary; terminate
  WSS — see `07`).

## The "own remote tool" plan (no fork)

The chosen direction is a **homegrown, CGO-free engine**: native per-OS capture
(Windows GDI + Linux X11 today) + input injection feeding one shared tile-delta
codec (`encoder.go`). A RustDesk fork is **explicitly not** pursued; the notes
below are kept only as historical context for the licensing analysis.

**Licensing — why a fork is now clean:** RustDesk is AGPL-3.0. Because this
project ships as **open source**, distributing a RustDesk-derived module under
AGPL-3.0 is fully compatible — AGPL only requires the derivative stay open, which
is the intent. (If any part of the product were ever closed-source, the AGPL
fork would have to remain a separate process, not linked code.)

Build options, in order of effort:
1. **Fork RustDesk** (recommended) — inherit capture/codecs/NAT/security; rebrand
   and connect its signaling to this relay. Medium effort.
2. **WebRTC-based** viewer/agent on permissive libs — full license freedom, more
   work.
3. **From scratch** — highest effort; only for very specific requirements.

If the native tool underperforms, remove the provider — MeshCentral remains.

## Choosing a default

`RMM_REMOTE_PROVIDER` sets the default (`native` or `meshcentral`). Reasonable
path: ship with `meshcentral` (fast, Apache, in-browser) and graduate to
`native` as its media engine matures.
