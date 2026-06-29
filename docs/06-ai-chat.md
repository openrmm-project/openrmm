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

`internal/ai/chat.go` — `Engine.Respond(ctx, sessionID, userContent, actor, scopeUser)`:

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

### Access-scope enforcement

`scopeUser` is the caller's user id when they have a **restricted access scope**
(see `07-guardrails-security`). It flows into `ToolContext.ScopeUserID`, and every
device-targeting tool (`list_devices`, `get_inventory`, `propose_script`,
`get_job_status`, `cancel_job`, `fleet_summary`) checks `tc.allows(ctx, agentID)`
before reading or proposing — so a scoped user's AI sees and acts on exactly the
devices that user can. Admins/unrestricted users pass an empty `scopeUser` and see
everything. The same enforcement applies to the MCP server (`11-mcp`).

## Tools (`internal/ai/tools.go`)

| Tool | Kind | Description |
|------|------|-------------|
| `list_devices` | read | List agents (id, hostname, os, arch, status) |
| `get_inventory` | read | Latest inventory for a device |
| `get_job_status` | read | Status + result of a job |
| `fleet_summary` | read | Aggregate counts + recent jobs (for reports) |
| `inventory_report` | read | Fleet inventory (software/hardware/machines) by client/site/department, scope-filtered, capped at 1000 rows — see [15-reports](15-reports.md) |
| `cancel_job` | control | Reject a pending job or request cancel of a running one |
| `propose_script` | **write (gated)** | Create a job in `awaiting_approval` — never executes |
| `propose_bulk_deploy` | **write (gated)** | Mass deployment: one script across many devices, **split into one approval-gated batch per company** — never executes, never spans companies |

### Why the write tools only *propose*

The model can read everything but the only mutating actions it can take are to
*propose* jobs. `propose_script` calls `jobs.Propose(..., requireApproval=true)`,
which creates a job in `awaiting_approval`. Nothing reaches an endpoint until a
human approves it via `POST /api/jobs/{id}/approve`. The tool's return value
explicitly tells the model the job is pending approval, so it won't claim
success. This is the guardrail, enforced in code.

`propose_bulk_deploy` is the same guardrail for **mass deployment**: it resolves
the targets (device ids ∪ site ∪ department), keeps only the caller's in-scope
devices, and calls `jobs.ProposeBulk`, which splits them **by company** into one
`awaiting_approval` batch per company (a deployment never spans companies). The
human approves each company batch in the UI; nothing runs before that.

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

**Reports are read-only and stay in the chat.** A system-prompt rule forbids the
model from proposing a script that writes a report file on the endpoint: to
"report / list / show" data it must use the read tools and answer in chat. If a
report genuinely needs a script, the script prints to **STDOUT** (the output comes
back in the job result) — it never writes a file on the device unless the user
explicitly asks to save one there.

### Downloadable CSV in the chat

When the user asks for a report/export (or mentions CSV/spreadsheet/download), the
model renders the data as a **GitHub Markdown table** (or a fenced ` ```csv ` block).
The chat UI (`openrmm/src/screens-chat.jsx`) parses the assistant message:

- Markdown tables render as real HTML tables.
- A **"Download CSV"** button appears under the message; it builds the CSV and
  downloads it **client-side via a `Blob`** — nothing is written on the server or
  the endpoint, and no file artifact is persisted.

This is the recommended way to get a report "as a file": the data is fetched with
read tools, shown in chat, and exported by the browser on demand. To pull an
*existing* file off an endpoint instead, use the remote-access file manager
(`08-remote-access`).

## Adding a tool

1. Add a `Tool` (name, description, JSON-Schema parameters) in `Tools()`.
2. Add a case in `Execute()` that validates args and calls the store/job service.
3. Keep mutating tools gated behind approval. For new high-level actions
   (e.g. `install_printer`), have the tool generate a job rather than act
   directly — same approval path.
