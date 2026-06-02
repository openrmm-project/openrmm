# 06 — AI Chat & Tool-Calling

The differentiator. Implemented in `internal/ai`.

## Model interface

`internal/ai/client.go` is a thin **OpenAI-compatible** client
(`POST {base}/chat/completions` with `tools`). This means BYO-model:

| Provider | `RMM_AI_BASE_URL` / UI Base URL | `RMM_AI_MODEL` / UI Model |
|----------|-------------------|----------------|
| OpenAI | `https://api.openai.com/v1` | `gpt-4o-mini` (etc.) |
| Ollama (local) | `http://localhost:11434/v1` | `llama3.1` (etc.) |
| vLLM (local) | `http://host:8000/v1` | served model id |

A local model keeps all command/inventory data on your own infrastructure —
the privacy-first posture this project targets.

AI settings can be bootstrapped from `.env` and then edited by an admin in the
Configuration screen (`PUT /api/config`). The API key is write-only: the backend
encrypts it in `app_settings`, updates the in-memory AI client immediately, and
only exposes an `api_key_set` boolean to the UI.

## The loop

`internal/ai/chat.go` — `Engine.Respond(sessionID, userContent, actor)`:

1. Persist the user message.
2. Load full history + a system prompt that hard-codes the guardrails.
3. Loop (max 8 iterations):
   - Call the model with the tool catalog.
   - Persist the assistant message (including any `tool_calls`).
   - If no tool calls → return the text + `created_job_ids`.
   - Otherwise run each tool, persist a `tool` message with the result, append
     it to history, and loop again.

History is replayed verbatim each turn (tool_calls and tool results stored as
they came), so the conversation is stateless on the server beyond Postgres.

## Tools (`internal/ai/tools.go`)

| Tool | Kind | Description |
|------|------|-------------|
| `list_devices` | read | List agents (id, hostname, os, arch, status) |
| `get_inventory` | read | Latest inventory for a device |
| `get_job_status` | read | Status + result of a job |
| `fleet_summary` | read | Aggregate counts + recent jobs (for reports) |
| `propose_script` | **write (gated)** | Create a job in `awaiting_approval` — never executes |

### Why `propose_script` is the only write tool

The model can read everything but the **single** mutating action it can take is
to *propose* a job. `propose_script` calls `jobs.Propose(..., requireApproval=true)`,
which creates a job in `awaiting_approval`. Nothing reaches an endpoint until a
human approves it via `POST /api/jobs/{id}/approve`. The tool's return value
explicitly tells the model the job is pending approval, so it won't claim
success. This is the guardrail, enforced in code.

## High-level tools (OS-aware actions)

On top of the generic `propose_script`, the catalog includes **semantic,
OS-aware** tools (in `internal/ai/highlevel.go`). The model supplies the
*intent*; the backend builds the correct, vetted command for the target
device's OS (Windows/Linux/macOS):

| Tool | Action |
|------|--------|
| `install_software` / `uninstall_software` | winget / apt / brew per OS |
| `restart_service` | Restart-Service / systemctl |
| `install_printer` | Add-Printer (TCP/IP port) / lpadmin |
| `flush_dns` | ipconfig / resolvectl / dscacheutil |
| `reboot_device` | shutdown /r / shutdown -r |

These create the **same approval-gated job** as `propose_script` (the AI can't
execute them). Parameters are validated server-side (shell-metacharacter
rejection, IP parsing) as defense in depth on top of the human approval gate.
They're code-defined (no per-deployment maintenance) and, because the MCP server
reuses the same catalog, they're available to external AI clients too. The
generic `propose_script` remains the fallback for anything without a dedicated
tool. Verified end-to-end via MCP `tools/call` (correct per-OS command, injection
rejected, job left awaiting approval).

## Report generation

Reports are just the model summarizing data it fetched via `fleet_summary` /
`get_inventory` / `get_job_status`. No special endpoint — ask for a report in
chat and the model assembles it from tool output.

## Adding a tool

1. Add a `Tool` (name, description, JSON-Schema parameters) in `Tools()`.
2. Add a case in `Execute()` that validates args and calls the store/job service.
3. Keep mutating tools gated behind approval. For new high-level actions
   (e.g. `install_printer`), have the tool generate a job rather than act
   directly — same approval path.
