# 08 — Remote Access

Remote access is **pluggable**: a single `Provider` interface
(`internal/remote`) with five implementations: `native` (remote desktop),
`files` (file transfer only — the media sidecar with **no** screen capture),
`terminal` (elevated shell), `meshcentral`, and optional `rustdesk`. Callers never depend on a specific
tool — if the native tool doesn't pan out, drop it and MeshCentral keeps working.

```go
type Provider interface {
    Name() string
    StartSession(ctx, agent, createdBy, opts) (*RemoteSession, error)
}
```

`POST /api/devices/{id}/remote { "provider": "..." }` returns a `RemoteSession`.
Native, files, and terminal sessions carry a one-time viewer ticket; external
providers carry a `connect_url` the UI acts on.

`GET /api/devices/{id}/remote-capabilities` performs an on-demand conservative
native-desktop preflight. The UI disables native desktop when it confirms Wayland
or no Xorg display; `unknown` never claims support and leaves the technician to
choose a configured fallback provider.

## Providers

### `terminal` (elevated shell)
- A built-in **interactive shell** brokered over the **same relay** as `native`:
  `POST /api/devices/{id}/remote {"provider":"terminal"}` mints role-specific
  one-time tickets, pushes `remote_start{kind:"terminal"}` to the agent, and
  returns the viewer ticket only to the authenticated caller.
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
- **Access control:** remote desktop, file transfer, and terminal are separate
  device sub-permissions: `devices.remote`, `devices.files`, and
  `devices.terminal`. In-band file transfer inside a native desktop session is
  also disabled unless `devices.files` is present. An elevated shell is equivalent
  to running scripts, so the `terminal` provider requires `devices.terminal`
  (enforced in `startRemote` and gated in the UI menu).
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
- **Enable/disable toggle:** on by default; set `RMM_MESHCENTRAL_ENABLED=false`
  (or the toggle in Settings ▸ Remote access) to turn it off. When disabled the
  provider refuses to start and the MeshCentral item is hidden from the device
  Remote menu — symmetric with the optional `rustdesk` provider.

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

### `rustdesk` (optional, on-demand id)

An optional, **thin** provider for operators who already run RustDesk. The
RustDesk **client is installed out-of-band** (your own script/MDM, not the RMM
agent), and the RMM never touches RustDesk's servers — the media path goes
endpoint ↔ your **hbbs/hbbr** (self-hosted or a plan) ↔ the technician's viewer.

- **Enable it** with `RMM_RUSTDESK_ENABLED=true` or the toggle in Settings ▸
  Remote access. When off, no RustDesk item shows in the device Remote menu.
- **On-demand id read:** clicking *Remote ▸ RustDesk* makes the server send an
  on-demand `remote_query` to the agent, which runs `rustdesk --get-id` and
  replies (`internal/protocol` `RemoteQueryMsg`/`RemoteQueryResultMsg`, correlated
  by request id in `remote.Manager`). The provider then returns a
  `rustdesk://<id>` deep link the UI opens — no id is stored ahead of time.
- **Password:** RustDesk's permanent password is **not** read back (it is not
  recoverable from the client). Supply it from whatever set it (your install
  script), or use RustDesk's attended / one-time-password flow.
- Not brokered by the relay; the RMM only hands off the id. See the provider in
  `internal/remote/remote.go` (`rustdeskProvider`).

### `native` (built-in)
- A session **broker + relay** built into the server (`internal/remote/relay.go`).
  On `StartSession` it mints separate one-time viewer/agent tickets, pushes
  `remote_start{session_id, agent_ticket}` to the agent, and returns the viewer
  ticket only to the authenticated caller. The server stores ticket hashes only.
- Both peers connect to `/ws/remote?session=...&role=agent|viewer`; tickets travel
  in the WebSocket handshake and the viewer must authenticate as the principal
  that started the session. The agent sends `Authorization: Remote-Agent <ticket>`;
  the viewer sends `rmm-ticket.<ticket>` and `rmm-auth.<bearer>` through
  `Sec-WebSocket-Protocol`. A viewer ticket in the URL is rejected.
- `RMM_ALLOW_LEGACY_AGENT_RELAY=true` temporarily admits a pre-ticket **agent
  sidecar** during a controlled rollout. It does not apply to viewers; return it
  to `false` once the fleet has the ticket-aware sidecar.
