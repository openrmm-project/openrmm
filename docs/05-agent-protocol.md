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
an HTTPS origin and `--update-public-key`/`-UpdatePublicKey` (on Linux and macOS,
OpenSSL 3 or the manifest's P-256 key in `--manifest-public-key`), and set
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
| `check_result` | `{ check_id, execution_id, value, output, success }` | record one check run; the server replies `check_result_ack`. `value` carries the number for threshold checks (free %, load %, exit code), `success` the verdict for boolean ones (reachable / running / exit == 0) |
| `remote_status` | (reserved) | native remote-session state |
| `credential_rotated` | `{ rotation_id }` | confirm durable replacement storage; replacement-token reconnect commits it |
| `credential_rotation_canceled` | `{ rotation_id }` | confirm durable restoration; re-sent until `credential_rotation_canceled_ack` arrives |

The reference agent sends a heartbeat every 20s and inventory every 5min (and
once on connect).

## Server → agent messages

| type | data | effect |
|------|------|--------|
| `job` | `{ job_id, type, shell, script, params? }` | run it, reply with `job_result`. `type` `device_query` runs a built-in query instead of a shell; `params` are a released script's parameters (see below) |
| `job_result_ack` | `{ job_id }` | confirm a `job_result` was durably recorded; the agent drops it from its retry outbox |
| `check_result_ack` | `{ execution_id }` | same for a `check_result`, keyed by the execution rather than the check: a result may be re-sent until it is acknowledged |
| `remote_start` | `{ session_id, agent_ticket, kind, allow_files }` | connect a remote module to the relay; the agent derives `/ws/remote` from its own control WebSocket origin |
| `credential_rotate` | `{ rotation_id, agent_token }` | durably store the replacement credential, then reconnect with it |
| `credential_rotation_cancel` | `{ rotation_id }` | durably restore the prior credential and confirm it |
| `credential_rotation_canceled_ack` | `{ rotation_id }` | server recorded restoration; drop the persisted confirmation retry |
| `job_cancel` | `{ job_id }` | stop a job: reject it if it has not started, or kill the running process |
| `run_check` | `{ check_id, execution_id, type, config, timeout_sec }` | run one monitoring check now, reply with `check_result`. `execution_id` identifies this run, so a retry is recognised as the same observation rather than a new one ([10](10-monitoring-policies.md)) |
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
| `python` | the resolved interpreter, run against a staged file (see below) |

An unknown shell is refused with exit 2 rather than falling back to Bash, so a
typo never runs under an interpreter the operator did not choose.

### Device queries and parameters (agent 0.10.0)

A job of `type` `device_query` carries `{"query": ..., "params": {...}}` as its
`script`. The agent runs fixed, read-only code for that query and replies with
its result as JSON on stdout (at most 64 KB). The query and its parameters are
checked against the catalog by the agent as well as the server
(`internal/protocol/devicequery.go`); nothing in them reaches a command line
except a whole number of hours, formatted by the agent. See
[23-ai-autonomy](23-ai-autonomy.md).

`params` on a `run_script` job are a library script's declared parameters. The
agent sets each one as the environment variable `OPENRMM_PARAM_<NAME>` for the
script's process and never splices it into the script text. It refuses a name
that is not `[a-z][a-z0-9_]{0,31}`, a value over 200 characters or holding a
line break, and any parameter on a `cmd` job (cmd expands variables into its own
command line). Agents before 0.10.0 ignore `params`, which is why the server
refuses parameterised jobs and device queries for them.

Execution is bounded by a 10-minute timeout; stdout/stderr and the exit code are
returned.

### Python

Python is the one shell that is **staged to a file** — `openrmm-script-*.py`,
mode 0600, removed when the run ends, including on timeout or operator cancel.
Passing it with `-c` would work until it didn't: a traceback would point at
`<string>` instead of a file and line, Windows caps a command line at ~32k
characters, and a script full of quotes and newlines is one quoting mistake away
from running as something else.

The interpreter is resolved at run time, in order: **`python3`**, then
`python`, then `py -3` on Windows only. `python3` comes first because on a
machine with both, `python` may still be Python 2 — under which a Python 3
script fails in ways that read like a syntax error in the operator's code. The
`py` launcher is the reliable way to reach an interpreter that is installed but
absent from `PATH`.

A device with no interpreter exits **127** (the shell convention for "command
not found") and says which candidates were tried. That is deliberately distinct
from a script failure: "this device has no Python" and "this script is broken"
are fixed by different people in different places.

Python scripts require agent **0.4.9** or later. Older agents answer "unsupported
shell", so the server refuses the job before dispatch instead — see
`models.MinAgentVersionFor`. A bulk deployment naming any outdated device is
refused whole, listing the devices to update, rather than creating jobs for part
of the selection behind an approval granted for all of it.

### Output encoding

A redirected child process on Windows writes in the **system code page**, not
UTF-8: Python resolves `locale.getpreferredencoding()` (cp1252 on a pt-BR
install), cmd and Windows PowerShell use the OEM page. Accented bytes are then
not valid UTF-8 on their own, and marshalling them as JSON replaces each with
U+FFFD — a **lossy** substitution, so `Olá, café` became `Ol<?>, caf<?>`
in the database with no way to recover it downstream.

Two defences, in order of preference:

1. **Ask the interpreter.** Python is run with `PYTHONIOENCODING=utf-8` — the
   variable rather than `-X utf8`, whose Python 3.7 requirement would fail the
   whole script with "unknown option" on an older interpreter instead of merely
   mis-encoding its output.
2. **Decode what still is not UTF-8**, using the code page the OS reports
   (`GetConsoleOutputCP`, falling back to `GetACP`), rather than discarding the
   byte. This covers cmd, Windows PowerShell and anything that ignores the
   request. Valid UTF-8 is passed through untouched.

Truncation of long output is **rune-aware**: slicing bytes at the cap used to
split a multi-byte character and leave an invalid tail, which corrupted output
from Linux and macOS agents too, not only Windows.
