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
- **Seamless login-token embed**: when a token key is
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
  server-side read path.) macOS uses a CoreGraphics engine that requires CGO
  (see *macOS engine* below). The viewer is
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
  technician/user messages separately and use purple/green native text; a
  guest's lines (below) are amber. From sidecar 0.18 the person at the machine
  is named by their account instead of "You" in the window and of the machine
  name in the console. The hello carries it as `user`. On Windows it is the
  user signed in to the session (the sidecar runs as SYSTEM there). On macOS
  it is the process's account. On Linux it is the owner of the active desktop's
  Xauthority file (GDM), else the user session logind has active on seat0
  (`loginctl`; LightDM keeps the file as root). A service account and a login
  screen give no name, and the old labels stay. Windows
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

### One desktop session per operator per device

Several operators may be on the same device at once — each is told who else
is there — and one operator may work on several devices at once. What is
refused is the **same operator twice on the same device's desktop**: that is a
forgotten tab, whose mouse and keyboard fight the tab in use while the endpoint
captures the screen twice.

- `POST /api/devices/{id}/remote` with `provider: native` answers **409**
  `{"code": "remote_session_active"}` when the operator already has a desktop
  session there. The console then offers **Continue here**, which repeats the
  request with `replace: true`: the old session ends, its tab shows "session
  moved" and does not reconnect, and `remote.replace` is written to the audit
  log.
- A viewer whose own connection dropped reconnects with `resume: <its last
  session id>` and takes that session back without asking; naming any other
  session does not get past the rule.
- The relay repeats the check when the viewer socket joins, under one lock, so
  two tabs connecting at the same moment cannot both get in. A refused socket is
  closed with code **4409**, a replaced one with **4410**; the console stops
  reconnecting on either.
- Terminal and file-transfer sessions are not limited: several shells on one
  machine are ordinary.

### Guest access (one-time link and code)

A technician can let someone without an account see a device's screen, or
control it, for a limited time: a vendor's support engineer, the customer's own
IT person. The guest is always supervised: they get in only when the
technician who invited them accepts them from that technician's own open
session, and they leave when that session ends.

**Turning it on.** Settings ▸ Remote access ▸ **Guest access** (off by
default, needs `settings.manage`). A technician then needs `devices.remote`
and **`devices.remote.share`**, which only the built-in administrator role has
by default; custom roles opt in. Turning the setting off revokes every open
invitation and ends every guest session at once.

**Inviting.** Device ▸ Remote ▸ **Share access…**:

- choose **View only** or **View and control**, and a validity of 15 minutes,
  1, 4, 8 or 24 hours;
- by default a guest who closes the page by mistake may come back with the
  same link and code until the invitation expires (see below). **Allow only
  one use** makes the link single-use instead. Over the API, `reusable` in the
  create request turns coming back on; it is off when omitted;
- the dialog shows, once, a link `https://<console>/join#<token>` (a 256-bit
  token in the URL fragment, so it never reaches access logs or a `Referer`)
  and a six-digit code. Send the code through another channel than the link.
  Only hashes of the token and the code are stored;
- the same dialog lists your open invitations on the device, with **Revoke**
  (or **Remove** for a guest who is in).

**The guest.** The link opens a page with no sign-in. It names the technician
and nothing about the device. The guest types their name and the code:

- the first correct code burns the link, whatever the technician decides next;
  a second visitor with the same link and code gets nothing;
- unless the invitation allows coming back, as the console does by default.
  Then the code works again while
  the invitation is waiting or accepted and not expired. Each use issues a new
  credential, ends the previous one and its session, and waits for the
  technician again: the banner says the guest wants to join *again*. One guest
  at a time per invitation, and a rejected, revoked or removed invitation is
  over either way. Whoever holds the link and the code can therefore replace
  the guest, but never gets in without the technician. The public IP address
  is shown, not trusted: many guests share one behind carrier-grade NAT;
- five wrong codes revoke the link. The link and code endpoints share the login
  rate limiter;
- the guest then waits. The page holds only a credential bound to this one
  request, in that browser tab.

**Accepting.** While a guest waits, the technician's **own desktop session on
that device** shows a banner at the top: name, IP address and mode, with
**Accept** and **Reject**. Only the technician who sent the invitation sees it
and decides, and accepting needs that session open at that moment. Once in,
the guest appears as a chip with **Remove**, and in the device's presence as
"Guest <name> (via <technician>)".

