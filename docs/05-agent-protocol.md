# 05 — Agent Protocol

The agent maintains one persistent WebSocket to `/ws/agent`. Every frame is a
JSON `Envelope`:

```json
{ "type": "<message type>", "data": { ... } }
```

Defined in `internal/protocol`.

## Handshake

1. Agent → `register`:
   - First run: `{ "enroll_token": "...", "hostname", "os", "arch", "version" }`
   - Later: `{ "agent_id": "...", "agent_token": "...", "hostname", ... }`
2. Server validates (consumes the enroll token, or verifies the token hash) and
   replies:
   - `registered` → `{ "agent_id": "...", "agent_token": "..." }`
     (the token is only returned on first enrollment; the agent persists it)
   - or `error` → `{ "error": "..." }` and closes.

The agent stores `{agent_id, agent_token}` in its state file (`-state`, default
`agent-state.json`, mode 0600) and re-authenticates with it on reconnect.

## Agent → server messages

| type | data | effect |
|------|------|--------|
| `heartbeat` | `{}` | refresh `last_seen`, mark online |
| `inventory` | `{ "data": {...} }` | upsert the inventory snapshot |
| `job_result` | `{ job_id, exit_code, stdout, stderr, success }` | finalize the job |
| `patch_report` | `{ os, patches:[{identifier,title,classification,kb,reboot_required,...}] }` | upsert the device's missing patches ([16](16-patch-management.md)) |
| `patch_result` | `{ request_id, results:[{identifier,success,reboot_required,error}], reboot_required }` | record an install run |
| `remote_status` | (reserved) | native remote-session state |

The reference agent sends a heartbeat every 20s and inventory every 5min (and
once on connect).

## Server → agent messages

| type | data | effect |
|------|------|--------|
| `job` | `{ job_id, type, shell, script }` | run it, reply with `job_result` |
| `remote_start` | `{ session_id, token, relay_url, kind }` | connect a remote module to the relay |
| `patch_scan` | `{}` | scan for missing OS patches, reply with `patch_report` |
| `patch_install` | `{ request_id, identifiers }` | install patches (empty = all approved), reply with `patch_result` |
| `reboot` | `{ delay_seconds }` | reboot the host |

`kind` selects which module the agent attaches: `"desktop"` (or empty) spawns the
media sidecar (screen + input); `"files"` spawns the same sidecar in
**file-transfer-only mode** (no screen capture); `"terminal"` opens an **elevated
interactive shell** in the agent process (real PTY on Unix / ConPTY on Windows)
and bridges it to the relay. See [08-remote-access](08-remote-access.md).

## Reliability

- The server sets a 90s read deadline and sends WebSocket pings every 30s; the
  agent's pong resets the deadline. Dead links are detected and cleaned up.
- Approved jobs for an offline agent stay `pending` and are delivered on
  reconnect (`DispatchPending`).
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
