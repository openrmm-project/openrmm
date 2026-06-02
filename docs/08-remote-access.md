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
- Builds a deep link into a configured MeshCentral server for the agent's node.
- **Turnkey embed (TODO):** generate a MeshCentral *login token* server-side and
  wrap its UI in an iframe — exactly how Tactical RMM does it. The deep link is
  in place; the token exchange is the remaining work.
- License: MeshCentral is **Apache-2.0** — no constraints on bundling.
- Best for: instant in-browser sessions and reusing native RDP/VNC via
  MeshCentral Router, with zero extra client install.

### `rustdesk`
- Returns a `rustdesk://connect/<id>` URI that launches the technician's local
  RustDesk client against the agent's RustDesk ID.
- Requires the agent to run RustDesk as a service with a permanent password and
  to report its RustDesk ID (the agent's `rustdesk_id`). Provisioning that is an
  agent-side task (roadmap).
- License: RustDesk is **AGPL-3.0** — see licensing below.
- Best for: superior native session quality without dealing with a web client.

### `native` (built-in)
- A session **broker + relay** built into the server (`internal/remote/relay.go`).
  On `StartSession` it mints a token, pushes `remote_start{token, relay_url}` to
  the agent, and returns a viewer `connect_url`.
- Both peers connect to `/ws/remote?token=...&role=agent|viewer`; the relay pairs
  them by token and pipes frames. This is the **transport plane** — the
  equivalent of RustDesk's `hbbr` relay.
- **What's missing:** the **media engine** (screen capture, encode/decode with
  VP8/9/AV1, input injection) in the agent's remote module and the viewer client.
  That rides on top of this relay.

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

If the native tool underperforms, remove the provider — MeshCentral and RustDesk
remain.

## Choosing a default

`RMM_REMOTE_PROVIDER` sets the default. Reasonable path: ship with `meshcentral`
(fast, Apache, in-browser), add `rustdesk` for quality, and graduate to `native`
once the forked media engine is solid.
