# 05 — Agent Protocol

The agent maintains one persistent WebSocket to `/ws/agent`. Every frame is a
JSON `Envelope`:

```json
{ "type": "<message type>", "data": { ... } }
```

Defined in `internal/protocol`.

## Handshake

1. Agent → `register`:
    - Explicit one-shot enrollment (`-enroll-only`): `{ "enroll_token": "...", "hostname", "os", "arch", "version" }`
   - Later: `{ "agent_id": "...", "agent_token": "...", "hostname", ... }`
2. Server validates (consumes the enroll token, or verifies the token hash) and
   replies:
    - `registered` → `{ "agent_id": "...", "agent_token": "...", "cancel_credential_rotation_id?": "..." }`
      (the token is only returned on first enrollment; the agent persists it)
   - or `error` → `{ "error": "..." }` and closes.

The agent stores `{agent_id, agent_token}` in its state file (`-state`, default
`agent-state.json`, mode 0600) and re-authenticates with it on reconnect. A
long-lived service never enrolls from `-enroll-token`; installers run
`-enroll-only` before creating the service so a deleted state file cannot reuse a
bootstrap token.

Production installers first perform this one-shot enrollment, then install the
long-lived service without the enrollment token in its arguments. They require
an HTTPS origin, OpenSSL 3, and `--update-public-key`/`-UpdatePublicKey`, and set
`-require-tls -update-verify strict`. Plain HTTP or missing update trust is
available only through explicit lab mode.

## Agent → server messages

| type | data | effect |
|------|------|--------|
| `heartbeat` | `{}` | refresh `last_seen`, mark online |
| `inventory` | `{ "data": {...} }` | upsert the inventory snapshot |
| `job_result` | `{ job_id, exit_code, stdout, stderr, success }` | finalize the job (server replies `job_result_ack`) |
| `patch_report` | `{ os, patches:[{identifier,title,classification,kb,reboot_required,...}] }` | upsert the device's missing patches ([16](16-patch-management.md)) |
| `patch_result` | `{ request_id, results:[{identifier,success,reboot_required,error}], reboot_required }` | record an install run |
| `remote_status` | (reserved) | native remote-session state |
| `credential_rotated` | `{ rotation_id }` | confirm durable replacement storage; replacement-token reconnect commits it |
| `credential_rotation_canceled` | `{ rotation_id }` | confirm durable restoration; re-sent until `credential_rotation_canceled_ack` arrives |

The reference agent sends a heartbeat every 20s and inventory every 5min (and
once on connect).

## Server → agent messages

| type | data | effect |
|------|------|--------|
| `job` | `{ job_id, type, shell, script }` | run it, reply with `job_result` |
| `job_result_ack` | `{ job_id }` | confirm a `job_result` was durably recorded; the agent drops it from its retry outbox |
| `remote_start` | `{ session_id, agent_ticket, kind, allow_files }` | connect a remote module to the relay; the agent derives `/ws/remote` from its own control WebSocket origin |
| `credential_rotate` | `{ rotation_id, agent_token }` | durably store the replacement credential, then reconnect with it |
| `credential_rotation_cancel` | `{ rotation_id }` | durably restore the prior credential and confirm it |
| `credential_rotation_canceled_ack` | `{ rotation_id }` | server recorded restoration; drop the persisted confirmation retry |
| `patch_scan` | `{}` | scan for missing OS patches, reply with `patch_report` |
| `patch_install` | `{ request_id, identifiers }` | install only the explicit approved identifiers; an empty list is rejected, reply with `patch_result` |
| `reboot` | `{ delay_seconds }` | reboot the host |

`kind` selects which module the agent attaches: `"desktop"` (or empty) spawns the
media sidecar (screen + input); `"files"` spawns the same sidecar in
**file-transfer-only mode** (no screen capture); `"terminal"` opens an **elevated
interactive shell** in the agent process (real PTY on Unix / ConPTY on Windows)
and bridges it to the relay. See [08-remote-access](08-remote-access.md).

### Credential rotation

Credential rotation and cancellation require agent version `0.4.5` or later. Only one
server-initiated rotation may be pending for an endpoint. On receiving
`credential_rotate`, the agent atomically writes the replacement token and its
`rotation_id`, acknowledges durable persistence, and reconnects with the
replacement; that reconnect is the commit point. The server first persists a
cancellation fence before sending `credential_rotation_cancel`, so an ACK or
replacement-token reconnect cannot win after cancellation starts. The agent
persists its restoration confirmation and retries it after reconnects until the
server replies with `credential_rotation_canceled_ack`. A reconnect while a
cancellation is pending carries `cancel_credential_rotation_id` in `registered`,
so the agent restores its prior credential before clearing local rotation state.
A different later `credential_rotate` implicitly confirms the preceding
cancellation, because the server cannot stage it until the older rotation is gone.

### Relay rollout compatibility

Deploy the server first, publish the ticket-aware sidecar, then update agents.
Current peers use the session id plus a handshake ticket. During rollout only, an
agent can launch a cached pre-ticket sidecar through the **agent-only** legacy
relay path when `RMM_ALLOW_LEGACY_AGENT_RELAY=true`; browser viewers never use
this fallback and never place a ticket in a URL. Set the flag back to `false`
after the fleet has the ticket-aware sidecar.

## Reliability

- The server sets a 90s read deadline and sends WebSocket pings every 30s; the
  agent's pong resets the deadline. Dead links are detected and cleaned up.
- Approved jobs for an offline agent stay `pending` and are delivered on
  reconnect (`DispatchPending`).
- **Job results are delivered at-least-once.** The agent keeps each `job_result`
  in a disk-backed **outbox** and re-sends it on every (re)connect and on the
  heartbeat timer until the server replies `job_result_ack`. This means a
  connection dropped between execution and delivery — or an agent restart — can't
  strand a finished job in `sent`; the result is redelivered and the job
  finalizes automatically. The server's handling is idempotent (a re-sent result
  for an already-terminal job is a no-op but is still acked), so duplicates are
  harmless. (For a result that never arrives at all, an operator can still force
  the outcome via manual complete — `POST /api/jobs/{id}/complete`, see
  [04-api-reference](04-api-reference.md).)
- The agent reconnects automatically with a 5s backoff.

## Self-update

The agent periodically checks `/dl/rmm-agent-<os>-<arch>` (HEAD, ETag) and, when
the published build changes, downloads it, verifies its detached Ed25519
signature when configured, validates it, atomically swaps the binary, and reloads
(re-exec on Unix; a detached service restart on Windows). Disable with
`RMM_AUTO_UPDATE=0`; tune the cadence with `RMM_UPDATE_INTERVAL` (default 30m).
Use `RMM_UPDATE_VERIFY=strict` and `RMM_UPDATE_PUBLIC_KEY` for production. See
[02-getting-started](02-getting-started.md#updating-agents-self-update) and
[13-security-rollout](13-security-rollout.md).

The installer verifies both the signed `SHA256SUMS` manifest and the selected
artifact signature. Runtime self-update verifies the artifact's detached
signature. The configured public key must match the release signing key; a
checksum fetched from the same untrusted origin is not sufficient by itself.

## Script execution

The agent maps `shell` to an interpreter:

| shell | command |
|-------|---------|
| `powershell` | `powershell -NoProfile -NonInteractive -Command <script>` |
| `cmd` | `cmd /C <script>` |
| `sh` | `sh -c <script>` |
| `bash` (default) | `bash -c <script>` |

Execution is bounded by a 10-minute timeout; stdout/stderr and the exit code are
returned.