**What a guest can do.** The guest gets a session of their own (their own relay
pair and sidecar, never the technician's stream), and the relay filters every
frame of it in both directions (`internal/remote/guest.go`):

- to the device: keyframe requests and the monitor choice; in *view and
  control* mode, also mouse and keyboard. Everything else is dropped: file
  transfer, clipboard, chat, Ctrl+Alt+Delete, UAC, the input lock;
- to the guest: the screen, and the `hello`, `monitors` and `notice` messages
  that describe it, and what the person at the machine writes in the chat. The
  endpoint's clipboard and files never reach them.

The filter is the guarantee, so it holds whatever version the endpoint's
sidecar runs.

**Chat.** The technician, their guests and the person at the machine share one
conversation, although each guest's session is separate
(`internal/api/api_guest_chat.go`). The server carries chat lines between the
technician's session on the device and the sessions of the guests they let
onto it:

- a guest's line goes to the technician's session, kept on one line: into the
  chat window on the device, and to the technician's viewer under the guest's
  name. A sidecar that announces `guest_chat` in its hello (0.18 and later)
  shows it as `<name> (guest): …` in a colour of its own. An older one gets
  `{from: "tech"}` with the name in the text, and shows
  `Technician: <name> (guest): …`. It never goes to the guest's own session on
  the device. A guest writes at most one line every 300 ms;
- what the technician writes, and what the person at the machine writes, reaches
  every guest of that technician on that device.

The guest's session opens no chat window of its own on the device (sidecar
0.18 and later, through the agent's `-no-chat`). An older sidecar still opens a
second window. What the person types there reaches the technician and the
guests too.

**When it ends.** The guest's socket is closed with a code their page explains:

| Code | Why |
|---|---|
| 4411 | removed by the technician, invitation revoked, or guest access turned off |
| 4412 | the technician's own session on the device ended; the guest's page waits, and reconnects when the technician is back, until the invitation expires |
| 4413 | the invitation expired |

The technician's permissions and device scope are rechecked when the guest is
accepted and every time the guest connects, so a change made after the
invitation was sent is not outlived by it.

**API.**

| Route | Who | Purpose |
|---|---|---|
| `POST /api/devices/{id}/remote-shares` | `devices.remote` + `devices.remote.share`, in scope | Create: `{mode, minutes}` → the share, `token` and `code` (once) |
| `GET /api/devices/{id}/remote-shares` | same | The caller's live invitations on the device |
| `GET /api/remote-shares/pending?agent_id=` | same | What the open session polls: guests waiting, and guests in |
| `POST /api/remote-shares/{id}/accept` · `/reject` | the creator; accept needs their live desktop session | Decide |
| `DELETE /api/remote-shares/{id}` | the creator, or `users.manage` | Revoke, or remove a guest who is in |
| `POST /api/guest/lookup` | public, login rate limit | `{token}` → the technician's name |
| `POST /api/guest/join` | public, login rate limit | `{token, code, name}` → `{request_id, guest_credential}` |
| `GET /api/guest/request` | `Authorization: Guest <credential>` | The decision, and whether the technician is present |
| `POST /api/guest/session` | guest credential | Brokers the guest's session; the viewer then presents `rmm-guest.<credential>` with its ticket on `/ws/remote` |

Every step is in the audit log: `remote.share.create`, `.code_failed`,
`.redeem`, `.accept`, `.reject`, `.revoke`, `.remove`, `.guest_start`,
`.guest_end`, with the guest's name, IP and user agent where known.

**Deployment.** The guest reaches `/join`, `/api/guest/*` and `/ws/remote` on
the **console** origin. A console behind an IP allowlist (for example
`CONSOLE_ALLOWED_RANGES` in the Caddy setup) or an access proxy has to admit
those paths for guests to get in. OpenRMM Cloud consoles are public and need
nothing.

Not yet: a notice to the device's local user that a guest is watching (needs a
sidecar release), sending the invitation by email from OpenRMM, and an
administrator-wide list of open invitations. Terminal and file transfer for
guests are not planned: both amount to running code on the device.

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

