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
  agent spawns on demand. On Windows it captures the screen via **GDI BitBlt →
  JPEG** and injects input via **SendInput**; the viewer is a plain
  browser client (`openrmm/src/screens-devices.jsx`, also openable in a new tab).
  A **multiplexed protocol** rides one WebSocket: binary `0x01`=video frame,
  `0x02`=file chunk; JSON text for input, chat, and file ops.
- **Capabilities:** multi-monitor selection, **in-session chat** (native dark
  Win32 window on the host, rounded/minimizable), **bidirectional free-path file
  transfer** with a RustDesk-style file manager (browse / create folder / copy /
  cut / delete / upload / download), **UAC handling** (sidecar runs as SYSTEM in
  the active console session via `CreateProcessAsUser`; `PromptOnSecureDesktop=0`),
  and **logoff survival** (desktop-following via `OpenInputDesktop`/`SetThreadDesktop`
  + viewer auto-reconnect). The agent keeps the sidecar current via an **ETag**
  check.
- **Still maturing:** the codec is JPEG (simple, works everywhere) rather than
  VP8/9/AV1; non-Windows hosts have a stub. The RustDesk-fork plan below is the
  path to hardware-accelerated codecs + E2E encryption.

## The "own remote tool" plan

The recommendation (and the reason the native relay exists) is to build the own
remote tool by **forking RustDesk** for the media engine and pointing it at this
relay, rather than writing screen capture/codecs from scratch (years of work).

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