- In split ingress, the agent peer uses the public **agents** origin and the
  browser peer uses the restricted **console** origin. Both nginx routes reach the
  same server relay; this keeps the browser same-origin check meaningful without
  exposing the console surface on the agents door. The production compose enables
  `RMM_ENFORCE_CONSOLE_RELAY_INGRESS=true`, so a viewer sent through the agents
  door is rejected even with a forged browser Origin. The relay pairs the peers by
  session id and pipes frames. This is the **transport plane** — a
  NAT-friendly relay (hbbr-equivalent).
- Relay pairs and agent WebSocket ownership are process-local. Only one server
  replica is supported; there is no distributed relay/hub or HA session failover.
- **Media engine (built):** a CGO-free **sidecar** (`cmd/rmm-remote`) that the
  agent spawns on demand. **Windows** captures via **GDI BitBlt** + **SendInput**;
  **Linux** captures via pure-Go **X11** `GetImage` — using **MIT-SHM** (shared
  memory) when the server is local, so the framebuffer isn't marshaled over the
  socket each frame, with an automatic fall back to plain `GetImage` if SHM isn't
  available — plus **uinput/XTEST** input injection (`media_linux.go` /
  `x11_linux.go`) — the same native technique, no fork. (Unlike XDamage, MIT-SHM
  works under a compositor such as GNOME/mutter, since it shares GetImage's
  server-side read path.) macOS uses an experimental CoreGraphics engine that
  requires CGO (see *macOS engine* below). The viewer is
  a plain browser client (`openrmm/src/screens-devices.jsx`, also openable in a new
  tab). A **multiplexed protocol** rides one WebSocket: binary `0x01`=video frame,
  `0x02`=file chunk; JSON text for input, chat, file ops, `{t:"kf"}` (the
  viewer asks for a keyframe on (re)connect), and `{t:"cad"}` (Windows secure
  attention sequence).
- **Codec — tile-delta (CGO-free, OS-agnostic):** `encoder.go` splits each
  captured RGBA frame into a grid of 64-px tiles and JPEG-encodes only the tiles
  that changed since the previous frame; a keyframe sends the whole screen as
  horizontal **stripes** (parallel-encoded across cores, painted progressively —
  no whole-screen single-JPEG stall). High-motion frames with many changed tiles
  also encode in parallel. Idle/static screens cost almost nothing on the wire,
  so the frame rate rises (20 fps) without saturating the link. Because the
  encoder works purely on the pixel buffer, **any per-OS capturer feeds the same
  codec** — no fork. The payload is `[flags][frameW][frameH][tileCount]` then per
  tile `[x][y][w][h][jpegLen][jpeg]` (big-endian); the viewer composites tiles
  onto a `<canvas>`. Unit-tested in `encoder_test.go` (keyframe, no-change → no
  send, changed-tile localization, forced keyframe, keyframe striping).
- **Latency control (both OSes):** the capture loop is **adaptive** — it runs at
  the active rate while the screen changes or input arrives and drops to an idle
  rate after ~2s of quiet, restoring instantly on activity. When the link can't
  drain the outbound queue, capture ticks are **skipped before encode**
  (backpressure) so latency never accumulates — a skipped tick just folds into
  the next tile diff. On Linux, **input is injected on its own goroutine** when
  `/dev/uinput` is available, so the cursor keeps responding even during a heavy
  encode; file-transfer chunks queue on a **separate lane with lower priority**
  than video, so a bulk download can't freeze the screen. Tunables (env of the
  **agent**, inherited by the sidecar): `RMM_REMOTE_FPS` (default 20),
  `RMM_REMOTE_IDLE_FPS` (4), `RMM_REMOTE_QUALITY` (65), `RMM_REMOTE_TILE` (64) —
  e.g. WAN profile `RMM_REMOTE_FPS=12 RMM_REMOTE_QUALITY=50`.
- **MIT-SHM resilience:** a SHM capture failure no longer disables SHM for the
  process lifetime — it falls back to socket `GetImage` for a 5-minute cooldown
  and then retries, so a transient failure (e.g. during a session switch) doesn't
  condemn a long session to the slow path.
