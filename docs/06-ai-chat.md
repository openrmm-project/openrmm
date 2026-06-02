# 06 — AI Chat & Tool-Calling

The differentiator. Implemented in `internal/ai`.

## Model interface

`internal/ai/client.go` is a thin **OpenAI-compatible** client
(`POST {base}/chat/completions` with `tools`). This means BYO-model:

| Provider | `RMM_AI_BASE_URL` | `RMM_AI_MODEL` |
|----------|-------------------|----------------|
| OpenAI | `https://api.openai.com/v1` | `gpt-4o-mini` (etc.) |
| Ollama (local) | `http://localhost:11434/v1` | `llama3.1` (etc.) |
| vLLM (local) | `http://host:8000/v1` | served model id |

A local model keeps all command/inventory data on your own infrastructure —
the privacy-first posture this project targets.

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
