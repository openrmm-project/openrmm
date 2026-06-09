# 08 — Remote Access

Remote access is **pluggable**: a single `Provider` interface
(`internal/remote`) with four implementations (`native` desktop, `terminal`
shell, `meshcentral`). Callers never depend on a specific tool — if the native
tool doesn't pan out, drop it and MeshCentral keeps working.

```go
type Provider interface {
    Name() string
    StartSession(ctx, agent, createdBy) (*RemoteSession, error)
}
```

`POST /api/devices/{id}/remote { "provider": "..." }` returns a `RemoteSession`
with a `connect_url` the UI acts on.

## Providers

### `terminal` (elevated shell)
- A built-in **interactive shell** brokered over the **same relay** as `native`:
  `POST /api/devices/{id}/remote {"provider":"terminal"}` mints a token, pushes
  `remote_start{kind:"terminal"}` to the agent, and returns a viewer `connect_url`.
- Unlike the desktop media engine (a sidecar in the interactive session), the
  shell runs **inside the agent process**, so it inherits the agent's privileges —
  and the agent runs as a **system service (root on Linux/macOS, SYSTEM on
  Windows)**, so the session is **elevated**. The viewer header shows a 🛡
  *elevated* badge with the shell user when `geteuid()==0` (Unix) / the process
  token is in the Administrators group (Windows).
- **Unix** opens a real **PTY** (`creack/pty`, CGO-free) running `bash -i` (falls
  back to `$SHELL`/`sh`). **Windows** opens a real **pseudo-console (ConPTY**,
  `UserExistsError/conpty`, Windows 10 1809+**)** running PowerShell. Both give a
  genuine terminal — echo, line editing, signals (Ctrl-C), ANSI/VT colors and
  curses TUIs (top/vim) all work.
- **Wire format** over the relay: viewer→agent binary = raw keystrokes, text JSON
  `{"t":"resize",cols,rows}`; agent→viewer binary = raw shell output, text JSON
  `{"t":"hello"|"exit"|"error"}`. The viewer is **xterm.js** (`TerminalViewer` in
  `openrmm/src/screens-devices.jsx`, opened from the device **Remote ▸ Terminal**
  menu).
- **Access control:** an elevated shell is equivalent to running scripts, so
  unlike read-only remote-desktop assistance the `terminal` provider requires the
  **`tech`** role (enforced in `startRemote` and gated in the UI menu).
- Verified end-to-end: broker → relay → live root/user PTY echoes a computed
  marker back to the viewer.

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
  check. The **Linux** engine does screen + mouse + keyboard, with **seat-following**:
  it attaches to the X server owning the active VT — the display-manager greeter
  before login, the user's session after — by scanning `/proc` for the X process
  (`-auth`/display/`vt`) and re-attaching when the session switches (login / logout
  / lock), the Linux analogue of the Windows desktop-following (`seat_linux.go`).
  This needs the sidecar to run as **root** (it does — the agent is a system
  service) to read the greeter's cookie. **Input** is injected via **`/dev/uinput`**
  (kernel level, `uinput_linux.go`) so it reaches the login greeter and any session
  (XTEST alone is unreliable on the GDM greeter); it falls back to XTEST if uinput
  isn't available. Chat / file-transfer are Windows-only for now.
- **Still maturing:** tiles are JPEG-encoded (works everywhere, no codec deps)
  rather than a real video codec (VP8/9/AV1) — fine for desktops, heavier for
  full-motion video. The Linux engine assumes a TrueColor little-endian X server
  and captures the active screen; **login-screen capture requires an Xorg greeter**
  (set GDM `WaylandEnable=false`, or use LightDM) — a Wayland greeter only exposes
  Xwayland, which can't capture native Wayland surfaces. macOS capture
  (CoreGraphics) needs CGO and stays a stub. End-to-end media encryption is still
  open (today the relay is the trust boundary; terminate WSS — see `07`).

### Linux requires Xorg (not Wayland)

Native remote on Linux needs an **Xorg** display server — for **both** the login
greeter and the user session. On a **Wayland** session the screen is **black**
(Wayland blocks screen capture by design; the only sanctioned path is the XDG
portal + PipeWire, which needs CGO, a per-machine consent prompt, and still can't
capture the login screen — so it's not used here). When the host is on Wayland the
viewer shows a notice instead of a silent black screen.

**Switch a host to Xorg** with the bundled, idempotent script
[`scripts/linux-force-xorg.sh`](../scripts/linux-force-xorg.sh) — it sets GDM
`WaylandEnable=false` (covers greeter + sessions), handles SDDM, leaves LightDM
(already X11), keeps a backup, and reports to STDOUT. Push it through the RMM as a
Script-library job (or a policy/scheduled task across the Linux fleet); a reboot
applies it (`RMM_FORCE_REBOOT=1` to reboot automatically). After reboot, confirm
with `echo $XDG_SESSION_TYPE` → `x11`.

**VirtualBox note:** GNOME-on-Xorg can black-screen in a VirtualBox guest unless
the graphics are right — set **Graphics Controller = VMSVGA**, **3D Acceleration
OFF**, and install the guest X driver (`virtualbox-guest-x11`/`-utils`).

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

Build options that were weighed (historical):
1. **Fork RustDesk** — inherit capture/codecs/NAT/security; rebrand and connect its
   signaling to this relay. Was the leading candidate before the homegrown engine.
2. **WebRTC-based** viewer/agent on permissive libs — full license freedom, more
   work.
3. **From scratch / homegrown** — the path actually taken: a CGO-free Go engine
   feeding the shared tile-delta codec (no AGPL, no fork, single static binary).

If the native tool underperforms, remove the provider — MeshCentral remains.

## Choosing a default

`RMM_REMOTE_PROVIDER` sets the default (`native` or `meshcentral`). Reasonable
path: ship with `meshcentral` (fast, Apache, in-browser) and graduate to
`native` as its media engine matures.
