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
| `remote_status` | (reserved) | native remote-session state |

The reference agent sends a heartbeat every 20s and inventory every 5min (and
once on connect).

## Server → agent messages

| type | data | effect |
|------|------|--------|
| `job` | `{ job_id, type, shell, script }` | run it, reply with `job_result` |
| `remote_start` | `{ session_id, token, relay_url }` | connect the remote module to the relay |

## Reliability

- The server sets a 90s read deadline and sends WebSocket pings every 30s; the
  agent's pong resets the deadline. Dead links are detected and cleaned up.
- Approved jobs for an offline agent stay `pending` and are delivered on
  reconnect (`DispatchPending`).
- The agent reconnects automatically with a 5s backoff.

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
