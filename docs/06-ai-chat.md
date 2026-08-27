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

`internal/ai/chat.go` — `Engine.Respond(ctx, sessionID, userContent, actor, scopeUser, permissions)`:

1. Persist the user message.
2. Load full history + a system prompt that hard-codes the guardrails.
3. Loop (max 8 iterations):
   - Call the model with the tool catalog filtered by the caller's permissions.
   - Persist the assistant message (including any `tool_calls`).
   - If no tool calls → return the text + `created_job_ids`.
   - Otherwise run each tool, persist a `tool` message with the result, append
     it to history, and loop again.

History is replayed verbatim each turn (tool_calls and tool results stored as
they came), so the conversation is stateless on the server beyond Postgres.

### Permissions & access-scope enforcement

`permissions` is the authenticated principal's permission set. The model only sees
tools allowed by that set, and `Execute` validates again before running a tool.
Examples: `inventory_report` requires `reports.view`, `list_patches` requires
`patches.view`, proposal tools require `jobs.create`, and cancel requires
`jobs.manage`.

`scopeUser` is the caller's user id when they have a **restricted access scope**
(see `07-guardrails-security`). It flows into `ToolContext.ScopeUserID`, and every
device-targeting tool checks `tc.allows(ctx, agentID)` before reading or proposing
— so a scoped user's AI sees and acts on exactly the devices that user can.
Admins/unrestricted users pass an empty `scopeUser` and see everything allowed by
their permissions. The same permission and scope enforcement applies to the MCP
server (`11-mcp`).

## Tools (`internal/ai/tools.go`)

| Tool | Required permission | Kind | Description |
|------|---------------------|------|-------------|
| `list_devices` | `devices.view` | read | List agents (id, hostname, os, arch, status) |
| `get_inventory` | `devices.view` | read | Latest inventory for a device |
| `get_job_status` | `jobs.view` | read | Status + result of a job |
| `fleet_summary` | `devices.view` (+ `jobs.view` for recent jobs) | read | Aggregate counts + recent jobs when allowed |
| `inventory_report` | `reports.view` | read | Fleet inventory (software/hardware/machines) by client/site/department, scope-filtered, capped at 1000 rows — see [15-reports](15-reports.md) |
| `list_patches` | `patches.view` | read | Missing OS patches on a device (KB/advisory, classification, status) — see [16-patch-management](16-patch-management.md) |
| `list_tasks` / `list_checks` / `list_policies` | `tasks.view` / `checks.view` / `policies.view` | read | Existing monitoring configuration, including which rows are unreviewed AI proposals |
| `install_patches` | `patches.manage` | **write** | Install patches **a person already approved**. `ClaimApprovedPatches` filters to approved identifiers, so an unapproved one installs nothing. There is deliberately no tool to approve a patch |
| `cancel_job` | `jobs.manage` | control | Reject a pending job or request cancel of a running one |
| `propose_script` | `jobs.create` | **write (gated)** | Create a job in `awaiting_approval` — never executes |
| `propose_bulk_deploy` | `jobs.create` | **write (gated)** | Mass deployment: one script across many devices, **split into one approval-gated batch per company** — never executes, never spans companies |
| `propose_task` | `tasks.manage` | **write (gated)** | Create a scheduled task **disabled** and stamped `proposed_by` — the scheduler selects `WHERE enabled`, so it never runs until a person enables it |
| `propose_check` | `checks.manage` | **write (gated)** | Same, for a monitoring check |
| `propose_policy` | `policies.manage` | **write (gated)** | Create a policy marked as awaiting review. Unrestricted admins only, like the REST handler |

### Configuration is proposed too, and here is why

Letting the AI create a **task** would have quietly undone everything above. A
task is a script with a schedule, and the scheduler runs one through
`jobs.RunAutomated` — pre-approved, dispatched immediately, repeating forever.
Its own comment says *"never for AI-authored scripts."* An AI able to create an
enabled task would have been **strictly more powerful** than one able to propose
a script: same arbitrary code, no approval, and it repeats.

So configuration reuses the kill switch that already existed. A proposed task,
check or policy is written with `enabled = false` and stamped with `proposed_by`.
The scheduler's query already reads `WHERE enabled`, so the row is inert with no
scheduler change at all. A person reviews it in the console — the existing Tasks,
Checks and Policies screens badge it as **AI proposal** — and enabling it *is* the
review.

Two columns rather than one, because they answer different questions:

- `proposed_by` — permanent provenance. It survives acceptance, so months later
  the row still says an AI wrote it.
- `approved_by` — who released it. Empty means nobody has.

"Awaiting review" is therefore `proposed_by <> '' AND approved_by = ''`, and a
`CHECK` constraint refuses to enable a row in that state. The constraint lives in
the **database**, not in the tool, because the REST handlers are a second path
into the same rows and a guard written in only one of them is exactly the bug the
column exists to prevent.

#### Patches: install yes, approve never

`install_patches` acts rather than proposes, and it is safe for a reason that has
nothing to do with the AI: `DispatchInstall` calls `ClaimApprovedPatches`, which
filters to identifiers a person already approved. An unapproved id installs
nothing. Approving a patch is the human decision about what enters the fleet, and
there is **no tool for it** — an AI that could approve and then install would make
the guarantee circular.

#### Why not trust "the admin asked for it"

Configuration is not created enabled even for an unrestricted administrator. The
model reads inventory, hostnames and alert text — data that comes from the
monitored machines. "An admin asked" is not evidence that an admin *authored* the
request, so the gate does not depend on who is asking.

### Why the write tools only *propose*

The model can only read through tools allowed by the user's permissions and scope.
The only mutating actions it can take are to *propose* jobs. `propose_script` calls `jobs.Propose(..., requireApproval=true)`,
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
They're code-defined (no per-deployment maintenance) and require `jobs.create`.
Because the MCP server reuses the same filtered catalog, they're available to
external AI clients only when the connector has that permission. The
generic `propose_script` remains the fallback for anything without a dedicated
tool. Verified end-to-end via MCP `tools/call` (correct per-OS command, injection
rejected, job left awaiting approval).

## Report generation

Reports are the model summarizing data fetched via permitted read tools:
`fleet_summary`, `get_inventory`, `get_job_status`, or `inventory_report` when the
principal has `reports.view`. Ask for a report in chat and the model assembles it
from tool output.

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