- **Bidirectional free-path file transfer** with a two-pane file manager
  (browse / create folder / copy / cut / delete / upload / download) works on
  **both Windows and Linux** (`transfer_windows.go` / `transfer_linux.go`); the
  browser file browser is path-aware per host OS (Windows `\`+drive list vs POSIX
  `/`). Open it from **Remote ▸ File transfer** or the **Files** button in the
  viewer; both require `devices.files`.
- **Viewer controls:** the browser shows the received screen resolution, offers
  fit-to-window and 50/75/100/125% display scaling, and can enter the browser's
  real full-screen mode. Scaling is viewer-only and never changes the endpoint's
  display settings.
- **Windows extras:** a dedicated **Ctrl+Alt+Delete** button uses Windows
  `SendSAS` **inside the agent service**, through a local SYSTEM-only named-pipe
  broker. Calling it from the sidecar directly does not satisfy the
  service/UIAccess requirement. The broker impersonates the pipe client only to
  authenticate it, and **reverts before sending**: `SendSAS(FALSE)` is the
  documented service path and Windows judges "is this a LocalSystem service" by
  the calling thread, so sending while impersonating a token from the
  interactive session produces nothing at all — acknowledged request, no
  security screen. The agent configures software SAS for services only when the
  policy is absent; explicit administrator choices are preserved. An
  acknowledgment means the request was dispatched, not that Windows confirmed
  the screen switch (the API returns void). Capability/error details are sent in
  the sidecar `hello` message. Also included: **in-session chat** (native dark
  Win32 window on the host, rounded/minimizable), **UAC handling** (sidecar runs
  as SYSTEM in the active console session via `CreateProcessAsUser`;
  `PromptOnSecureDesktop=0`),
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
  isn't available. **In-session chat** also works on Linux via a **native X11
  window** (`chat_linux.go`) — a CGO-free, toolkit-free window drawn with core-X
  requests on its own X connection, so it works on any X server the sidecar
  reaches (the user session and the display-manager greeter), mirroring the
  Win32 chat window. The host user submits a reply with **Enter** (Windows and
  Linux); the Linux window is **drag-to-move** from anywhere (many WMs give it no
  title bar).
- **Linux shortcut:** Ctrl+Alt+Delete is dispatched as an ordered key chord on
  the input worker. Its action depends on the desktop's shortcut configuration;
  it is not the Windows security screen and may open a logout dialog.
- **Chat roles:** the console and native Windows, Linux and macOS windows label
  technician/user messages separately and use purple/green native text. Windows
  uses Unicode RichEdit history and a UI-thread message queue; reply text has
  heap-owned storage across Win32 callbacks. Linux/macOS refresh the sidecar
  before launch, just like Windows, so the first new session uses the update.
- **Input lock (Windows):** the **Lock input** button in the viewer blocks the
  host's **physical** keyboard and mouse for the duration of the session, so the
  person at the machine can't fight the technician. It uses system-wide
  low-level hooks (`WH_KEYBOARD_LL`/`WH_MOUSE_LL`, `inputlock_windows.go`) that
  swallow real events while letting **injected** input (the technician's
  `SendInput`, flagged `LLKHF_INJECTED`) through. The hook thread follows the
  active input desktop just like capture, so the lock holds across the **user
  desktop, the UAC/secure desktop and the Winlogon login screen**. The host is
  told via the chat window (transparency), and the lock is always released on
  release/disconnect/session-end (and reset to off on reconnect). **Not covered:**
  a **locally typed** `Ctrl+Alt+Del` (the Secure Attention Sequence) is a kernel
  trusted path no user-mode hook can intercept — suppressing it would need the
  Enterprise/IoT Keyboard Filter driver and is intentionally out of scope. This
  is separate from the technician's viewer button, which sends SAS through the
  OS-supported `SendSAS` path.
- **Input lock (Linux):** same **Lock input** button; the sidecar grabs every
  physical keyboard/pointer via the evdev **`EVIOCGRAB`** ioctl
  (`inputlock_linux.go`), making itself the exclusive consumer so the local user
  is locked out while the technician's **uinput/XTEST** injection (a separate
  path) still works. Devices are picked from `/proc/bus/input/devices`
  (keyboards + mice/touchpads; power/lid buttons skipped). Because the grab is in
  the kernel input layer it is independent of the active X server, so it holds
  across the seat-following greeter↔session transitions and **covers the login
  screen** with no re-arming. Failsafe: the kernel frees the grab when the fd
  closes, so input is restored on release/disconnect/session-end (or if the
  sidecar dies). Requires the sidecar to run as **root** (it does).
- **Still maturing:** tiles are JPEG-encoded (works everywhere, no codec deps)
  rather than a real video codec (VP8/9/AV1) — fine for desktops, heavier for
  full-motion video. The Linux engine assumes a TrueColor little-endian X server
  and captures the active screen; **login-screen capture requires an Xorg greeter**
  (set GDM `WaylandEnable=false`, or use LightDM) — a Wayland greeter only exposes
  Xwayland, which can't capture native Wayland surfaces. End-to-end media
  encryption is still open (today the relay is the trust boundary; terminate
  WSS — see `07`).

### Multiple monitors

Windows and Linux stream **every monitor at once**, as one image. Windows
captures the virtual desktop (`SM_XVIRTUALSCREEN` … `SM_CYVIRTUALSCREEN`) and
Linux the X screen, which RandR has already stitched. Pointer input matches:
coordinates are normalized across that whole rectangle and injected with
`MOUSEEVENTF_VIRTUALDESK`, so clicks land on the right monitor.

**Picking one display.** The sidecar announces the host's displays with
`{"t":"monitors","list":[{id,w,h,primary}],"selected":N}` — sent once the
capture geometry is known and again whenever it changes, so a desktop switch or
a hot-plug refreshes it. The list is omitted for a single display, and the
viewer only shows its selector when there are two or more. `{"t":"mon","id":N}`
selects one: `0` is the stitched whole desktop, `1..N` a display, ordered left
to right so the viewer's index and the captured region cannot disagree.

Switching rebuilds the capture buffer and forces a keyframe, because the frame
size changes and the encoder keys its tile diff on it. Pointer coordinates stay
normalized against whatever is being captured, so each capturer maps them back
before injection — Windows onto `MOUSEEVENTF_VIRTUALDESK`'s virtual-desktop
space, Linux onto the X screen. Getting that wrong sends clicks to the wrong
display, which is why the Linux path is verified against a pixel comparison of
the region capture with the corresponding half of a full-screen frame.

An index that no longer exists (display unplugged mid-session) falls back to the
whole desktop rather than failing the session.

**macOS selects, but never stitches.** `CGDisplayCreateImage` works one display
at a time, so there is no equivalent of the whole-desktop frame: the host sends
`"all": false` with its list and the viewer drops the *All displays* option
rather than offering something the endpoint cannot do. Selection itself works
the same way, over `CGGetActiveDisplayList`.

Pointer input there goes through the display's bounds rather than the captured
image, which also settles the points-vs-pixels question the capturer carried as
a to-do: `CGEventPost` works in points, a Retina display reports twice as many
pixels as points, and a second display does not start at the origin. Multiplying
the normalized coordinate by the image size was right only for a non-Retina main
display.

The darwin engine is CGO and compiles only on a Mac; the macOS CI job is where
it is built.

### macOS engine (experimental, CGO)

Unlike Windows (GDI) and Linux (X11), macOS has **no CGO-free capture path** —
screen capture (`CGDisplayCreateImage`) and input (`CGEvent`) only exist in
Apple's C frameworks. So the macOS sidecar (`media_darwin.go`) is the **one place
that uses CGO**, isolated by a `//go:build darwin` tag: Linux/Windows builds never
see it and stay CGO-free and cross-compilable. It feeds the same tile-delta codec
and shares the POSIX file transfer (`transfer_posix.go`).

- **Build it on a Mac** (CGO needs Apple's frameworks/clang):
  `make remote-dist-darwin` (`CGO_ENABLED=1`). The Linux/Windows `remote-dist`
  no longer cross-builds darwin.
- **Permissions** (TCC, enforced even for root): the binary needs **Screen
  Recording** (capture) and **Accessibility** (input) granted in System Settings ▸
  Privacy & Security. Without them, capture is blank / input is dropped.
- **Chat and shortcut (sidecar 0.11.0+):** Cocoa chat supports Unicode, replies via
  Enter/Send, and distinct technician/user colors. AppKit runs on the initial
  process thread; a LaunchDaemon launches desktop mode in the console user's
  Aqua session and identity. File-only sessions remain non-graphical. The viewer
  offers **⌘⌥Esc** (Force Quit Applications), gated on Accessibility permission,
  rather than a Windows-specific CAD/UAC action. Full screen and viewer scaling
  are shared with the other platforms.
- **Not yet covered**: login-window capture (needs entitlements).
- **Status**: first implementation; validate capture, Retina coordinate scaling,
  and the keycode map on real hardware.
- Release CI currently builds the arm64 sidecar on macos-14, but does not Apple-code-sign,
  notarize or staple the sidecar. Gatekeeper/TCC approval remains an operator
  concern; OpenRMM's Ed25519 update signature is a separate trust mechanism.
- macOS source/native CI changes do not update an existing `/dl/` binary by
  themselves. Compile on a Mac with the compatible SDK, verify on a logged-in
  device, then publish the binary and detached signature together. A Linux-only
  test deployment must not claim that the old macOS sidecar includes new chat.

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

### When no screen arrives at all

The sidecar is the only peer that ever reaches the relay, so if the agent cannot
prepare or launch it, nothing connects: the viewer opens, reports *streaming*
(that is only its own socket), and shows a black canvas until the relay's
pairing timeout. Two things make that diagnosable:

- The agent **tells the viewer why**, taking the sidecar's place on the relay
  for a single `{"t":"notice"}` message — "this endpoint could not prepare the
  remote module: …". The console shows it as a banner plus one toast (repeated
  reconnects don't re-toast the same text).
- On **Windows** the agent keeps `agent.log` next to `rmm-agent.exe`
  (`C:\ProgramData\rmm-agent`), rotating once at 4 MB. A Windows service has no
  stderr, so before this every `log.Printf` — including the reason a session
  never started — was discarded. systemd and launchd already capture the
  daemon's output on the other platforms.

The sidecar keeps its own `rmm-remote.log` in the same directory; the agent
re-downloads `rmm-remote` from `/dl/` whenever it is missing or its ETag moved,
so deleting the binary is a safe way to force a clean copy.

**Antivirus quarantine (the usual Windows cause).** The agent downloads the
module to `rmm-remote.exe.new` and runs it once (`-print-version`) to validate
it. An unsigned executable written by a service and then executed is a textbook
heuristic trigger: Windows Defender's cloud verdict arrives *after* the first
copy has already run, so the first session works and every later one has its
download deleted mid-flight. The agent now says so instead of failing with a
bare "file not found". Confirm it on the endpoint with

```powershell
Get-MpThreatDetection | Select-Object -Last 5 InitialDetectionTime,Resources
```

and clear it with the bundled, idempotent
[`scripts/windows-defender-exclusion.ps1`](../scripts/windows-defender-exclusion.ps1)
— it excludes only the agent directory (no process exclusion, and it never
disables real-time protection), prints every action, reverts with `-Remove`, and
says so plainly when Intune or Group Policy owns the exclusion list. Push it
through the RMM as a Script-library job to cover the fleet. On a new endpoint,
`install.ps1 -DefenderExclusion` does the same at install time; it is opt-in,
because an antivirus exclusion is the operator's decision to make.

A copy already quarantined is cleared with `Remove-MpThreat` or from Defender's
protection history; the agent re-downloads the sidecar on the next remote
session, with no service restart.

The durable fix is **Authenticode-signing** the Windows binaries — OpenRMM's
Ed25519 update signature protects the update channel but means nothing to
Defender, which reads the PE signature, reputation and behaviour. Signing is
tracked separately; the exclusion is what makes the current, Ed25519-signed
distribution work.

## The "own remote tool" plan

The chosen direction is a **homegrown, CGO-free engine**: native per-OS capture
(Windows GDI + Linux X11 today) + input injection feeding one shared tile-delta
codec (`encoder.go`) — a single static binary with no third-party fork and no
extra licensing constraints (the project ships open source).

The earlier (abandoned) plan to fork an existing remote tool, and its licensing
analysis, are kept only as historical context in
[`12-native-remote-fork.md`](12-native-remote-fork.md).

If the native tool underperforms, remove the provider — MeshCentral remains.

## Choosing a default

`RMM_REMOTE_PROVIDER` sets the default (`native` or `meshcentral`). Reasonable
path: ship with `meshcentral` (fast, Apache, in-browser) and graduate to
`native` as its media engine matures.