### macOS engine (CGO)

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
  Privacy & Security. Without them, capture is blank / input is dropped. The
  request runs on AppKit's main thread so the native consent sheet belongs to
  the logged-in desktop. When macOS does not show that sheet again after a
  denial, OpenRMM displays its own host-side alert with buttons that open the
  exact Screen Recording and Accessibility panes.

  **One prompt at a time (sidecar 0.17.0+).** macOS shows a single privacy
  prompt at once and silently drops a request made while another is on screen:
  no prompt, no row in the privacy database, and the app never appears in the
  Accessibility list. Asking for both back to back therefore left a first
  session with Screen Recording allowed and nothing to allow for Accessibility
  until it was reopened (seen on macOS 15.8). The sidecar now asks for
  Accessibility only once Screen Recording is allowed — or after a minute, since
  a dismissed prompt leaves nothing to observe.

  Both grants reach the running process, measured on macOS 15.8: capture
  resumed seconds after Screen Recording was allowed (the viewer's banner is
  withdrawn), and mouse and keyboard worked in the same session right after
  Accessibility was. What does **not** update is `CGPreflightPostEventAccess`:
  in the process that asked, it kept answering "denied" half an hour later.
  Command-V and ⌘⌥Esc used to be gated on it and failed in exactly that session
  while typing worked, so nothing is gated on it any more — the events are
  always posted and the check only goes to the log. No session has to be
  reopened after a permission is allowed.

  **Where to look.** The sidecar logs to `~/Library/Logs/rmm-remote.log` of the
  console user (capped at 5 MB, one previous file kept as `.1`). Each session
  opens with the build, the macOS release and the bundle's code identity
  (certificate or ad-hoc), then the permission state and every request and
  grant. A failure that repeats on every frame is written once and summarised
  each minute, with a line when it stops. The agent's own log
  (`/usr/local/libexec/openrmm/agent.log`, root only) records each sidecar
  refresh: the ETags, the download, and the identity change with whether the
  privacy grants were reset. A relay dial that fails with `no route to host` is
  the **Local Network** permission, not the network, and the log says so.

  **First session after an update.** Local Network access is enforced per
  executable UUID, which every build changes. The grant survives (nehelper:
  "Local network allowed by preference … Clearing cached UUIDs and restarting
  session"), but the new build's first dial races the cache rebuild — about
  half a second — and was refused, which showed as a black screen until the
  session was reopened. The sidecar now retries that one error up to five
  times over under four seconds; the relay never saw the refused attempt, so
  the single-use ticket is still good. The same update also makes macOS post a
  "background item" notification for the LaunchAgent, whose click opens
  *Login Items & Extensions*; it needs no action. A capture that fails while Screen
  Recording is allowed is logged as the display being asleep or locked, which
  is what it was every time it was investigated; the session also ends with
  `no active display` when it is opened against a sleeping screen.
- **Chat and shortcut (sidecar 0.11.0+):** Cocoa chat supports Unicode, replies via
  Enter/Send, and distinct technician/user colors. AppKit runs on the initial
  process thread; a LaunchDaemon launches desktop mode into the console user's
  Aqua session with `launchctl asuser`, staying root — **not** through `sudo`.
  File-only sessions remain non-graphical. The viewer
  offers **⌘⌥Esc** (Force Quit Applications), gated on Accessibility permission,
  rather than a Windows-specific CAD/UAC action. Full screen and viewer scaling
  are shared with the other platforms.
- **Never put `sudo` in that chain.** TCC judges a request by its *responsible*
  process, and `sudo` is an Apple-signed platform binary: `tccd` then refuses to
  prompt at all — `Platform binary prompting is 'Deny' because: is Platform
  Binary` — so the sidecar is denied and never appears in any Privacy list for
  an operator to allow. On macOS 15 the first thing that fails is not capture
  but **local networking**: the sidecar cannot reach the relay and reports
  `connect: no route to host`, which reads like a routing fault and is not one.
  Measured on 15.8, seconds apart, same binary and same user: through
  `launchctl asuser` it connects; through `launchctl asuser` + `sudo` it does
  not.
- **A server on the local network needs Local Network permission, and a
  command-line tool cannot be given it.** macOS 15 gates access to private
  address ranges behind a per-app grant. The list is populated only by attributed
  requests, and a bare executable never appears there — measured with a
  `CFBundleIdentifier` and `NSLocalNetworkUsageDescription` embedded in
  `__TEXT,__info_plist` and an ad-hoc signature, which is as much identity as a
  non-bundle can carry. The denial surfaces as `connect: no route to host`, which
  reads like a routing fault and is not one.

  This is **specific to a server on a private address**. Through the same
  LaunchAgent, seconds apart, the sidecar reaches a public host and is refused by
  a `10.x` one, so a deployment whose server is on the internet was never
  affected.

  **Fixed by shipping the sidecar as a bundle.** macOS publishes
  `rmm-remote-darwin-<arch>.app.tar.gz`; the agent verifies its signature,
  unpacks it and installs `RMMRemote.app` under
  `/Library/Application Support/OpenRMM`, and the LaunchAgent runs the executable
  inside it. Once bundled, the sidecar appears in Privacy & Security as *OpenRMM
  Remote*, the operator allows it once, and a LAN-hosted server works. Nothing
  short of a bundle achieved this: an ad-hoc signature and the same plist
  embedded in `__TEXT,__info_plist` were both measured and both still left the
  tool absent from the list.

  **Nothing may be written inside the bundle.** A signed bundle seals its
  contents, so a single added file invalidates the signature and with it every
  privacy grant the bundle holds — the sidecar's own log, written next to its
  executable, was enough to put the black screen back after an update. The log
  goes to `~/Library/Logs/rmm-remote.log` on macOS, and the unpack step does not
  restore the archive's numeric ownership.

  **Updates and the Designated Requirement.** macOS records the app's DR when a
  permission is granted and checks whether a new version satisfies it. The
  default ad-hoc DR is tied to one build. Before replacing the bundle, the
  updater compares the staged bundle against the installed one's DR and writes
  the outcome to `agent.log`: an identity change means the operator has to grant
  the permissions again. **The update still goes through** — refusing it would
  freeze the module at the installed version for as long as builds keep changing
  identity, which is a worse trade than one re-grant. The comparison needs
  `codesign`, which ships with the Xcode Command Line Tools rather than with
  macOS, so on a stock Mac it is skipped; the archive's Ed25519 signature is the
  integrity check in every case.

  **Stale grants are cleared, not left to mislead.** A privacy row is keyed by
  bundle identifier, so it outlives the build it was granted to: after an update
  the panes still list the sidecar as allowed while macOS denies it, which looks
  like a broken permission rather than one that has to be granted again. After
  replacing the bundle the agent therefore runs `tccutil reset All
  io.openrmm.remote`, once as root for the system-wide rows (Accessibility) and
  once through `launchctl asuser` for the console user's (Screen Recording), and
  logs it. The rows disappear, and the next session prompts properly. To do it by
  hand on an endpoint:

  ```sh
  sudo tccutil reset All io.openrmm.remote   # system-wide rows
  tccutil reset All io.openrmm.remote        # the logged-in user's rows
  ```

  The reset is skipped only when both bundles carry the same
  `Contents/Resources/StableCodeIdentity`. The builder writes the actual DR into
  that sealed marker when `RMM_MACOS_CODESIGN_IDENTITY` signs the bundle. Moving
  to a different certificate, moving to or from ad-hoc signing, or replacing one
  ad-hoc build with another clears the stale rows.

  **What this costs, and who pays it.** Official builds are ad-hoc signed, so
  every published sidecar is a new application to macOS and the operator is asked
  for Screen Recording and Accessibility again. In steady state a sidecar release
  is rare, which makes this an occasional prompt rather than a routine — but it
  is the behaviour to expect. The updater removes only rows whose recorded code
  identity cannot apply to the new bundle, and the viewer explains how to repair
  a row that macOS still displays as enabled.

##### Signing the macOS sidecar yourself

A self-hosted install downloads the sidecar from **its own** `/dl/`, so whoever
signed that artifact decides the code identity the endpoints record. Three cases,
with different consequences:

1. **You publish the official artifacts unchanged.** Each release carries two
   bundles per architecture, built from the same binary:
   `rmm-remote-darwin-<arch>.app.tar.gz`, signed with the project's
   certificate, and `rmm-remote-darwin-<arch>.adhoc.app.tar.gz`, ad-hoc. Every
   Mac fetches the signed one unless it was installed with
   `install.sh --macos-bundle adhoc`.

   | | `signed` (default) | `adhoc` |
   |---|---|---|
   | Privacy grants across sidecar updates | kept | asked again on every update |
   | Depends on the project's certificate | yes — if it is ever lost or replaced, every Mac on it is asked once | no |

   The choice is written to `/etc/rmm-agent/remote-bundle` and **stays with the
   endpoint**: a reinstall that does not pass `--macos-bundle` keeps it, because
   switching variants is itself an identity change and costs every grant on
   that Mac. To switch deliberately, reinstall with the other value; the
   operator is asked for the permissions once. Linux and Windows accept the
   flag and ignore it, so one install command serves a mixed fleet.
2. **You build the sidecar yourself** (rebranding, auditing, or a patched
   build). Set `RMM_MACOS_CODESIGN_IDENTITY` to a persistent signing identity and
   the grants survive your upgrades, because the DR then follows the certificate
   instead of the build. `scripts/build-macos-bundle.sh` reads that variable and
   records `Contents/Resources/StableCodeIdentity` inside the bundle.
3. **Developer ID**, which only the entity that owns it can use. It is the only
   option that also gives notarization, so a bundle downloaded through a browser
   is not blocked by Gatekeeper.

To create an identity for case 2, on a Mac: Keychain Access ▸ Certificate
Assistant ▸ *Create a Certificate…*, a **self-signed root** of type **Code
Signing**; tick *Let me override defaults* and give it a validity of years —
expiry costs every endpoint a fresh grant. `security find-identity -v -p
codesigning` prints the name to pass in the variable. Export it as a `.p12` and
guard it the way you guard the update-signing key: **losing it re-prompts every
Mac in your fleet**, and unlike the Ed25519 key there is no two-key rotation that
would hide the change from users. Building where the certificate lives is part of
the deal — a CI runner needs the `.p12` as a secret and imports it into a
temporary keychain.

Note what a self-signed identity does **not** buy: no notarization, no Gatekeeper
clearance for a browser download, and no verified publisher name — the Privacy
panes still say the app is from an unidentified developer. It buys exactly one
thing, which is that an update stops looking like a different application.

**The project deliberately does not sign official builds with a self-signed
certificate.** It would spare operators the prompt, but it would put a key whose
loss re-prompts every macOS endpoint of every self-hosted install under the
project's custody, and it would do nothing for anyone building from source. The
intended path for official builds is Developer ID.

  Measured, not assumed: after the first auto-update the endpoint prompted again
  while Privacy & Security still showed the entry as allowed — the list matches
  by path and name, the decision by DR, and the two disagree after a rebuild.
  Toggling the switch off and on re-grants it (`tccutil reset <service>
  io.openrmm.remote` does the same). That is why the updater clears the rows
  itself: the operator is asked once, instead of being shown a permission that
  is already on and still denied.

  **The bundle identifier is `io.openrmm.remote`.** Reverse-DNS names a domain
  the project controls, and the earlier `com.openrmm.remote` did not. The
  identifier is the privacy identity and the LaunchAgent label, so endpoints
  installed under the old name are migrated: the agent (and a reinstall) boots
  out the old job and deletes `/Library/LaunchAgents/com.openrmm.remote.plist`.
  The agent also rewrites its own LaunchAgent plist whenever it no longer
  matches, because agents update themselves and installers do not re-run.

  The old identifier's **privacy rows are not cleared automatically**, and
  never were in practice: `tccutil reset` resolves an identifier through
  LaunchServices and refuses one whose app is gone (`No such bundle identifier`,
  OSStatus -10814). They grant nothing to the current module and only leave a
  dead "OpenRMM Remote" entry in System Settings. Only test machines ran that
  identifier. To clear them anyway, run this as a script job (it touches only
  `com.openrmm.remote`): it registers a placeholder app carrying the identifier
  so `tccutil` can resolve it — `/Applications`, not `/tmp`, which
  LaunchServices did not pick up — resets both databases, and removes it.

  ```bash
  L=com.openrmm.remote
  LS=/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister
  U="$(stat -f %u /dev/console)"; APP=/Applications/.OpenRMMLegacyCleanup.app
  mkdir -p "$APP/Contents/MacOS"
  printf '<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>%s</string><key>CFBundleExecutable</key><string>x</string><key>CFBundlePackageType</key><string>APPL</string></dict></plist>' "$L" > "$APP/Contents/Info.plist"
  printf '#!/bin/sh\n' > "$APP/Contents/MacOS/x"; chmod 755 "$APP/Contents/MacOS/x"
  "$LS" -f -R -trusted "$APP"; launchctl asuser "$U" sudo -u "#$U" "$LS" -f -R -trusted "$APP"
  sleep 2
  tccutil reset All "$L"; launchctl asuser "$U" sudo -u "#$U" tccutil reset All "$L"
  "$LS" -u "$APP"; launchctl asuser "$U" sudo -u "#$U" "$LS" -u "$APP"; rm -rf "$APP"
  ```
  **A Terminal window at every login is not the installer's doing.** macOS runs a
  Unix executable added to Login Items inside Terminal.app, so a bare
  `rmm-agent` or `rmm-remote` listed under *General ▸ Login Items ▸ Open at
  Login* opens a terminal showing its log on every sign-in. Nothing here adds
  login items — the agent is started by launchd from its LaunchDaemon — so an
  entry there was added by hand while debugging and should be removed with the
  **−** button. The separate *Allow in the Background* list is where the real
  daemon and LaunchAgent appear, and both must stay enabled.

#### Clipboard

The technician's clipboard reaches the endpoint on their own paste keystroke.
The viewer listens for the browser's `paste` event, which carries the text with
it — nothing asks for clipboard *read* permission — and sends
`{"t":"clip","text":"…","paste":true}`. The sidecar replaces the endpoint's
clipboard and then presses the paste shortcut **its own platform uses**: ⌘V on
macOS, Ctrl+V on Windows and Linux. Forwarding the technician's raw Ctrl+V as
keystrokes would paste nothing on a Mac, which is the bug this replaces.

For ordinary keyboard shortcuts, the viewer maps the technician's Control key
to Command when the endpoint is macOS. Thus Ctrl+C reaches the Mac as ⌘C. Before
the special paste path runs it releases any forwarded modifier, so ⌘V is not
accidentally posted as Control+Command+V. The macOS pasteboard write completes on
the AppKit thread before ⌘V is serialized with the remaining input events.

Text only, capped at 256 KB: the message shares the session's socket with the
video stream.

**The other direction (sidecar 0.16.0+).** While a session is open the sidecar
reads the endpoint's clipboard every 700 ms and sends each new text value to the
viewer, which writes it to the technician's clipboard, so Ctrl+C on the remote
screen gives them something to paste at home. Same limits: text only, 256 KB. A
mirror remembers what has already crossed in either direction, so a paste is not
read straight back and sent over as if the user had copied it. The browser writes
it only in a secure context (HTTPS) and while the viewer tab has focus.

Be plain about what this is: while a session is open, what the endpoint's user
copies reaches the technician, the same way their screen already does. Nothing is
watched outside a session; the watch stops with it.

On Linux the text is handed to (and read back from) `wl-copy`/`wl-paste`,
`xclip` or `xsel`, whichever is installed — Wayland first, since the X11 tools reach Xwayland's clipboard rather
than the one the compositor's applications read. Owning the X11 CLIPBOARD
selection directly would mean answering selection requests for as long as the
content should live, and the capture loop owns that connection. If none of the
three is installed the paste is refused and logged, not silently dropped.

- **Not yet covered**: login-window capture (needs entitlements).
- **Status**: first implementation; validate capture, Retina coordinate scaling,
  and the keycode map on real hardware.
- Release CI builds **both** sidecars on one macos-14 runner — arm64 natively and
  x86_64 cross-built with `clang -arch x86_64`, since Xcode's SDK is universal —
  packages and ad-hoc signs each `.app.tar.gz`, and verifies with `file` that the
  two slices really are different architectures. A cross-build that silently
  produced two arm64 binaries would ship an Intel release no Intel Mac can start,
  and nothing downstream would notice: the bundle unpacks and the signature
  verifies either way. It does not sign with Developer ID, notarize or staple.
  Gatekeeper/TCC approval remains an operator concern; OpenRMM's Ed25519 update
  signature is a separate trust mechanism.
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

`RMM_REMOTE_PROVIDER` sets the default (`native` or `meshcentral`). The
production compose file defaults to `native`, which needs nothing outside
OpenRMM; the development compose file defaults to `meshcentral`. MeshCentral
remains the choice where you already run it, or for what the native engine does
not cover yet (Wayland sessions, the macOS login window).
